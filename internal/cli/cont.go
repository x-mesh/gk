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
	runner := &git.ExecRunner{Dir: RepoFlag()}
	if state.Kind == gitstate.StateNone {
		if unmerged := listUnmergedFiles(ctx, runner); len(unmerged) > 0 {
			return WithBlocked(
				fmt.Errorf("unmerged paths exist but no rebase/merge/cherry-pick is in progress (likely stash/apply conflict): %s", strings.Join(unmerged, ", ")),
				"unmerged-index",
				"resolve the unmerged paths; stash/apply conflicts have nothing to continue",
				errRemedy{Command: selfCmd("resolve --ai"), Safety: "safe"},
			)
		}
		return fmt.Errorf("no rebase/merge/cherry-pick in progress")
	}

	yes, _ := cmd.Flags().GetBool("yes")

	if !yes {
		// --continue commits the index only: unstaged edits and untracked
		// files (e.g. resolve's *.orig backups) are left behind in the
		// working tree, not folded into the resolved commit. Staged
		// changes are the expected post-resolve state — no note for them.
		out, _, err := runner.Run(ctx, "status", "--porcelain=v1")
		if err != nil {
			return err
		}
		if flags := git.ParsePorcelainV1(out); flags.Modified || hasUntrackedEntry(out) {
			printNote(os.Stderr, "unstaged/untracked files stay in the working tree — they are NOT included in the resolved commit")
		}
	}

	sub, err := stateSubcommand(state.Kind)
	if err != nil {
		return err
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

	// Success feedback: silence here reads as a hang, so always say what
	// happened. The operation can legitimately still be in progress after
	// a successful --continue (an `edit`/`break` rebase step).
	done := true
	if after, err := gitstate.Detect(ctx, RepoFlag()); err == nil && after.Kind != gitstate.StateNone {
		done = false
	}
	rep := continueReport{Action: sub, Done: done}
	if JSONOut() {
		if err := emitAgentResult(cmd.OutOrStdout(), rep); err != nil {
			return err
		}
	} else if done {
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s complete\n", color.GreenString("✓"), sub)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "%s %s continued — still in progress\n", color.GreenString("✓"), sub)
	}
	// Still in progress (an edit/break step) → exit 3 so batch/land see the pause.
	return pausedExitIf(rep)
}

// stateSubcommand maps an in-progress state to the git subcommand that
// owns its --continue/--abort/--skip verbs.
func stateSubcommand(kind gitstate.StateKind) (string, error) {
	switch kind {
	case gitstate.StateRebaseMerge, gitstate.StateRebaseApply:
		return "rebase", nil
	case gitstate.StateMerge:
		return "merge", nil
	case gitstate.StateCherryPick:
		return "cherry-pick", nil
	case gitstate.StateRevert:
		return "revert", nil
	default:
		return "", fmt.Errorf("unsupported state %s", kind)
	}
}

// continueReport is the JSON payload for a successful `gk continue`.
type continueReport struct {
	Action string `json:"action"` // rebase | merge | cherry-pick | revert
	Done   bool   `json:"done"`   // false when the operation paused again (edit/break step)
}

// agentState reports paused when the operation stopped again at an edit/break
// step instead of finishing — the agent must run another continue/abort.
func (r continueReport) agentState() string {
	if !r.Done {
		return envStatePaused
	}
	return ""
}

// hasUntrackedEntry reports whether porcelain v1 output lists an
// untracked file. ParsePorcelainV1 deliberately skips `??` entries, but
// for the pre-continue note untracked leftovers (resolve's *.orig
// backups) are exactly what the user needs to hear about.
func hasUntrackedEntry(out []byte) bool {
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "??") {
			return true
		}
	}
	return false
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
	fmt.Fprintf(w, "    1. fix conflict markers — pick one:\n")
	fmt.Fprintf(w, "         %s             %s\n",
		bold(selfRewrite("gk resolve")), faint("AI-assisted (preview with --dry-run)"))
	fmt.Fprintf(w, "         %s   %s\n",
		bold(selfRewrite("gk resolve --strategy ours")), faint("take HEAD across all conflicts"))
	fmt.Fprintf(w, "         %s %s\n",
		bold(selfRewrite("gk resolve --strategy theirs")), faint("take incoming across all conflicts"))
	fmt.Fprintf(w, "         %s            %s\n",
		faint("manual:"),
		faint("edit each file, then "+bold("git add <file>")))
	fmt.Fprintf(w, "    2. %s         %s\n",
		bold(selfRewrite("gk continue")), faint("(retry — this command)"))
	fmt.Fprintf(w, "       %s            %s\n",
		bold(selfRewrite("gk abort")), faint("(give up and return to pre-pull state)"))

	if branch, err := client.CurrentBranch(ctx); err == nil && branch != "" {
		if ref := client.LatestBackupRef(ctx, branch); ref != "" {
			fmt.Fprintf(w, "\n  %s   %s\n", faint("backup:"), bold(ref))
		}
	}
	fmt.Fprintln(w)
}
