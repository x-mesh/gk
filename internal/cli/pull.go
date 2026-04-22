package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

const (
	pullStrategyRebase = "rebase"
	pullStrategyMerge  = "merge"
	pullStrategyFFOnly = "ff-only"
	pullStrategyAuto   = "auto"
)

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
	}

	// 2) validate ref name (argv injection defence)
	if err := client.CheckRefFormat(ctx, base); err != nil {
		return fmt.Errorf("invalid base branch %q: %w", base, err)
	}

	// 3) resolve upstream: prefer tracking @{u}, fall back to remote/base
	upstream, fetchRemote, fetchBranch := resolveUpstream(ctx, runner, remote, base)
	fmt.Fprintf(os.Stderr, "fetching %s...\n", upstream)

	// 4) dirty check
	dirty, err := client.IsDirty(ctx)
	if err != nil {
		return err
	}

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
		fmt.Fprintln(os.Stderr, "done (fetch only)")
		return nil
	}

	// 6) resolve strategy
	strategy := resolveStrategy(ctx, strategyFlag, cfg, runner)

	// 7) D — fast-forward optimisation: if HEAD is already an ancestor of the
	//    upstream and strategy is rebase, substitute merge --ff-only (same
	//    end-state, no rebase process overhead).
	if strategy == pullStrategyRebase && isFastForwardPossible(ctx, runner, upstream) {
		strategy = pullStrategyFFOnly
	}

	// 8) integrate
	fmt.Fprintf(os.Stderr, "integrating %s (%s)...\n", upstream, strategy)
	if err := executePullStrategy(ctx, client, runner, upstream, strategy, stashed); err != nil {
		return err
	}

	// 9) pop stash
	if stashed {
		if err := popStash(ctx, runner); err != nil {
			return fmt.Errorf("stash pop failed: %w", err)
		}
	}
	return nil
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
	return resolveStrategyFromRunner(ctx, flag, cfg.Pull.Strategy, runner)
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
		if strings.Contains(strings.ToLower(string(stdout)+string(stderr)), "already up to date") {
			fmt.Fprintln(os.Stderr, "already up to date")
		}

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
		if strings.Contains(strings.ToLower(string(stdout)+string(stderr)), "already up to date") {
			fmt.Fprintln(os.Stderr, "already up to date")
		}

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
		if res.NothingTo {
			fmt.Fprintln(os.Stderr, "already up to date")
		}
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

func popStashBestEffort(ctx context.Context, r git.Runner) {
	_, _, _ = r.Run(ctx, "stash", "pop")
}
