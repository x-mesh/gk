package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/chat/tools"
)

const (
	defaultMaxToolRounds = 15
	defaultRoundTimeout  = 60 * time.Second
	// defaultTurnByteCap bounds the cumulative tool-result bytes one turn
	// may feed back to the model. The per-call 32KB cap alone still allows
	// 32KB × 15 rounds ≈ 480KB in a single turn — this is the aggregate
	// guard the research flagged.
	defaultTurnByteCap = 192 * 1024
	// repeatCallLimit is how many times the identical (name, input) call
	// may execute in one turn before further repeats are refused — a
	// cheap circuit breaker for models stuck re-issuing the same probe.
	repeatCallLimit = 2
)

// ErrMaxRounds reports a turn that spent its round budget without the
// model producing a final answer.
var ErrMaxRounds = errors.New("chat: tool-call round limit reached without a final answer")

// Engine drives the agentic loop: one RunTurn per user input, each turn
// alternating provider round-trips with sandboxed tool dispatch until the
// model answers in text. The engine owns every limit (rounds, bytes,
// repeats, timeouts) — providers and tools stay policy-free.
type Engine struct {
	Caller       provider.ToolCaller
	Registry     *tools.Registry
	SystemPrompt string
	// MaxTokens caps each provider response (0 → provider default).
	MaxTokens int
	// MaxToolRounds bounds provider round-trips per turn (0 → 15).
	MaxToolRounds int
	// RoundTimeout bounds ONE provider call, not the whole turn — a
	// 15-round turn is legitimate; a single hung HTTP call is not.
	RoundTimeout time.Duration
	// TurnByteCap bounds cumulative tool-result bytes per turn.
	TurnByteCap int
	// HistoryBudget approximates the token budget for replayed history
	// (0 → no trimming).
	HistoryBudget int
	// Session, when set, persists every message as it happens (post-
	// redaction — Registry redacts before results reach the engine).
	Session *Session
	// OnToolCall/OnToolResult are UI hooks for the one-line transparency
	// display; nil-safe.
	OnToolCall   func(call provider.ToolCall)
	OnToolResult func(call provider.ToolCall, res provider.ToolResult)
	Dbg          func(string, ...any)

	history []provider.ChatMessage
}

// TurnResult summarizes one completed turn.
type TurnResult struct {
	Text       string
	Model      string
	Rounds     int
	ToolCalls  int
	TokensUsed int
}

// LoadHistory seeds the conversation (from Session.Replay for --continue).
func (e *Engine) LoadHistory(msgs []provider.ChatMessage) {
	e.history = append([]provider.ChatMessage(nil), msgs...)
}

// History returns the live conversation (for tests and the /clear
// command).
func (e *Engine) History() []provider.ChatMessage { return e.history }

// ClearHistory drops the in-memory conversation (the session file keeps
// its record; new turns simply start from an empty context).
func (e *Engine) ClearHistory() { e.history = nil }

func (e *Engine) dbg(format string, args ...any) {
	if e.Dbg != nil {
		e.Dbg(format, args...)
	}
}

func (e *Engine) maxRounds() int {
	if e.MaxToolRounds > 0 {
		return e.MaxToolRounds
	}
	return defaultMaxToolRounds
}

func (e *Engine) roundTimeout() time.Duration {
	if e.RoundTimeout > 0 {
		return e.RoundTimeout
	}
	return defaultRoundTimeout
}

func (e *Engine) turnByteCap() int {
	if e.TurnByteCap > 0 {
		return e.TurnByteCap
	}
	return defaultTurnByteCap
}

