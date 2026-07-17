package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/chat"
	"github.com/x-mesh/gk/internal/chat/tools"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/lineedit"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "chat [question]",
		Short: "repo와 대화 — AI가 git 히스토리와 코드를 직접 탐색하며 답한다",
		Long: `저장소와 대화한다. gk ask가 미리 모은 컨텍스트로 한 번 답하는 것과 달리,
chat은 모델이 읽기 전용 도구(git log/show/diff/blame/grep, 파일 읽기)를
스스로 호출해 근거를 찾아가며 답한다. 모든 도구 호출은 화면에 표시된다.

인자 없이 실행하면 대화 루프(REPL), 질문을 주면 한 번 답하고 끝난다.
대화는 .git/gk-chat/ 아래 저장되며 --continue로 이어가거나, --session <id>로
특정 세션을 골라 재개할 수 있다. gk chat sessions로 세션 목록(id·시작
시각·제목·턴 수)을 본다.

tool-calling을 지원하는 HTTP 프로바이더(anthropic/openai/groq/nvidia)가
필요하다. CLI형 프로바이더(gemini/qwen/kiro)는 지원하지 않는다 — 단발
질문은 gk ask를 사용.

예시:
  gk chat                                # 대화 시작
  gk chat "이 함수 언제 왜 바뀌었지?"      # one-shot
  gk chat --continue                     # 지난 대화 이어가기
  gk chat sessions                       # 세션 목록
  gk chat --session 20260709-143210-123  # 특정 세션 재개
  gk chat sessions prune --keep-days 30  # 오래된 세션 정리`,
		RunE: runChat,
	}
	cmd.Flags().String("provider", "", "AI 프로바이더 지정 (anthropic|openai|groq|nvidia)")
	cmd.Flags().String("model", "", "이번 실행에만 모델 지정")
	cmd.Flags().String("lang", "", "출력 언어 지정 (en|ko|...)")
	cmd.Flags().Bool("continue", false, "가장 최근 세션을 이어서 대화")
	cmd.Flags().String("session", "", "지정한 id의 세션을 재개 (gk chat sessions 로 id 확인)")
	cmd.AddCommand(newChatSessionsCmd())
	rootCmd.AddCommand(cmd)
}

type chatFlags struct {
	provider string
	model    string
	lang     string
	cont     bool
	session  string
}

func readChatFlags(cmd *cobra.Command) chatFlags {
	var f chatFlags
	f.provider, _ = cmd.Flags().GetString("provider")
	f.model, _ = cmd.Flags().GetString("model")
	f.lang, _ = cmd.Flags().GetString("lang")
	f.cont, _ = cmd.Flags().GetBool("continue")
	f.session, _ = cmd.Flags().GetString("session")
	return f
}

// chatToolCallLog is one executed tool call in the agent-mode envelope.
type chatToolCallLog struct {
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
}

// chatJSONResult backs `GK_AGENT=1 gk chat "질문"`.
type chatJSONResult struct {
	Answer     string            `json:"answer"`
	ToolCalls  []chatToolCallLog `json:"tool_calls,omitempty"`
	Provider   string            `json:"provider"`
	Model      string            `json:"model,omitempty"`
	Lang       string            `json:"lang"`
	SessionID  string            `json:"session_id"`
	Rounds     int               `json:"rounds"`
	TokensUsed int               `json:"tokens_used,omitempty"`
	// TokensApprox marks TokensUsed as a chars/4 estimate for rounds whose
	// provider returned no usage field.
	TokensApprox bool  `json:"tokens_approx,omitempty"`
	DurationMS   int64 `json:"duration_ms"`
	// Failure marks a one-shot turn that ended WITHOUT an answer instead of
	// completing normally: "max_rounds" (chat.ErrMaxRounds — the round
	// budget ran out before a final answer), "round_timeout" (one provider
	// round exceeded ai.chat.round_timeout), "no_provider" (every
	// session-start fallback candidate failed), "canceled" (the turn was
	// interrupted, e.g. Ctrl-C), or "error" (an unclassified failure —
	// e.g. a provider 500, an auth error, a malformed JSON reply — that
	// chatOneShotFailure could not name more specifically; see
	// classifyChatTurnError's chatErrOther). "" (the zero value, the
	// success case) leaves Answer as the real reply and agentState() falls
	// through to emitAgentResult's own "ok" default.
	Failure string `json:"failure,omitempty"`
}

// agentState implements agentStater (agent_envelope.go) so a GK_AGENT=1
// one-shot failure reports its actual state instead of the "ok" every
// non-agentStater payload gets by default. ErrMaxRounds is a precondition
// the caller can clear — narrow the question, raise the round budget — so
// it maps to "blocked": nothing was lost, a remedy unblocks the same
// question. A round timeout is not fixed by retrying the identical turn —
// "error". "no_provider" (every session-start fallback candidate failed)
// is also "blocked": fixing auth/network and retrying can succeed, same
// as max_rounds. "canceled" (the turn was interrupted) maps to "error": it
// produced no answer, the same as any other incomplete turn — before this
// case existed, Failure stayed "" for a canceled one-shot and this method
// fell through to its own "ok" default, so an interrupted turn with no
// answer misreported success. "error" (an unclassified failure —
// classifyChatTurnError's chatErrOther, e.g. a provider 500/auth/JSON
// error) maps to "error" for the same reason: before chatOneShotFailure
// named this case explicitly, it fell through with Failure=="" too, so ANY
// unrecognized failure — not just a cancellation — misreported "ok" with
// an empty answer. Anything else reaching default (including a genuinely
// unrecognized string, which should never happen but must not crash) falls
// back to "" so emitAgentResult's own "ok" default applies — the ONLY
// value that legitimately means that is the true zero value, the success
// case, so this default must never be the landing spot for a real failure
// code again.
func (r chatJSONResult) agentState() string {
	switch r.Failure {
	case "max_rounds":
		return envStateBlocked
	case "round_timeout":
		return envStateError
	case "no_provider":
		return envStateBlocked
	case "canceled":
		return envStateError
	case "error":
		return envStateError
	default:
		return ""
	}
}

