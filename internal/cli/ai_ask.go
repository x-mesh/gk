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
)

func init() {
	cmd := &cobra.Command{
		Use:   "ask",
		Short: "저장소 컨텍스트 기반 git/gk Q&A",
		Long: `git/gk 관련 질문에 대해 현재 저장소 컨텍스트를 활용하여
교육적 답변을 생성한다. 실제 브랜치명, 커밋 해시, 파일명을 포함한
구체적인 예시를 제공하며, 관련 gk 명령어를 제안한다.

프로바이더 해석 순서:
  1. --provider 플래그
  2. ai.provider in .gk.yaml
  3. 자동 감지 (nvidia → gemini → qwen → kiro-cli)

예시:
  gk ask "rebase와 merge의 차이점은?"
  gk ask "현재 브랜치에서 main으로 어떻게 머지하나요?"
  gk ask "마지막 커밋을 취소하려면?"
  gk ask "How does gk sync work?" --provider anthropic --lang ko`,
		RunE: runAsk,
	}
	cmd.Flags().String("provider", "", "AI 프로바이더 지정 (anthropic|openai|nvidia|gemini|groq|qwen|kiro)")
	cmd.Flags().String("lang", "", "출력 언어 지정 (en|ko|...)")

	rootCmd.AddCommand(cmd)
}

// askFlags captures CLI flags for `gk ask`.
type askFlags struct {
	provider string
	lang     string
}

func readAskFlags(cmd *cobra.Command) askFlags {
	var f askFlags
	f.provider, _ = cmd.Flags().GetString("provider")
	f.lang, _ = cmd.Flags().GetString("lang")
	return f
}

func runAsk(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	flags := readAskFlags(cmd)

	// Require at least one argument (the question).
	if len(args) == 0 {
		return fmt.Errorf("ask: 질문이 필요합니다\n사용법: gk ask \"<질문>\"")
	}
	question := strings.Join(args, " ")

	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return fmt.Errorf("ask: load config: %w", err)
	}

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
			return fmt.Errorf("ask: %w", fcErr)
		}
		prov = fc
	} else {
		p, pErr := provider.NewProvider(ctx, aiFactoryOptionsFromAI(ai))
		if pErr != nil {
			return fmt.Errorf("ask: provider: %w", pErr)
		}
		prov = p
	}
	Dbg("ask: provider=%s model=%s lang=%s", prov.Name(), providerModel(prov), fallbackLang(ai.Lang))

	// Type-assert Summarizer.
	sum, ok := prov.(provider.Summarizer)
	if !ok {
		return fmt.Errorf("ask: provider %q does not support Summarize", prov.Name())
	}

	// Privacy Gate: redact user input for remote providers.
	redactedQuestion, pgFindings, pgErr := applyPrivacyGate(prov, question, ai)
	if pgErr != nil {
		renderPrivacyFindings(cmd.ErrOrStderr(), pgFindings)
		return fmt.Errorf("ask: privacy gate: %w", pgErr)
	}

	// --show-prompt: display redacted payload.
	showPromptIfRequested(cmd, redactedQuestion)

	// Parse timeout from config.
	timeout := parseDurationOrDefault(ai.Chat.Timeout, 30*time.Second)

	// QAEngine: answer git/gk questions with repository context.
	engine := &aichat.QAEngine{
		Summarizer: sum,
		Context:    &aichat.RepoContextCollector{Runner: runner, TokenBudget: 2000, Dbg: Dbg},
		Lang:       fallbackLang(ai.Lang),
		Timeout:    timeout,
		Dbg:        Dbg,
	}

	Dbg("ask: prompt size=%d bytes", len(redactedQuestion))
	start := time.Now()

	result, err := engine.Answer(ctx, redactedQuestion)
	if err != nil {
		return err
	}

	dur := time.Since(start)
	Dbg("ask: AI response in %s", dur.Round(time.Millisecond))

	// Easy Mode: TranslateTerms post-processing + term annotation (원본 영어 용어 병기).
	if easyEngine != nil && easyEngine.IsEnabled() {
		result = easyEngine.TranslateTerms(result)
	}

	fmt.Fprintln(cmd.OutOrStdout(), result)
	return nil
}
