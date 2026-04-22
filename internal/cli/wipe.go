package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "wipe",
		Short: "Discard ALL local changes and untracked files (with backup ref)",
		Long: `Hard-resets HEAD and runs 'git clean -fd' to wipe the working tree.

DESTRUCTIVE — uncommitted changes and untracked files are gone. A backup
ref is created at refs/gk/wipe-backup/<branch>/<unix> pointing at the
pre-wipe HEAD so local commits remain recoverable (untracked files do not).
Requires TTY confirmation or --yes.

Examples:
  gk wipe --dry-run          # show what would be wiped
  gk wipe --yes              # reset --hard + clean -fd, non-interactive
  gk wipe --include-ignored  # also remove .gitignore'd files (clean -fdx)
`,
		RunE: runWipe,
	}
	cmd.Flags().BoolP("yes", "y", false, "skip confirmation prompt")
	cmd.Flags().Bool("dry-run", false, "print what would happen without wiping")
	cmd.Flags().Bool("include-ignored", false, "also remove ignored files (clean -fdx)")
	rootCmd.AddCommand(cmd)
}

func runWipe(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	yes, _ := cmd.Flags().GetBool("yes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	includeIgnored, _ := cmd.Flags().GetBool("include-ignored")

	branch, _ := client.CurrentBranch(ctx) // detached is OK

	cleanArgs := []string{"clean", "-fd"}
	if includeIgnored {
		cleanArgs = []string{"clean", "-fdx"}
	}

	fmt.Fprintf(w, "branch:  %s\n", displayBranch(branch))
	fmt.Fprintln(w, "actions: git reset --hard HEAD")
	fmt.Fprintf(w, "         git %s\n", strings.Join(cleanArgs[1:], " "))
	if !dryRun {
		fmt.Fprintf(w, "backup:  refs/gk/wipe-backup/%s/<unix>\n", gitsafe.SanitizeBranchSegment(branch))
	}

	if dryRun {
		fmt.Fprintln(w, "[dry-run] skipping reset and clean")
		return nil
	}

	if !yes {
		if !ui.IsTerminal() {
			return WithHint(fmt.Errorf("refusing to wipe without confirmation"),
				"pass --yes to proceed non-interactively")
		}
		ok, cerr := ui.Confirm(
			fmt.Sprintf("Discard all changes and untracked files on %s?", displayBranch(branch)),
			false,
		)
		if cerr != nil {
			return cerr
		}
		if !ok {
			return fmt.Errorf("aborted")
		}
	}

	// Preflight: refuse during rebase/merge/cherry-pick even though wipe
	// intentionally destroys dirty state. Running `reset --hard` while a
	// rebase is in progress leaves the repo in a half-broken state.
	rep, err := gitsafe.Check(ctx, runner, gitsafe.WithWorkDir(RepoFlag()))
	if err != nil {
		return err
	}
	if err := rep.AllowDirty().Err(); err != nil {
		return err
	}

	restorer := gitsafe.NewRestorer(runner, time.Now, "wipe")
	backupRef, err := restorer.Backup(ctx, branch)
	if err != nil {
		return fmt.Errorf("create backup ref: %w", err)
	}

	if _, stderr, err := runner.Run(ctx, "reset", "--hard", "HEAD"); err != nil {
		return fmt.Errorf("reset --hard: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	if _, stderr, err := runner.Run(ctx, cleanArgs...); err != nil {
		return fmt.Errorf("%s: %s: %w", strings.Join(cleanArgs, " "), strings.TrimSpace(string(stderr)), err)
	}

	fmt.Fprintf(w, "wiped — backup saved at %s\n", backupRef)
	fmt.Fprintf(w, "to recover commits: git reset --hard %s\n", backupRef)
	return nil
}

func displayBranch(branch string) string {
	if branch == "" {
		return "(detached HEAD)"
	}
	return branch
}