func runChat(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	flags := readChatFlags(cmd)
	oneShot := len(args) > 0
	if flags.session != "" && flags.cont {
		return fmt.Errorf("chat: --session and --continue are mutually exclusive — pick one")
	}

	// The REPL needs a real interactive terminal — under GK_AGENT/--json/CI
	// there is nobody to type the next line (same gate as doctor --fix).
	if !oneShot && !promptAllowed() {
		return fmt.Errorf("chat: the REPL needs an interactive terminal (disabled under --json/GK_AGENT/CI)\nhint: use `gk chat \"<질문>\"` for a one-shot answer")
	}

	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return fmt.Errorf("chat: load config: %w", err)
	}
	ai := cfg.AI
	if flags.provider != "" {
		ai.Provider = flags.provider
	}
	lang := resolveResponseLang(flags.lang, cfg.AI.Lang, cfg.Output.Lang)

	if !ai.Enabled {
		return fmt.Errorf("AI features are disabled (ai.enabled=false)\nhint: set ai.enabled=true in .gk.yaml")
	}
	if strings.EqualFold(os.Getenv("GK_AI_DISABLE"), "1") {
		return fmt.Errorf("AI features are disabled (GK_AI_DISABLE=1)")
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}
	topOut, _, tErr := runner.Run(ctx, "rev-parse", "--show-toplevel")
	if tErr != nil {
		return fmt.Errorf("chat: not inside a git repository")
	}
	repoRoot := strings.TrimSpace(string(topOut))

	// One chat round carries tool definitions, repo context, accumulated
	// tool results, AND the adapter's internal 429/5xx retries — a proxy
	// that occasionally 500s needs ~3 attempts + backoff, which blows the
	// 30s single-shot budget do/ask use. round_timeout (120s default) is
	// the chat-specific budget: resolveChatProviderChain gives every
	// candidate's retry LOOP (not its per-attempt HTTP timeout — the two
	// are deliberately independent, see provider.Nvidia.RetryBudget) room
	// to match it, so the loop never silently undercuts the round, while
	// a single slow/hung attempt can no longer consume the whole budget
	// by itself and starve the remaining retries.
	roundTimeout := parseDurationOrDefault(ai.Chat.RoundTimeout, 120*time.Second)

	// Every ToolCaller-capable, available candidate is resolved up front
	// (auto-detect order, or the one explicit ai.Provider/--provider
	// choice) so a brand-new session's first round can fail over to the
	// next candidate if it never gets started — see runFirstChatTurn.
	// Once a turn gets even one round in, the provider is fixed for the
	// rest of the session: tool-call IDs are vendor-specific, so
	// mid-conversation failover would corrupt the history. This is NOT
	// the general-purpose FallbackChain (buildFallbackChain) other AI
	// commands use — that abstraction has no notion of "only before any
	// tool_use ID exists," so reusing it here would risk exactly the
	// mid-session switch this feature must never do.
	candidates, pErr := resolveChatProviderChain(ctx, ai, flags.model, roundTimeout)
	if pErr != nil {
		return fmt.Errorf("chat: %w", pErr)
	}
	prov := candidates[0].prov
	if err := ensureRemoteAllowed(prov, ai); err != nil {
		return fmt.Errorf("chat: %w", err)
	}

	// Limits and deny surface. Limits come from the GLOBAL config only
	// (repo-local raising them is the init.ai_gitignore attack shape);
	// the deny list is a union across defaults + commit denies + global
	// chat extras, so no layer can shrink it.
	defaults := config.Defaults()
	maxRounds, resultCap, denyExtra := config.GlobalChatSettings()
	if maxRounds == 0 {
		maxRounds = defaults.AI.Chat.MaxToolRounds
	}
	if resultCap == 0 {
		resultCap = defaults.AI.Chat.ToolResultCap
	}
	deny := unionGlobs(config.DefaultDenyPaths(), cfg.AI.Commit.DenyPaths, denyExtra)

	sandbox, sbErr := tools.NewSandbox(repoRoot, deny)
	if sbErr != nil {
		return fmt.Errorf("chat: %w", sbErr)
	}
	// Every tool result is scrubbed before it reaches the provider or the
	// session file. MaxSecrets:-1 — redact always, never abort a live
	// conversation over the finding count.
	redactor := func(s string) string {
		red, _, _ := aicommit.Redact(s, aicommit.PrivacyGateOptions{
			DenyPaths:      deny,
			SecretPatterns: vendorSecretPatterns,
			MaxSecrets:     -1,
		})
		return red
	}
	registry := tools.NewRegistry(redactor, resultCap)
	gitTools := &tools.GitTools{Runner: runner, Sandbox: sandbox, DenyGlobs: deny}
	tools.RegisterGitTools(registry, gitTools)
	tools.RegisterStatusTools(registry, gitTools)
	tools.RegisterFileTools(registry, &tools.FileTools{Sandbox: sandbox})
	tools.RegisterContextTools(registry, func(ctx context.Context) (string, error) {
		return chatContextJSONString(ctx, runner, cfg, deny)
	})

	// REPO_CONTEXT is gk context's own collector (collectContext), projected
	// down to a token-budget-sensitive core — branch, upstream, ahead/
	// behind, dirty summary, in-progress rebase/merge, base drift, latest
	// tag, worktree count — NOT aichat's shallower branch/HEAD/status/
	// reflog collector (that one still backs gk ask/do/explain, untouched
	// here). buildChatSystemPrompt below routes it through the SAME
	// redactor every tool result uses before it reaches the model or the
	// session file: untrusted repo data must never leak a secret pattern
	// into the prompt just because it arrived via a collector instead of a
	// tool call.
	repoCtxRaw, rcErr := chatContextJSONString(ctx, runner, cfg, deny)
	if rcErr != nil {
		Dbg("chat: repo context collection failed: %v", rcErr)
		repoCtxRaw = ""
	}
	// REPO_MAP: opt-in (ai.chat.auto_context, default off) directory tree
	// from `git ls-files`, "" when unset/off/unavailable — see
	// chatRepoMapString for the full degrade contract. It takes the same
	// deny list the tools enforce: redaction hides secret values, not the
	// filenames a tree is made of.
	repoMapRaw := chatRepoMapString(ctx, runner, cfg, deny)

	sess, contWarn := openChatSession(ctx, runner, flags.cont, flags.session)
	if sess == nil {
		return fmt.Errorf("chat: cannot create session under .git — is the repository writable?")
	}
	if contWarn != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), stylizeHintLine("hint: "+contWarn))
	}

	engine := &chat.Engine{
		// Caller starts as the primary candidate; runFirstChatTurn may
		// swap it to a later candidate if the primary fails to start the
		// session (chat.ErrFirstRoundFailed) — see the oneShot/REPL
		// dispatch below.
		Caller:        remoteGuardedCaller{inner: candidates[0].caller, prov: candidates[0].prov, flags: cmd.Flags()},
		Registry:      registry,
		SystemPrompt:  buildChatSystemPrompt(repoCtxRaw, repoMapRaw, redactor, lang, EasyEngine().IsEnabled()),
		MaxTokens:     aiChatMaxTokens(ai),
		MaxToolRounds: maxRounds,
		RoundTimeout:  roundTimeout,
		// History replay budget (~tokens, ai.chat.history_budget, default
		// 32768). Provider windows comfortably exceed this; trimming
		// protects cost, not correctness.
		HistoryBudget: aiChatHistoryBudget(ai),
		Session:       sess,
		Dbg:           Dbg,
	}
	// historySeed carries prior user turns into the REPL's ↑/↓ arrow
	// history — x/term.Terminal's History is scoped to the one live
	// process, so without this a --continue/--session session's own past
	// questions are invisible to the arrow keys until the user types
	// something new in the resumed process.
	var historySeed []string
	if flags.cont || flags.session != "" {
		if msgs, skipped, rErr := sess.Replay(); rErr == nil {
			engine.LoadHistory(msgs)
			historySeed = chatHistorySeed(msgs)
			if skipped > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "chat: %d corrupted session line(s) skipped\n", skipped)
			}
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "chat: could not replay session %s: %v — starting fresh\n", sess.ID, rErr)
		}
	}

	Dbg("chat: provider=%s model=%s session=%s rounds=%d cap=%d deny=%d candidates=%d",
		prov.Name(), providerModel(prov), sess.ID, maxRounds, resultCap, len(deny), len(candidates))

	if oneShot {
		return runChatOneShotWithFallback(cmd, engine, candidates, sess, lang, strings.Join(args, " "))
	}
	return runChatREPL(cmd, engine, candidates, sess, lang, historySeed)
}

// chatHistorySeed extracts prior user turns, in chronological order, from
// a replayed session — the input the REPL's arrow-key history should be
// primed with. Pure slice extraction (no I/O), kept separate from
// chatLineReader so it's unit-testable without a live terminal.
func chatHistorySeed(msgs []provider.ChatMessage) []string {
	var seed []string
	for _, m := range msgs {
		// Skip engine-synthesized user messages (the /compact intro): the
		// user never typed them, so they must not surface under ↑/↓.
		if m.Role == "user" && m.Text != "" && !chat.IsSyntheticUserMessage(m.Text) {
			seed = append(seed, m.Text)
		}
	}
	return seed
}

