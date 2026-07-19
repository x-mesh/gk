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
	// `gk pr` LISTS open pull requests (repo / --org / --mine); the AI
	// description generator moved to the `gk pr new` subcommand.
	prCmd := &cobra.Command{
		Use:   "pr",
		Short: "List open pull requests (current repo, --org, or --mine)",
		Long: `Lists open pull requests via the GitHub search API.

No flag lists the current repo's PRs (owner/repo from origin). --org lists
a whole org/account's PRs in one query; --mine restricts to PRs you opened.
--state open|closed|all and --json are supported.

Auth comes from GH_TOKEN / GITHUB_TOKEN / a prior 'gh auth login'. Without
a token only public results show, under a lower rate limit.

To generate a PR *description* from branch commits, use 'gk pr new'.`,
		Args: cobra.MaximumNArgs(1), // permits the `--org acme` space form
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGitHubList(cmd, args, true)
		},
	}
	addGitHubScopeFlags(prCmd)

	newCmd := &cobra.Command{
		Use:   "new",
		Short: "Generate a PR description from branch commits",
		Long: `Computes the diff between the current branch and the base branch,
collects commit messages, and generates a structured PR description
via an AI provider's Summarize capability.

The base branch is determined from config (base_branch) or auto-detected
(main/master). Output goes to stdout by default; use --output clipboard
to copy directly.`,
		RunE: runAIPR,
	}
	newCmd.Flags().String("output", "stdout", `output target: "stdout" or "clipboard"`)
	newCmd.Flags().Bool("dry-run", false, "show the prompt without calling the provider")
	newCmd.Flags().String("provider", "", "override ai.provider")
	newCmd.Flags().String("lang", "", "override ai.lang (en|ko|...)")
	addAINoCacheFlag(newCmd)

	checkoutCmd := &cobra.Command{
		Use:   "checkout <number>",
		Short: "Fetch and check out a pull request's branch locally",
		Long: `Fetches the pull request's head via GitHub's refs/pull/<n>/head (which
exists for every PR, including forks) and switches to a local branch. Uses
git only — no GitHub API or token required (git's own auth applies).`,
		Args: cobra.ExactArgs(1),
		RunE: runPRCheckout,
	}
	checkoutCmd.Flags().String("branch", "", "local branch name (default: pr/<number>)")
	checkoutCmd.Flags().String("remote", "", "remote to fetch from (default: config remote, else origin)")

	prCmd.AddCommand(newCmd, checkoutCmd)
	rootCmd.AddCommand(prCmd)
}

// aiPRFlags captures CLI flags for `gk pr`.
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
		return fmt.Errorf("pr: load config: %w", err)
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
			return fmt.Errorf("pr: %w", fcErr)
		}
		prov = fc
	} else {
		p, pErr := provider.NewProvider(ctx, aiFactoryOptionsFromAI(ai))
		if pErr != nil {
			return fmt.Errorf("pr: provider: %w", pErr)
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
			return fmt.Errorf("pr: %w", err)
		}
		base = detected
	}
	Dbg("pr: base_branch=%s", base)

	// Compute merge-base.
	mbOut, _, err := deps.Runner.Run(ctx, "merge-base", "HEAD", base)
	if err != nil {
		return fmt.Errorf("pr: merge-base: %w", err)
	}
	mergeBase := strings.TrimSpace(string(mbOut))
	if mergeBase == "" {
		return fmt.Errorf("pr: could not determine merge-base between HEAD and %s", base)
	}
	Dbg("pr: merge_base=%s", mergeBase)

	// Collect diff and commits.
	diffOut, _, err := deps.Runner.Run(ctx, "diff", mergeBase+"..HEAD")
	if err != nil {
		return fmt.Errorf("pr: diff: %w", err)
	}
	diff := string(diffOut)

	logOut, _, err := deps.Runner.Run(ctx, "log", "--oneline", mergeBase+"..HEAD")
	if err != nil {
		return fmt.Errorf("pr: log: %w", err)
	}
	commitLines := strings.TrimSpace(string(logOut))

	// Edge case: no commits ahead of base.
	if diff == "" || commitLines == "" {
		fmt.Fprintln(deps.Out, "pr: no commits ahead of base branch — nothing to summarize")
		return nil
	}

	commits := strings.Split(commitLines, "\n")
	Dbg("pr: %d commit(s) in range %s..HEAD", len(commits), mergeBase[:minLen(len(mergeBase), 8)])

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

	// A PR body is deterministic in (diff, commits, lang, provider), so a
	// re-run on the same branch tip reuses the previous draft.
	lang := fallbackLang(deps.Lang)
	answer, err := runAIQuery(ctx, deps.Cmd, deps.Runner, deps.Provider, deps.AI, aiQuery{
		Kind:        "pr",
		Payload:     diff,
		Lang:        lang,
		MaxTokens:   aiChatMaxTokens(deps.AI),
		Timeout:     aiCallTimeout(deps.AI),
		TimeoutHint: "the provider exceeded ai.chat.timeout — raise it, or set a faster ai.<provider>.model.",
		// The commit list is part of the question, so it has to key the
		// cache too — the diff alone can be identical across reworded commits.
		CacheExtra:    []string{strings.Join(commits, "\n")},
		SpinnerLabel:  "pr — drafting summary",
		CacheEnabled:  true,
		SkipCacheRead: aiNoCacheRequested(deps.Cmd),
		// NOTE: commits travel UNREDACTED here, which is pre-existing
		// behaviour — `gk changelog` runs its commit list through the privacy
		// gate and `gk pr` never has. Preserved rather than changed as a side
		// effect of this refactor; it needs its own decision.
		Input: func(redacted string) provider.SummarizeInput {
			return provider.SummarizeInput{
				Kind:      "pr",
				Diff:      redacted,
				Commits:   commits,
				Lang:      lang,
				MaxTokens: aiChatMaxTokens(deps.AI),
			}
		},
	})
	if err != nil {
		return fmt.Errorf("pr: %w", err)
	}
	return outputPRResult(deps.Out, deps.ErrOut, answer.Text, flags.output)
}

func outputPRResult(out, errOut io.Writer, text, mode string) error {
	switch mode {
	case "clipboard":
		if err := copyToClipboard(text); err != nil {
			fmt.Fprintf(errOut, "pr: clipboard unavailable (%v), falling back to stdout\n", err)
			fmt.Fprint(out, text)
			return nil
		}
		fmt.Fprintln(out, "pr: PR description copied to clipboard")
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
