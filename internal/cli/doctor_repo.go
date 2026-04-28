package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// errSkipFix marks a fix attempt that should *not* re-trigger the
// multi-pass loop — e.g. user skipped, or the operation can't proceed
// without manual intervention.
var errSkipFix = errors.New("skip fix")

// resolveGitDir returns the absolute .git directory path (or work-tree
// gitdir for a worktree) — empty string when the cwd isn't a git repo.
func resolveGitDir(ctx context.Context, runner git.Runner) string {
	out, _, err := runner.Run(ctx, "rev-parse", "--git-dir")
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return ""
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	repoDir := RepoFlag()
	if repoDir == "" {
		repoDir, _ = os.Getwd()
	}
	return filepath.Join(repoDir, dir)
}

// checkRepoLockFile flags a stale .git/index.lock — the most common
// reason "git could not write the index" surfaces during everyday use.
func checkRepoLockFile(gitDir string) doctorCheck {
	lock := filepath.Join(gitDir, "index.lock")
	if info, err := os.Stat(lock); err == nil {
		return doctorCheck{
			Name:   "repo: index.lock",
			Status: statusFail,
			Detail: fmt.Sprintf("present (%d bytes, modified %s)", info.Size(), info.ModTime().Format("15:04:05")),
			Fix:    "remove if no git process is running: rm .git/index.lock  (or run `gk doctor --fix`)",
		}
	}
	return doctorCheck{
		Name:   "repo: index.lock",
		Status: statusPass,
		Detail: "no stale lock",
	}
}

// checkInProgressOp surfaces an interrupted rebase / merge / cherry-pick
// / bisect / revert. Each leaves a marker under .git that blocks new
// operations like stash and reset.
func checkInProgressOp(gitDir string) doctorCheck {
	type op struct {
		marker string
		label  string
		abort  string
	}
	ops := []op{
		{"rebase-merge", "rebase (interactive)", "git rebase --abort"},
		{"rebase-apply", "rebase", "git rebase --abort"},
		{"MERGE_HEAD", "merge", "git merge --abort"},
		{"CHERRY_PICK_HEAD", "cherry-pick", "git cherry-pick --abort"},
		{"REVERT_HEAD", "revert", "git revert --abort"},
		{"BISECT_LOG", "bisect", "git bisect reset"},
	}
	for _, o := range ops {
		if _, err := os.Stat(filepath.Join(gitDir, o.marker)); err == nil {
			return doctorCheck{
				Name:   "repo: in-progress op",
				Status: statusFail,
				Detail: o.label + " is in progress",
				Fix:    "abort with `" + o.abort + "` (or `gk abort`), or finish with the matching --continue",
			}
		}
	}
	return doctorCheck{
		Name:   "repo: in-progress op",
		Status: statusPass,
		Detail: "none",
	}
}

// checkUnmergedPaths flags conflicts left from a merge/rebase that
// neither completed nor aborted.
func checkUnmergedPaths(ctx context.Context, runner git.Runner) doctorCheck {
	out, _, err := runner.Run(ctx, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return doctorCheck{Name: "repo: unmerged paths", Status: statusWarn, Detail: "could not query: " + err.Error()}
	}
	names := strings.Fields(strings.TrimSpace(string(out)))
	if len(names) == 0 {
		return doctorCheck{Name: "repo: unmerged paths", Status: statusPass, Detail: "none"}
	}
	preview := strings.Join(names, ", ")
	if len(preview) > 60 {
		preview = preview[:57] + "..."
	}
	return doctorCheck{
		Name:   "repo: unmerged paths",
		Status: statusFail,
		Detail: fmt.Sprintf("%d file(s): %s", len(names), preview),
		Fix:    "resolve with `gk resolve` or edit conflict markers, then `git add` + continue",
	}
}

