package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// ErrFirstRoundFailed marks a failure at round 0 of a virgin session — no
// assistant/tool message exists anywhere in history yet (turnStart == 0
// AND round == 0). This is the ONE failure gk chat's session-start
// fallback (chat.go's resolveChatProviderChain + runFirstChatTurn) may
// safely retry against the next ToolCaller in its provider chain: since
// nothing vendor-specific (tool_use IDs) has been generated yet,
// restarting fresh with a different provider can't corrupt anything. Any
// LATER round's failure is returned bare instead — the session has
// already committed to this provider's tool_use IDs, so mid-conversation
// failover is never attempted.
var ErrFirstRoundFailed = errors.New("chat: round 0 failed before any assistant/tool history existed")

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
	// display; nil-safe. OnRound fires after every provider reply with the
	// turn's cumulative token count so a live spinner can show spend as it
	// grows.
	OnToolCall   func(call provider.ToolCall)
	OnToolResult func(call provider.ToolCall, res provider.ToolResult)
	OnRound      func(round, tokensSoFar int, approx bool)
	// OnTextDelta, when set, opts every round of every turn into
	// text-only SSE streaming: it is forwarded verbatim as
	// ChatInput.OnTextDelta on each ChatWithTools call. nil (the
	// default) leaves every round on the existing non-stream path — see
	// ChatInput.OnTextDelta's docstring for the streaming/fallback
	// contract. GK_AGENT/--json callers must leave this nil to keep the
	// envelope contract (chat.go gates it on JSONOut()).
	OnTextDelta func(string)
	// OnStreamReset is forwarded as ChatInput.OnStreamReset: an adapter
	// fires it when a streaming round is abandoned for the non-stream
	// fallback after already delivering text, so the terminal UI can void
	// the stale partial. nil when OnTextDelta is nil (no streaming).
	OnStreamReset func()
	Dbg           func(string, ...any)

	history []provider.ChatMessage
}

// TurnResult summarizes one completed turn.
type TurnResult struct {
	Text      string
	Model     string
	Rounds    int
	ToolCalls int
	// TokensUsed accumulates provider-reported usage across the turn's
	// rounds. Rounds whose provider returned no usage (some proxies drop
	// the field) are filled with the chars/4 estimate and flip
	// TokensApprox — the number is then a floor-ish approximation, not
	// billing truth.
	TokensUsed   int
	TokensApprox bool
}

// LoadHistory seeds the conversation (from Session.Replay for --continue).
func (e *Engine) LoadHistory(msgs []provider.ChatMessage) {
	e.history = append([]provider.ChatMessage(nil), msgs...)
}

// History returns the live conversation (for tests and the /clear
// command).
func (e *Engine) History() []provider.ChatMessage { return e.history }

// ClearHistory drops the in-memory conversation and records a clear
// marker so a later --continue replay starts from the same empty context
// instead of resurrecting what the user explicitly cleared (the file
// keeps the full record for audit; only replay semantics change). The
// returned error means the marker did NOT land — live memory is cleared
// but a future --continue would restore the pre-clear context, which the
// caller should tell the user instead of hiding.
func (e *Engine) ClearHistory() error {
	e.history = nil
	if e.Session == nil {
		return nil
	}
	if err := e.Session.Append(SessionRecord{TS: time.Now().UTC(), Role: recordRoleClear}); err != nil {
		return fmt.Errorf("chat: clear marker not persisted (--continue would restore the cleared context): %w", err)
	}
	return nil
}

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

