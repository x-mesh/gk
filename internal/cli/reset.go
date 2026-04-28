package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Reset current branch to its remote (git fetch + git reset --hard)",
		Long: `Fetch the current branch's upstream and hard-reset the working tree to it.

DESTRUCTIVE — any uncommitted changes or local commits ahead of the remote
will be lost. Requires a TTY confirmation or --yes unless --dry-run is set.

Examples:
  gk reset                         # reset to tracked upstream
  gk reset --to-remote             # reset to <remote>/<current> (bypass upstream)
  gk reset --to origin/main        # reset to a specific ref
  gk reset --yes --clean           # non-interactive, also wipe untracked
`,
		RunE: runReset,
	}
	cmd.Flags().String("to", "", "override target ref (e.g. origin/main); default: current branch upstream")
	cmd.Flags().Bool("to-remote", false, "reset to <remote>/<current branch>, ignoring configured upstream")
	cmd.Flags().String("remote", "", "remote to fetch from (default: config.remote / origin)")
	cmd.Flags().BoolP("yes", "y", false, "skip confirmation prompt")
	cmd.Flags().Bool("clean", false, "also run 'git clean -fd' to remove untracked files")
	cmd.Flags().Bool("dry-run", false, "print what would happen without resetting")
	rootCmd.AddCommand(cmd)
}

func runReset(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	ctx := cmd.Context()
	cfg, _ := config.Load(cmd.Flags())
	w := cmd.OutOrStdout()

	to, _ := cmd.Flags().GetString("to")
	toRemote, _ := cmd.Flags().GetBool("to-remote")
	remoteFlag, _ := cmd.Flags().GetString("remote")
	yes, _ := cmd.Flags().GetBool("yes")
	clean, _ := cmd.Flags().GetBool("clean")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	if to != "" && toRemote {
		return fmt.Errorf("--to and --to-remote are mutually exclusive")
	}

	branch, err := client.CurrentBranch(ctx)
	if err != nil {
		return WithHint(fmt.Errorf("reset requires a branch: %w", err),
			"check out a branch first (gk switch <name>)")
	}

	if toRemote {
		r := remoteFlag
		if r == "" {
			r = cfg.Remote
		}
		if r == "" {
			r = "origin"
		}
		to = r + "/" + branch
	}

	target, err := resolveResetTarget(ctx, runner, branch, to)
	if err != nil {
		return err
	}

	fetchRemote, fetchRef := splitRemoteRef(target, remoteFlag, cfg.Remote)

	fmt.Fprintf(w, "branch:  %s\n", branch)
	fmt.Fprintf(w, "target:  %s\n", target)
	fmt.Fprintf(w, "fetch:   %s %s\n", fetchRemote, fetchRef)
	if clean {
		fmt.Fprintln(w, "clean:   yes (git clean -fd)")
	}

	if dryRun {
		fmt.Fprintln(w, "[dry-run] skipping fetch and reset")
		return nil
	}

	if !yes {
		if !ui.IsTerminal() {
			return WithHint(fmt.Errorf("refusing to reset without confirmation"),
				"pass --yes to proceed non-interactively")
		}
		ok, cerr := ui.Confirm(
			fmt.Sprintf("Discard all local changes on %s and reset to %s?", branch, target),
			false,
		)
		if cerr != nil {
			return cerr
		}
		if !ok {
			return fmt.Errorf("aborted")
		}
	}

	stopFetch := ui.StartBubbleSpinner(fmt.Sprintf("reset — fetching %s/%s", fetchRemote, fetchRef))
	fetchErr := client.Fetch(ctx, fetchRemote, fetchRef, false)
	stopFetch()
	if fetchErr != nil {
		return fmt.Errorf("fetch failed: %w", fetchErr)
	}

	if _, stderr, err := runner.Run(ctx, "reset", "--hard", target); err != nil {
		return fmt.Errorf("reset --hard %s: %s: %w", target, strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprintf(w, "reset: %s is now at %s\n", branch, target)

	if clean {
		if _, stderr, err := runner.Run(ctx, "clean", "-fd"); err != nil {
			return fmt.Errorf("clean -fd: %s: %w", strings.TrimSpace(string(stderr)), err)
		}
		fmt.Fprintln(w, "cleaned untracked files")
	}
	return nil
}

// resolveResetTarget returns the upstream ref to reset to.
// Precedence: explicit --to → branch@{upstream} → error.
func resolveResetTarget(ctx context.Context, runner git.Runner, branch, to string) (string, error) {
	if to != "" {
		return to, nil
	}
	stdout, stderr, err := runner.Run(ctx,
		"rev-parse", "--abbrev-ref", "--symbolic-full-name", branch+"@{upstream}")
	if err != nil {
		return "", WithHint(
			fmt.Errorf("no upstream configured for %s: %s", branch, strings.TrimSpace(string(stderr))),
			"pass --to <remote>/<branch>",
		)
	}
	up := strings.TrimSpace(string(stdout))
	if up == "" {
		return "", WithHint(
			fmt.Errorf("empty upstream for %s", branch),
			"pass --to <remote>/<branch>",
		)
	}
	return up, nil
}

// splitRemoteRef teases apart a ref like "origin/main" into (remote, branch).
// If --remote was passed explicitly we honor it and treat the full ref as the
// branch part. If the target has no slash, we fall back to cfgRemote/origin.
func splitRemoteRef(target, remoteFlag, cfgRemote string) (remote, ref string) {
	if remoteFlag != "" {
		return remoteFlag, target
	}
	if i := strings.IndexByte(target, '/'); i > 0 {
		return target[:i], target[i+1:]
	}
	r := cfgRemote
	if r == "" {
		r = "origin"
	}
	return r, target
}
