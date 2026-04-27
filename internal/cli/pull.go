package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

const (
	pullStrategyRebase = "rebase"
	pullStrategyMerge  = "merge"
	pullStrategyFFOnly = "ff-only"
	pullStrategyAuto   = "auto"
)

var pullVerbose int

// ConflictError is returned by runPullCore when a rebase conflict is detected.
// The caller (runPull) should exit with Code instead of printing an error.
type ConflictError struct {
	Code    int
	Stashed bool
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("rebase conflict (exit %d)", e.Code)
}

func init() {
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Fetch and integrate upstream changes",
		Long: `Fetches from the upstream and integrates it into the current branch.

Strategy resolution order (first match wins):
  1. --strategy flag
  2. pull.strategy in .gk.yaml
  3. git config pull.rebase  (true→rebase, false→merge)
  4. default: rebase

Upstream resolution:
  If the current branch tracks a remote branch (@{u}), that upstream is used.
  Otherwise gk falls back to <remote>/<base-branch>.

Fast-forward optimisation (D):
  When the strategy is rebase and HEAD is already an ancestor of the upstream,
  gk substitutes git merge --ff-only — identical result, no rebase overhead.`,
		RunE: runPull,
	}
	cmd.Flags().String("base", "", "base branch (auto-detect if empty)")
	cmd.Flags().String("strategy", "", "pull strategy: rebase|merge|ff-only|auto")
	cmd.Flags().Bool("no-rebase", false, "fetch only, do not integrate (skip rebase/merge)")
	cmd.Flags().Bool("autostash", false, "stash dirty changes before integration and pop after")
	cmd.Flags().CountVarP(&pullVerbose, "verbose", "v", "show upstream, strategy, and integration details; repeat for diagnostics")
	rootCmd.AddCommand(cmd)
}

func runPull(cmd *cobra.Command, args []string) error {
	err := runPullCore(cmd)
	var ce *ConflictError
	if errors.As(err, &ce) {
		os.Exit(ce.Code)
	}
	return err
}

