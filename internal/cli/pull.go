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
	"github.com/x-mesh/gk/internal/gitstate"
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
	cmd.Flags().Bool("rebase", false, "shorthand for --strategy rebase (also acts as explicit consent on diverged history)")
	cmd.Flags().Bool("merge", false, "shorthand for --strategy merge (also acts as explicit consent on diverged history)")
	cmd.Flags().Bool("fetch-only", false, "fetch only, do not integrate")
	cmd.Flags().Bool("no-rebase", false, "deprecated alias for --fetch-only")
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
	rebaseFlag, _ := cmd.Flags().GetBool("rebase")
	mergeFlag, _ := cmd.Flags().GetBool("merge")
	fetchOnly, _ := cmd.Flags().GetBool("fetch-only")
	noRebase, _ := cmd.Flags().GetBool("no-rebase")
	autostash, _ := cmd.Flags().GetBool("autostash")

	// Translate --rebase/--merge into --strategy and reject conflicting flags.
	// These shorthands also act as explicit consent for diverged-history pulls.
	if rebaseFlag && mergeFlag {
		return errors.New("--rebase and --merge are mutually exclusive")
	}
	if rebaseFlag && strategyFlag != "" && strategyFlag != pullStrategyRebase {
		return errors.New("--rebase conflicts with --strategy " + strategyFlag)
	}
	if mergeFlag && strategyFlag != "" && strategyFlag != pullStrategyMerge {
		return errors.New("--merge conflicts with --strategy " + strategyFlag)
	}
	if rebaseFlag {
		strategyFlag = pullStrategyRebase
	}
	if mergeFlag {
		strategyFlag = pullStrategyMerge
	}
	// --fetch-only is the preferred name; --no-rebase is its legacy alias.
	if fetchOnly {
		noRebase = true
	}

	repo := RepoFlag()
	runner := &git.ExecRunner{Dir: repo}
	client := git.NewClient(runner)
	ctx := cmd.Context()
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}

	// 0) Refuse early when a previous rebase / merge / cherry-pick is
	//    still paused. Without this check `gk pull` proceeds to fetch
	//    and then tries to autostash, which git rejects with an opaque
	//    "could not write index" because the index is reserved by the
	//    paused operation. The user should resume or abort that first.
	if state, err := gitstate.Detect(ctx, repo); err == nil && state.Kind != gitstate.StateNone {
		printPullBlockedByState(cmd.ErrOrStderr(), ctx, client, runner, state.Kind)
		return fmt.Errorf("a %s is in progress — resolve it first", inProgressLabel(state.Kind))
	}

	// 1) Resolve upstream — prefer the current branch's tracking @{u}.
	//    Only fall back to <remote>/<base> when no upstream is configured,
	//    which is when base detection actually matters. This matches what
	//    `git pull` does and avoids spurious "could not determine default
	//    branch" failures in repos where origin/HEAD is unset but the
	//    branch tracks something perfectly fine.
	upstream, fetchRemote, fetchBranch, hasTracking := tryTrackingUpstream(ctx, runner)
	if !hasTracking {
		// No upstream configured for the current branch. Before falling back
		// to the repo's base branch (which can confuse users — "I'm on
		// develop, why is it pulling main?"), see if <remote>/<currentBranch>
		// exists. If it does, that ref matches user intent far better than
		// the base, and we surface a hint explaining why it isn't tracked.
		current, currErr := client.CurrentBranch(ctx)
		if currErr == nil && current != "" && git.RefExists(ctx, runner, remote+"/"+current) {
			upstream = remote + "/" + current
			fetchRemote = remote
			fetchBranch = current
			fmt.Fprintf(cmd.ErrOrStderr(),
				"note: '%s' has no upstream configured — using %s (set tracking with: git branch --set-upstream-to=%s %s)\n",
				current, upstream, upstream, current,
			)
			Dbg("pull: no @{u}; using same-name remote ref %s", upstream)
		} else {
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
			if err := client.CheckRefFormat(ctx, base); err != nil {
				return fmt.Errorf("invalid base branch %q: %w", base, err)
			}
			upstream = remote + "/" + base
			fetchRemote = remote
			fetchBranch = base
			if current != "" && current != base {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"note: '%s' has no upstream and no cached '%s/%s' — falling back to base branch %s\n"+
						"      if '%s/%s' exists on the remote, run: git fetch %s %s && git branch --set-upstream-to=%s/%s\n",
					current, remote, current, upstream,
					remote, current, remote, current, remote, current,
				)
			}
		}
	}
	Dbg("pull: upstream=%s fetchRemote=%s fetchBranch=%s tracking=%v", upstream, fetchRemote, fetchBranch, hasTracking)

	// 4) dirty check
	dirty, err := client.IsDirty(ctx)
	if err != nil {
		return err
	}
	Dbg("pull: dirty=%v autostash=%v", dirty, autostash)

	var stashed bool
	if dirty {
		switch {
		case autostash:
			created, sErr := stashIfChanged(ctx, runner, "push", "-m", "gk pull autostash")
			if sErr != nil {
				return fmt.Errorf("stash failed: %w", sErr)
			}
			if !created {
				hint := describeDirtyButNotStashed(ctx, runner)
				if hint == "" {
					hint = "stash push reported success but produced no entry"
				}
				Dbg("pull: --autostash created no stash entry — %s", hint)
			}
			stashed = created
			Dbg("pull: autostashed working tree (--autostash) stashed=%v", stashed)
		case ui.IsTerminal():
			ok, perr := promptStashDirty(ctx, runner, "gk pull autostash")
			if perr != nil {
				if errors.Is(perr, errSkipDirty) {
					return WithHint(
						errors.New("working tree has uncommitted changes"),
						hintCommand("gk pull --autostash"),
					)
				}
				return perr
			}
			stashed = ok
			Dbg("pull: autostashed working tree (interactive prompt)")
		default:
			return WithHint(
				errors.New("working tree has uncommitted changes"),
				hintCommand("gk pull --autostash"),
			)
		}
	}

	// 5) fetch — when --verbose, stream git's progress into a viewport
	// so the user sees objects/deltas in real time. Default path keeps
	// the quieter spinner-based fetch via client.Fetch.
	var fetchErr error
	if Verbose() && ui.IsTerminal() {
		args := []string{"-C", RepoFlag()}
		if RepoFlag() == "" {
			args = []string{}
		}
		args = append(args, "fetch", fetchRemote)
		if fetchBranch != "" {
			args = append(args, fetchBranch)
		}
		fetchErr = ui.RunCommandStreamTUI(ctx, "fetching "+upstream, "git", args...)
	} else {
		stopFetch := ui.StartBubbleSpinner(fmt.Sprintf("fetching %s", upstream))
		fetchErr = client.Fetch(ctx, fetchRemote, fetchBranch, false)
		stopFetch()
	}
	if fetchErr != nil {
		if stashed {
			popStashBestEffort(ctx, runner)
		}
		return fmt.Errorf("fetch failed: %w", fetchErr)
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

	// 6) ahead/behind dispatch — decide whether integration is needed at
	//    all, and refuse on diverged history unless the user has expressed
	//    explicit consent (flag, .gk.yaml, or git config).
	preHEAD := headRev(ctx, runner)
	ahead, behind, abErr := computeAheadBehind(ctx, runner, upstream)
	if abErr != nil {
		// Detection failed — preserve legacy behaviour rather than block.
		Dbg("pull: ahead/behind detection failed: %v (falling through)", abErr)
	}
	Dbg("pull: ahead=%d behind=%d", ahead, behind)

	// 6a) Already up to date.
	if abErr == nil && ahead == 0 && behind == 0 {
		if stashed {
			if err := popStash(ctx, runner); err != nil {
				return fmt.Errorf("stash pop failed: %w", err)
			}
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "already up to date at %s\n", shortSHA(preHEAD))
		return nil
	}

	// 6b) Local has commits not on upstream, upstream has nothing new.
	//     Nothing to integrate.
	if abErr == nil && ahead > 0 && behind == 0 {
		if stashed {
			if err := popStash(ctx, runner); err != nil {
				return fmt.Errorf("stash pop failed: %w", err)
			}
		}
		fmt.Fprintf(cmd.ErrOrStderr(),
			"no upstream changes; local is ahead by %d commit%s — nothing to pull\n",
			ahead, plural(ahead))
		return nil
	}

	// 7) Resolve strategy.
	strategy, strategySource := resolveStrategyWithSource(ctx, strategyFlag, cfg, runner)
	Dbg("pull: strategy=%s source=%s (flag=%q cfg=%q)", strategy, strategySource, strategyFlag, cfg.Pull.Strategy)

	// 7a) Diverged — refuse unless the user gave explicit consent.
	//     "default" source means we'd be rebasing on autopilot, which
	//     silently rewrites SHAs of any unpushed local commits.
	diverged := abErr == nil && ahead > 0 && behind > 0
	if diverged && strategySource == "default" {
		if stashed {
			popStashBestEffort(ctx, runner)
		}
		printDivergenceRefusal(cmd.ErrOrStderr(), runner, ctx, upstream, ahead, behind)
		return errors.New("histories diverged: choose --rebase, --merge, or --fetch-only")
	}

	// 7a') Detection failure with no explicit consent — apply the same
	//      safety net via a legacy ancestry probe so a transient rev-list
	//      error can't bypass the refusal. If HEAD is provably an ancestor
	//      of upstream, ff is safe; otherwise we cannot rule out a real
	//      divergence and must refuse.
	if abErr != nil && strategySource == "default" {
		if !isFastForwardPossible(ctx, runner, upstream) {
			if stashed {
				popStashBestEffort(ctx, runner)
			}
			fmt.Fprintf(cmd.ErrOrStderr(),
				"\n%s could not verify ahead/behind state (%v).\n",
				color.YellowString("⚠"), abErr)
			fmt.Fprintln(cmd.ErrOrStderr(),
				"  refusing to auto-rebase without confirmation. choose:")
			fmt.Fprintln(cmd.ErrOrStderr(),
				"    gk pull --rebase | --merge | --fetch-only")
			return errors.New("ahead/behind detection failed and no explicit strategy chosen")
		}
	}

	// 7b) Pure fast-forward — pick ff-only regardless of resolved strategy.
	//     `merge` would auto-FF anyway and `rebase` reduces to FF, so we
	//     normalise here for a clear summary line.
	requestedStrategy := strategy
	ffOptimized := false
	if abErr == nil && ahead == 0 && behind > 0 && strategy != pullStrategyFFOnly {
		strategy = pullStrategyFFOnly
		ffOptimized = true
		Dbg("pull: ff-possible — substituting merge --ff-only")
	} else if abErr != nil && strategy == pullStrategyRebase && isFastForwardPossible(ctx, runner, upstream) {
		// Detection failed; fall back to legacy ff probe.
		strategy = pullStrategyFFOnly
		ffOptimized = true
	}

	// 7c) Backup ref before any history-rewriting integration. Diverged
	//     rebase rewrites local SHAs; merge creates a merge commit but
	//     also changes the branch tip, so a backup is cheap insurance.
	//     ff-only never rewrites anything, so skip it there.
	if diverged && (strategy == pullStrategyRebase || strategy == pullStrategyMerge) && preHEAD != "" {
		if currentBranch, cerr := client.CurrentBranch(ctx); cerr == nil && currentBranch != "" {
			if ref, berr := client.CreateBackup(ctx, currentBranch, preHEAD); berr == nil {
				Dbg("pull: backup ref created: %s", ref)
				_ = client.PruneBackups(ctx, currentBranch, 30*24*time.Hour, 5)
			} else {
				Dbg("pull: backup creation failed: %v", berr)
			}
		}
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

// tryTrackingUpstream reads the current branch's @{u} and parses it into
// (upstreamRef, fetchRemote, fetchBranch). Returns ok=false when no upstream
// is configured (detached HEAD, branch without tracking, etc.) — in that
// case the caller is responsible for falling back to a base branch.
func tryTrackingUpstream(ctx context.Context, runner git.Runner) (upstream, fetchRemote, fetchBranch string, ok bool) {
	out, _, err := runner.Run(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	if err != nil {
		return "", "", "", false
	}
	tracking := strings.TrimSpace(string(out))
	if tracking == "" || tracking == "@{u}" || !strings.Contains(tracking, "/") {
		return "", "", "", false
	}
	// tracking = "origin/feat/foo" → fetchRemote="origin", fetchBranch="feat/foo"
	// Reject malformed inputs where either side is empty ("origin/" or "/foo")
	// — those values would propagate as empty refs and silently bypass the
	// ahead/behind detection.
	idx := strings.Index(tracking, "/")
	remote, branch := tracking[:idx], tracking[idx+1:]
	if remote == "" || branch == "" {
		return "", "", "", false
	}
	return tracking, remote, branch, true
}

// computeAheadBehind reports how many commits HEAD has that upstream lacks
// (ahead) and vice versa (behind). It runs a single `git rev-list
// --left-right --count HEAD...upstream` invocation, so the cost is O(merge
// distance), not O(history). Errors propagate so the caller can decide
// whether to abort or fall through to legacy heuristics.
func computeAheadBehind(ctx context.Context, runner git.Runner, upstream string) (ahead, behind int, err error) {
	out, _, runErr := runner.Run(ctx, "rev-list", "--left-right", "--count", "HEAD..."+upstream)
	if runErr != nil {
		return 0, 0, runErr
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("rev-list output malformed: %q", out)
	}
	a, aerr := strconv.Atoi(fields[0])
	if aerr != nil {
		return 0, 0, fmt.Errorf("ahead parse: %w", aerr)
	}
	b, berr := strconv.Atoi(fields[1])
	if berr != nil {
		return 0, 0, fmt.Errorf("behind parse: %w", berr)
	}
	return a, b, nil
}

// printDivergenceRefusal renders a multi-line explanation when gk pull
// declines to integrate a diverged history without explicit consent. It
// shows the local commits at risk (their SHA will be rewritten on rebase)
// plus an upstream count, then enumerates the three resolution paths:
// rebase, merge, or fetch-only.
func printDivergenceRefusal(w io.Writer, runner git.Runner, ctx context.Context, upstream string, ahead, behind int) {
	bold := color.New(color.Bold).SprintFunc()
	yellow := color.YellowString
	faint := color.New(color.Faint).SprintFunc()

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s histories diverged.\n", yellow("⚠"))
	fmt.Fprintf(w, "  local has %s unpushed commit%s, upstream %s has %s new commit%s.\n",
		bold(strconv.Itoa(ahead)), plural(ahead),
		bold(upstream),
		bold(strconv.Itoa(behind)), plural(behind))

	// One-line list of the at-risk local commits (capped).
	if commits, _, err := runner.Run(ctx, "log",
		fmt.Sprintf("--max-count=%d", pullCommitLimit),
		"--pretty=format:%h %s",
		upstream+"..HEAD",
	); err == nil {
		lines := strings.Split(strings.TrimRight(string(commits), "\n"), "\n")
		if len(lines) > 0 && lines[0] != "" {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "  local commits at risk (SHAs change on rebase):")
			for _, line := range lines {
				if line == "" {
					continue
				}
				parts := strings.SplitN(line, " ", 2)
				if len(parts) == 2 {
					fmt.Fprintf(w, "    %s  %s\n", yellow(parts[0]), parts[1])
				}
			}
			if ahead > pullCommitLimit {
				fmt.Fprintf(w, "    %s\n", faint(fmt.Sprintf("… +%d more", ahead-pullCommitLimit)))
			}
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "  pick one:")
	fmt.Fprintf(w, "    %s   %s\n", bold("gk pull --rebase"), faint("replay local on top of upstream (rewrites SHA)"))
	fmt.Fprintf(w, "    %s    %s\n", bold("gk pull --merge"), faint("create a merge commit (preserves SHA)"))
	fmt.Fprintf(w, "    %s   %s\n", bold("gk pull --fetch-only"), faint("just fetch, decide later"))
	fmt.Fprintln(w, faint("  a backup ref is created automatically before --rebase or --merge."))
	fmt.Fprintln(w)
}

// resolveStrategyFromRunner is the testable core of the strategy
// resolution chain (--strategy → pull.strategy → git config pull.rebase
// → rebase default); it accepts a git.Runner and the raw config string.
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
	return resolveIntegrationStrategy(ctx, flag, cfg.Pull.Strategy, ".gk.yaml pull.strategy", runner)
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
				printIntegrationConflict(os.Stderr, ctx, client, runner, "merge", stashed)
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
			printIntegrationConflict(os.Stderr, ctx, client, runner, "rebase", stashed)
			return &ConflictError{Code: 3, Stashed: stashed}
		}
		_ = res
	}
	return nil
}

