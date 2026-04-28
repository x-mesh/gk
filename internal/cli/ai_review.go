package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "review",
		Short: "AI-powered code review on staged or range diff",
		Long: `Reviews staged changes (default) or a specific ref range via an AI
provider's Summarize capability.

By default the staged diff (git diff --cached) is reviewed. Use --range
to specify a ref range like "ref1..ref2". Output format is plain text
by default; use --format json for structured output.`,
		RunE: runAIReview,
	}
	cmd.Flags().String("range", "", `ref range to review (e.g. "main..HEAD"); default: staged diff`)
	cmd.Flags().String("format", "text", `output format: "text" (default) or "json"`)
	cmd.Flags().Bool("dry-run", false, "show the prompt without calling the provider")
	cmd.Flags().String("provider", "", "override ai.provider")

	rootCmd.AddCommand(cmd)
}

// aiReviewFlags captures CLI flags for `gk review`.
type aiReviewFlags struct {
	rangeRef string // e.g. "main..HEAD"; empty = staged diff
	format   string // "text" | "json"
	dryRun   bool
	provider string
}

func readAIReviewFlags(cmd *cobra.Command) aiReviewFlags {
	var f aiReviewFlags
	f.rangeRef, _ = cmd.Flags().GetString("range")
	f.format, _ = cmd.Flags().GetString("format")
	f.dryRun, _ = cmd.Flags().GetBool("dry-run")
	f.provider, _ = cmd.Flags().GetString("provider")
	return f
}

func runAIReview(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return fmt.Errorf("review: load config: %w", err)
	}

	flags := readAIReviewFlags(cmd)

	ai := cfg.AI
	if flags.provider != "" {
		ai.Provider = flags.provider
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}

	// Fallback Chain when no explicit provider; single provider otherwise.
	var prov provider.Provider
	if ai.Provider == "" {
		fc, fcErr := buildFallbackChain(nil, provider.ExecRunner{})
		if fcErr != nil {
			return fmt.Errorf("review: %w", fcErr)
		}
		prov = fc
	} else {
		p, pErr := provider.NewProvider(ctx, provider.FactoryOptions{
			Name:   ai.Provider,
			Runner: provider.ExecRunner{},
		})
		if pErr != nil {
			return fmt.Errorf("review: provider: %w", pErr)
		}
		prov = p
	}

	return runAIReviewCore(ctx, aiReviewDeps{
		Runner:   runner,
		Provider: prov,
		Lang:     ai.Lang,
		AI:       ai,
		Cmd:      cmd,
		Out:      cmd.OutOrStdout(),
		ErrOut:   cmd.ErrOrStderr(),
	}, flags)
}

// aiReviewDeps holds injectable dependencies for testability.
type aiReviewDeps struct {
	Runner   git.Runner
	Provider provider.Provider
	Lang     string
	AI       config.AIConfig
	Cmd      *cobra.Command // for --show-prompt; nil in tests
	Out      io.Writer
	ErrOut   io.Writer
}

func runAIReviewCore(ctx context.Context, deps aiReviewDeps, flags aiReviewFlags) error {
	// Compute diff: --range or staged.
	var diff string
	if flags.rangeRef != "" {
		Dbg("review: using range diff %s", flags.rangeRef)
		out, _, err := deps.Runner.Run(ctx, "diff", flags.rangeRef)
		if err != nil {
			return fmt.Errorf("review: diff %s: %w", flags.rangeRef, err)
		}
		diff = string(out)
	} else {
		Dbg("review: using staged diff")
		out, _, err := deps.Runner.Run(ctx, "diff", "--cached")
		if err != nil {
			return fmt.Errorf("review: staged diff: %w", err)
		}
		diff = string(out)
	}

	// Edge case: empty diff.
	if strings.TrimSpace(diff) == "" {
		fmt.Fprintln(deps.Out, "review: no changes to review")
		return nil
	}

	Dbg("review: diff length=%d bytes", len(diff))

	// Dry-run: show what would be sent.
	if flags.dryRun {
		fmt.Fprintln(deps.Out, "--- dry-run: prompt that would be sent ---")
		fmt.Fprintf(deps.Out, "Kind: review\nLang: %s\n", fallbackLang(deps.Lang))
		if flags.rangeRef != "" {
			fmt.Fprintf(deps.Out, "Range: %s\n", flags.rangeRef)
		} else {
			fmt.Fprintln(deps.Out, "Range: staged (--cached)")
		}
		fmt.Fprintf(deps.Out, "Diff length: %d bytes\n", len(diff))
		return nil
	}

	// Privacy Gate: redact diff for remote providers.
	redactedDiff, _, err := applyPrivacyGate(deps.Provider, diff, deps.AI)
	if err != nil {
		return fmt.Errorf("review: privacy gate: %w", err)
	}

	// --show-prompt: display redacted payload.
	if deps.Cmd != nil {
		showPromptIfRequested(deps.Cmd, redactedDiff)
	}

	// Type-assert Summarizer.
	sum, ok := deps.Provider.(provider.Summarizer)
	if !ok {
		return fmt.Errorf("review: provider %q does not support Summarize", deps.Provider.Name())
	}

	// Call Summarize.
	stop := ui.StartBubbleSpinner(fmt.Sprintf("review — analyzing diff via %s", deps.Provider.Name()))
	result, err := sum.Summarize(ctx, provider.SummarizeInput{
		Kind: "review",
		Diff: redactedDiff,
		Lang: fallbackLang(deps.Lang),
	})
	stop()
	if err != nil {
		return fmt.Errorf("review: summarize: %w", err)
	}

	// Output based on --format.
	fmt.Fprint(deps.Out, result.Text)
	return nil
}