// unionGlobs merges deny glob lists, deduplicating while keeping order.
func unionGlobs(lists ...[]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, l := range lists {
		for _, g := range l {
			g = strings.TrimSpace(g)
			if g == "" || seen[g] {
				continue
			}
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}

// chatCandidate pairs a resolved provider with its ToolCaller adapter —
// one entry in gk chat's session-start fallback chain (see
// resolveChatProviderChain and runFirstChatTurn).
type chatCandidate struct {
	prov   provider.Provider
	caller provider.ToolCaller
}

// resolveChatProviderChain resolves the ordered list of candidates gk
// chat's session-start fallback may try: the configured provider alone
// when ai.Provider/--provider is set (and it must support tool calling —
// an explicit choice is never silently overridden by auto-detect, same
// as resolveChatProvider always guaranteed), else every ToolCaller-
// capable, Available() provider in auto-detect order.
//
// roundTimeout is applied to BOTH Timeout and RetryBudget on every
// candidate — two independent FactoryOptions fields (see
// provider.Nvidia.RetryBudget's docstring), raised together here:
//
//   - Timeout is the per-attempt HTTP timeout. provider.NewDefaultHTTPClient
//     builds it as http.Client{Timeout: ...}, and http.Client.Timeout bounds
//     the ENTIRE round trip including reading the response body — for an
//     SSE stream, that one stream IS the whole answer. Leaving Timeout at
//     the provider's short do/ask-sized default (ai.<provider>.timeout,
//     ~30-60s) truncates any chat round whose model legitimately takes
//     longer than that to finish (a bug this replaces: for a few releases
//     Timeout was left untouched here and only RetryBudget was raised,
//     which fixed the ORIGINAL minTimeout regression below but reopened
//     this one — a single slow-but-successful response over the short
//     default now got cut off mid-stream regardless of RetryBudget
//     headroom). It is raised to a FLOOR of roundTimeout, not a fixed
//     value: an explicitly configured ai.<provider>.timeout larger than
//     roundTimeout is left alone.
//   - RetryBudget separately widens the retry LOOP's overall deadline
//     (every attempt plus backoff) to match the round, so a burst of
//     fast-failing 5xx retries can't outlive it.
//
// Be precise about what this pair does and does not buy, because getting
// that wrong is what produced both regressions above. In THIS path the two
// end up equal, so RetryBudget adds no bound the round context doesn't
// already impose — a single hung attempt can still spend the whole round.
// Nothing here prevents that, and nothing should: a total timeout cannot
// distinguish "hung" from "legitimately slow", and cutting attempts short
// to tell them apart is exactly the bug that truncated real answers. The
// protection that DOES exist is liveness-shaped and lives elsewhere: the
// engine's per-round context, and provider.streamAttemptContext, which
// spends only part of the round on the streaming attempt so the non-stream
// fallback still has time to run. RetryBudget stays a distinct field
// because callers other than chat set Timeout well below it.
func resolveChatProviderChain(ctx context.Context, ai config.AIConfig, modelOverride string, roundTimeout time.Duration) ([]chatCandidate, error) {
	build := func(cfg config.AIConfig) (provider.Provider, error) {
		opts := aiFactoryOptionsWithModel(cfg, modelOverride)
		if opts.Timeout < roundTimeout {
			opts.Timeout = roundTimeout
		}
		opts.RetryBudget = roundTimeout
		return provider.NewProvider(ctx, opts)
	}
	if ai.Provider != "" {
		p, err := build(ai)
		if err != nil {
			return nil, fmt.Errorf("provider: %w", err)
		}
		tc, ok := p.(provider.ToolCaller)
		if !ok {
			return nil, fmt.Errorf("provider %q does not support tool calling — chat needs anthropic/openai/groq/nvidia; use `gk ask` for %q", p.Name(), p.Name())
		}
		return []chatCandidate{{prov: p, caller: tc}}, nil
	}
	var candidates []chatCandidate
	for _, name := range aiAutoOrder {
		cfg := ai
		cfg.Provider = name
		p, err := build(cfg)
		if err != nil {
			continue
		}
		tc, ok := p.(provider.ToolCaller)
		if !ok {
			continue
		}
		if err := p.Available(ctx); err != nil {
			Dbg("chat: skip %s: %v", name, err)
			continue
		}
		candidates = append(candidates, chatCandidate{prov: p, caller: tc})
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no tool-calling provider available (need anthropic/openai/groq/nvidia with an API key) — run `gk doctor` for setup hints")
	}
	return candidates, nil
}

// errChatFallbackExhausted marks gk chat's session-start fallback chain
// exhausted: every candidate provider failed at round 0 of a virgin
// session (chat.ErrFirstRoundFailed on every one of them). Wrapped with
// each candidate's own reason so the caller can list them (see
// runFirstChatTurn); chatOneShotFailure/agentState() map it to a
// distinct, doctor-clearable failure instead of a bare "error".
var errChatFallbackExhausted = errors.New("chat: every provider failed to start the session")

// runFirstChatTurn runs a brand-new session's very first turn across the
// resolved candidate chain, advancing to the next candidate ONLY on
// chat.ErrFirstRoundFailed (round 0 of a still-empty session — see that
// sentinel's docstring). engine.History() stays empty across every
// failed attempt (RunTurn rolls a failed turn back to where it started),
// so swapping engine.Caller between attempts is safe: nothing vendor-
// specific (a tool_use ID) has been generated yet. A LATER round's
// failure (round > 0 of THIS turn, or any failure once turnStart > 0 —
// i.e. a --continue'd session that already has history) is returned
// as-is with NO further candidates tried: the session has already
// committed to whichever provider got that far, and switching now would
// corrupt vendor-specific tool_use IDs already in play.
func runFirstChatTurn(cmd *cobra.Command, engine *chat.Engine, candidates []chatCandidate, lang, input string) (res *chat.TurnResult, prov provider.Provider, turnUI *chatTurnUI, err error) {
	var reasons []string
	for i, c := range candidates {
		engine.Caller = remoteGuardedCaller{inner: c.caller, prov: c.prov, flags: cmd.Flags()}
		turnUI = &chatTurnUI{out: cmd.OutOrStdout(), label: chatSpinnerMessage(lang, c.prov.Name())}
		res, err = runChatTurn(cmd, engine, turnUI, input)
		if err == nil {
			return res, c.prov, turnUI, nil
		}
		if !errors.Is(err, chat.ErrFirstRoundFailed) {
			return nil, c.prov, turnUI, err
		}
		// First-round failure. With a SINGLE candidate there is no other
		// provider to fall back to, so wrapping it as errChatFallbackExhausted
		// (which classifies as no_provider/blocked) would mask the real cause.
		// engine wraps the underlying error inside ErrFirstRoundFailed with a
		// double %w, so returning err as-is lets classifyChatTurnError still
		// see a round_timeout / max_rounds through the chain and report it
		// accurately. Only a genuine multi-provider sweep that ALL fail earns
		// the no_provider verdict below.
		if len(candidates) == 1 {
			return nil, c.prov, turnUI, err
		}
		reasons = append(reasons, fmt.Sprintf("%s: %v", c.prov.Name(), err))
		if i < len(candidates)-1 {
			Dbg("chat: %s failed to start the session, trying next provider: %v", c.prov.Name(), err)
		}
		prov = c.prov
	}
	return nil, prov, turnUI, fmt.Errorf("%w:\n  - %s", errChatFallbackExhausted, strings.Join(reasons, "\n  - "))
}

// remoteGuardedCaller re-checks the remote policy before EVERY provider
// round — a long-lived REPL must notice when the user flips
// ai.commit.allow_remote off mid-session (the log --ai gate-ordering
// lesson applied to a session-shaped surface). The config re-read is a
// local file parse; its cost is noise next to the HTTP call.
type remoteGuardedCaller struct {
	inner provider.ToolCaller
	prov  provider.Provider
	// flags is the command's FlagSet from startup — the reload must see
	// the SAME config the session started with (--repo included), not a
	// flag-less view of the current working directory.
	flags *pflag.FlagSet
}

func (r remoteGuardedCaller) ChatWithTools(ctx context.Context, in provider.ChatInput) (provider.ChatResult, error) {
	// Fail CLOSED: a config that stops parsing mid-session must stop
	// remote calls, not silently keep the last-known permission (the
	// GlobalConfigHealthy lesson — safety settings never degrade to
	// "allowed" on a broken file).
	cfg, err := config.Load(r.flags)
	if err != nil {
		return provider.ChatResult{}, fmt.Errorf("remote policy re-check: config unreadable: %w", err)
	}
	if gErr := ensureRemoteAllowed(r.prov, cfg.AI); gErr != nil {
		return provider.ChatResult{}, gErr
	}
	return r.inner.ChatWithTools(ctx, in)
}

// Summarize lets remoteGuardedCaller double as a provider.Summarizer —
// engine.Caller.(provider.Summarizer) is exactly how /compact reaches the
// SESSION'S OWN provider (see chat.Engine.Compact's docstring on why it
// must never be a different vendor) while still going through the same
// fail-closed remote-policy re-check every ChatWithTools round gets.
func (r remoteGuardedCaller) Summarize(ctx context.Context, in provider.SummarizeInput) (provider.SummarizeResult, error) {
	cfg, err := config.Load(r.flags)
	if err != nil {
		return provider.SummarizeResult{}, fmt.Errorf("remote policy re-check: config unreadable: %w", err)
	}
	if gErr := ensureRemoteAllowed(r.prov, cfg.AI); gErr != nil {
		return provider.SummarizeResult{}, gErr
	}
	sum, ok := r.prov.(provider.Summarizer)
	if !ok {
		return provider.SummarizeResult{}, fmt.Errorf("provider %q does not support Summarize", r.prov.Name())
	}
	return sum.Summarize(ctx, in)
}

// openChatSession opens the --session target, the --continue (last-session)
// target, or creates a fresh session — in that priority order (runChat
// already refuses passing both --session and --continue together). A
// missing/corrupt requested session degrades to a new one with a warning
// string — never a fatal error. Opening either target re-marks it as the
// --continue pointer (chat.OpenSession's existing behavior), so resuming an
// older session via --session also makes it the next bare --continue's
// target — the same "last touched wins" rule --continue already followed.
func openChatSession(ctx context.Context, runner git.Runner, cont bool, sessionID string) (*chat.Session, string) {
	warn := ""
	switch {
	case sessionID != "":
		if s, err := chat.OpenSession(ctx, runner, sessionID); err == nil {
			return s, ""
		}
		warn = fmt.Sprintf("session %q could not be opened — starting a new one", sessionID)
	case cont:
		if id := chat.LastSessionID(ctx, runner); id != "" {
			if s, err := chat.OpenSession(ctx, runner, id); err == nil {
				return s, ""
			}
			warn = fmt.Sprintf("previous session %q could not be opened — starting a new one", id)
		} else {
			warn = "no previous chat session found — starting a new one"
		}
	}
	id := time.Now().UTC().Format("20060102-150405") + fmt.Sprintf("-%d", os.Getpid())
	s, err := chat.NewSession(ctx, runner, id)
	if err != nil {
		return nil, warn
	}
	return s, warn
}

// ── turn execution (shared by REPL and one-shot) ─────────────────────

// chatTurnUI prints the one-line tool transparency feed and manages the
// thinking spinner around provider rounds. The spinner label is LIVE:
// elapsed seconds tick every frame and the token counter updates as each
// round's usage arrives (atomics — the label callback runs on the
// spinner's render goroutine).
type chatTurnUI struct {
	out      io.Writer
	spin     func()
	spinning bool
	label    string
	calls    []chatToolCallLog
	start    time.Time
	tokens   atomic.Int64
	rounds   atomic.Int64
	approx   atomic.Bool
	// streamed accumulates text delivered via onTextDelta for the round
	// CURRENTLY in flight. onToolCall resets it: engine.RunTurn's only
	// text-only (no tool_calls) round is always the LAST one of a turn
	// (any earlier round with no tool_calls would itself have ended the
	// turn), so any text streamed before a tool call is leaked preamble
	// for a round that turned out to need tools, never the final
	// answer — forgetting it keeps the post-turn "was this already
	// printed?" check (textAlreadyPrinted) scoped to the final round.
	streamed strings.Builder
}

// onTextDelta streams one text chunk straight to the terminal as it
// arrives — the opt-in path wired only when !JSONOut() (runChatTurn).
// Stopping the spinner on every chunk (not just the first) matters
// because a later round's first chunk arrives after onToolResult
// restarted the spinner for the round that just dispatched tools.
func (u *chatTurnUI) onTextDelta(s string) {
	u.stopSpin()
	u.streamed.WriteString(s)
	fmt.Fprint(u.out, s)
}

// onStreamReset fires when the adapter abandons a streaming round for the
// non-stream fallback AFTER text already reached the terminal. The printed
// bytes can't be unprinted, but the stale partial line is terminated with a
// newline (so the fallback answer starts fresh instead of concatenating
// onto it) and streamed is cleared — that makes textAlreadyPrinted return
// false, so runChatTurn's caller re-prints the authoritative fallback reply
// in full rather than assuming the partial already covered it.
func (u *chatTurnUI) onStreamReset() {
	if u.streamed.Len() == 0 {
		return
	}
	fmt.Fprintln(u.out)
	u.streamed.Reset()
}

// textAlreadyPrinted reports whether text is exactly what streamed to
// the terminal via onTextDelta this turn — true only when the FINAL
// round streamed cleanly start to finish with no tool_use/fallback in
// between. Callers use this to skip re-printing/re-boxing an answer
// that's already fully on screen.
func (u *chatTurnUI) textAlreadyPrinted(text string) bool {
	return u.streamed.Len() > 0 && strings.TrimSpace(u.streamed.String()) == strings.TrimSpace(text)
}

// onRound receives the engine's cumulative token count after each
// provider reply.
func (u *chatTurnUI) onRound(round, tokensSoFar int, approx bool) {
	u.rounds.Store(int64(round))
	u.tokens.Store(int64(tokensSoFar))
	if approx {
		u.approx.Store(true)
	}
}

// liveLabel renders "chat - openai 탐색 중 · 12s · ~3.4k tok" for the
// spinner. Tokens appear once the first round reports them.
func (u *chatTurnUI) liveLabel() string {
	s := fmt.Sprintf("%s · %ds", u.label, int(time.Since(u.start).Seconds()))
	if t := u.tokens.Load(); t > 0 {
		s += " · " + formatChatTokens(t, u.approx.Load()) + " tok"
	}
	return s
}

func (u *chatTurnUI) startSpin() {
	if u.spinning || Debug() || JSONOut() {
		return
	}
	u.spin = ui.StartBubbleSpinnerLive(u.liveLabel)
	u.spinning = true
}

func (u *chatTurnUI) stopSpin() {
	if u.spinning && u.spin != nil {
		u.spin()
	}
	u.spinning = false
}

func (u *chatTurnUI) onToolCall(call provider.ToolCall) {
	u.stopSpin()
	if u.streamed.Len() > 0 {
		// Terminate the leaked-preamble line (see streamed's docstring)
		// before the tool-call transparency line so the two don't run
		// together on one terminal line.
		fmt.Fprintln(u.out)
		u.streamed.Reset()
	}
	if !JSONOut() {
		faint := color.New(color.Faint).SprintFunc()
		fmt.Fprintln(u.out, faint("  ▸ "+call.Name+" "+compactJSON(call.Input, 80)))
	}
}

func (u *chatTurnUI) onToolResult(call provider.ToolCall, res provider.ToolResult) {
	if res.IsError && !JSONOut() {
		fmt.Fprintln(u.out, color.New(color.Faint, color.FgYellow).Sprint("    ✕ "+firstLine(res.Content)))
	}
	u.calls = append(u.calls, chatToolCallLog{Name: call.Name, Input: call.Input, IsError: res.IsError})
	u.startSpin()
}

// runChatTurn executes one turn with Ctrl-C canceling ONLY the turn: the
// signal handler is installed for the turn's duration and removed after,
// so at the prompt Ctrl-C keeps its default meaning (exit — the session
// file is already durable at every point).
func runChatTurn(cmd *cobra.Command, engine *chat.Engine, ui *chatTurnUI, input string) (*chat.TurnResult, error) {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt)
	defer signal.Stop(sigc)
	go func() {
		select {
		case <-sigc:
			cancel()
		case <-ctx.Done():
		}
	}()

	engine.OnToolCall = ui.onToolCall
	engine.OnToolResult = ui.onToolResult
	engine.OnRound = ui.onRound
	// Text streaming is opt-in and gated on JSONOut() (which is also
	// true under GK_AGENT — root.go's init sets flagJSON alongside
	// flagAgent): GK_AGENT/--json need the exact machine-readable
	// envelope on stdout, not tokens interleaved with it. Assigned on
	// every call (not just once) because engine is reused across an
	// entire REPL session — JSONOut() cannot change mid-process, but
	// keeping this next to the other three callback wires avoids a
	// stale nil/non-nil assumption if that ever changes.
	if !JSONOut() {
		engine.OnTextDelta = ui.onTextDelta
		engine.OnStreamReset = ui.onStreamReset
	} else {
		engine.OnTextDelta = nil
		engine.OnStreamReset = nil
	}
	ui.start = time.Now()
	ui.startSpin()
	defer ui.stopSpin()
	return engine.RunTurn(ctx, input)
}

// turnStatsLine renders the completion footer: elapsed, tokens (with the
// approx marker when any round lacked provider usage), rounds, tools.
func turnStatsLine(u *chatTurnUI, res *chat.TurnResult) string {
	elapsed := time.Since(u.start)
	s := fmt.Sprintf("⏱ %.1fs", elapsed.Seconds())
	if res.TokensUsed > 0 {
		s += " · " + formatChatTokens(int64(res.TokensUsed), res.TokensApprox) + " tokens"
	}
	s += fmt.Sprintf(" · %d round(s)", res.Rounds)
	if res.ToolCalls > 0 {
		s += fmt.Sprintf(" · %d tool(s)", res.ToolCalls)
	}
	return s
}

// formatTokens renders a token count compactly ("342", "3.4k"), prefixing
// "~" when the number contains estimated rounds.
func formatChatTokens(n int64, approx bool) string {
	var s string
	switch {
	case n >= 999_950: // %.1fk would round to "1000.0k"
		s = fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		s = fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		s = fmt.Sprintf("%d", n)
	}
	if approx {
		return "~" + s
	}
	return s
}

func runChatOneShot(cmd *cobra.Command, engine *chat.Engine, prov provider.Provider, sess *chat.Session, lang, question string) error {
	turnUI := &chatTurnUI{out: cmd.OutOrStdout(), label: chatSpinnerMessage(lang, prov.Name())}
	res, err := runChatTurn(cmd, engine, turnUI, question)
	if err != nil {
		return chatOneShotFailure(cmd, engine, prov, sess, turnUI, lang, question, err)
	}
	return renderChatOneShotSuccess(cmd, engine, prov, sess, lang, turnUI, res)
}

// renderChatOneShotSuccess prints/emits a completed one-shot turn's
// answer — split out of runChatOneShot so runChatOneShotWithFallback can
// share it after resolving which candidate provider actually answered
// (that turn already ran inside runFirstChatTurn, so it must not run
// again here).
func renderChatOneShotSuccess(cmd *cobra.Command, engine *chat.Engine, prov provider.Provider, sess *chat.Session, lang string, turnUI *chatTurnUI, res *chat.TurnResult) error {
	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), chatJSONResult{
			Answer:       res.Text,
			ToolCalls:    turnUI.calls,
			Provider:     prov.Name(),
			Model:        res.Model,
			Lang:         lang,
			SessionID:    sess.ID,
			Rounds:       res.Rounds,
			TokensUsed:   res.TokensUsed,
			TokensApprox: res.TokensApprox,
			DurationMS:   time.Since(turnUI.start).Milliseconds(),
		})
	}
	out := cmd.OutOrStdout()
	if turnUI.textAlreadyPrinted(res.Text) {
		// The answer already streamed straight to the terminal via
		// onTextDelta — re-running it through emitAIAdvice's boxed
		// section would duplicate it (and the box needs the full text
		// up front anyway, which defeats token-at-a-time display).
		// Just terminate the streamed line before the stats footer.
		fmt.Fprintln(out)
	} else {
		emitAIAdvice(out, "gk chat", res.Text)
	}
	fmt.Fprintln(out, color.New(color.Faint).Sprint(turnStatsLine(turnUI, res)))
	fmt.Fprintln(out, stylizeHintLine("hint: gk chat --continue 로 이 대화를 이어갈 수 있습니다"))
	if warn := chatBudgetWarningLine(engine, isKoLang(lang)); warn != "" {
		fmt.Fprintln(cmd.OutOrStdout(), stylizeHintLine("hint: "+warn))
	}
	return nil
}