// inProgressLabel maps a gitstate StateKind to a human label suitable
// for the "a X is in progress" sentence. Kept narrow on purpose — only
// the operations that conflict with `gk pull`'s preconditions need a
// label here. Anything else falls through to "git operation".
func inProgressLabel(k gitstate.StateKind) string {
	switch k {
	case gitstate.StateRebaseMerge, gitstate.StateRebaseApply:
		return "rebase"
	case gitstate.StateMerge:
		return "merge"
	case gitstate.StateCherryPick:
		return "cherry-pick"
	case gitstate.StateRevert:
		return "revert"
	default:
		return "git operation"
	}
}

// printPullBlockedByState explains why `gk pull` refuses to proceed when
// a prior integration is still paused. Reuses the same conflict-file
// listing as the in-flight conflict banner so the user sees the exact
// same recovery surface whether they hit a fresh conflict or run pull
// while one is already pending.
func printPullBlockedByState(w io.Writer, ctx context.Context, client *git.Client, runner git.Runner, kind gitstate.StateKind) {
	yellow := color.YellowString
	red := color.RedString
	green := color.GreenString
	bold := color.New(color.Bold).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()

	label := inProgressLabel(kind)
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s cannot pull: a %s is already in progress\n", yellow("✗"), bold(label))

	repoDir := ""
	if er, ok := runner.(*git.ExecRunner); ok {
		repoDir = er.Dir
	}

	var info *git.RebaseConflictInfo
	switch kind {
	case gitstate.StateRebaseMerge, gitstate.StateRebaseApply:
		info, _ = client.RebaseConflictStatus(ctx)
		if info != nil {
			if info.StoppedSHA != "" {
				short := info.StoppedSHA
				if len(short) > 7 {
					short = short[:7]
				}
				if info.StoppedSubj != "" {
					fmt.Fprintf(w, "  paused on %s  %s\n", bold(short), info.StoppedSubj)
				} else {
					fmt.Fprintf(w, "  paused on %s\n", bold(short))
				}
			}
			if info.Total > 0 {
				fmt.Fprintf(w, "  progress %d/%d\n", info.Done, info.Total)
			}
			renderConflictFileLists(w, info, red, green)
		} else {
			info = probeUnmergedFiles(ctx, runner)
			renderConflictFileLists(w, info, red, green)
		}
	default:
		// Merge / cherry-pick / revert — only the file lists matter.
		info = probeUnmergedFiles(ctx, runner)
		renderConflictFileLists(w, info, red, green)
	}

	if info != nil && len(info.Unmerged) > 0 {
		renderInlineConflicts(w, repoDir, info.Unmerged)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "  resolve first:")
	fmt.Fprintf(w, "    1. fix conflict markers — pick one:\n")
	fmt.Fprintf(w, "         %s             %s\n",
		bold("gk resolve"), faint("AI-assisted (preview with --dry-run)"))
	fmt.Fprintf(w, "         %s   %s\n",
		bold("gk resolve --strategy ours"), faint("take HEAD across all conflicts"))
	fmt.Fprintf(w, "         %s %s\n",
		bold("gk resolve --strategy theirs"), faint("take incoming across all conflicts"))
	fmt.Fprintf(w, "         %s            %s\n",
		faint("manual:"),
		faint("edit each file, then "+bold("git add <file>")))
	if kind == gitstate.StateRebaseMerge || kind == gitstate.StateRebaseApply {
		fmt.Fprintf(w, "    2. %s         %s\n", bold("gk continue"), faint("(finish the paused rebase)"))
		fmt.Fprintf(w, "       %s            %s\n", bold("gk abort"), faint("(discard the rebase, return to pre-pull state)"))
	} else {
		fmt.Fprintf(w, "    2. %s         %s\n", bold("gk continue"), faint("(complete the paused operation)"))
		fmt.Fprintf(w, "       %s            %s\n", bold("gk abort"), faint("(discard it)"))
	}
	fmt.Fprintln(w, "  then re-run `gk pull`.")

	if branch, err := client.CurrentBranch(ctx); err == nil && branch != "" {
		if ref := client.LatestBackupRef(ctx, branch); ref != "" {
			fmt.Fprintf(w, "\n  %s   %s\n", faint("backup:"), bold(ref))
		}
	}
	fmt.Fprintln(w)
}