// RunTurn processes one user input to a final text answer. On error the
// conversation state stays coherent (the user message and any completed
// rounds are kept and persisted) so a failed turn never corrupts the
// session — the caller reports the error and the REPL continues.
func (e *Engine) RunTurn(ctx context.Context, userInput string) (*TurnResult, error) {
	e.appendAndPersist(provider.ChatMessage{Role: "user", Text: userInput}, SessionRecord{Role: "user", Text: userInput})

	res := &TurnResult{}
	seen := make(map[string]int)
	turnBytes := 0

	for round := 0; round < e.maxRounds(); round++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res.Rounds = round + 1

		msgs := e.history
		if e.HistoryBudget > 0 {
			msgs = trimHistory(msgs, e.HistoryBudget)
		}

		roundCtx, cancel := context.WithTimeout(ctx, e.roundTimeout())
		reply, err := e.Caller.ChatWithTools(roundCtx, provider.ChatInput{
			System:    e.SystemPrompt,
			Messages:  msgs,
			Tools:     e.Registry.Specs(),
			MaxTokens: e.MaxTokens,
		})
		cancel()
		if err != nil {
			return nil, err
		}
		res.Model = reply.Model
		res.TokensUsed += reply.TokensUsed

		if len(reply.ToolCalls) == 0 {
			e.appendAndPersist(
				provider.ChatMessage{Role: "assistant", Text: reply.Text},
				SessionRecord{Role: "assistant", Text: reply.Text, Model: reply.Model, TokensUsed: reply.TokensUsed},
			)
			res.Text = reply.Text
			return res, nil
		}

		e.appendAndPersist(
			provider.ChatMessage{Role: "assistant", Text: reply.Text, ToolCalls: reply.ToolCalls},
			SessionRecord{Role: "assistant", Text: reply.Text, ToolCalls: reply.ToolCalls, Model: reply.Model, TokensUsed: reply.TokensUsed},
		)

		for _, call := range reply.ToolCalls {
			res.ToolCalls++
			if e.OnToolCall != nil {
				e.OnToolCall(call)
			}
			result := e.dispatchGuarded(ctx, call, seen, &turnBytes)
			if e.OnToolResult != nil {
				e.OnToolResult(call, result)
			}
			r := result
			e.appendAndPersist(
				provider.ChatMessage{Role: "tool", ToolResult: &r},
				SessionRecord{Role: "tool", ToolResult: &r},
			)
		}
	}
	return nil, fmt.Errorf("%w (%d rounds) — narrow the question or raise ai.chat.max_tool_rounds", ErrMaxRounds, e.maxRounds())
}

// dispatchGuarded wraps Registry.Dispatch with the engine-level guards:
// identical-call repetition and the cumulative turn byte cap. Refusals are
// IsError tool results — the model sees why and can change course, and
// the round cap still bounds the worst case.
func (e *Engine) dispatchGuarded(ctx context.Context, call provider.ToolCall, seen map[string]int, turnBytes *int) provider.ToolResult {
	key := callKey(call)
	seen[key]++
	if seen[key] > repeatCallLimit {
		e.dbg("chat: refusing repeated call %s (%d times)", call.Name, seen[key])
		return provider.ToolResult{
			ToolCallID: call.ID,
			Content:    fmt.Sprintf("refused: identical %s call already executed %d times this turn — use the earlier result or change the arguments", call.Name, repeatCallLimit),
			IsError:    true,
		}
	}
	if *turnBytes >= e.turnByteCap() {
		e.dbg("chat: turn byte cap reached (%d)", *turnBytes)
		return provider.ToolResult{
			ToolCallID: call.ID,
			Content:    "refused: this turn's tool-output budget is exhausted — answer with what you have",
			IsError:    true,
		}
	}
	result := e.Registry.Dispatch(ctx, call)
	*turnBytes += len(result.Content)
	return result
}

func callKey(call provider.ToolCall) string {
	h := sha256.New()
	h.Write([]byte(call.Name))
	h.Write([]byte{0})
	h.Write(call.Input)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// appendAndPersist grows the in-memory history and mirrors the message to
// the session file. Persistence failures are logged, not fatal — losing
// durability must not kill a live conversation.
func (e *Engine) appendAndPersist(msg provider.ChatMessage, rec SessionRecord) {
	e.history = append(e.history, msg)
	if e.Session == nil {
		return
	}
	rec.TS = time.Now().UTC()
	if err := e.Session.Append(rec); err != nil {
		e.dbg("chat: session append failed: %v", err)
	}
}