// runChatOneShotWithFallback runs gk chat's one-and-only turn across the
// resolved candidate chain (resolveChatProviderChain + runFirstChatTurn)
// — the one-shot counterpart to runChatREPL's first-turn handling.
func runChatOneShotWithFallback(cmd *cobra.Command, engine *chat.Engine, candidates []chatCandidate, sess *chat.Session, lang, question string) error {
	res, prov, turnUI, err := runFirstChatTurn(cmd, engine, candidates, lang, question)
	if err != nil {
		return chatOneShotFailure(cmd, engine, prov, sess, turnUI, lang, question, err)
	}
	return renderChatOneShotSuccess(cmd, engine, prov, sess, lang, turnUI, res)
}

// chatOneShotFailure handles a one-shot turn that returned an error instead
// of a TurnResult. gk's agent-mode contract (agent_envelope.go) is ok |
// paused | blocked | error with machine-executable error.remedies[] — before
// this, EVERY chat failure fell through as a bare wrapped error, so agent
// mode saw state:"error" with no remedies even for chat.ErrMaxRounds, a
// precondition an agent can clear by itself (narrow the question, raise
// ai.chat.max_tool_rounds). classifyChatTurnError (shared with runChatREPL)
// names the failure modes this decorates:
//
//   - chat.ErrMaxRounds → blocked, with remedies to raise the round budget
//     or retry with a faster --model.
//   - a round timeout (isDeadlineErr) → error, with the same --model/
//     round_timeout remedies (retrying the identical turn will not help).
//   - the session-start fallback chain exhausted → blocked, pointing at
//     `gk doctor`.
//   - context.Canceled (Ctrl-C) → error, no remedy: the previous code left
//     this unclassified, so failure stayed "" — chatJSONResult's OWN
//     success marker — and a Ctrl-C'd one-shot reported state:"ok" with no
//     answer. Retrying the exact same turn is already what a bare re-run
//     does, so there is nothing to suggest beyond that.
//   - anything else (chatErrOther — a provider 500, an auth failure, a
//     malformed JSON reply, ...) → "error", no remedy: this switch used to
//     have no default case at all, so an unclassified failure ALSO left
//     failure=="" — the same success marker a genuine success leaves — and
//     agentState()'s own default ("" → "ok") turned a bare stdlib error
//     into a reported success with an empty answer. Naming it "error"
//     here is what lets agentState() return envStateError instead of
//     falling through to its "ok" default.
//
// In JSON/agent mode it ALSO emits chatJSONResult on stdout — Answer empty,
// Failure set — carrying whatever the turn produced before it gave up
// (rounds run, tool calls made, tokens spent), same two-channel shape as
// land.go's landResultJSON: the command's own typed result on stdout (state
// via agentState()), the generic error envelope with hint+remedies on
// stderr (main.go, via FormatErrorJSON on the error this returns).
func chatOneShotFailure(cmd *cobra.Command, engine *chat.Engine, prov provider.Provider, sess *chat.Session, turnUI *chatTurnUI, lang, question string, err error) error {
	wrapped := fmt.Errorf("chat: %w", err)
	failure := ""

	switch classifyChatTurnError(err) {
	case chatErrMaxRounds:
		failure = "max_rounds"
		wrapped = WithBlocked(wrapped, "chat-max-rounds",
			"질문을 좁히거나 ai.chat.max_tool_rounds를 올리면 라운드 한도 안에 답할 수 있습니다",
			errRemedy{Command: selfCmd(fmt.Sprintf("config set ai.chat.max_tool_rounds %d", engine.MaxToolRounds*2)), Safety: "safe"},
			errRemedy{Command: selfCmd(fmt.Sprintf("chat --model <faster-model> %q", question)), Safety: "safe"},
		)
	case chatErrRoundTimeout:
		failure = "round_timeout"
		wrapped = WithRemedy(wrapped,
			"한 라운드가 ai.chat.round_timeout을 초과했습니다 — 값을 올리거나 --model로 더 빠른 모델을 지정해 보세요",
			errRemedy{Command: selfCmd(fmt.Sprintf("config set ai.chat.round_timeout %s", (engine.RoundTimeout * 2).String())), Safety: "safe"},
			errRemedy{Command: selfCmd(fmt.Sprintf("chat --model <faster-model> %q", question)), Safety: "safe"},
		)
	case chatErrNoProvider:
		failure = "no_provider"
		wrapped = WithBlocked(wrapped, "chat-no-provider",
			"세션을 시작할 수 있는 provider가 없습니다 — `gk doctor`로 인증/네트워크 상태를 확인하세요",
			errRemedy{Command: selfCmd("doctor"), Safety: "safe"},
		)
	case chatErrCanceled:
		failure = "canceled"
	default:
		// chatErrOther: an unclassified error (provider 500, auth
		// failure, malformed JSON reply, ...). Before this case existed
		// the switch fell through with failure=="" — chatJSONResult's
		// OWN success marker — so agentState() applied its "ok" default
		// to a turn that produced no answer (F1: agent mode reported
		// state:"ok" for a real failure). No remedy: unlike max_rounds/
		// round_timeout/no_provider, there is no config knob that
		// reliably clears an unclassified failure.
		failure = "error"
	}

	if JSONOut() {
		_ = emitAgentResult(cmd.OutOrStdout(), chatJSONResult{
			ToolCalls:    turnUI.calls,
			Provider:     prov.Name(),
			Lang:         lang,
			SessionID:    sess.ID,
			Rounds:       int(turnUI.rounds.Load()),
			TokensUsed:   int(turnUI.tokens.Load()),
			TokensApprox: turnUI.approx.Load(),
			DurationMS:   time.Since(turnUI.start).Milliseconds(),
			Failure:      failure,
		})
	}
	return wrapped
}

