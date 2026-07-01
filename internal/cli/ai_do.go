package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/aichat"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "do",
		Short: "자연어로 git/gk 명령어 생성 및 실행",
		Long: `자연어 입력을 분석하여 git/gk 명령어 시퀀스를 생성하고,
사용자 확인 후 순차적으로 실행한다.

기본 동작은 dry-run 미리보기이며, 사용자가 확인(y/Enter)하면 실행한다.
위험 명령어(force push, hard reset 등)는 추가 확인을 요구한다.

프로바이더 해석 순서:
  1. --provider 플래그
  2. ai.provider in .gk.yaml
  3. 자동 감지 (nvidia → gemini → qwen → kiro-cli)

예시:
  gk do "이 브랜치를 main에 리베이스해줘"
  gk do "어제 커밋 취소" --yes
  gk do "feature/auth 브랜치 만들고 체크아웃" --dry-run
  gk do "rename this branch to feature/login" --json`,
		RunE: runDo,
	}
	cmd.Flags().BoolP("yes", "y", false, "일반 확인 건너뜀 (위험 명령어는 여전히 확인)")
	cmd.Flags().Bool("force", false, "모든 확인 건너뜀 (위험 명령어 포함)")
	cmd.Flags().Bool("dry-run", false, "플랜만 출력, 명령어 미실행")
	cmd.Flags().Bool("json", false, "JSON 형식으로 출력")
	cmd.Flags().String("provider", "", "AI 프로바이더 지정 (anthropic|openai|nvidia|gemini|groq|qwen|kiro)")
	cmd.Flags().String("model", "", "이번 실행에만 모델 지정 (HTTP 프로바이더 한정)")
	cmd.Flags().String("lang", "", "출력 언어 지정 (en|ko|...)")

	rootCmd.AddCommand(cmd)
}

// doFlags captures CLI flags for `gk do`.
type doFlags struct {
	yes      bool
	force    bool
	dryRun   bool
	json     bool
	provider string
	model    string
	lang     string
}

func readDoFlags(cmd *cobra.Command) doFlags {
	var f doFlags
	f.yes, _ = cmd.Flags().GetBool("yes")
	f.force, _ = cmd.Flags().GetBool("force")
	f.dryRun, _ = cmd.Flags().GetBool("dry-run")
	f.json, _ = cmd.Flags().GetBool("json")
	f.json = f.json || JSONOut()
	f.provider, _ = cmd.Flags().GetString("provider")
	f.model, _ = cmd.Flags().GetString("model")
	f.lang, _ = cmd.Flags().GetString("lang")
	return f
}

