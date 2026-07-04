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
	"github.com/x-mesh/gk/internal/aichat"
	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/chat"
	"github.com/x-mesh/gk/internal/chat/tools"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
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
대화는 .git/gk-chat/ 아래 저장되며 --continue로 이어갈 수 있다.

tool-calling을 지원하는 HTTP 프로바이더(anthropic/openai/groq/nvidia)가
필요하다. CLI형 프로바이더(gemini/qwen/kiro)는 지원하지 않는다 — 단발
질문은 gk ask를 사용.

예시:
  gk chat                                # 대화 시작
  gk chat "이 함수 언제 왜 바뀌었지?"      # one-shot
  gk chat --continue                     # 지난 대화 이어가기`,
		RunE: runChat,
	}
	cmd.Flags().String("provider", "", "AI 프로바이더 지정 (anthropic|openai|groq|nvidia)")
	cmd.Flags().String("model", "", "이번 실행에만 모델 지정")
	cmd.Flags().String("lang", "", "출력 언어 지정 (en|ko|...)")
	cmd.Flags().Bool("continue", false, "가장 최근 세션을 이어서 대화")
	rootCmd.AddCommand(cmd)
}

type chatFlags struct {
	provider string
	model    string
	lang     string
	cont     bool
}

func readChatFlags(cmd *cobra.Command) chatFlags {
	var f chatFlags
	f.provider, _ = cmd.Flags().GetString("provider")
	f.model, _ = cmd.Flags().GetString("model")
	f.lang, _ = cmd.Flags().GetString("lang")
	f.cont, _ = cmd.Flags().GetBool("continue")
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
}

func runChat(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	flags := readChatFlags(cmd)
	oneShot := len(args) > 0

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
	// tool results, AND the adapter's internal 5xx retries — a proxy that
	// occasionally 500s needs ~3 attempts × response time + backoff, which
	// blows the 30s single-shot budget do/ask use. round_timeout (120s
	// default) is the chat-specific budget, and the provider's own HTTP
	// deadline is raised to match so it never silently undercuts it.
	roundTimeout := parseDurationOrDefault(ai.Chat.RoundTimeout, 120*time.Second)

	// Provider is resolved ONCE and fixed for the whole session: tool-call
	// IDs are vendor-specific, so mid-conversation failover would corrupt
	// the history. No FallbackChain here — auto-detect picks the first
	// ToolCaller-capable provider and stays on it.
	prov, caller, pErr := resolveChatProvider(ctx, ai, flags.model, roundTimeout)
	if pErr != nil {
		return fmt.Errorf("chat: %w", pErr)
	}
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
	tools.RegisterGitTools(registry, &tools.GitTools{Runner: runner, Sandbox: sandbox, DenyGlobs: deny})
	tools.RegisterFileTools(registry, &tools.FileTools{Sandbox: sandbox})

	collector := &aichat.RepoContextCollector{Runner: runner, Dbg: Dbg}
	repoCtx := collector.Collect(ctx).Format()

	sess, contWarn := openChatSession(ctx, runner, flags.cont)
	if sess == nil {
		return fmt.Errorf("chat: cannot create session under .git — is the repository writable?")
	}
	if contWarn != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), stylizeHintLine("hint: "+contWarn))
	}

	engine := &chat.Engine{
		Caller:        remoteGuardedCaller{inner: caller, prov: prov, flags: cmd.Flags()},
		Registry:      registry,
		SystemPrompt:  chat.SystemPrompt(repoCtx, lang, EasyEngine().IsEnabled()),
		MaxTokens:     aiChatMaxTokens(ai),
		MaxToolRounds: maxRounds,
		RoundTimeout:  roundTimeout,
		// History replay budget (~tokens). Provider windows comfortably
		// exceed this; trimming protects cost, not correctness.
		HistoryBudget: 32768,
		Session:       sess,
		Dbg:           Dbg,
	}
	if flags.cont {
		if msgs, skipped, rErr := sess.Replay(); rErr == nil {
			engine.LoadHistory(msgs)
			if skipped > 0 {
				fmt.Fprintf(cmd.ErrOrStderr(), "chat: %d corrupted session line(s) skipped\n", skipped)
			}
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "chat: could not replay session %s: %v — starting fresh\n", sess.ID, rErr)
		}
	}

	Dbg("chat: provider=%s model=%s session=%s rounds=%d cap=%d deny=%d",
		prov.Name(), providerModel(prov), sess.ID, maxRounds, resultCap, len(deny))

	if oneShot {
		return runChatOneShot(cmd, engine, prov, sess, lang, strings.Join(args, " "))
	}
	return runChatREPL(cmd, engine, prov, sess, lang)
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

// resolveChatProvider picks the session's provider: the configured one
// when set (and it must support tool calling), else the first available
// ToolCaller in the auto-detect order. minTimeout raises the adapter's
// HTTP deadline (its internal retry loop included) up to the chat round
// budget — otherwise the adapter's 60s default would silently bind under
// a 120s round_timeout.
func resolveChatProvider(ctx context.Context, ai config.AIConfig, modelOverride string, minTimeout time.Duration) (provider.Provider, provider.ToolCaller, error) {
	build := func(cfg config.AIConfig) (provider.Provider, error) {
		opts := aiFactoryOptionsWithModel(cfg, modelOverride)
		if opts.Timeout < minTimeout {
			opts.Timeout = minTimeout
		}
		return provider.NewProvider(ctx, opts)
	}
	if ai.Provider != "" {
		p, err := build(ai)
		if err != nil {
			return nil, nil, fmt.Errorf("provider: %w", err)
		}
		tc, ok := p.(provider.ToolCaller)
		if !ok {
			return nil, nil, fmt.Errorf("provider %q does not support tool calling — chat needs anthropic/openai/groq/nvidia; use `gk ask` for %q", p.Name(), p.Name())
		}
		return p, tc, nil
	}
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
		return p, tc, nil
	}
	return nil, nil, fmt.Errorf("no tool-calling provider available (need anthropic/openai/groq/nvidia with an API key) — run `gk doctor` for setup hints")
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