// chatLineReader returns the REPL's line source. On a real terminal it uses a
// vendored, wide-rune-aware fork of x/term's line editor (internal/lineedit),
// which gives shell-style history (↑/↓ walk previous questions) and inline
// editing while — unlike bubbletea's inline renderer — leaving the hardware
// cursor at the caret so terminal IME preedit (한글 조합) composes in place, and
// unlike stock x/term, deleting wide runes leaves no half-glyph residue. Raw
// mode is entered ONLY while reading a line and restored before the turn runs,
// so Ctrl-C still raises SIGINT during provider calls (turn cancellation); at
// the prompt the editor maps Ctrl-C and empty-line Ctrl-D to io.EOF, which the
// loop treats as a graceful exit. A non-TTY stdin or stdout (tests, pipes,
// redirects) falls back to a plain scanner (seed and Tab-completion are both
// no-ops there — a scanner has no history or cursor to complete against).
//
// seed primes the arrow-key history with a --continue session's prior user
// turns, oldest first: lineedit.History.Add appends the most-recent entry, so
// replaying in chronological order leaves the LAST past question one ↑ press
// away, exactly like a session that never restarted.
func chatLineReader(cmd *cobra.Command, prompt string, seed []string) func() (string, error) {
	stdin, okIn := cmd.InOrStdin().(*os.File)
	stdout, okOut := cmd.OutOrStdout().(*os.File)
	interactive := okIn && okOut &&
		term.IsTerminal(int(stdin.Fd())) && term.IsTerminal(int(stdout.Fd()))
	if !interactive {
		// ONE scanner for the whole loop — recreating it per prompt makes
		// the buffered reader swallow the next line (ai_do.go's confirm).
		scanner := bufio.NewScanner(cmd.InOrStdin())
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		out := cmd.OutOrStdout()
		return func() (string, error) {
			fmt.Fprint(out, prompt)
			if !scanner.Scan() {
				if err := scanner.Err(); err != nil {
					return "", err
				}
				return "", io.EOF
			}
			return scanner.Text(), nil
		}
	}
	fd := int(stdin.Fd())
	t := lineedit.NewTerminal(struct {
		io.Reader
		io.Writer
	}{stdin, stdout}, prompt)
	// Feed the real terminal width so line wrapping and cursor math line up
	// on wide windows (the editor otherwise assumes 80 columns).
	if w, h, err := term.GetSize(fd); err == nil {
		_ = t.SetSize(w, h)
	}
	for _, line := range seed {
		t.History.Add(line)
	}
	t.AutoCompleteCallback = chatAutoComplete
	return func() (string, error) {
		old, err := term.MakeRaw(fd)
		if err != nil {
			return "", err
		}
		defer func() { _ = term.Restore(fd, old) }()
		return t.ReadLine()
	}
}

