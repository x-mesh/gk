package cli

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Generate a PR description from branch commits",
		Long: `Computes the diff between the current branch and the base branch,
collects commit messages, and generates a structured PR description
via an AI provider's Summarize capability.

The base branch is determined from config (base_branch) or auto-detected
(main/master). Output goes to stdout by default; use --output clipboard
to copy directly.`,
		RunE: runAIPR,
	}
	cmd.Flags().String("output", "stdout", `output target: "stdout" or "clipboard"`)
	cmd.Flags().Bool("dry-run", false, "show the prompt without calling the provider")
	cmd.Flags().String("provider", "", "override ai.provider")
	cmd.Flags().String("lang", "", "override ai.lang (en|ko|...)")

	aiCmd.AddCommand(cmd)
}

// aiPRFlags captures CLI flags for `gk ai pr`.
type aiPRFlags struct {
	output   string // "stdout" | "clipboard"
	dryRun   bool
	provider string
	lang     string
}

func readAIPRFlags(cmd *cobra.Command) aiPRFlags {
	var f aiPRFlags
	f.output, _ = cmd.Flags().GetString("output")
	f.dryRun, _ = cmd.Flags().GetBool("dry-run")
	f.provider, _ = cmd.Flags().GetString("provider")
	f.lang, _ = cmd.Flags().GetString("lang")
	return f
}

func runAIPR(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return fmt.Errorf("ai pr: load config: %w", err)
	}

	flags := readAIPRFlags(cmd)

	ai := cfg.AI
	if flags.provider != "" {
		ai.Provider = flags.provider
	}
	if flags.lang != "" {
		ai.Lang = flags.lang
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}

	// Fallback Chain when no explicit provider; single provider otherwise.
	var prov provider.Provider
	if ai.Provider == "" {
		fc, fcErr := buildFallbackChain(nil, provider.ExecRunner{})
		if fcErr != nil {
			return fmt.Errorf("ai pr: %w", fcErr)
		}
		prov = fc
	} else {
		p, pErr := provider.NewProvider(ctx, provider.FactoryOptions{
			Name:   ai.Provider,
			Runner: provider.ExecRunner{},
		})
		if pErr != nil {
			return fmt.Errorf("ai pr: provider: %w", pErr)
		}
		prov = p
	}

	return runAIPRCore(ctx, aiPRDeps{
		Runner:   runner,
		Provider: prov,
		Lang:     ai.Lang,
		BaseCfg:  cfg.BaseBranch,
		Remote:   cfg.Remote,
		AI:       ai,
		Cmd:      cmd,
		Out:      cmd.OutOrStdout(),
		ErrOut:   cmd.ErrOrStderr(),
	}, flags)
}

// aiPRDeps holds injectable dependencies for testability.
type aiPRDeps struct {
	Runner   git.Runner
	Provider provider.Provider
	Lang     string
	BaseCfg  string // config base_branch; empty = auto-detect
	Remote   string // config remote; empty = "origin"
	AI       config.AIConfig
	Cmd      *cobra.Command // for --show-prompt; nil in tests
	Out      io.Writer
	ErrOut   io.Writer
}

func runAIPRCore(ctx context.Context, deps aiPRDeps, flags aiPRFlags) error {
	client := git.NewClient(deps.Runner)

	// Resolve base branch.
	base := deps.BaseCfg
	if base == "" {
		remote := deps.Remote
		if remote == "" {
			remote = "origin"
		}
		detected, err := client.DefaultBranch(ctx, remote)
		if err != nil {
			return fmt.Errorf("ai pr: %w", err)
		}
		base = detected
	}
	Dbg("ai pr: base_branch=%s", base)

	// Compute merge-base.
	mbOut, _, err := deps.Runner.Run(ctx, "merge-base", "HEAD", base)
	if err != nil {
		return fmt.Errorf("ai pr: merge-base: %w", err)
	}
	mergeBase := strings.TrimSpace(string(mbOut))
	if mergeBase == "" {
		return fmt.Errorf("ai pr: could not determine merge-base between HEAD and %s", base)
	}
	Dbg("ai pr: merge_base=%s", mergeBase)

	// Collect diff and commits.
	diffOut, _, err := deps.Runner.Run(ctx, "diff", mergeBase+"..HEAD")
	if err != nil {
		return fmt.Errorf("ai pr: diff: %w", err)
	}
	diff := string(diffOut)

	logOut, _, err := deps.Runner.Run(ctx, "log", "--oneline", mergeBase+"..HEAD")
	if err != nil {
		return fmt.Errorf("ai pr: log: %w", err)
	}
	commitLines := strings.TrimSpace(string(logOut))

	// Edge case: no commits ahead of base.
	if diff == "" || commitLines == "" {
		fmt.Fprintln(deps.Out, "ai pr: no commits ahead of base branch — nothing to summarize")
		return nil
	}

	commits := strings.Split(commitLines, "\n")
	Dbg("ai pr: %d commit(s) in range %s..HEAD", len(commits), mergeBase[:minLen(len(mergeBase), 8)])

	// Dry-run: show what would be sent.
	if flags.dryRun {
		fmt.Fprintln(deps.Out, "--- dry-run: prompt that would be sent ---")
		fmt.Fprintf(deps.Out, "Kind: pr\nLang: %s\n", fallbackLang(deps.Lang))
		fmt.Fprintf(deps.Out, "Commits (%d):\n", len(commits))
		for _, c := range commits {
			fmt.Fprintf(deps.Out, "  %s\n", c)
		}
		fmt.Fprintf(deps.Out, "Diff length: %d bytes\n", len(diff))
		return nil
	}

	// Privacy Gate: redact diff for remote providers.
	redactedDiff, _, err := applyPrivacyGate(deps.Provider, diff, deps.AI)
	if err != nil {
		return fmt.Errorf("ai pr: privacy gate: %w", err)
	}

	// --show-prompt: display redacted payload.
	if deps.Cmd != nil {
		showPromptIfRequested(deps.Cmd, redactedDiff)
	}

	// Type-assert Summarizer.
	sum, ok := deps.Provider.(provider.Summarizer)
	if !ok {
		return fmt.Errorf("ai pr: provider %q does not support Summarize", deps.Provider.Name())
	}

	// Call Summarize.
	result, err := sum.Summarize(ctx, provider.SummarizeInput{
		Kind:    "pr",
		Diff:    redactedDiff,
		Commits: commits,
		Lang:    fallbackLang(deps.Lang),
	})
	if err != nil {
		return fmt.Errorf("ai pr: summarize: %w", err)
	}

	// Output.
	return outputPRResult(deps.Out, deps.ErrOut, result.Text, flags.output)
}

func outputPRResult(out, errOut io.Writer, text, mode string) error {
	switch mode {
	case "clipboard":
		if err := copyToClipboard(text); err != nil {
			fmt.Fprintf(errOut, "ai pr: clipboard unavailable (%v), falling back to stdout\n", err)
			fmt.Fprint(out, text)
			return nil
		}
		fmt.Fprintln(out, "ai pr: PR description copied to clipboard")
	default: // "stdout"
		fmt.Fprint(out, text)
	}
	return nil
}

// copyToClipboard writes text to the system clipboard.
func copyToClipboard(text string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name = "pbcopy"
	case "linux":
		name = "xclip"
		args = []string{"-selection", "clipboard"}
	default:
		return fmt.Errorf("unsupported OS %q", runtime.GOOS)
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func fallbackLang(s string) string {
	if s == "" {
		return "en"
	}
	return s
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}
