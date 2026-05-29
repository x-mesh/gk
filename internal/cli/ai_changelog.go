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
		Use:   "changelog",
		Short: "Generate a changelog from a range of commits",
		Long: `Collects commits between two refs and generates a changelog
via an AI provider's Summarize capability.

By default --from is the latest reachable tag (git describe --tags --abbrev=0)
and --to is HEAD. Output format is Markdown by default; use --format json
for structured output.`,
		RunE: runAIChangelog,
	}
	cmd.Flags().String("from", "", `start ref (default: latest tag)`)
	cmd.Flags().String("to", "", `end ref (default: HEAD)`)
	cmd.Flags().String("format", "markdown", `output format: "markdown" (default) or "json"`)
	cmd.Flags().Bool("dry-run", false, "show the prompt without calling the provider")
	cmd.Flags().String("provider", "", "override ai.provider")
	cmd.Flags().String("model", "", "override the model for this run (HTTP providers only)")

	rootCmd.AddCommand(cmd)
}

// aiChangelogFlags captures CLI flags for `gk changelog`.
type aiChangelogFlags struct {
	from     string // start ref; empty = latest tag
	to       string // end ref; empty = HEAD
	format   string // "markdown" | "json"
	dryRun   bool
	provider string
	model    string
}

func readAIChangelogFlags(cmd *cobra.Command) aiChangelogFlags {
	var f aiChangelogFlags
	f.from, _ = cmd.Flags().GetString("from")
	f.to, _ = cmd.Flags().GetString("to")
	f.format, _ = cmd.Flags().GetString("format")
	f.dryRun, _ = cmd.Flags().GetBool("dry-run")
	f.provider, _ = cmd.Flags().GetString("provider")
	f.model, _ = cmd.Flags().GetString("model")
	return f
}

func runAIChangelog(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return fmt.Errorf("changelog: load config: %w", err)
	}

	flags := readAIChangelogFlags(cmd)

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
			return fmt.Errorf("changelog: %w", fcErr)
		}
		prov = fc
	} else {
		p, pErr := provider.NewProvider(ctx, aiFactoryOptionsWithModel(ai, flags.model))
		if pErr != nil {
			return fmt.Errorf("changelog: provider: %w", pErr)
		}
		prov = p
	}

	return runAIChangelogCore(ctx, aiChangelogDeps{
		Runner:   runner,
		Provider: prov,
		Lang:     ai.Lang,
		AI:       ai,
		Cmd:      cmd,
		Out:      cmd.OutOrStdout(),
		ErrOut:   cmd.ErrOrStderr(),
	}, flags)
}

// aiChangelogDeps holds injectable dependencies for testability.
type aiChangelogDeps struct {
	Runner   git.Runner
	Provider provider.Provider
	Lang     string
	AI       config.AIConfig
	Cmd      *cobra.Command // for --show-prompt; nil in tests
	Out      io.Writer
	ErrOut   io.Writer
}

func runAIChangelogCore(ctx context.Context, deps aiChangelogDeps, flags aiChangelogFlags) error {
	// Resolve --from: default to latest tag.
	from := flags.from
	if from == "" {
		out, _, err := deps.Runner.Run(ctx, "describe", "--tags", "--abbrev=0")
		if err != nil {
			return fmt.Errorf("changelog: no tags found — use --from to specify a start ref")
		}
		from = strings.TrimSpace(string(out))
		if from == "" {
			return fmt.Errorf("changelog: no tags found — use --from to specify a start ref")
		}
	}

	// Resolve --to: default to HEAD.
	to := flags.to
	if to == "" {
		to = "HEAD"
	}
	Dbg("changelog: range=%s..%s", from, to)

	// Collect commits.
	logOut, _, err := deps.Runner.Run(ctx, "log", "--oneline", from+".."+to)
	if err != nil {
		return fmt.Errorf("changelog: git log: %w", err)
	}
	commitLines := strings.TrimSpace(string(logOut))

	// Edge case: no commits in range.
	if commitLines == "" {
		fmt.Fprintln(deps.Out, "changelog: no commits in range — nothing to summarize")
		return nil
	}

	commits := strings.Split(commitLines, "\n")
	Dbg("changelog: %d commit(s) in range %s..%s", len(commits), from, to)

	// Dry-run: show what would be sent.
	if flags.dryRun {
		fmt.Fprintln(deps.Out, "--- dry-run: prompt that would be sent ---")
		fmt.Fprintf(deps.Out, "Kind: changelog\nLang: %s\n", fallbackLang(deps.Lang))
		fmt.Fprintf(deps.Out, "Range: %s..%s\n", from, to)
		fmt.Fprintf(deps.Out, "Commits (%d):\n", len(commits))
		for _, c := range commits {
			fmt.Fprintf(deps.Out, "  %s\n", c)
		}
		return nil
	}

	// Remote policy: refuse to upload when allow_remote is off.
	if err := ensureRemoteAllowed(deps.Provider, deps.AI); err != nil {
		return fmt.Errorf("changelog: %w", err)
	}

	// Privacy Gate: redact commits for remote providers.
	commitPayload := strings.Join(commits, "\n")
	redactedPayload, pgFindings, err := applyPrivacyGate(deps.Cmd, deps.Provider, commitPayload, deps.AI)
	if err != nil {
		if deps.Cmd != nil {
			renderPrivacyFindings(deps.Cmd.ErrOrStderr(), pgFindings)
		}
		return fmt.Errorf("changelog: privacy gate: %w", err)
	}
	redactedCommits := strings.Split(redactedPayload, "\n")

	// --show-prompt: display redacted payload.
	if deps.Cmd != nil {
		showPromptIfRequested(deps.Cmd, redactedPayload)
	}

	// Type-assert Summarizer.
	sum, ok := deps.Provider.(provider.Summarizer)
	if !ok {
		return fmt.Errorf("changelog: provider %q does not support Summarize", deps.Provider.Name())
	}

	// Cache: a changelog is deterministic in (commits, lang, provider).
	lang := fallbackLang(deps.Lang)
	cacheKey := aiCacheKey("changelog", redactedPayload, lang, deps.Provider.Name())
	var text, model string
	if cached, ok := readAICache(ctx, deps.Runner, "changelog", cacheKey); ok {
		text = cached
	} else {
		callCtx, cancel := aiCallContext(ctx, deps.AI)
		defer cancel()
		stop := ui.StartBubbleSpinner(fmt.Sprintf("changelog — drafting via %s", deps.Provider.Name()))
		result, err := sum.Summarize(callCtx, provider.SummarizeInput{
			Kind:      "changelog",
			Commits:   redactedCommits,
			Lang:      lang,
			MaxTokens: aiChatMaxTokens(deps.AI),
		})
		stop()
		if err != nil {
			return fmt.Errorf("changelog: summarize: %w", err)
		}
		text, model = result.Text, result.Model
		writeAICache(ctx, deps.Runner, "changelog", cacheKey, text)
	}

	// Output based on --format.
	if strings.EqualFold(flags.format, "json") {
		return writeAIJSON(deps.Out, map[string]string{
			"provider":  deps.Provider.Name(),
			"model":     model,
			"changelog": strings.TrimSpace(text),
		})
	}
	fmt.Fprint(deps.Out, text)
	return nil
}