// chatMetaCommands lists the REPL's slash commands — the single source of
// truth Tab-completion matches against (printChatHelp's text is free-form
// and not derived from this, so keep the two in sync by hand on change).
var chatMetaCommands = []string{"/clear", "/compact", "/exit", "/help", "/quit", "/rename", "/tokens"}

// chatRenameArg parses a "/rename <title>" REPL line. ok reports whether the
// line was a /rename invocation at all — false for anything else,
// including a line that merely starts with the substring "/rename" without
// the required space or exact match (e.g. "/renamed"), which the caller
// should treat as an unrecognized command rather than a bad rename. A true
// with an empty title means "/rename" was typed with no argument — usage,
// not an error.
func chatRenameArg(line string) (title string, ok bool) {
	switch {
	case line == "/rename":
		return "", true
	case strings.HasPrefix(line, "/rename "):
		return strings.TrimSpace(strings.TrimPrefix(line, "/rename ")), true
	default:
		return "", false
	}
}

// chatAutoComplete is lineedit's AutoCompleteCallback: it fires on every key
// that isn't otherwise handled by the line editor, so the first job is
// ignoring everything except Tab — regular typing must fall through
// unchanged (ok=false re-processes the key normally).
func chatAutoComplete(line string, pos int, key rune) (newLine string, newPos int, ok bool) {
	if key != '\t' {
		return line, pos, false
	}
	return completeChatCommand(line, pos)
}

// completeChatCommand computes Tab-completion for one REPL input line.
// It only fires for a bare slash command with the cursor at the end of the
// line — completing mid-line ("/help<cursor> extra") or a line that isn't
// a slash command at all is left untouched. A single unambiguous prefix
// match completes to the full command; zero or multiple matches (or a
// line that's already a complete command) leave the line as-is.
func completeChatCommand(line string, pos int) (newLine string, newPos int, ok bool) {
	if pos != len(line) || !strings.HasPrefix(line, "/") {
		return line, pos, false
	}
	match := ""
	matches := 0
	for _, c := range chatMetaCommands {
		if strings.HasPrefix(c, line) {
			match = c
			matches++
		}
	}
	if matches != 1 || match == line {
		return line, pos, false
	}
	return match, len(match), true
}

