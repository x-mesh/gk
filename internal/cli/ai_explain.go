package cli

import (
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
		Use:   "explain",
		Short: "에러 메시지 진단 또는 마지막 명령어 설명",
		Long: `에러 메시지를 분석하여 원인, 해결 방법, 예방 팁을 제시하거나,
--last 플래그로 마지막 실행 명령어를 단계별로 설명한다.

프로바이더 해석 순서:
  1. --provider 플래그
  2. ai.provider in .gk.yaml
  3. 자동 감지 (nvidia → gemini → qwen → kiro-cli)

예시:
  gk explain "fatal: not a git repository"
  gk explain "error: failed to push some refs"
  gk explain --last
  gk explain "merge conflict" --provider anthropic --lang ko`,
		RunE: runExplain,
	}
	cmd.Flags().Bool("last", false, "마지막 명령어를 단계별로 설명")
	cmd.Flags().String("provider", "", "AI 프로바이더 지정 (anthropic|openai|nvidia|gemini|groq|qwen|kiro)")
	cmd.Flags().String("model", "", "이번 실행에만 모델 지정 (HTTP 프로바이더 한정)")
	cmd.Flags().String("lang", "", "출력 언어 지정 (en|ko|...)")

	rootCmd.AddCommand(cmd)
}

// explainFlags captures CLI flags for `gk explain`.
type explainFlags struct {
	last     bool
	provider string
	model    string
	lang     string
}

func readExplainFlags(cmd *cobra.Command) explainFlags {
	var f explainFlags
	f.last, _ = cmd.Flags().GetBool("last")
	f.provider, _ = cmd.Flags().GetString("provider")
	f.model, _ = cmd.Flags().GetString("model")
	f.lang, _ = cmd.Flags().GetString("lang")
	return f
}

func runExplain(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	flags := readExplainFlags(cmd)

	// Require either --last or at least one argument (the error message).
	if !flags.last && len(args) == 0 {
		return fmt.Errorf("explain: 에러 메시지 또는 --last 플래그가 필요합니다\n사용법: gk explain \"<에러 메시지>\" 또는 gk explain --last")
	}

	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return fmt.Errorf("explain: load config: %w", err)
	}

	ai := cfg.AI

	// --provider flag overrides config.
	if flags.provider != "" {
		ai.Provider = flags.provider
	}
	// Conversational output (explain) follows output.lang; ai.lang governs git
	// artifacts (commit/pr). The --lang flag still wins. See resolveResponseLang.
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
			return fmt.Errorf("explain: %w", fcErr)
		}
		prov = fc
	} else {
		p, pErr := provider.NewProvider(ctx, aiFactoryOptionsWithModel(ai, flags.model))
		if pErr != nil {
			return fmt.Errorf("explain: provider: %w", pErr)
		}
		prov = p
	}
	Dbg("explain: provider=%s model=%s lang=%s", prov.Name(), providerModel(prov), lang)

	// Type-assert Summarizer.
	sum, ok := prov.(provider.Summarizer)
	if !ok {
		return fmt.Errorf("explain: provider %q does not support Summarize", prov.Name())
	}

	// Build the input payload for privacy gate.
	var inputPayload string
	if flags.last {
		inputPayload = "--last"
	} else {
		inputPayload = strings.Join(args, " ")
	}

	// Remote policy: refuse to upload when allow_remote is off.
	if err := ensureRemoteAllowed(prov, ai); err != nil {
		return fmt.Errorf("explain: %w", err)
	}

	// Privacy Gate: redact user input for remote providers.
	redactedInput, pgFindings, pgErr := applyPrivacyGate(cmd, prov, inputPayload, ai)
	if pgErr != nil {
		renderPrivacyFindings(cmd.ErrOrStderr(), pgFindings)
		return fmt.Errorf("explain: privacy gate: %w", pgErr)
	}

	// --show-prompt: display redacted payload.
	showPromptIfRequested(cmd, redactedInput)

	// Parse timeout from config.
	timeout := parseDurationOrDefault(ai.Chat.Timeout, 30*time.Second)

	// ErrorAnalyzer: diagnose errors or explain last command.
	analyzer := &aichat.ErrorAnalyzer{
		Summarizer: sum,
		Context:    &aichat.RepoContextCollector{Runner: runner, TokenBudget: 2000, Dbg: Dbg},
		Lang:       lang,
		Easy:       EasyEngine().IsEnabled(),
		Timeout:    timeout,
		MaxTokens:  aiChatMaxTokens(ai),
		Dbg:        Dbg,
	}

	Dbg("explain: prompt size=%d bytes", len(redactedInput))
	start := time.Now()

	// Spinner during the provider round-trip (see ai_ask.go for the rationale).
	stopSpin := func() {}
	if !flagDebug {
		stopSpin = ui.StartBubbleSpinner(explainSpinnerMessage(lang, providerLabel(prov)))
	}
	var result string
	if flags.last {
		// --last: explain the most recent command from reflog.
		result, err = analyzer.ExplainLast(ctx)
	} else {
		// Diagnose the provided error message.
		result, err = analyzer.DiagnoseError(ctx, redactedInput)
	}
	stopSpin()
	if err != nil {
		return err
	}

	dur := time.Since(start)
	Dbg("explain: AI response in %s", dur.Round(time.Millisecond))

	// Easy Mode: TranslateTerms post-processing + hint addition.
	if easyEngine != nil && easyEngine.IsEnabled() {
		result = easyEngine.TranslateTerms(result)
		// Add a next-action hint for error diagnosis.
		hint := easyEngine.FormatHint("explain")
		if hint != "" {
			result = result + "\n\n" + hint
		}
	}

	emitAIAdvice(cmd.OutOrStdout(), "explain", result, providerAttribution(prov, "", false))
	return nil
}

func explainSpinnerMessage(lang, providerName string) string {
	if isKoLang(lang) {
		return fmt.Sprintf("%s 분석 중…", providerName)
	}
	return fmt.Sprintf("%s analyzing…", providerName)
}
