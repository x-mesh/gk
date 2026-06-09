package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

// snapshotRefPrefix namespaces non-destructive working-tree snapshots. Each
// branch gets refs/wip/<branch>; that ref's reflog is the time-ordered
// snapshot history. Living outside refs/heads means snapshots never appear in
// `git branch`, never push, never tangle with rebase, and survive `git gc`.
const snapshotRefPrefix = "refs/wip/"

func init() {
	snapshot := &cobra.Command{
		Use:   "snapshot",
		Short: "Save a non-destructive safety-net snapshot of the working tree",
		Long: `Records the current working tree (tracked changes plus untracked files,
respecting .gitignore) as a snapshot under refs/wip/<branch> WITHOUT touching
your working tree, index, or branch history.

Unlike 'gk wip', nothing is committed to your branch — the snapshot lives in a
shadow ref whose reflog is the snapshot history. It never pushes, never shows in
'git branch', and survives 'git gc'. Use it as an automatic safety net (e.g.
from a Claude Code Stop hook: 'gk snapshot -q').

  gk snapshot                # save the current working tree
  gk snapshot list           # list snapshots for this branch
  gk snapshot restore        # restore the latest snapshot
  gk snapshot restore 2      # restore an older one`,
		Args: cobra.NoArgs,
		RunE: runSnapshotSave,
	}
	snapshot.Flags().StringP("message", "m", "", "note recorded with the snapshot")
	snapshot.Flags().BoolP("quiet", "q", false, "suppress output (for hooks); still errors on failure")
	rootCmd.AddCommand(snapshot)

	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List safety-net snapshots for the current branch",
		Args:    cobra.NoArgs,
		RunE:    runSnapshotList,
	}
	snapshot.AddCommand(list)

	restore := &cobra.Command{
		Use:   "restore [n]",
		Short: "Restore snapshot n (default 0, the latest) into the working tree",
		Long: `Restores snapshot <n> (default 0, the latest) for the current branch into
the working tree and index. If the working tree is dirty, the current state is
first saved as a fresh snapshot so nothing is lost. Files present now but absent
from the snapshot are left untouched.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runSnapshotRestore,
	}
	restore.Flags().StringP("message", "m", "", "note for the auto-backup snapshot taken when the tree is dirty")
	snapshot.AddCommand(restore)

	// Top-level convenience alias for `gk snapshot list`.
	snapshots := &cobra.Command{
		Use:   "snapshots",
		Short: "List safety-net snapshots for the current branch",
		Args:  cobra.NoArgs,
		RunE:  runSnapshotList,
	}
	rootCmd.AddCommand(snapshots)
}

func runSnapshotSave(cmd *cobra.Command, _ []string) error {
	note, _ := cmd.Flags().GetString("message")
	quiet, _ := cmd.Flags().GetBool("quiet")
	runner := &git.ExecRunner{Dir: RepoFlag()}

	ref, sha, created, err := createWorkingTreeSnapshot(cmd.Context(), runner, note)
	if err != nil {
		return err
	}
	if quiet {
		return nil
	}
	w := cmd.OutOrStdout()
	if !created {
		fmt.Fprintln(w, "nothing to snapshot — working tree is clean")
		return nil
	}
	fmt.Fprintln(w, successLinef("snapshot saved", "%s (%s)", ref, shortSHA(sha)))
	fmt.Fprintln(w, stylizeHintLine("hint: gk snapshots   # list snapshots"))
	return nil
}

// createWorkingTreeSnapshot captures the working tree into refs/wip/<branch>
// without disturbing the working tree, index, or branch history. Returns
// created=false (and no error) when there is nothing to snapshot.
func createWorkingTreeSnapshot(ctx context.Context, runner *git.ExecRunner, note string) (ref, sha string, created bool, err error) {
	dirty, err := workingTreeDirty(ctx, runner)
	if err != nil {
		return "", "", false, err
	}
	if !dirty {
		return "", "", false, nil
	}

	branch, err := snapshotBranch(ctx, runner)
	if err != nil {
		return "", "", false, err
	}
	ref = snapshotRefPrefix + branch

	tree, err := snapshotTree(ctx, runner)
	if err != nil {
		return "", "", false, err
	}

	commit, err := commitSnapshotTree(ctx, runner, tree, note)
	if err != nil {
		return "", "", false, err
	}

	msg := snapshotMessage(note)
	if _, stderr, e := runner.Run(ctx, "update-ref", "--create-reflog", "-m", msg, ref, commit); e != nil {
		return "", "", false, fmt.Errorf("update-ref %s: %s: %w", ref, strings.TrimSpace(string(stderr)), e)
	}
	return ref, commit, true, nil
}

// snapshotTree writes the full working tree (including untracked files,
// respecting .gitignore) to a tree object using a throwaway index file, so the
// real index is never touched.
func snapshotTree(ctx context.Context, runner *git.ExecRunner) (string, error) {
	gitDir, err := gitDirPath(ctx, runner)
	if err != nil {
		return "", err
	}
	// Reserve a unique path inside the git dir, then remove it so git creates
	// a fresh empty index there. add -A against an empty index records the
	// entire working tree as it stands right now.
	tmp, err := os.CreateTemp(gitDir, "gk-snapshot-index-")
	if err != nil {
		return "", fmt.Errorf("create temp index: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	if rmErr := os.Remove(tmpPath); rmErr != nil {
		return "", fmt.Errorf("reset temp index: %w", rmErr)
	}
	defer os.Remove(tmpPath)

	idx := &git.ExecRunner{Dir: runner.Dir, ExtraEnv: []string{"GIT_INDEX_FILE=" + tmpPath}}
	if _, stderr, e := idx.Run(ctx, "add", "-A"); e != nil {
		return "", fmt.Errorf("stage working tree: %s: %w", strings.TrimSpace(string(stderr)), e)
	}
	out, stderr, e := idx.Run(ctx, "write-tree")
	if e != nil {
		return "", fmt.Errorf("write-tree: %s: %w", strings.TrimSpace(string(stderr)), e)
	}
	return strings.TrimSpace(string(out)), nil
}

// commitSnapshotTree wraps a tree in a commit, parented on HEAD when one
// exists (so the snapshot is diffable against the branch tip).
func commitSnapshotTree(ctx context.Context, runner git.Runner, tree, note string) (string, error) {
	args := []string{"commit-tree", tree}
	if head, ok := headSHA(ctx, runner); ok {
		args = append(args, "-p", head)
	}
	args = append(args, "-m", snapshotMessage(note))
	out, stderr, err := runner.Run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("commit-tree: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func runSnapshotList(cmd *cobra.Command, _ []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	branch, err := snapshotBranch(ctx, runner)
	if err != nil {
		return err
	}
	ref := snapshotRefPrefix + branch
	if !refExists(ctx, runner, ref) {
		fmt.Fprintf(w, "no snapshots for %s yet\n", branch)
		fmt.Fprintln(w, stylizeHintLine("hint: gk snapshot   # save one"))
		return nil
	}

	// %gd → selector (refs/wip/<branch>@{n}); %cr → relative time of the
	// snapshot commit (which is when it was taken); %gs → reflog subject.
	out, _, err := runner.Run(ctx, "log", "-g", "--format=%gd%x09%cr%x09%gs", ref)
	if err != nil {
		return fmt.Errorf("read snapshot reflog: %w", err)
	}
	for _, ln := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if ln == "" {
			continue
		}
		parts := strings.SplitN(ln, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		sel := snapshotSelector(parts[0])
		fmt.Fprintf(w, "%s  %s  %s\n", cellCyan(sel), parts[1], cellFaint(parts[2]))
	}
	return nil
}

func runSnapshotRestore(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	n := 0
	if len(args) == 1 {
		v, err := strconv.Atoi(strings.Trim(args[0], "@{}"))
		if err != nil || v < 0 {
			return WithHint(fmt.Errorf("invalid snapshot index %q", args[0]),
				"use a number like: gk snapshot restore 2")
		}
		n = v
	}

	branch, err := snapshotBranch(ctx, runner)
	if err != nil {
		return err
	}
	ref := snapshotRefPrefix + branch
	if !refExists(ctx, runner, ref) {
		return WithHint(fmt.Errorf("no snapshots for %s", branch),
			"save one first: gk snapshot")
	}

	// Resolve @{n} to a fixed SHA up front: the auto-backup below pushes a new
	// reflog entry, shifting every @{i}, so we must pin the target first.
	out, _, e := runner.Run(ctx, "rev-parse", "--verify", "--quiet", fmt.Sprintf("%s@{%d}^{commit}", ref, n))
	if e != nil {
		return WithHint(fmt.Errorf("snapshot @{%d} does not exist", n),
			"list with: gk snapshots")
	}
	targetSHA := strings.TrimSpace(string(out))

	// The restore situation is usually "I lost work, get it back" — so never
	// clobber whatever is in the tree now. Save it as a fresh snapshot first.
	if dirty, _ := workingTreeDirty(ctx, runner); dirty {
		note, _ := cmd.Flags().GetString("message")
		if note == "" {
			note = "auto-backup before restore"
		}
		if _, _, _, e := createWorkingTreeSnapshot(ctx, runner, note); e != nil {
			return fmt.Errorf("back up current state: %w", e)
		}
		fmt.Fprintln(w, cellFaint("  current changes saved as the latest snapshot"))
	}

	// Apply the snapshot tree to the index and working tree. ":/" is a
	// repo-root pathspec so this works regardless of the cwd. Files present
	// now but absent from the snapshot are intentionally left in place.
	if _, stderr, e := runner.Run(ctx, "checkout", targetSHA, "--", ":/"); e != nil {
		return fmt.Errorf("restore snapshot: %s: %w", strings.TrimSpace(string(stderr)), e)
	}
	fmt.Fprintln(w, successLinef("restored", "snapshot @{%d}", n))
	return nil
}

// --- small git helpers -----------------------------------------------------

// snapshotBranch returns the current branch name, refusing detached HEAD where
// there is no stable ref to anchor refs/wip to.
func snapshotBranch(ctx context.Context, runner git.Runner) (string, error) {
	b, err := currentBranchName(ctx, runner)
	if err != nil || b == "" || b == "HEAD" {
		return "", WithHint(fmt.Errorf("cannot snapshot in detached HEAD state"),
			"check out a branch first: git switch -c <name>")
	}
	return b, nil
}

func workingTreeDirty(ctx context.Context, runner git.Runner) (bool, error) {
	out, _, err := runner.Run(ctx, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func headSHA(ctx context.Context, runner git.Runner) (string, bool) {
	out, _, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return "", false
	}
	sha := strings.TrimSpace(string(out))
	return sha, sha != ""
}

func refExists(ctx context.Context, runner git.Runner, ref string) bool {
	_, _, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

func gitDirPath(ctx context.Context, runner git.Runner) (string, error) {
	out, _, err := runner.Run(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("locate git dir: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func snapshotMessage(note string) string {
	if note = strings.TrimSpace(note); note != "" {
		return "gk snapshot: " + note
	}
	return "gk snapshot"
}

// snapshotSelector trims "refs/wip/<branch>" off a reflog selector, leaving the
// "@{n}" suffix used as the restore argument.
func snapshotSelector(sel string) string {
	if i := strings.LastIndex(sel, "@{"); i >= 0 {
		return sel[i:]
	}
	return sel
}