func runChatREPL(cmd *cobra.Command, engine *chat.Engine, candidates []chatCandidate, sess *chat.Session, lang string, historySeed []string) error {
	out := cmd.OutOrStdout()
	ko := isKoLang(lang)
	prov := candidates[0].prov
	printChatWelcome(out, prov, sess, ko)

	prompt := color.New(color.Bold, color.FgCyan).Sprint("gk chat › ")
	readLine := chatLineReader(cmd, prompt, historySeed)

	// firstTurnPending gates the session-start fallback to exactly the
	// REPL's first real turn (meta commands below never touch it) — once
	// it runs once, engine.Caller is fixed for the rest of the process,
	// matching runFirstChatTurn's "no mid-session switch" contract.
	firstTurnPending := true

	for {
		input, rErr := readLine()
		if rErr != nil { // Ctrl-D / Ctrl-C at prompt / EOF
			if !errors.Is(rErr, io.EOF) {
				fmt.Fprintf(cmd.ErrOrStderr(), "chat: input: %v\n", rErr)
			}
			fmt.Fprintln(out)
			printChatBye(out, sess, ko)
			return nil
		}
		line := strings.TrimSpace(input)
		renameTitle, isRename := chatRenameArg(line)
		switch {
		case line == "":
			continue
		case line == "/exit" || line == "/quit":
			printChatBye(out, sess, ko)
			return nil
		case line == "/clear":
			if cErr := engine.ClearHistory(); cErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "%v\n", cErr)
			}
			if ko {
				fmt.Fprintln(out, "대화 맥락을 비웠습니다 (세션 파일은 유지).")
			} else {
				fmt.Fprintln(out, "conversation context cleared (session file kept).")
			}
			continue
		case line == "/help":
			printChatHelp(out, ko)
			continue
		case line == "/tokens":
			printChatTokens(out, engine, ko)
			continue
		case line == "/compact":
			handleChatCompact(cmd, engine, lang, ko)
			continue
		case isRename:
			if renameTitle == "" {
				if ko {
					fmt.Fprintln(out, "사용법: /rename <제목>")
				} else {
					fmt.Fprintln(out, "usage: /rename <title>")
				}
			} else if rErr := sess.SetTitle(renameTitle); rErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "chat: rename failed — %v\n", rErr)
			} else if ko {
				fmt.Fprintf(out, "세션 제목을 %q로 바꿨습니다.\n", renameTitle)
			} else {
				fmt.Fprintf(out, "session renamed to %q.\n", renameTitle)
			}
			continue
		case strings.HasPrefix(line, "/"):
			if ko {
				fmt.Fprintf(out, "알 수 없는 명령 %s — /help 참고\n", line)
			} else {
				fmt.Fprintf(out, "unknown command %s — see /help\n", line)
			}
			continue
		}

		var turnUI *chatTurnUI
		var res *chat.TurnResult
		var err error
		if firstTurnPending {
			// The session's very first turn tries the whole candidate
			// chain (round-0-only fallback); prov is updated to whichever
			// candidate actually answered so every LATER turn's spinner/
			// stats label the right provider.
			res, prov, turnUI, err = runFirstChatTurn(cmd, engine, candidates, lang, line)
			// Only a SUCCESSFUL first turn retires the fallback gate.
			// RunTurn rolls a failed turn back to empty history (its own
			// guarantee — see chat.Engine.RunTurn's docstring), so a
			// session that failed on every candidate is still exactly as
			// virgin as it was before this attempt: the NEXT question
			// deserves the full candidate chain again, not a permanent
			// pin to whichever provider happened to fail last. Before
			// this check existed, firstTurnPending dropped to false
			// unconditionally, so an all-candidates-failed first turn
			// silently disabled the fallback for the rest of the
			// session — every later question retried the single
			// leftover engine.Caller from the failed attempt with no
			// fallback at all (F4).
			if err == nil {
				firstTurnPending = false
				if prov.Name() != candidates[0].prov.Name() {
					Dbg("chat: session-start fallback selected %s (primary %s failed to start)", prov.Name(), candidates[0].prov.Name())
				}
			}
		} else {
			turnUI = &chatTurnUI{out: out, label: chatSpinnerMessage(lang, prov.Name())}
			res, err = runChatTurn(cmd, engine, turnUI, line)
		}
		if err != nil {
			// classifyChatTurnError is the same classification
			// chatOneShotFailure uses — only the PRESENTATION differs here:
			// the REPL's philosophy is a failed turn never kills the
			// session, so every case just prints and falls through to the
			// shared continue below instead of building a returned error.
			switch classifyChatTurnError(err) {
			case chatErrCanceled:
				fmt.Fprintln(out, color.YellowString("· 턴을 취소했습니다"))
			case chatErrMaxRounds:
				fmt.Fprintf(cmd.ErrOrStderr(), "chat: %v\n", err)
			case chatErrRoundTimeout:
				fmt.Fprintf(cmd.ErrOrStderr(), "chat: %v\n", err)
				fmt.Fprintln(cmd.ErrOrStderr(), stylizeHintLine("hint: 한 라운드가 ai.chat.round_timeout을 초과했습니다 — 프록시/모델이 느리거나 일시 오류를 재시도 중일 수 있습니다. 값을 올리거나(global config) --model로 더 빠른 모델을 지정해 보세요"))
			case chatErrNoProvider:
				fmt.Fprintf(cmd.ErrOrStderr(), "chat: %v\n", err)
			default:
				fmt.Fprintf(cmd.ErrOrStderr(), "chat: %v — 다시 시도하세요\n", err)
			}
			continue // a failed turn never kills the session
		}
		fmt.Fprintln(out)
		if !turnUI.textAlreadyPrinted(res.Text) {
			// Nothing streamed (JSONOut()/GK_AGENT, tool_use interleave
			// on the final round, or a mid-round cutoff) — print the
			// answer the normal, non-streamed way.
			fmt.Fprintln(out, res.Text)
		}
		fmt.Fprintln(out, color.New(color.Faint).Sprint(turnStatsLine(turnUI, res)))
		if warn := chatBudgetWarningLine(engine, ko); warn != "" {
			fmt.Fprintln(out, stylizeHintLine("hint: "+warn))
		}
		fmt.Fprintln(out)
	}
}

// ── small helpers ─────────────────────────────────────────────────────

func chatSpinnerMessage(lang, providerName string) string {
	if isKoLang(lang) {
		return fmt.Sprintf("chat - %s 탐색 중", providerName)
	}
	return fmt.Sprintf("chat - exploring via %s", providerName)
}

func printChatWelcome(w io.Writer, prov provider.Provider, sess *chat.Session, ko bool) {
	faint := color.New(color.Faint).SprintFunc()
	if ko {
		fmt.Fprintf(w, "gk chat — 저장소와 대화합니다 (provider: %s)\n", prov.Name())
		fmt.Fprintln(w, faint("  /help 명령 목록 · /exit 종료 · Ctrl-C 현재 턴 취소 · 세션 "+sess.ID))
	} else {
		fmt.Fprintf(w, "gk chat — talk to your repository (provider: %s)\n", prov.Name())
		fmt.Fprintln(w, faint("  /help commands · /exit to quit · Ctrl-C cancels the turn · session "+sess.ID))
	}
}

func printChatBye(w io.Writer, sess *chat.Session, ko bool) {
	if ko {
		fmt.Fprintf(w, "세션 %s 저장됨 — `gk chat --continue`로 이어갈 수 있습니다\n", sess.ID)
	} else {
		fmt.Fprintf(w, "session %s saved — resume with `gk chat --continue`\n", sess.ID)
	}
}

func printChatHelp(w io.Writer, ko bool) {
	if ko {
		fmt.Fprintln(w, "명령: /help 도움말 · /clear 맥락 초기화 · /rename <제목> 세션 제목 변경 · /tokens 컨텍스트 사용량 · /compact 오래된 턴 요약 · /exit 종료 (Ctrl-D 동일)")
		fmt.Fprintln(w, "턴 진행 중 Ctrl-C는 그 턴만 취소합니다. 프롬프트에서 Ctrl-C는 즉시 종료.")
		fmt.Fprintln(w, "도구: git log/show/diff/blame/grep + 파일 읽기/목록 (전부 읽기 전용)")
		fmt.Fprintln(w, "세션: gk chat sessions 로 목록 확인, gk chat --session <id> 로 재개")
	} else {
		fmt.Fprintln(w, "commands: /help · /clear resets context · /rename <title> renames the session · /tokens shows context usage · /compact folds older turns into a summary · /exit quits (Ctrl-D too)")
		fmt.Fprintln(w, "Ctrl-C during a turn cancels that turn; at the prompt it exits.")
		fmt.Fprintln(w, "tools: git log/show/diff/blame/grep + file read/list (all read-only)")
		fmt.Fprintln(w, "sessions: gk chat sessions to list, gk chat --session <id> to resume")
	}
}

// chatBudgetWarnThreshold is the fraction of Engine.HistoryBudget that
// triggers the automatic one-line end-of-turn warning — 80%, per this
// feature's own spec, so a long investigation session gets a heads-up
// before the NEXT turn's hard trim starts throwing away whole turns.
const chatBudgetWarnThreshold = 0.8

// chatBudgetWarningLine returns the one-line 80%-of-history-budget warning
// to print after a completed turn, or "" when no budget is configured
// (Engine.HistoryBudget <= 0, trimming disabled) or usage is still below
// the threshold — the common case, so callers can print unconditionally
// on a non-empty result.
func chatBudgetWarningLine(engine *chat.Engine, ko bool) string {
	r := engine.TokenUsage()
	if r.BudgetTokens <= 0 || r.Percent() < chatBudgetWarnThreshold {
		return ""
	}
	pct := int(r.Percent() * 100)
	used := formatChatTokens(int64(r.UsedTokens), true)
	budget := formatChatTokens(int64(r.BudgetTokens), true)
	if ko {
		return fmt.Sprintf("history budget %d%% 사용 중(%s / %s) — /compact로 오래된 턴을 요약해 여유를 확보하세요", pct, used, budget)
	}
	return fmt.Sprintf("history budget %d%% used (%s / %s) — run /compact to fold older turns and free up room", pct, used, budget)
}

