package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

func init() {
	cmd := &cobra.Command{
		Use:   "continue",
		Short: "Continue the current rebase/merge/cherry-pick after resolving conflicts",
		RunE:  runContinue,
	}
	cmd.Flags().Bool("yes", false, "skip prompt and continue immediately")
	rootCmd.AddCommand(cmd)
}

func runContinue(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	state, err := gitstate.Detect(ctx, RepoFlag())
	if err != nil {
		return err
	}
	if state.Kind == gitstate.StateNone {
		return fmt.Errorf("no rebase/merge/cherry-pick in progress")
	}

	runner := &git.ExecRunner{
		Dir:      RepoFlag(),
		ExtraEnv: os.Environ(),
	}
	yes, _ := cmd.Flags().GetBool("yes")

	if !yes {
		client := git.NewClient(runner)
		dirty, err := client.IsDirty(ctx)
		if err != nil {
			return err
		}
		if dirty {
			fmt.Fprintf(os.Stderr, "note: working tree still has changes (they will be included).\n")
		}
	}

	var sub string
	switch state.Kind {
	case gitstate.StateRebaseMerge, gitstate.StateRebaseApply:
		sub = "rebase"
	case gitstate.StateMerge:
		sub = "merge"
	case gitstate.StateCherryPick:
		sub = "cherry-pick"
	case gitstate.StateRevert:
		sub = "revert"
	default:
		return fmt.Errorf("unsupported state %s", state.Kind)
	}

	_, stderr, err := runner.Run(ctx, sub, "--continue")
	if err != nil {
		// Detect the most common failure mode — unresolved conflict
		// markers — and print the specific files so the user doesn't
		// have to re-run `git status` to find them. Fall back to git's
		// raw stderr when no unmerged files are reported (means the
		// failure was something else: empty commit, hook rejection,
		// etc.) and the original git message is more informative.
		client := git.NewClient(runner)
		unmerged := listUnmergedFiles(ctx, runner)
		if len(unmerged) > 0 {
			printContinueUnresolved(os.Stderr, sub, unmerged, client, ctx, runner.Dir)
			return fmt.Errorf("%s --continue blocked: %d file%s still unresolved",
				sub, len(unmerged), plural(len(unmerged)))
		}
		return fmt.Errorf("git %s --continue failed: %s: %w", sub, strings.TrimSpace(string(stderr)), err)
	}
	return nil
}

// listUnmergedFiles returns the paths in the index that still carry
// conflict markers. Empty slice means everything's resolved (in which
// case the --continue failure was caused by something else).
func listUnmergedFiles(ctx context.Context, runner git.Runner) []string {
	out, _, err := runner.Run(ctx, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil
	}
	out = []byte(strings.TrimRight(string(out), "\n"))
	if len(out) == 0 {
		return nil
	}
	return strings.Split(string(out), "\n")
}

func printContinueUnresolved(w *os.File, sub string, files []string, client *git.Client, ctx context.Context, repoDir string) {
	yellow := color.YellowString
	red := color.RedString
	bold := color.New(color.Bold).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s %s --continue blocked: conflict markers still present\n",
		yellow("✗"), sub)
	fmt.Fprintf(w, "\n  %s files needing resolution:\n", red("✗"))
	for _, f := range files {
		fmt.Fprintf(w, "    %s\n", red(f))
	}

	// Inline first conflict region for the first file — same treatment
	// as `gk pull`'s initial banner so the surface stays consistent.
	renderInlineConflicts(w, repoDir, files)

	fmt.Fprintln(w)
	fmt.Fprintln(w, "  resolve:")
	fmt.Fprintf(w, "    1. edit each file — pick the right side, remove %s / %s / %s markers\n",
		bold("<<<<<<<"), bold("======="), bold(">>>>>>>"))
	fmt.Fprintf(w, "    2. %s    %s\n",
		bold("git add <file>"), faint("(stage the resolved file)"))
	fmt.Fprintf(w, "    3. %s         %s\n",
		bold("gk continue"), faint("(retry — this command)"))
	fmt.Fprintf(w, "       %s            %s\n",
		bold("gk abort"), faint("(give up and return to pre-pull state)"))

	if branch, err := client.CurrentBranch(ctx); err == nil && branch != "" {
		if ref := client.LatestBackupRef(ctx, branch); ref != "" {
			fmt.Fprintf(w, "\n  %s   %s\n", faint("backup:"), bold(ref))
		}
	}
	fmt.Fprintln(w)
}