// runPullCore contains the full pull logic and is separated for testability.
func runPullCore(cmd *cobra.Command) error {
	cfg, _ := config.Load(cmd.Flags())

	base, _ := cmd.Flags().GetString("base")
	if base == "" {
		base = cfg.BaseBranch
	}
	strategyFlag, _ := cmd.Flags().GetString("strategy")
	noRebase, _ := cmd.Flags().GetBool("no-rebase")
	autostash, _ := cmd.Flags().GetBool("autostash")

	repo := RepoFlag()
	runner := &git.ExecRunner{Dir: repo}
	client := git.NewClient(runner)
	ctx := cmd.Context()
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}

	// 1) auto-detect base if needed (only required when @{u} is absent)
	if base == "" {
		detected, err := client.DefaultBranch(ctx, remote)
		if err != nil {
			return fmt.Errorf("could not determine base branch: %w (use --base)", err)
		}
		base = detected
		Dbg("pull: auto-detected base=%s via remote=%s", base, remote)
	} else {
		Dbg("pull: base=%s (explicit)", base)
	}

	// 2) validate ref name (argv injection defence)
	if err := client.CheckRefFormat(ctx, base); err != nil {
		return fmt.Errorf("invalid base branch %q: %w", base, err)
	}

	// 3) resolve upstream: prefer tracking @{u}, fall back to remote/base
	upstream, fetchRemote, fetchBranch := resolveUpstream(ctx, runner, remote, base)
	Dbg("pull: upstream=%s fetchRemote=%s fetchBranch=%s", upstream, fetchRemote, fetchBranch)
	fmt.Fprintf(os.Stderr, "fetching %s...\n", upstream)

	// 4) dirty check
	dirty, err := client.IsDirty(ctx)
	if err != nil {
		return err
	}
	Dbg("pull: dirty=%v autostash=%v", dirty, autostash)

	var stashed bool
	if dirty {
		if !autostash {
			return WithHint(
				errors.New("working tree has uncommitted changes"),
				hintCommand("gk pull --autostash"),
			)
		}
		if _, _, err := runner.Run(ctx, "stash", "push", "-m", "gk pull autostash"); err != nil {
			return fmt.Errorf("stash failed: %w", err)
		}
		stashed = true
		Dbg("pull: autostashed working tree")
	}

	// 5) fetch
	if err := client.Fetch(ctx, fetchRemote, fetchBranch, false); err != nil {
		if stashed {
			popStashBestEffort(ctx, runner)
		}
		return fmt.Errorf("fetch failed: %w", err)
	}

	if noRebase {
		if stashed {
			popStashBestEffort(ctx, runner)
		}
		if pullVerbose > 0 {
			renderPullVerbosePlan(cmd.ErrOrStderr(), pullPlan{
				Repo:        repoDisplayPath(),
				Upstream:    upstream,
				FetchRemote: fetchRemote,
				FetchBranch: fetchBranch,
				Dirty:       dirty,
				Autostash:   autostash,
				Stashed:     stashed,
				NoRebase:    true,
				PreHEAD:     headRev(ctx, runner),
			})
		}
		renderFetchOnlySummary(cmd, runner, upstream)
		return nil
	}

	// 6) resolve strategy
	strategy, strategySource := resolveStrategyWithSource(ctx, strategyFlag, cfg, runner)
	Dbg("pull: strategy=%s (flag=%q cfg=%q)", strategy, strategyFlag, cfg.Pull.Strategy)

	// Capture pre-integration HEAD so we can summarize what the
	// integration actually pulled in. Failure to read HEAD is tolerated —
	// we fall back to a silent summary rather than aborting the pull.
	preHEAD := headRev(ctx, runner)

	// 7) D — fast-forward optimisation: if HEAD is already an ancestor of the
	//    upstream and strategy is rebase, substitute merge --ff-only (same
	//    end-state, no rebase process overhead).
	requestedStrategy := strategy
	ffOptimized := false
	if strategy == pullStrategyRebase && isFastForwardPossible(ctx, runner, upstream) {
		strategy = pullStrategyFFOnly
		ffOptimized = true
		Dbg("pull: ff-possible — substituting merge --ff-only for rebase")
	}

	if pullVerbose > 0 {
		renderPullVerbosePlan(cmd.ErrOrStderr(), pullPlan{
			Repo:              repoDisplayPath(),
			Upstream:          upstream,
			FetchRemote:       fetchRemote,
			FetchBranch:       fetchBranch,
			Dirty:             dirty,
			Autostash:         autostash,
			Stashed:           stashed,
			RequestedStrategy: requestedStrategy,
			Strategy:          strategy,
			StrategySource:    strategySource,
			FFOptimized:       ffOptimized,
			PreHEAD:           preHEAD,
			NoRebase:          noRebase,
		})
	}

	// 8) integrate
	fmt.Fprintf(os.Stderr, "integrating %s (%s)...\n", upstream, strategy)
	if err := executePullStrategy(ctx, client, runner, upstream, strategy, stashed); err != nil {
		return err
	}

	// Summary block: what came in, diffstat, one-line commit list.
	postHEAD := headRev(ctx, runner)
	renderPullSummary(cmd, runner, preHEAD, postHEAD, strategy)

	// 9) pop stash
	if stashed {
		if err := popStash(ctx, runner); err != nil {
			return fmt.Errorf("stash pop failed: %w", err)
		}
	}
	return nil
}