// printChatTokens renders the `/tokens` report: context composition
// (system prompt / history / tool results, in chars and chars/4-estimated
// tokens) plus the history-budget headroom trimHistory enforces. All
// numbers are the same chars/4 heuristic used everywhere else in gk chat
// (estimateTokens/formatChatTokens), so every figure is prefixed "~".
func printChatTokens(w io.Writer, engine *chat.Engine, ko bool) {
	r := engine.TokenUsage()
	tok := func(n int) string { return formatChatTokens(int64(n), true) }
	faint := color.New(color.Faint).SprintFunc()
	if ko {
		fmt.Fprintln(w, "컨텍스트 구성:")
		fmt.Fprintf(w, "  system prompt   %6d chars   %s tok\n", r.SystemChars, tok(r.SystemTokens))
		fmt.Fprintf(w, "  history         %6d chars   %s tok\n", r.HistoryChars, tok(r.HistoryTokens))
		fmt.Fprintf(w, "  tool results    %6d chars   %s tok\n", r.ToolChars, tok(r.ToolTokens))
		if r.BudgetTokens > 0 {
			fmt.Fprintln(w, faint(fmt.Sprintf("  history budget: %s / %s (%.0f%%) — 80%% 도달 시 자동 경고, 초과 시 오래된 턴부터 트림됩니다",
				tok(r.UsedTokens), tok(r.BudgetTokens), r.Percent()*100)))
		} else {
			fmt.Fprintln(w, faint("  history budget: 설정 없음 (트리밍 비활성)"))
		}
	} else {
		fmt.Fprintln(w, "context composition:")
		fmt.Fprintf(w, "  system prompt   %6d chars   %s tok\n", r.SystemChars, tok(r.SystemTokens))
		fmt.Fprintf(w, "  history         %6d chars   %s tok\n", r.HistoryChars, tok(r.HistoryTokens))
		fmt.Fprintf(w, "  tool results    %6d chars   %s tok\n", r.ToolChars, tok(r.ToolTokens))
		if r.BudgetTokens > 0 {
			fmt.Fprintln(w, faint(fmt.Sprintf("  history budget: %s / %s (%.0f%%) — auto-warns at 80%%, trims oldest turns once over",
				tok(r.UsedTokens), tok(r.BudgetTokens), r.Percent()*100)))
		} else {
			fmt.Fprintln(w, faint("  history budget: unset (trimming disabled)"))
		}
	}
}

// handleChatCompact runs the `/compact` REPL command: it type-asserts
// engine.Caller as a provider.Summarizer (remoteGuardedCaller implements
// it, delegating to the SAME provider driving the rest of the session —
// see its docstring) and calls Engine.Compact with the session's round
// timeout as the deadline. Ctrl-C cancels only this call, mirroring
// runChatTurn's turn-scoped signal handling, so an unexpectedly slow
// summarize call doesn't force the user to kill the whole REPL.
func handleChatCompact(cmd *cobra.Command, engine *chat.Engine, lang string, ko bool) {
	out := cmd.OutOrStdout()
	sum, ok := engine.Caller.(provider.Summarizer)
	if !ok {
		if ko {
			fmt.Fprintln(out, "이 provider는 /compact(요약)를 지원하지 않습니다.")
		} else {
			fmt.Fprintln(out, "this provider does not support /compact (summarization).")
		}
		return
	}

	timeout := engine.RoundTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt)
	defer signal.Stop(sigc)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigc:
			cancel()
		case <-done:
		}
	}()

	stopSpin := func() {}
	if !Debug() && !JSONOut() {
		msg := "chat - compacting…"
		if ko {
			msg = "chat - 대화 압축 중…"
		}
		stopSpin = ui.StartBubbleSpinner(msg)
	}
	res, err := engine.Compact(ctx, sum, lang)
	close(done)
	stopSpin()

	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(out, color.YellowString("· 압축을 취소했습니다"))
			return
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "chat: /compact failed — %v\n", err)
		return
	}
	if !res.Compacted {
		if ko {
			fmt.Fprintln(out, "요약할 만큼 대화가 길지 않습니다 (최근 2개 턴은 항상 원본 그대로 유지됩니다).")
		} else {
			fmt.Fprintln(out, "not enough history to compact yet (the last 2 turns always stay verbatim).")
		}
		return
	}
	before, after := formatChatTokens(int64(res.TokensBefore), true), formatChatTokens(int64(res.TokensAfter), true)
	if ko {
		fmt.Fprintf(out, "대화를 압축했습니다 — %d개 턴을 요약, 최근 %d개 턴은 원본 유지 (%s → %s).\n",
			res.TurnsFolded, res.TurnsKept, before, after)
	} else {
		fmt.Fprintf(out, "compacted %d turn(s) into a summary, keeping the last %d verbatim (%s → %s).\n",
			res.TurnsFolded, res.TurnsKept, before, after)
	}
}

// isDeadlineErr matches a round timeout whether it surfaces as the
// context error itself or wrapped in an adapter's message string (nvidia
// wraps it as "openai: http call: … context deadline exceeded").
func isDeadlineErr(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(err.Error(), "deadline exceeded")
}

// chatTurnErrorKind names the small, fixed set of turn-ending failure
// modes gk chat recognizes. It is the single classification both
// runChatOneShot(WithFallback)'s chatOneShotFailure and runChatREPL branch
// on — before this type existed the two call sites each ran their own
// errors.Is(chat.ErrMaxRounds)/isDeadlineErr/errors.Is(errChatFallbackExhausted)
// switch, and runChatOneShot's copy never even checked context.Canceled, so
// a Ctrl-C'd one-shot fell through to the "no special case" branch — which
// for chatOneShotFailure meant failure stayed "" (chatJSONResult's OWN
// success marker), so agent mode reported state:"ok" for a turn that never
// produced an answer. classifyChatTurnError is now the only place that maps
// an error to a name; each call site only decides how to PRESENT that name.
type chatTurnErrorKind int

const (
	chatErrOther chatTurnErrorKind = iota
	chatErrCanceled
	chatErrMaxRounds
	chatErrRoundTimeout
	chatErrNoProvider
)

// classifyChatTurnError maps a chat.Engine.RunTurn/runFirstChatTurn error to
// its chatTurnErrorKind. context.Canceled is checked first — a turn
// cancelled mid-flight (Ctrl-C) can still, in principle, also look like a
// timeout to a racing goroutine, and "the user asked to stop" should always
// win that race over "a deadline fired."
//
// Every EXACT sentinel check (errors.Is) is ordered before isDeadlineErr's
// substring heuristic, and this order matters: errChatFallbackExhausted's
// message joins each failed candidate's own error text (see its docstring
// and runFirstChatTurn), so when every candidate happened to fail on a
// timeout, that joined text itself contains "deadline exceeded" —
// isDeadlineErr would then match errChatFallbackExhausted-wrapped errors
// too if it ran first. Putting isDeadlineErr LAST among the specific
// checks means it only ever fires when no exact sentinel matched, so a
// fully-exhausted fallback chain always classifies as chatErrNoProvider
// (blocked, doctor-clearable) and never gets misread as chatErrRoundTimeout
// (a plain "error" with no path to a fix) just because its reasons happen
// to mention a deadline.
func classifyChatTurnError(err error) chatTurnErrorKind {
	switch {
	case errors.Is(err, context.Canceled):
		return chatErrCanceled
	case errors.Is(err, chat.ErrMaxRounds):
		return chatErrMaxRounds
	case errors.Is(err, errChatFallbackExhausted):
		return chatErrNoProvider
	case isDeadlineErr(err):
		return chatErrRoundTimeout
	default:
		return chatErrOther
	}
}

// compactJSON renders tool input as a single trimmed line for the feed.
func compactJSON(raw json.RawMessage, max int) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "{}" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