// checkDirtyTree is a soft warning — many gk subcommands (undo, pull,
// sync) refuse a dirty tree by default. Surfacing it on `gk doctor`
// keeps the user informed without blocking anything.
func checkDirtyTree(ctx context.Context, runner git.Runner) doctorCheck {
	out, _, err := runner.Run(ctx, "status", "--porcelain")
	if err != nil {
		return doctorCheck{Name: "repo: working tree", Status: statusWarn, Detail: "could not query: " + err.Error()}
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	count := 0
	for _, l := range lines {
		if l != "" {
			count++
		}
	}
	if count == 0 {
		return doctorCheck{Name: "repo: working tree", Status: statusPass, Detail: "clean"}
	}
	return doctorCheck{
		Name:   "repo: working tree",
		Status: statusWarn,
		Detail: fmt.Sprintf("%d uncommitted change(s)", count),
		Fix:    "stash with `gk stash push -u` or commit with `gk commit`",
	}
}

// runDoctorFix walks the repo-state findings (FAIL/WARN) and offers
// in-line repair via ScrollSelectTUI. After each potential mutation
// the repo is re-checked so a fix that uncovers (or unblocks) the next
// finding is acted on too — e.g. resolving conflicts unblocks the
// stash step that follows.
func runDoctorFix(ctx context.Context, cmd *cobra.Command, runner git.Runner, gitDir string, _ []doctorCheck) error {
	if !ui.IsTerminal() {
		fmt.Fprintln(cmd.ErrOrStderr(), "doctor --fix needs a TTY")
		return nil
	}

	// Limit to a small number of passes so a stubborn finding can't
	// loop forever. Three is plenty for the current finding set.
	const maxPasses = 3
	for pass := 0; pass < maxPasses; pass++ {
		fresh := []doctorCheck{
			checkRepoLockFile(gitDir),
			checkInProgressOp(gitDir),
			checkUnmergedPaths(ctx, runner),
			checkDirtyTree(ctx, runner),
		}
		acted := false
		stop := false
		for _, c := range fresh {
			if c.Status != statusFail && c.Status != statusWarn {
				continue
			}
			var err error
			switch c.Name {
			case "repo: index.lock":
				err = promptFixLock(ctx, cmd, gitDir)
			case "repo: in-progress op":
				err = promptFixInProgress(ctx, cmd, runner, c.Detail)
			case "repo: unmerged paths":
				err = promptFixUnmerged(ctx, cmd, runner)
			case "repo: working tree":
				err = promptFixDirty(ctx, cmd, runner)
			}
			if errors.Is(err, errSkipFix) {
				// User skipped or the op can't proceed without manual
				// help — abandon the loop instead of spamming the same
				// prompt every pass.
				stop = true
			} else if err == nil {
				acted = true
			}
			// Re-check from the top of `fresh` after every fix attempt
			// so dependent findings (e.g. dirty-tree unblocked by
			// resolve) are reconsidered immediately.
			break
		}
		if !acted || stop {
			break
		}
	}
	return nil
}

// inProgressOpActive returns true when .git contains a marker for an
// active merge/rebase/cherry-pick. `gk resolve` requires one; without
// it, only manual `git add` (after editing markers) clears unmerged
// paths.
func inProgressOpActive(gitDir string) bool {
	for _, m := range []string{
		"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD",
		"rebase-merge", "rebase-apply",
	} {
		if _, err := os.Stat(filepath.Join(gitDir, m)); err == nil {
			return true
		}
	}
	return false
}

// promptFixUnmerged offers to launch `gk resolve` (when an op is active)
// or to mark files resolved via `git add` (when only stale markers are
// left). Returns errSkipFix on user skip OR unrecoverable failure so
// the multi-pass loop in runDoctorFix knows not to retry.
func promptFixUnmerged(ctx context.Context, cmd *cobra.Command, runner git.Runner) error {
	out, _, _ := runner.Run(ctx, "diff", "--name-only", "--diff-filter=U")
	files := strings.Fields(strings.TrimSpace(string(out)))
	if len(files) == 0 {
		return nil
	}

	gitDir := resolveGitDir(ctx, runner)
	hasOp := gitDir != "" && inProgressOpActive(gitDir)

	body := "Files with unmerged paths:\n  • " + strings.Join(files, "\n  • ")
	options := []ui.ScrollSelectOption{}
	if hasOp {
		body += "\n\nA merge/rebase/cherry-pick is in progress — `gk resolve` can walk each hunk."
		options = append(options,
			ui.ScrollSelectOption{Key: "r", Value: "resolve", Display: "launch `gk resolve` now (auto-stages on success)", IsDefault: true})
	} else {
		body += "\n\nNo merge/rebase/cherry-pick is active — only `git add` (after editing the conflict markers) will clear these.\n" +
			"Open the files in your editor, fix the markers, then choose `mark resolved` here."
		options = append(options,
			ui.ScrollSelectOption{Key: "a", Value: "addall", Display: "mark resolved — `git add` every unmerged file (run only after editing!)", IsDefault: true})
	}
	options = append(options,
		ui.ScrollSelectOption{Key: "s", Value: "skip", Display: "skip — handle manually with editor + `git add`"})

	choice, err := ui.ScrollSelectTUI(ctx, "fix: unmerged paths", body, options)
	if err != nil || choice == "skip" || choice == "" {
		return errSkipFix
	}

	switch choice {
	case "resolve":
		binary, eErr := os.Executable()
		if eErr != nil || binary == "" {
			binary = "gk"
		}
		resolve := exec.CommandContext(ctx, binary, "resolve")
		resolve.Stdin = os.Stdin
		resolve.Stdout = os.Stdout
		resolve.Stderr = os.Stderr
		if rErr := resolve.Run(); rErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"`gk resolve` exited with error: %v\n"+
					"resolve the conflicts manually (edit + `git add`), then re-run `gk doctor --fix`.\n",
				rErr)
			return errSkipFix
		}
	case "addall":
		args := append([]string{"add", "--"}, files...)
		if _, errOut, aErr := runner.Run(ctx, args...); aErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"git add failed: %s\n"+
					"some files still contain conflict markers — fix them in your editor first.\n",
				strings.TrimSpace(string(errOut)))
			return errSkipFix
		}
		fmt.Fprintf(cmd.OutOrStdout(), "marked resolved: %s\n", strings.Join(files, ", "))
	}
	return nil
}