func runDo(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Require at least one argument (the natural-language input).
	if len(args) == 0 {
		return fmt.Errorf("do: 자연어 설명이 필요합니다\n사용법: gk do \"<자연어 설명>\"")
	}
	input := strings.Join(args, " ")

	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return fmt.Errorf("do: load config: %w", err)
	}

	flags := readDoFlags(cmd)

	ai := cfg.AI

	// --provider flag overrides config.
	if flags.provider != "" {
		ai.Provider = flags.provider
	}
	// Conversational output (do plan descriptions) follows output.lang; ai.lang
	// governs git artifacts (commit/pr). The --lang flag still wins. See
	// resolveResponseLang.
	lang := resolveResponseLang(flags.lang, cfg.AI.Lang, cfg.Output.Lang)

	// ai.enabled=false check (6.5).
	if !ai.Enabled {
		return fmt.Errorf("AI features are disabled (ai.enabled=false)\nhint: set ai.enabled=true in .gk.yaml or unset GK_AI_DISABLE")
	}
	if strings.EqualFold(os.Getenv("GK_AI_DISABLE"), "1") {
		return fmt.Errorf("AI features are disabled (GK_AI_DISABLE=1)\nhint: unset GK_AI_DISABLE to enable AI features")
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}

	// Resolve provider: FallbackChain or single provider.
	var prov provider.Provider
	if ai.Provider == "" {
		fc, fcErr := buildFallbackChain(nil, provider.ExecRunner{})
		if fcErr != nil {
			return fmt.Errorf("do: %w", fcErr)
		}
		prov = fc
	} else {
		p, pErr := provider.NewProvider(ctx, aiFactoryOptionsWithModel(ai, flags.model))
		if pErr != nil {
			return fmt.Errorf("do: provider: %w", pErr)
		}
		prov = p
	}
	Dbg("do: provider=%s model=%s lang=%s", prov.Name(), providerModel(prov), lang)

	// Type-assert Summarizer.
	sum, ok := prov.(provider.Summarizer)
	if !ok {
		return fmt.Errorf("do: provider %q does not support Summarize", prov.Name())
	}

	// Remote policy: refuse to upload when allow_remote is off.
	if err := ensureRemoteAllowed(prov, ai); err != nil {
		return fmt.Errorf("do: %w", err)
	}

	// Privacy Gate: redact user input for remote providers.
	redactedInput, pgFindings, pgErr := applyPrivacyGate(cmd, prov, input, ai)
	if pgErr != nil {
		renderPrivacyFindings(cmd.ErrOrStderr(), pgFindings)
		return fmt.Errorf("do: privacy gate: %w", pgErr)
	}

	// --show-prompt: display redacted payload.
	showPromptIfRequested(cmd, redactedInput)

	// Parse timeout from config.
	timeout := parseDurationOrDefault(ai.Chat.Timeout, 30*time.Second)

	// IntentParser: natural language → ExecutionPlan.
	parser := &aichat.IntentParser{
		Summarizer: sum,
		Context:    &aichat.RepoContextCollector{Runner: runner, TokenBudget: 2000, Dbg: Dbg},
		Safety:     &aichat.SafetyClassifier{},
		Lang:       lang,
		Easy:       EasyEngine().IsEnabled(),
		Timeout:    timeout,
		MaxTokens:  aiChatMaxTokens(ai),
		Dbg:        Dbg,
		// Redact the assembled prompt (input + repo context) for remote
		// providers — the gate above only covered the raw input.
		Redact: func(s string) (string, error) {
			red, _, rerr := applyPrivacyGate(cmd, prov, s, ai)
			return red, rerr
		},
	}

	Dbg("do: prompt size=%d bytes", len(redactedInput))
	parseStart := time.Now()

	// Show a spinner during the provider round-trip so the terminal isn't
	// frozen for several seconds while the model thinks. Suppressed under
	// --debug — the Dbg timeline already narrates progress and a bubbletea
	// spinner would fight the debug writes for the same stderr line. No-op on
	// non-TTY stderr (pipes/CI), so scripted output stays clean.
	stopSpin := func() {}
	if !flagDebug {
		stopSpin = ui.StartBubbleSpinner(doSpinnerMessage(lang, prov.Name()))
	}
	plan, err := parser.Parse(ctx, redactedInput)
	stopSpin()
	if err != nil {
		return err
	}

	parseDur := time.Since(parseStart)
	Dbg("do: AI response in %s", parseDur.Round(time.Millisecond))

	if plan == nil || len(plan.Commands) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "do: AI가 실행할 명령어를 생성하지 못했습니다")
		return nil
	}

	// TTY detection.
	isTTY := promptAllowed()

	// CommandExecutor: preview + execute.
	gkPath, _ := os.Executable()
	executor := &aichat.CommandExecutor{
		Runner:     runner,
		Out:        cmd.OutOrStdout(),
		ErrOut:     cmd.ErrOrStderr(),
		EasyEngine: EasyEngine(),
		GkPath:     gkPath,
		Dbg:        Dbg,
	}

	// Wire up the confirm function using bufio for interactive prompts.
	// The scanner is created ONCE and captured: a fresh bufio.Scanner per
	// prompt can buffer-ahead and swallow the next answer when a plan asks
	// for several confirmations (e.g. multiple dangerous commands).
	stdinScanner := bufio.NewScanner(os.Stdin)
	executor.ConfirmFunc = func(prompt string) (bool, error) {
		fmt.Fprint(cmd.OutOrStdout(), prompt)
		if !stdinScanner.Scan() {
			return false, nil
		}
		answer := strings.ToLower(strings.TrimSpace(stdinScanner.Text()))
		return answer == "" || answer == "y" || answer == "yes", nil
	}

	opts := aichat.ExecuteOptions{
		Yes:    flags.yes,
		Force:  flags.force,
		DryRun: flags.dryRun,
		JSON:   flags.json,
		NonTTY: !isTTY,
	}

	// Frame the plan on stderr so it reads as a structured plan rather than a
	// bare command dump, and surface which provider/lang produced it (only the
	// --debug timeline showed this before). Kept off stdout so `--json` and
	// piped plans stay machine-clean.
	if !flags.json {
		renderDoPlanHeader(cmd.ErrOrStderr(), prov.Name(), lang, flags.dryRun, len(plan.Commands))
	}

	result, err := executor.Execute(ctx, plan, opts)
	if err != nil {
		// NonTTYError has exit code 2.
		if _, ok := err.(*aichat.NonTTYError); ok {
			fmt.Fprintln(cmd.ErrOrStderr(), err.Error())
			fmt.Fprintln(cmd.ErrOrStderr(), stylizeHintLine("hint: pass --yes or --dry-run for non-interactive use"))
			return err
		}
		return err
	}

	// Easy Mode previously ran TranslateTerms over each command's
	// stdout/stderr here. Removed: cr.Stdout/cr.Stderr is the *raw* output of
	// the child git process (executor.go runCommand → e.Runner.Run), not gk's
	// own already-Easy-processed text. Translating git terms inside it is the
	// same corruption translateErrorBody guards against — it rewrites source
	// code and identifiers the user must read verbatim (e.g. a struct tag
	// `Branch string `json:"branch"`` in lint output). Child output stays
	// literal; gk's own framing prose is what Easy Mode translates elsewhere.

	// Print backup ref hint if created.
	if result != nil && result.BackupRef != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "do: backup ref created: %s\n", result.BackupRef)
	}

	// dry-run is a preview: tell the user nothing ran and how to execute.
	if flags.dryRun && !flags.json {
		renderDoDryRunFooter(cmd.ErrOrStderr(), lang)
	}

	return nil
}

