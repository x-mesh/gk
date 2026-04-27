package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

// wipMarker is the conventional subject used by gwip / gunwip.
const wipMarker = "--wip-- [skip ci]"

func init() {
	wip := &cobra.Command{
		Use:   "wip",
		Short: "Stage everything and create a throwaway WIP commit",
		Long: `Creates a disposable commit so you can switch contexts without losing work.

Stages all tracked changes (including deletions) and commits with subject
"--wip-- [skip ci]". The commit skips hooks (--no-verify) and signing
(--no-gpg-sign) so it is fast and reversible — use 'gk unwip' later to
undo the commit while keeping the changes staged.`,
		RunE: runWip,
	}
	rootCmd.AddCommand(wip)

	unwip := &cobra.Command{
		Use:   "unwip",
		Short: "Undo a WIP commit created by 'gk wip'",
		Long: `If HEAD is a commit whose subject starts with '--wip--', resets it with
'git reset HEAD~1' so the changes return to the working tree. Refuses to
act on non-WIP commits.`,
		RunE: runUnwip,
	}
	rootCmd.AddCommand(unwip)
}

func runWip(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	return createWipCommit(cmd.Context(), runner, cmd.OutOrStdout())
}

func createWipCommit(ctx context.Context, runner git.Runner, w io.Writer) error {
	if _, stderr, err := runner.Run(ctx, "add", "-A"); err != nil {
		return fmt.Errorf("git add -A: %s: %w", strings.TrimSpace(string(stderr)), err)
	}

	// Nothing to commit? Report cleanly so the WIP commit doesn't fail.
	clean, err := stagingIsEmpty(ctx, runner)
	if err != nil {
		return err
	}
	if clean {
		fmt.Fprintln(w, "nothing to wip — working tree is clean")
		return nil
	}

	_, stderr, err := runner.Run(ctx,
		"commit", "--no-verify", "--no-gpg-sign", "-m", wipMarker)
	if err != nil {
		return fmt.Errorf("git commit: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprintln(w, "wip commit created — run 'gk unwip' to restore")
	return nil
}

func runUnwip(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	subj, _, err := runner.Run(ctx, "log", "-1", "--format=%s")
	if err != nil {
		return fmt.Errorf("git log: %w", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(subj)), "--wip--") {
		return WithHint(fmt.Errorf("HEAD is not a wip commit"),
			"inspect with: git log -1")
	}

	if _, stderr, err := runner.Run(ctx, "reset", "HEAD~1"); err != nil {
		return fmt.Errorf("git reset HEAD~1: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprintln(w, "wip commit undone — changes returned to working tree")
	return nil
}

// stagingIsEmpty reports whether `git diff --cached` has no changes.
func stagingIsEmpty(ctx context.Context, r git.Runner) (bool, error) {
	stdout, _, err := r.Run(ctx, "diff", "--cached", "--name-only")
	if err != nil {
		return false, fmt.Errorf("git diff --cached: %w", err)
	}
	return strings.TrimSpace(string(stdout)) == "", nil
}