// openChatSession opens the --continue target or creates a fresh session.
// A missing/corrupt previous session degrades to a new one with a warning
// string — never a fatal error.
func openChatSession(ctx context.Context, runner git.Runner, cont bool) (*chat.Session, string) {
	warn := ""
	if cont {
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
		if isDeadlineErr(err) {
			fmt.Fprintln(cmd.ErrOrStderr(), stylizeHintLine("hint: 한 라운드가 ai.chat.round_timeout을 초과했습니다 — 값을 올리거나(global config) --model로 더 빠른 모델을 지정해 보세요"))
		}
		return fmt.Errorf("chat: %w", err)
	}
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
	emitAIAdvice(cmd.OutOrStdout(), "gk chat", res.Text)
	fmt.Fprintln(cmd.OutOrStdout(), color.New(color.Faint).Sprint(turnStatsLine(turnUI, res)))
	fmt.Fprintln(cmd.OutOrStdout(), stylizeHintLine("hint: gk chat --continue 로 이 대화를 이어갈 수 있습니다"))
	return nil
}

// chatLineReader returns the REPL's line source. On a real terminal it
// uses x/term's line editor, which gives shell-style history (↑/↓ walk
// previous questions) and inline editing for free. Raw mode is entered
// ONLY while reading a line and restored before the turn runs, so Ctrl-C
// still raises SIGINT during provider calls (turn cancellation); at the
// prompt the editor maps Ctrl-C and empty-line Ctrl-D to io.EOF, which
// the loop treats as a graceful exit. Non-TTY stdin (tests, pipes) falls
// back to a plain scanner.
func chatLineReader(cmd *cobra.Command, prompt string) func() (string, error) {
	stdin, okIn := cmd.InOrStdin().(*os.File)
	if !okIn || !term.IsTerminal(int(stdin.Fd())) {
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
	t := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{stdin, cmd.OutOrStdout()}, prompt)
	return func() (string, error) {
		old, err := term.MakeRaw(fd)
		if err != nil {
			return "", err
		}
		defer func() { _ = term.Restore(fd, old) }()
		return t.ReadLine()
	}
}

func runChatREPL(cmd *cobra.Command, engine *chat.Engine, prov provider.Provider, sess *chat.Session, lang string) error {
	out := cmd.OutOrStdout()
	ko := isKoLang(lang)
	printChatWelcome(out, prov, sess, ko)

	prompt := color.New(color.Bold, color.FgCyan).Sprint("gk chat › ")
	readLine := chatLineReader(cmd, prompt)

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
		case strings.HasPrefix(line, "/"):
			if ko {
				fmt.Fprintf(out, "알 수 없는 명령 %s — /help 참고\n", line)
			} else {
				fmt.Fprintf(out, "unknown command %s — see /help\n", line)
			}
			continue
		}

		turnUI := &chatTurnUI{out: out, label: chatSpinnerMessage(lang, prov.Name())}
		res, err := runChatTurn(cmd, engine, turnUI, line)
		if err != nil {
			switch {
			case errors.Is(err, context.Canceled):
				fmt.Fprintln(out, color.YellowString("· 턴을 취소했습니다"))
			case errors.Is(err, chat.ErrMaxRounds):
				fmt.Fprintf(cmd.ErrOrStderr(), "chat: %v\n", err)
			case isDeadlineErr(err):
				fmt.Fprintf(cmd.ErrOrStderr(), "chat: %v\n", err)
				fmt.Fprintln(cmd.ErrOrStderr(), stylizeHintLine("hint: 한 라운드가 ai.chat.round_timeout을 초과했습니다 — 프록시/모델이 느리거나 일시 오류를 재시도 중일 수 있습니다. 값을 올리거나(global config) --model로 더 빠른 모델을 지정해 보세요"))
			default:
				fmt.Fprintf(cmd.ErrOrStderr(), "chat: %v — 다시 시도하세요\n", err)
			}
			continue // a failed turn never kills the session
		}
		fmt.Fprintln(out)
		fmt.Fprintln(out, res.Text)
		fmt.Fprintln(out, color.New(color.Faint).Sprint(turnStatsLine(turnUI, res)))
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
		fmt.Fprintln(w, "명령: /help 도움말 · /clear 맥락 초기화 · /exit 종료 (Ctrl-D 동일)")
		fmt.Fprintln(w, "턴 진행 중 Ctrl-C는 그 턴만 취소합니다. 프롬프트에서 Ctrl-C는 즉시 종료.")
		fmt.Fprintln(w, "도구: git log/show/diff/blame/grep + 파일 읽기/목록 (전부 읽기 전용)")
	} else {
		fmt.Fprintln(w, "commands: /help · /clear resets context · /exit quits (Ctrl-D too)")
		fmt.Fprintln(w, "Ctrl-C during a turn cancels that turn; at the prompt it exits.")
		fmt.Fprintln(w, "tools: git log/show/diff/blame/grep + file read/list (all read-only)")
	}
}

// isDeadlineErr matches a round timeout whether it surfaces as the
// context error itself or wrapped in an adapter's message string (nvidia
// wraps it as "openai: http call: … context deadline exceeded").
func isDeadlineErr(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(err.Error(), "deadline exceeded")
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