// printIntegrationConflict renders a richer paused-integration banner
// than the previous one-liner: which commit stopped the rebase, how
// far through the plan we are, which files still carry markers, which
// auto-merged cleanly, and what to type next. mode is "rebase" or
// "merge"; the resolution commands differ slightly between them.
func printIntegrationConflict(w io.Writer, ctx context.Context, client *git.Client, runner git.Runner, mode string, stashed bool) {
	yellow := color.YellowString
	red := color.RedString
	green := color.GreenString
	bold := color.New(color.Bold).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()

	repoDir := ""
	if er, ok := runner.(*git.ExecRunner); ok {
		repoDir = er.Dir
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s %s paused on conflict\n", yellow("✗"), mode)

	var info *git.RebaseConflictInfo
	// rebase-specific context: which commit stopped us, position in plan.
	if mode == "rebase" {
		info, _ = client.RebaseConflictStatus(ctx)
		if info != nil {
			if info.StoppedSHA != "" {
				short := info.StoppedSHA
				if len(short) > 7 {
					short = short[:7]
				}
				if info.StoppedSubj != "" {
					fmt.Fprintf(w, "  applying %s  %s\n", bold(short), info.StoppedSubj)
				} else {
					fmt.Fprintf(w, "  applying %s\n", bold(short))
				}
			}
			if info.Total > 0 {
				fmt.Fprintf(w, "  progress %d/%d  %s\n",
					info.Done, info.Total,
					faint(fmt.Sprintf("(%d remaining after this)", info.Remaining())))
			}
			renderConflictFileLists(w, info, red, green)
		} else {
			info = probeUnmergedFiles(ctx, runner)
			renderConflictFileLists(w, info, red, green)
		}
	} else {
		// Merge: no rebase metadata; just probe the working tree.
		info = probeUnmergedFiles(ctx, runner)
		renderConflictFileLists(w, info, red, green)
	}

	// Inline preview of the first conflict region — usually enough for
	// the user to decide "trivial rename, fix in 30s" vs "open editor".
	if info != nil && len(info.Unmerged) > 0 {
		renderInlineConflicts(w, repoDir, info.Unmerged)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "  resolve:")
	fmt.Fprintf(w, "    1. fix conflict markers — pick one:\n")
	fmt.Fprintf(w, "         %s             %s\n",
		bold("gk resolve"), faint("AI-assisted (preview with --dry-run)"))
	fmt.Fprintf(w, "         %s   %s\n",
		bold("gk resolve --strategy ours"), faint("take HEAD across all conflicts"))
	fmt.Fprintf(w, "         %s %s\n",
		bold("gk resolve --strategy theirs"), faint("take incoming across all conflicts"))
	fmt.Fprintf(w, "         %s            %s\n",
		faint("manual:"),
		faint("edit each file, remove "+bold("<<<<<<<")+" / "+bold("=======")+" / "+bold(">>>>>>>")+" markers, then "+bold("git add <file>")))
	if mode == "rebase" {
		fmt.Fprintf(w, "    2. %s         %s\n",
			bold("gk continue"), faint("(finish this commit, proceed to next pick)"))
		fmt.Fprintf(w, "       %s            %s\n",
			bold("gk abort"), faint("(give up rebase, return to pre-pull state)"))
	} else {
		fmt.Fprintf(w, "    2. %s         %s\n",
			bold("gk continue"), faint("(create the merge commit)"))
		fmt.Fprintf(w, "       %s            %s\n",
			bold("gk abort"), faint("(discard the merge attempt)"))
	}

	// Backup ref hint — only show when we actually have one for the
	// current branch, so the user sees something they can copy.
	if branch, err := client.CurrentBranch(ctx); err == nil && branch != "" {
		if ref := client.LatestBackupRef(ctx, branch); ref != "" {
			fmt.Fprintf(w, "\n  %s   %s\n",
				faint("backup:"),
				bold(ref))
			fmt.Fprintf(w, "  %s\n",
				faint("   → recover with `git reset --hard "+ref+"` if you need to bail"))
		}
	}

	if stashed {
		fmt.Fprintf(w, "\n%s autostash still applied — pop manually with `git stash pop` if you abort\n",
			yellow("!"))
	}
	fmt.Fprintln(w)
}

// probeUnmergedFiles is a fallback for code paths where we don't have
// the full RebaseConflictInfo (merge conflict, rebase-apply legacy).
// It populates only the file lists from porcelain output.
func probeUnmergedFiles(ctx context.Context, runner git.Runner) *git.RebaseConflictInfo {
	info := &git.RebaseConflictInfo{}
	if out, _, err := runner.Run(ctx, "diff", "--name-only", "--diff-filter=U"); err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			if line != "" {
				info.Unmerged = append(info.Unmerged, line)
			}
		}
	}
	if out, _, err := runner.Run(ctx, "diff", "--name-only", "--cached", "--diff-filter=ACMRT"); err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			if line != "" {
				info.Staged = append(info.Staged, line)
			}
		}
	}
	return info
}

func renderConflictFileLists(w io.Writer, info *git.RebaseConflictInfo, red, green func(string, ...interface{}) string) {
	if info == nil {
		return
	}
	if len(info.Unmerged) > 0 {
		fmt.Fprintf(w, "\n  %s files with conflicts (need manual resolution):\n", red("✗"))
		for _, f := range info.Unmerged {
			fmt.Fprintf(w, "    %s\n", red(f))
		}
	}
	if len(info.Staged) > 0 {
		fmt.Fprintf(w, "\n  %s files already staged (auto-merged or resolved):\n", green("✓"))
		for _, f := range info.Staged {
			fmt.Fprintf(w, "    %s\n", green(f))
		}
	}
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
