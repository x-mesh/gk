package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

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
	lang     string
}

func readDoFlags(cmd *cobra.Command) doFlags {
	var f doFlags
	f.yes, _ = cmd.Flags().GetBool("yes")
	f.force, _ = cmd.Flags().GetBool("force")
	f.dryRun, _ = cmd.Flags().GetBool("dry-run")
	f.json, _ = cmd.Flags().GetBool("json")
	f.provider, _ = cmd.Flags().GetString("provider")
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
	// --lang flag overrides ai.lang.
	if flags.lang != "" {
		ai.Lang = flags.lang
	}

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
		p, pErr := provider.NewProvider(ctx, aiFactoryOptionsFromAI(ai))
		if pErr != nil {
			return fmt.Errorf("do: provider: %w", pErr)
		}
		prov = p
	}
	Dbg("do: provider=%s model=%s lang=%s", prov.Name(), providerModel(prov), fallbackLang(ai.Lang))

	// Type-assert Summarizer.
	sum, ok := prov.(provider.Summarizer)
	if !ok {
		return fmt.Errorf("do: provider %q does not support Summarize", prov.Name())
	}

	// Privacy Gate: redact user input for remote providers.
	redactedInput, pgFindings, pgErr := applyPrivacyGate(prov, input, ai)
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
		Lang:       fallbackLang(ai.Lang),
		Timeout:    timeout,
		Dbg:        Dbg,
	}

	Dbg("do: prompt size=%d bytes", len(redactedInput))
	parseStart := time.Now()

	plan, err := parser.Parse(ctx, redactedInput)
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
	isTTY := ui.IsTerminal()

	// CommandExecutor: preview + execute.
	executor := &aichat.CommandExecutor{
		Runner:     runner,
		Out:        cmd.OutOrStdout(),
		ErrOut:     cmd.ErrOrStderr(),
		EasyEngine: EasyEngine(),
		SafetyConfig: aichat.SafetyConfig{
			SafetyConfirm: ai.Chat.SafetyConfirm,
		},
		Dbg: Dbg,
	}

	// Wire up the confirm function using bufio for interactive prompts.
	executor.ConfirmFunc = func(prompt string) (bool, error) {
		fmt.Fprint(cmd.OutOrStdout(), prompt)
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			return false, nil
		}
		answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
		return answer == "" || answer == "y" || answer == "yes", nil
	}

	opts := aichat.ExecuteOptions{
		Yes:    flags.yes,
		Force:  flags.force,
		DryRun: flags.dryRun,
		JSON:   flags.json,
		NonTTY: !isTTY,
	}

	result, err := executor.Execute(ctx, plan, opts)
	if err != nil {
		// NonTTYError has exit code 2.
		if _, ok := err.(*aichat.NonTTYError); ok {
			fmt.Fprintln(cmd.ErrOrStderr(), err.Error())
			fmt.Fprintln(cmd.ErrOrStderr(), "hint: pass --yes or --dry-run for non-interactive use")
			return err
		}
		return err
	}

	// Easy Mode: TranslateTerms post-processing on output.
	if easyEngine != nil && easyEngine.IsEnabled() && result != nil {
		for i, cr := range result.Executed {
			result.Executed[i].Stdout = easyEngine.TranslateTerms(cr.Stdout)
			result.Executed[i].Stderr = easyEngine.TranslateTerms(cr.Stderr)
		}
	}

	// Print backup ref hint if created.
	if result != nil && result.BackupRef != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "do: backup ref created: %s\n", result.BackupRef)
	}

	return nil
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
