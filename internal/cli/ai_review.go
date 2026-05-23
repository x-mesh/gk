package cli

import (
	"context"
	"encoding/json"
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
to specify a ref range like "ref1..ref2", or --base <branch> to review the
whole branch (everything since it forked from <branch>, via merge-base, so
the base's own new commits don't pollute the review). Output format is plain
text by default; use --format json for structured output.`,
		RunE: runAIReview,
	}
	cmd.Flags().String("range", "", `ref range to review (e.g. "main..HEAD"); default: staged diff`)
	cmd.Flags().String("base", "", "review the whole branch since it forked from <branch> (merge-base diff)")
	cmd.Flags().String("format", "text", `output format: "text" (default) or "json"`)
	cmd.Flags().Bool("dry-run", false, "show the prompt without calling the provider")
	cmd.Flags().String("provider", "", "override ai.provider")

	rootCmd.AddCommand(cmd)
}

// aiReviewFlags captures CLI flags for `gk review`.
type aiReviewFlags struct {
	rangeRef string // e.g. "main..HEAD"; empty = staged diff
	base     string // review the whole branch via merge-base <base>..HEAD
	format   string // "text" | "json"
	dryRun   bool
	provider string
}

func readAIReviewFlags(cmd *cobra.Command) aiReviewFlags {
	var f aiReviewFlags
	f.rangeRef, _ = cmd.Flags().GetString("range")
	f.base, _ = cmd.Flags().GetString("base")
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
		p, pErr := provider.NewProvider(ctx, aiFactoryOptionsFromAI(ai))
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
	// Compute diff: --range > --base (merge-base) > staged.
	var diff string
	switch {
	case flags.rangeRef != "":
		Dbg("review: using range diff %s", flags.rangeRef)
		out, _, err := deps.Runner.Run(ctx, "diff", flags.rangeRef)
		if err != nil {
			return fmt.Errorf("review: diff %s: %w", flags.rangeRef, err)
		}
		diff = string(out)
	case flags.base != "":
		// Review the whole branch: diff from the fork point so the base's
		// own newer commits don't show up (the 2-dot `base..HEAD` pitfall).
		mbOut, _, err := deps.Runner.Run(ctx, "merge-base", "HEAD", flags.base)
		if err != nil {
			return fmt.Errorf("review: merge-base HEAD %s: %w", flags.base, err)
		}
		mergeBase := strings.TrimSpace(string(mbOut))
		if mergeBase == "" {
			return fmt.Errorf("review: could not determine merge-base between HEAD and %s", flags.base)
		}
		Dbg("review: using merge-base diff %s..HEAD", mergeBase)
		out, _, err := deps.Runner.Run(ctx, "diff", mergeBase+"..HEAD")
		if err != nil {
			return fmt.Errorf("review: diff %s..HEAD: %w", mergeBase, err)
		}
		diff = string(out)
	default:
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

	// Remote policy: refuse to upload when allow_remote is off.
	if err := ensureRemoteAllowed(deps.Provider, deps.AI); err != nil {
		return fmt.Errorf("review: %w", err)
	}

	// Privacy Gate: redact diff for remote providers.
	redactedDiff, pgFindings, err := applyPrivacyGate(deps.Cmd, deps.Provider, diff, deps.AI)
	if err != nil {
		if deps.Cmd != nil {
			renderPrivacyFindings(deps.Cmd.ErrOrStderr(), pgFindings)
		}
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

	// Cache: a review is deterministic in (diff, lang, provider).
	lang := fallbackLang(deps.Lang)
	cacheKey := aiCacheKey("review", redactedDiff, lang, deps.Provider.Name())
	var text, model string
	if cached, ok := readAICache(ctx, deps.Runner, "review", cacheKey); ok {
		text = cached
	} else {
		callCtx, cancel := aiCallContext(ctx, deps.AI)
		defer cancel()
		stop := ui.StartBubbleSpinner(fmt.Sprintf("review — analyzing diff via %s", deps.Provider.Name()))
		result, err := sum.Summarize(callCtx, provider.SummarizeInput{
			Kind:      "review",
			Diff:      redactedDiff,
			Lang:      lang,
			MaxTokens: aiChatMaxTokens(deps.AI),
		})
		stop()
		if err != nil {
			return fmt.Errorf("review: summarize: %w", err)
		}
		text, model = result.Text, result.Model
		writeAICache(ctx, deps.Runner, "review", cacheKey, text)
	}

	// Output: prefer the actionable-findings contract; fall back to raw
	// text when the provider didn't honour it (e.g. some CLI providers).
	if fr, ok := parseReviewFindings(text); ok {
		fr.Provider = deps.Provider.Name()
		fr.Model = model
		if strings.EqualFold(flags.format, "json") {
			return writeAIJSON(deps.Out, fr)
		}
		renderReviewFindings(deps.Out, fr)
		return nil
	}
	if strings.EqualFold(flags.format, "json") {
		return writeAIJSON(deps.Out, map[string]string{
			"provider": deps.Provider.Name(),
			"model":    model,
			"review":   strings.TrimSpace(text),
		})
	}
	emitAIAdvice(deps.Out, "review", text)
	return nil
}

// reviewFindings is the actionable-review contract the review prompt asks
// the model to emit (see buildSummarizeUserPrompt, Kind "review").
type reviewFindings struct {
	Provider string          `json:"provider,omitempty"`
	Model    string          `json:"model,omitempty"`
	Verdict  string          `json:"verdict"`
	Summary  string          `json:"summary"`
	Findings []reviewFinding `json:"findings"`
}

type reviewFinding struct {
	Severity string `json:"severity"`
	Loc      string `json:"loc"`
	Issue    string `json:"issue"`
	Why      string `json:"why"`
	Fix      string `json:"fix"`
}

// parseReviewFindings extracts the review JSON contract from model output,
// tolerating Markdown fences or surrounding prose by slicing the outermost
// {...}. ok=false when no usable contract is present, so the caller can
// fall back to rendering the raw text.
func parseReviewFindings(text string) (reviewFindings, bool) {
	s := strings.TrimSpace(text)
	if i := strings.IndexByte(s, '{'); i >= 0 {
		if j := strings.LastIndexByte(s, '}'); j > i {
			s = s[i : j+1]
		}
	}
	var fr reviewFindings
	if err := json.Unmarshal([]byte(s), &fr); err != nil {
		return reviewFindings{}, false
	}
	if fr.Verdict == "" && fr.Summary == "" && len(fr.Findings) == 0 {
		return reviewFindings{}, false
	}
	return fr, true
}

// renderReviewFindings prints the findings as a severity-ordered checklist
// in a titled section, colour-keyed by verdict.
func renderReviewFindings(out io.Writer, fr reviewFindings) {
	sectionColor := ui.SectionInfo
	switch strings.ToLower(fr.Verdict) {
	case "changes_requested":
		sectionColor = ui.SectionCaution
	case "approve":
		sectionColor = ui.SectionHealth
	}
	summary := strings.TrimSpace(fr.Summary)
	if fr.Verdict != "" {
		v := strings.ReplaceAll(fr.Verdict, "_", " ")
		if summary != "" {
			summary = v + " · " + summary
		} else {
			summary = v
		}
	}
	var body []string
	if len(fr.Findings) == 0 {
		body = append(body, "no blocking findings")
	}
	for _, f := range fr.Findings {
		head := "[" + strings.ToUpper(f.Severity) + "]"
		if f.Loc != "" {
			head += " " + f.Loc
		}
		body = append(body, head)
		if f.Issue != "" {
			body = append(body, "  "+f.Issue)
		}
		if f.Why != "" {
			body = append(body, "  why: "+f.Why)
		}
		if f.Fix != "" {
			body = append(body, "  fix: "+f.Fix)
		}
	}
	fmt.Fprintln(out)
	fmt.Fprint(out, ui.RenderSection("review", summary, body, ui.SectionOpts{
		Layout: ui.SectionLayoutBar,
		Color:  sectionColor,
	}))
}