// RunTurn processes one user input to a final text answer. On ANY error
// the whole turn is rolled back — in memory by truncation, in the session
// file by an "aborted" marker that Replay honors — because a partial turn
// is not just untidy, it is POISON: Anthropic rejects two consecutive
// user messages and every tool_use must be answered by a tool_result, so
// a dangling user message or an unanswered tool call would wedge every
// subsequent round of the session (three review vendors converged on
// this). Roll back, report, and the next turn starts clean.
func (e *Engine) RunTurn(ctx context.Context, userInput string) (_ *TurnResult, err error) {
	turnStart := len(e.history)
	defer func() {
		if err != nil {
			e.history = e.history[:turnStart]
			if e.Session != nil {
				if aErr := e.Session.Append(SessionRecord{TS: time.Now().UTC(), Role: recordRoleAborted}); aErr != nil {
					e.dbg("chat: abort marker append failed: %v", aErr)
				}
			}
		}
	}()

	e.appendAndPersist(provider.ChatMessage{Role: "user", Text: userInput}, SessionRecord{Role: "user", Text: userInput})

	res := &TurnResult{}
	seen := make(map[string]int)
	turnBytes := 0
	// groundingReprompted guards the code-first grounding gate below to
	// fire at most once per turn — a hard stop against a reprompt loop,
	// independent of the res.ToolCalls==0 check it sits beside.
	groundingReprompted := false

	// History trimming is turn-scoped: trimHistory never touches the
	// in-flight turn, so the boundary at turnStart never moves once the
	// turn starts. Seed the cache once here instead of re-running
	// trimHistory's full O(history) scan on every one of this turn's
	// (up to maxRounds) round-trips — see trimmedTurnHistory's docstring.
	var histCache *trimmedTurnHistory
	if e.HistoryBudget > 0 {
		histCache = newTrimmedTurnHistory(e.history, turnStart, e.HistoryBudget)
	}

	for round := 0; round < e.maxRounds(); round++ {
		if cErr := ctx.Err(); cErr != nil {
			return nil, cErr
		}
		res.Rounds = round + 1

		msgs := e.history
		if histCache != nil {
			msgs = histCache.forRound(e.history, e.HistoryBudget)
		}

		roundCtx, cancel := context.WithTimeout(ctx, e.roundTimeout())
		reply, err := e.Caller.ChatWithTools(roundCtx, provider.ChatInput{
			System:        e.SystemPrompt,
			Messages:      msgs,
			Tools:         e.Registry.Specs(),
			MaxTokens:     e.MaxTokens,
			OnTextDelta:   e.OnTextDelta,
			OnStreamReset: e.OnStreamReset,
		})
		if err == nil && len(reply.ToolCalls) > 0 {
			if violations := toolSchemaViolations(e.Registry.Specs(), reply.ToolCalls); len(violations) > 0 {
				reply = e.retrySchemaViolation(roundCtx, msgs, reply, violations, res)
			}
		}
		cancel()
		if err != nil {
			// A virgin session (nothing recorded anywhere yet) failing on
			// its very first provider round is the one case chat.go's
			// session-start fallback may retry with the next provider —
			// mark it so the caller can tell this apart from an ordinary
			// mid-conversation failure. EXCEPT a user cancellation
			// (Ctrl-C, context.Canceled): that says nothing about whether
			// THIS candidate works, so wrapping it here would make
			// chat.go's runFirstChatTurn treat "the user hit Ctrl-C" as
			// "this provider is broken, try the next one" and re-fire the
			// identical question at a different candidate — the opposite
			// of what a cancellation asked for. A round-scoped timeout
			// still wraps normally: e.roundTimeout()'s own deadline firing
			// is context.DeadlineExceeded, a distinct error that never
			// matches this check.
			if round == 0 && turnStart == 0 && !errors.Is(err, context.Canceled) {
				err = fmt.Errorf("%w: %w", ErrFirstRoundFailed, err)
			}
			return nil, err
		}
		res.Model = reply.Model
		if reply.TokensUsed > 0 {
			res.TokensUsed += reply.TokensUsed
		} else {
			// Provider gave no usage (some proxies strip it) — estimate
			// this round's request+response with the project-standard
			// chars/4 heuristic. Each round re-sends the full history, so
			// per-round input estimation mirrors real billing.
			res.TokensUsed += estimateTokens(msgs) +
				(len(e.SystemPrompt)+len(reply.Text))/charsPerToken
			res.TokensApprox = true
		}
		if e.OnRound != nil {
			e.OnRound(round+1, res.TokensUsed, res.TokensApprox)
		}

		if len(reply.ToolCalls) == 0 {
			// Code-first grounding gate. gk chat is a repository exploration
			// assistant, but a model will happily answer a code-answerable
			// question ("what command checks the remote?") from general git
			// knowledge without ever opening a file here. When that happens —
			// a final answer produced with zero tool calls this turn, on a
			// question the code here can answer — spend ONE reprompt that
			// tells it to investigate first. res.ToolCalls stays 0 only until
			// the model calls a tool, and groundingReprompted latches after
			// the first nudge, so this fires at most once and never loops:
			// if the reprompt still yields no tool call, the answer is
			// returned as-is (a nudge, not a wall). Repo-independent
			// questions and already-grounded answers skip the gate entirely.
			if !groundingReprompted && res.ToolCalls == 0 && IsCodeAnswerable(userInput) {
				groundingReprompted = true
				// Persist the ungrounded draft as a real assistant turn
				// BEFORE the user reprompt: two consecutive user messages are
				// the POISON described on RunTurn, so the alternation must be
				// assistant(draft) → user(reprompt) → assistant(round 2).
				e.appendAndPersist(
					provider.ChatMessage{Role: "assistant", Text: reply.Text},
					SessionRecord{Role: "assistant", Text: reply.Text, Model: reply.Model, TokensUsed: reply.TokensUsed},
				)
				e.appendAndPersist(
					provider.ChatMessage{Role: "user", Text: groundingReprompt},
					SessionRecord{Role: "user", Text: groundingReprompt},
				)
				// In REPL streaming the draft already reached the terminal;
				// void the stale partial before the regenerated answer streams.
				if e.OnStreamReset != nil {
					e.OnStreamReset()
				}
				e.dbg("chat: grounding gate — reprompting to investigate (code-answerable question answered with 0 tool calls)")
				continue
			}
			if groundingReprompted && res.ToolCalls == 0 {
				// The nudge did not take: answer is returned, but flag the
				// weak grounding so --debug shows the answer was not verified
				// against the repository (R7 — pass through, do not loop).
				e.dbg("chat: grounding gate — answered without tools after reprompt; grounding unverified")
			}
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

// toolCallViolation reports why call fails the registry's own tool
// schema — an unknown tool name, or a JSON Schema "required" field the
// call's input omits — or "" when the call is schema-valid. This is a
// best-effort, non-recursive check (top-level "required" keys only); it
// exists to catch the same class of error Registry.Dispatch already
// detects at execution time (executor.go's defense-in-depth rule still
// holds — this is not a replacement validator), just early enough that
// RunTurn can spend its one semantic reprompt before burning a whole
// round on a call that was never going to execute.
func toolCallViolation(specs map[string]provider.ToolSpec, call provider.ToolCall) string {
	spec, ok := specs[call.Name]
	if !ok {
		return fmt.Sprintf("tool %q does not exist", call.Name)
	}
	if missing := missingRequiredFields(spec.InputSchema, call.Input); len(missing) > 0 {
		return fmt.Sprintf("tool %q call is missing required argument(s): %s", call.Name, strings.Join(missing, ", "))
	}
	return ""
}

// missingRequiredFields extracts a JSON Schema object's top-level
// "required" array and reports which of those keys are absent from
// input's top-level object. A schema with no "required" array, or an
// input that fails to parse as a JSON object, yields no findings — this
// is a best-effort check, not a full validator, and a malformed
// schema/input is the handler's problem to report, not a reason to
// false-positive a retry.
func missingRequiredFields(schema, input json.RawMessage) []string {
	var s struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil || len(s.Required) == 0 {
		return nil
	}
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	var in map[string]json.RawMessage
	if err := json.Unmarshal(input, &in); err != nil {
		return nil
	}
	var missing []string
	for _, req := range s.Required {
		if _, ok := in[req]; !ok {
			missing = append(missing, req)
		}
	}
	return missing
}

// toolSchemaViolations checks every call in one reply against the
// registry's tool specs, returning one violation message per BAD call
// keyed by ToolCall.ID. An empty map means every call is schema-valid.
func toolSchemaViolations(specs []provider.ToolSpec, calls []provider.ToolCall) map[string]string {
	bySpec := make(map[string]provider.ToolSpec, len(specs))
	for _, s := range specs {
		bySpec[s.Name] = s
	}
	violations := make(map[string]string)
	for _, c := range calls {
		if v := toolCallViolation(bySpec, c); v != "" {
			violations[c.ID] = v
		}
	}
	return violations
}

// buildSchemaRetryMessages appends the model's own (violating) reply plus
// a synthetic tool_result per call — every vendor adapter requires each
// tool_use to be answered before the conversation can continue
// (Anthropic rejects a dangling tool_use outright, and two consecutive
// user-role messages besides; OpenAI's "tool" role is keyed by
// tool_call_id the same way a real dispatch round already uses). The
// offending call(s) get their specific violation text; any call that was
// itself fine but shared the reply with a violator gets a generic
// "not executed" note. None of this reaches e.history or the session
// file — it is a one-shot, ephemeral coaching message the model never
// sees again on --continue replay.
func buildSchemaRetryMessages(msgs []provider.ChatMessage, original provider.ChatResult, violations map[string]string) []provider.ChatMessage {
	out := make([]provider.ChatMessage, 0, len(msgs)+1+len(original.ToolCalls))
	out = append(out, msgs...)
	out = append(out, provider.ChatMessage{Role: "assistant", Text: original.Text, ToolCalls: original.ToolCalls})
	for _, c := range original.ToolCalls {
		content, bad := violations[c.ID]
		if !bad {
			content = "not executed — a sibling tool call in this reply violated its schema; reissue this call again if it's still needed"
		}
		r := provider.ToolResult{ToolCallID: c.ID, Content: content, IsError: true}
		out = append(out, provider.ChatMessage{Role: "tool", ToolResult: &r})
	}
	return out
}

// retrySchemaViolation re-prompts the model once, in place, when its
// reply requested tool call(s) the registry can't satisfy — a
// content-quality retry (the transport call already succeeded; the
// CONTENT doesn't match what was asked), the same shape as
// Classify/Compose's maxContentRetry pattern
// (internal/ai/provider/nvidia.go), ported here for ChatWithTools. It is
// entirely independent of invoke()'s transport-level 429/5xx backoff.
//
// A transport failure on the retry call keeps the ORIGINAL reply:
// dispatchGuarded/Registry.Dispatch's existing "unknown tool" error path
// still gives the model a chance to self-correct on the NEXT round,
// exactly as if this retry didn't exist. A retry that succeeds but is
// STILL schema-invalid is used anyway — one reprompt is the budget; the
// still-broken call(s) surface through the normal dispatch error path from
// here on.
//
// Token accounting is charged for the DISCARDED original ONLY on the
// success path. When the retry succeeds, the original reply is thrown away
// and its cost would otherwise vanish — a rejected attempt is never
// silently free — so it is folded into res here, and the retried reply's
// own cost is charged by RunTurn's normal accounting afterward. When the
// retry FAILS, the original is returned and becomes `reply` in RunTurn,
// which charges it there — so charging it here too would double-count it
// (the v2 review's exact finding). Hence: no pre-charge; charge only when
// we are about to drop the original for a successful retry.
func (e *Engine) retrySchemaViolation(ctx context.Context, msgs []provider.ChatMessage, original provider.ChatResult, violations map[string]string, res *TurnResult) provider.ChatResult {
	retryMsgs := buildSchemaRetryMessages(msgs, original, violations)
	retried, err := e.Caller.ChatWithTools(ctx, provider.ChatInput{
		System:    e.SystemPrompt,
		Messages:  retryMsgs,
		Tools:     e.Registry.Specs(),
		MaxTokens: e.MaxTokens,
	})
	if err != nil {
		// Original is kept and charged by RunTurn — do not charge it here.
		e.dbg("chat: schema-violation retry failed, keeping the original reply: %v", err)
		return original
	}
	// Original is discarded: charge its cost now, since RunTurn will only
	// see (and charge) the retried reply.
	if original.TokensUsed > 0 {
		res.TokensUsed += original.TokensUsed
	} else {
		res.TokensUsed += estimateTokens(msgs) + (len(e.SystemPrompt)+len(original.Text))/charsPerToken
		res.TokensApprox = true
	}
	return retried
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