// headRev returns the current HEAD SHA or empty when it cannot be read
// (fresh repo with no commits, detached HEAD parse error, etc.).
func headRev(ctx context.Context, runner git.Runner) string {
	out, _, err := runner.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// resolveUpstream returns (upstreamRef, fetchRemote, fetchBranch).
// It checks whether the current branch has a tracking upstream (@{u}) and
// prefers that; falls back to remote/base when absent or detached.
func resolveUpstream(ctx context.Context, runner *git.ExecRunner, remote, base string) (string, string, string) {
	return resolveUpstreamFromRunner(ctx, runner, remote, base)
}

// resolveUpstreamFromRunner is the testable core of resolveUpstream; it
// accepts a git.Runner so tests can inject a FakeRunner.
func resolveUpstreamFromRunner(ctx context.Context, runner git.Runner, remote, base string) (string, string, string) {
	out, _, err := runner.Run(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err == nil {
		tracking := strings.TrimSpace(string(out))
		if tracking != "" && tracking != "@{u}" && strings.Contains(tracking, "/") {
			// tracking = "origin/feat/foo" → fetchRemote="origin", fetchBranch="feat/foo"
			idx := strings.Index(tracking, "/")
			return tracking, tracking[:idx], tracking[idx+1:]
		}
	}
	return remote + "/" + base, remote, base
}

// resolveStrategy determines the effective pull strategy using the priority chain:
//
//  1. explicit --strategy flag
//  2. pull.strategy in .gk.yaml (when not empty / not "auto")
//  3. git config pull.rebase
//  4. default: rebase
func resolveStrategy(ctx context.Context, flag string, cfg *config.Config, runner *git.ExecRunner) string {
	strategy, _ := resolveStrategyWithSource(ctx, flag, cfg, runner)
	return strategy
}

// resolveStrategyFromRunner is the testable core of resolveStrategy; it
// accepts a git.Runner and the raw config strategy string.
func resolveStrategyFromRunner(ctx context.Context, flag, cfgStrategy string, runner git.Runner) string {
	if flag != "" && flag != pullStrategyAuto {
		return flag
	}
	if cfgStrategy != "" && cfgStrategy != pullStrategyAuto {
		return cfgStrategy
	}
	// git config pull.rebase: "true"/"1" → rebase, "false"/"0" → merge
	if out, _, err := runner.Run(ctx, "config", "--get", "pull.rebase"); err == nil {
		switch strings.TrimSpace(string(out)) {
		case "true", "1", "yes":
			return pullStrategyRebase
		case "false", "0", "no":
			return pullStrategyMerge
		}
	}
	return pullStrategyRebase
}

func resolveStrategyWithSource(ctx context.Context, flag string, cfg *config.Config, runner git.Runner) (string, string) {
	if flag != "" && flag != pullStrategyAuto {
		return flag, "--strategy"
	}
	if cfg.Pull.Strategy != "" && cfg.Pull.Strategy != pullStrategyAuto {
		return cfg.Pull.Strategy, ".gk.yaml pull.strategy"
	}
	if out, _, err := runner.Run(ctx, "config", "--get", "pull.rebase"); err == nil {
		switch strings.TrimSpace(string(out)) {
		case "true", "1", "yes":
			return pullStrategyRebase, "git config pull.rebase"
		case "false", "0", "no":
			return pullStrategyMerge, "git config pull.rebase"
		}
	}
	return pullStrategyRebase, "default"
}

// isFastForwardPossible reports whether HEAD is an ancestor of upstream,
// meaning a fast-forward integration is possible without any divergence.
func isFastForwardPossible(ctx context.Context, runner git.Runner, upstream string) bool {
	_, _, err := runner.Run(ctx, "merge-base", "--is-ancestor", "HEAD", upstream)
	return err == nil
}

// executePullStrategy runs the chosen integration strategy and maps conflicts
// to ConflictError so the caller can os.Exit with the right code.
func executePullStrategy(ctx context.Context, client *git.Client, runner *git.ExecRunner, upstream, strategy string, stashed bool) error {
	switch strategy {
	case pullStrategyFFOnly:
		stdout, stderr, err := runner.Run(ctx, "merge", "--ff-only", upstream)
		if err != nil {
			combined := string(stdout) + string(stderr)
			if strings.Contains(combined, "Not possible to fast-forward") ||
				strings.Contains(combined, "fatal: Not possible") {
				return fmt.Errorf("fast-forward not possible — histories have diverged; try --strategy rebase or --strategy merge")
			}
			return fmt.Errorf("merge --ff-only: %w", err)
		}
		_ = stdout
		_ = stderr

	case pullStrategyMerge:
		stdout, stderr, err := runner.Run(ctx, "merge", "--no-edit", upstream)
		if err != nil {
			combined := string(stdout) + string(stderr)
			if strings.Contains(combined, "CONFLICT") || strings.Contains(combined, "Merge conflict") {
				fmt.Fprintln(os.Stderr, "conflict detected. resolve manually, then `git merge --continue` or `gk abort`.")
				if stashed {
					fmt.Fprintln(os.Stderr, "warning: autostash still applied — pop manually with `git stash pop`")
				}
				return &ConflictError{Code: 3, Stashed: stashed}
			}
			return fmt.Errorf("merge: %w\n%s", err, strings.TrimSpace(combined))
		}
		_ = stdout
		_ = stderr

	default: // rebase
		res, err := client.RebaseOnto(ctx, upstream)
		if err != nil {
			return err
		}
		if res.Conflict {
			fmt.Fprintln(os.Stderr, "conflict detected. run `gk continue`, `gk abort`, or `git rebase --continue` to resolve.")
			if stashed {
				fmt.Fprintln(os.Stderr, "warning: autostash still has changes stashed — pop manually with `git stash pop`")
			}
			return &ConflictError{Code: 3, Stashed: stashed}
		}
		_ = res
	}
	return nil
}

func popStash(ctx context.Context, r git.Runner) error {
	_, stderr, err := r.Run(ctx, "stash", "pop")
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// pullCommitLimit caps the one-line commit listing in renderPullSummary.
// Ten entries is enough to recognise a sprint's worth of catch-up without
// overflowing a terminal; the `+N more` footer advertises the remainder.
const pullCommitLimit = 10

type pullPlan struct {
	Repo              string
	Upstream          string
	FetchRemote       string
	FetchBranch       string
	Dirty             bool
	Autostash         bool
	Stashed           bool
	RequestedStrategy string
	Strategy          string
	StrategySource    string
	FFOptimized       bool
	PreHEAD           string
	NoRebase          bool
}

func renderPullVerbosePlan(w io.Writer, plan pullPlan) {
	dirty := "clean"
	dirtyNote := ""
	if plan.Dirty {
		dirty = "dirty"
		if plan.Stashed {
			dirtyNote = "autostashed"
		} else if plan.Autostash {
			dirtyNote = "autostash requested"
		}
	}

	strategy := plan.Strategy
	strategyNote := plan.StrategySource
	if plan.FFOptimized {
		strategy = plan.RequestedStrategy + " → " + plan.Strategy
		strategyNote = "fast-forward possible"
	}
	if plan.NoRebase {
		strategy = "fetch-only"
		strategyNote = "--no-rebase"
	}

	rows := []ui.SummaryRow{
		{Key: "repo", Value: plan.Repo},
		{Key: "upstream", Value: plan.Upstream, Note: "fetch " + plan.FetchRemote + " " + plan.FetchBranch},
		{Key: "strategy", Value: strategy, Note: strategyNote},
		{Key: "dirty", Value: dirty, Note: dirtyNote},
	}
	if plan.PreHEAD != "" {
		rows = append(rows, ui.SummaryRow{Key: "head", Value: shortSHA(plan.PreHEAD)})
	}
	block := ui.SummaryTable(rows)
	if NoColorFlag() {
		block = ui.PlainSummaryTable(rows)
	}
	if block != "" {
		fmt.Fprintln(w, block)
	}
}

// renderPullSummary prints a compact block describing what the
// integration actually changed — range, commit count, one-line subject
// list, and diffstat. When pre == post nothing changed; we emit the
// single "already up to date at <sha>" line so the user still confirms
// what HEAD resolved to. All output goes to stderr to match the rest
// of gk pull's progress stream.
func renderPullSummary(cmd *cobra.Command, runner git.Runner, pre, post, strategy string) {
	ctx := cmd.Context()
	out := cmd.ErrOrStderr()
	faint := color.New(color.Faint).SprintFunc()
	bold := color.New(color.Bold).SprintFunc()

	if pre == "" || post == "" {
		// Can't diff without both refs. Stay silent rather than lie.
		return
	}
	if pre == post {
		fmt.Fprintf(out, "already up to date at %s\n", bold(shortSHA(post)))
		return
	}

	// Commit count — cheap single-call roll-up.
	count := 0
	if n, _, err := runner.Run(ctx, "rev-list", "--count", pre+".."+post); err == nil {
		count, _ = strconv.Atoi(strings.TrimSpace(string(n)))
	}

	header := fmt.Sprintf("updated %s → %s", bold(shortSHA(pre)), bold(shortSHA(post)))
	meta := fmt.Sprintf("(+%d commit%s · %s)", count, plural(count), strategy)
	fmt.Fprintf(out, "%s  %s\n", header, faint(meta))

	// One-line commit list. Format fields with unit-separator (\x1f) so
	// subjects containing tabs/pipes do not split mid-row.
	commits, _, err := runner.Run(ctx, "log",
		fmt.Sprintf("--max-count=%d", pullCommitLimit),
		"--pretty=format:%h\x1f%s\x1f%an\x1f%at",
		pre+".."+post,
	)
	if err == nil {
		lines := strings.Split(strings.TrimRight(string(commits), "\n"), "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\x1f", 4)
			if len(parts) != 4 {
				continue
			}
			sha, subj, author, atStr := parts[0], parts[1], parts[2], parts[3]
			age := "now"
			if ts, err := strconv.ParseInt(strings.TrimSpace(atStr), 10, 64); err == nil {
				if a := formatAge(time.Since(time.Unix(ts, 0))); a != "" {
					age = a
				}
			}
			meta := fmt.Sprintf("<%s · %s>", author, age)
			fmt.Fprintf(out, "  %s  %s  %s\n", color.YellowString(sha), subj, faint(meta))
		}
		if count > pullCommitLimit {
			fmt.Fprintf(out, "  %s\n", faint(fmt.Sprintf("… +%d more", count-pullCommitLimit)))
		}
	}

	// Diffstat summary. `--shortstat` prints "N files changed, X insertions(+), Y deletions(-)".
	if stat, _, err := runner.Run(ctx, "diff", "--shortstat", pre+".."+post); err == nil {
		if s := strings.TrimSpace(string(stat)); s != "" {
			fmt.Fprintln(out, faint(s))
		}
	}
}

// renderFetchOnlySummary is the --no-rebase counterpart to
// renderPullSummary. After a fetch-only run we cannot describe "what
// integrated" (nothing did), so instead we surface how many upstream
// commits are now waiting locally and hint at the follow-up command.
func renderFetchOnlySummary(cmd *cobra.Command, runner git.Runner, upstream string) {
	ctx := cmd.Context()
	out := cmd.ErrOrStderr()
	faint := color.New(color.Faint).SprintFunc()

	// rev-list --left-right --count HEAD...upstream prints "ahead\tbehind".
	raw, _, err := runner.Run(ctx, "rev-list", "--left-right", "--count", "HEAD..."+upstream)
	if err != nil {
		fmt.Fprintln(out, "fetched; integrate with `gk pull`")
		return
	}
	fields := strings.Fields(strings.TrimSpace(string(raw)))
	if len(fields) != 2 {
		fmt.Fprintln(out, "fetched; integrate with `gk pull`")
		return
	}
	ahead, _ := strconv.Atoi(fields[0])
	behind, _ := strconv.Atoi(fields[1])

	switch {
	case ahead == 0 && behind == 0:
		fmt.Fprintln(out, "already up to date")
	case behind > 0 && ahead == 0:
		fmt.Fprintf(out, "fetched %s: %s %s waiting  %s\n",
			upstream,
			color.GreenString("+%d", behind),
			plural2(behind, "commit"),
			faint("(run `gk pull` to integrate)"))
	case behind > 0 && ahead > 0:
		fmt.Fprintf(out, "fetched %s: ↑%d local · ↓%d upstream  %s\n",
			upstream, ahead, behind,
			faint("(diverged — run `gk pull` to rebase/merge)"))
	case ahead > 0:
		fmt.Fprintf(out, "fetched %s: ↑%d local, upstream unchanged\n", upstream, ahead)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func plural2(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

func popStashBestEffort(ctx context.Context, r git.Runner) {
	_, _, _ = r.Run(ctx, "stash", "pop")
}