// doSpinnerMessage is the inline spinner label shown while the provider
// generates a plan. Localised to the resolved response language.
func doSpinnerMessage(lang, providerName string) string {
	if isKoLang(lang) {
		return fmt.Sprintf("%s에게 계획 요청 중…", providerName)
	}
	return fmt.Sprintf("asking %s for a plan…", providerName)
}

// renderDoPlanHeader prints a compact, styled banner above the plan body:
// a bold title (plus a "preview" badge in dry-run) and a faint meta line
// naming the provider, language, and command count. fatih/color auto-strips
// styling on non-TTY writers.
func renderDoPlanHeader(w io.Writer, providerName, lang string, dryRun bool, n int) {
	ko := isKoLang(lang)

	title := "Execution plan"
	count := fmt.Sprintf("%d command(s)", n)
	badge := ""
	if dryRun {
		badge = "  (preview · nothing runs)"
	}
	if ko {
		title = "실행 계획"
		count = fmt.Sprintf("명령 %d개", n)
		if dryRun {
			badge = "  (미리보기 · 실행 안 함)"
		}
	}

	bold := color.New(color.Bold, color.FgCyan).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()
	fmt.Fprintf(w, "\n%s%s\n", bold(title), faint(badge))
	fmt.Fprintln(w, faint(fmt.Sprintf("provider: %s · lang: %s · %s", providerName, lang, count)))
	fmt.Fprintln(w)
}

// renderDoDryRunFooter tells the user the dry-run executed nothing and how
// to actually run the plan.
func renderDoDryRunFooter(w io.Writer, lang string) {
	faint := color.New(color.Faint).SprintFunc()
	msg := "to run: re-run with --yes, or drop --dry-run and answer y at the prompt"
	if isKoLang(lang) {
		msg = "실행하려면: --yes 로 다시 실행하거나, --dry-run 없이 실행해 확인 프롬프트에서 y 입력"
	}
	fmt.Fprintln(w, faint(msg))
}

// isKoLang reports whether a BCP-47 short code denotes Korean.
func isKoLang(lang string) bool {
	return strings.HasPrefix(strings.ToLower(lang), "ko")
}

// parseDurationOrDefault parses a Go duration string, returning the
// default on failure or empty input.
func parseDurationOrDefault(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}