func promptFixLock(ctx context.Context, cmd *cobra.Command, gitDir string) error {
	lock := filepath.Join(gitDir, "index.lock")
	body := "Stale .git/index.lock blocks every write (commit, stash, reset, merge).\n\n" +
		"Path: " + lock + "\n\n" +
		"Make sure no other git process is running before deleting:\n" +
		"  pgrep -fl '^git ' || echo \"no git running\""
	choice, err := ui.ScrollSelectTUI(ctx, "fix: stale index.lock", body, []ui.ScrollSelectOption{
		{Key: "r", Value: "remove", Display: "remove the lock file (no git process must be running)", IsDefault: true},
		{Key: "s", Value: "skip", Display: "skip — fix manually"},
	})
	if err != nil || choice == "skip" || choice == "" {
		return errSkipFix
	}
	if err := os.Remove(lock); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "remove %s: %v\n", lock, err)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", lock)
	return nil
}

func promptFixInProgress(ctx context.Context, cmd *cobra.Command, runner git.Runner, detail string) error {
	body := "An interrupted git operation is recorded in .git.\n\n" +
		"Detail: " + detail + "\n\n" +
		"Choose abort to roll the working tree back to before the operation, or skip to handle it manually with the matching --continue."
	choice, err := ui.ScrollSelectTUI(ctx, "fix: in-progress operation", body, []ui.ScrollSelectOption{
		{Key: "a", Value: "abort", Display: "abort — roll back via `gk abort` (works for rebase/merge/cherry-pick)", IsDefault: true},
		{Key: "s", Value: "skip", Display: "skip — fix manually"},
	})
	if err != nil || choice == "skip" || choice == "" {
		return errSkipFix
	}
	// `gk abort` is preferred over the per-op git command because it
	// already routes to the correct --abort variant.
	if _, errOut, aErr := runner.Run(ctx, "merge", "--abort"); aErr == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "git merge --abort succeeded")
		return nil
	} else if !strings.Contains(string(errOut), "MERGE_HEAD") {
		// Fall through to other op types.
		_ = aErr
	}
	if _, _, aErr := runner.Run(ctx, "rebase", "--abort"); aErr == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "git rebase --abort succeeded")
		return nil
	}
	if _, _, aErr := runner.Run(ctx, "cherry-pick", "--abort"); aErr == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "git cherry-pick --abort succeeded")
		return nil
	}
	if _, _, aErr := runner.Run(ctx, "revert", "--abort"); aErr == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "git revert --abort succeeded")
		return nil
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "no abort handled the in-progress op — try `gk abort` directly")
	return nil
}

func promptFixDirty(ctx context.Context, cmd *cobra.Command, runner git.Runner) error {
	// If conflicts are still present, git refuses to stash. Surface the
	// dependency before the user picks an option that's bound to fail.
	unmergedOut, _, _ := runner.Run(ctx, "diff", "--name-only", "--diff-filter=U")
	unmerged := strings.TrimSpace(string(unmergedOut)) != ""

	statusOut, _, _ := runner.Run(ctx, "status", "--short")
	body := strings.TrimRight(string(statusOut), "\n")
	if body == "" {
		body = "(no diff against HEAD)"
	}
	if unmerged {
		body += "\n\n⚠ unmerged paths are present — git will refuse to stash until they're resolved.\n" +
			"  resolve first: `gk resolve` (interactive) or edit conflict markers + `git add`."
	}
	choice, err := ui.ScrollSelectTUI(ctx, "fix: working tree is dirty", body, []ui.ScrollSelectOption{
		{Key: "s", Value: "stash", Display: "stash & continue (recoverable with `git stash pop`)", IsDefault: true},
		{Key: "k", Value: "skip", Display: "skip — keep changes as-is"},
	})
	if err != nil || choice == "skip" || choice == "" {
		return errSkipFix
	}
	if unmerged {
		fmt.Fprintln(cmd.ErrOrStderr(),
			"can't stash while conflicts remain. resolve them first:\n"+
				"  gk resolve              # interactive AI-assisted picker\n"+
				"  # or edit the conflict markers manually, then: git add <file>\n"+
				"then re-run `gk doctor --fix`.")
		return nil
	}
	if _, errOut, sErr := runner.Run(ctx, "stash", "push", "--include-untracked", "-m", "gk-doctor-stash"); sErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"stash failed: %s\n"+
				"hint: see `gk doctor` for the underlying repo state, or check `.git/index.lock` and any in-progress merge/rebase.\n",
			strings.TrimSpace(string(errOut)))
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), "stashed — restore with `gk stash` or `git stash pop`")
	return nil
}
