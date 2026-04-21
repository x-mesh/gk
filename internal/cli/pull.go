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
		Short: "Fetch and rebase onto the base branch",
		Long:  "Runs `git fetch <remote> <base>` then `git rebase origin/<base>` on the current branch.",
		RunE:  runPull,
	}
	cmd.Flags().String("base", "", "base branch (auto-detect if empty)")
	cmd.Flags().Bool("no-rebase", false, "only fetch, do not rebase")
	cmd.Flags().Bool("autostash", false, "stash dirty changes before rebase and pop after")
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
// It returns *ConflictError when a rebase conflict is detected (caller should os.Exit).
func runPullCore(cmd *cobra.Command) error {
	cfg, _ := config.Load(cmd.Flags())

	base, _ := cmd.Flags().GetString("base")
	if base == "" {
		base = cfg.BaseBranch
	}
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

	// 1) auto-detect base
	if base == "" {
		detected, err := client.DefaultBranch(ctx, remote)
		if err != nil {
			return fmt.Errorf("could not determine base branch: %w (use --base)", err)
		}
		base = detected
	}

	// 2) validate ref name (argv injection defense)
	if err := client.CheckRefFormat(ctx, base); err != nil {
		return fmt.Errorf("invalid base branch %q: %w", base, err)
	}

	// 3) dirty check
	dirty, err := client.IsDirty(ctx)
	if err != nil {
		return err
	}

	var stashed bool
	if dirty {
		if !autostash {
			return errors.New("working tree has uncommitted changes (use --autostash)")
		}
		if _, _, err := runner.Run(ctx, "stash", "push", "-m", "gk pull autostash"); err != nil {
			return fmt.Errorf("stash failed: %w", err)
		}
		stashed = true
	}

	// 4) fetch
	fmt.Fprintf(os.Stderr, "fetching %s/%s...\n", remote, base)
	if err := client.Fetch(ctx, remote, base, false); err != nil {
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

	// 5) rebase
	upstream := remote + "/" + base
	fmt.Fprintf(os.Stderr, "rebasing onto %s...\n", upstream)
	res, err := client.RebaseOnto(ctx, upstream)
	if err != nil {
		if stashed {
			popStashBestEffort(ctx, runner)
		}
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

	// 6) pop stash
	if stashed {
		if err := popStash(ctx, runner); err != nil {
			return fmt.Errorf("stash pop failed: %w", err)
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
