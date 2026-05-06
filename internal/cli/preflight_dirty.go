package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

// guardWorkingTreeReady runs the cheap pre-stash safety checks that
// pull / sync / merge share. The motivating bug: `git stash push`
// refuses to run when there are unmerged paths and (on git 2.43) does
// so with an empty stderr + exit 1, so any caller that prompted the
// user for "stash & continue" first sees a silent failure that
// `gk doctor` *would* have caught. By looking before the user has to
// answer the prompt, we surface the real cause up front.
//
// op is the user-facing verb ("pull", "sync", "merge") used in the
// error message so the same helper produces consistent wording across
// commands.
func guardWorkingTreeReady(ctx context.Context, runner git.Runner, op string) error {
	files := listUnmergedFiles(ctx, runner)
	if len(files) == 0 {
		return nil
	}
	preview := strings.Join(previewPaths(files, 4), "\n  ")
	hint := fmt.Sprintf(
		"resolve markers, then continue the in-progress operation:\n  %s\n"+
			"fix:\n"+
			"  gk resolve                       # interactive walk\n"+
			"  git rebase --continue            # or merge/cherry-pick --continue\n"+
			"  git rebase --abort               # to cancel instead",
		preview,
	)
	return WithHint(
		fmt.Errorf("%s: working tree has %d unmerged path(s); finish the in-progress merge/rebase first", op, len(files)),
		hint,
	)
}

// previewPaths returns up to max entries plus a "(+N more)" tail when
// truncated. Used to keep multi-file errors readable without spamming
// the terminal when a sweeping rebase has 50 conflicts.
func previewPaths(files []string, max int) []string {
	if len(files) <= max {
		return files
	}
	out := append([]string(nil), files[:max]...)
	out = append(out, fmt.Sprintf("... (+%d more)", len(files)-max))
	return out
}

// diagnoseStashFailure inspects the repo state right after a `git
// stash push` returned a non-zero exit and produces a concrete hint
// the caller can show to the user. Probes (in priority order):
//
//  1. .git/index.lock present — a concurrent git process is holding
//     the index; running gk twice in parallel or a crashed earlier
//     git is the usual cause.
//  2. unmerged paths — git stash refuses; finish the in-progress
//     merge/rebase first.
//  3. in-progress op (rebase / merge / cherry-pick / bisect / revert)
//     even with no unmerged paths — surface the operation so the user
//     knows to continue or abort it.
//  4. fall-through — the failure is something we don't recognize;
//     point them at the raw command so they can see git's message
//     without gk in the way.
//
// Returns a single human-readable hint string, never empty.
func diagnoseStashFailure(ctx context.Context, runner git.Runner) string {
	// 1) Stale index.lock — cheapest probe; only requires a stat.
	if commonDir := gitCommonDir(ctx, runner); commonDir != "" {
		lock := filepath.Join(commonDir, "index.lock")
		if _, err := os.Stat(lock); err == nil {
			return fmt.Sprintf(
				"stale lock at %s — another git process is holding the index, or a previous git crashed.\n"+
					"  fix: rm %s   # only after confirming no `git` is running",
				lock, lock,
			)
		}
	}

	// 2) Unmerged paths — the case that motivated this helper.
	if files := listUnmergedFiles(ctx, runner); len(files) > 0 {
		preview := strings.Join(previewPaths(files, 4), "\n    ")
		return fmt.Sprintf(
			"working tree has %d unmerged path(s); git stash refuses until conflicts are resolved:\n    %s\n"+
				"  fix: gk resolve   # or edit, git add, git rebase --continue",
			len(files), preview,
		)
	}

	// 3) In-progress op without unmerged paths — rare but real (e.g. rebase
	//    paused mid-pick, conflicts already cleared but not continued).
	if state, err := gitstate.Detect(ctx, runnerDir(runner)); err == nil && state != nil && state.Kind != gitstate.StateNone {
		return fmt.Sprintf(
			"a %s is already in progress; finish or abort it before stashing.\n"+
				"  fix: git %s --continue   # or --abort",
			state.Kind, opAbortVerb(state.Kind),
		)
	}

	// 4) Genuinely unknown — direct the user at the raw command so git's
	//    real stderr surfaces (some 2.43 paths return exit 1 with empty
	//    stderr; running stash directly outside gk usually reveals it).
	return "git stash exited non-zero with no stderr; reproduce directly to see the underlying message:\n" +
		"  git stash push --include-untracked -m gk-debug\n" +
		"  gk doctor   # for a full repo-state report"
}

// gitCommonDir resolves --git-common-dir for absolute path use in the
// stale-lock probe. Returns "" on failure (caller skips the probe).
func gitCommonDir(ctx context.Context, runner git.Runner) string {
	out, _, err := runner.Run(ctx, "rev-parse", "--git-common-dir")
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
	if base := runnerDir(runner); base != "" {
		return filepath.Join(base, dir)
	}
	return dir
}

// runnerDir best-effort extracts the working dir from a git.Runner.
// gitstate.Detect needs an absolute working tree path; the ExecRunner
// stores it on its `Dir` field but the interface doesn't expose it,
// so we type-assert and fall back to "" (gitstate then uses the cwd).
func runnerDir(runner git.Runner) string {
	if er, ok := runner.(*git.ExecRunner); ok {
		return er.Dir
	}
	return ""
}

// opAbortVerb maps a gitstate.StateKind onto the git verb used to
// continue / abort it. Used to compose the in-progress-op hint.
func opAbortVerb(k gitstate.StateKind) string {
	switch k {
	case gitstate.StateRebaseMerge, gitstate.StateRebaseApply:
		return "rebase"
	case gitstate.StateMerge:
		return "merge"
	case gitstate.StateCherryPick:
		return "cherry-pick"
	case gitstate.StateRevert:
		return "revert"
	case gitstate.StateBisect:
		return "bisect"
	}
	return "rebase"
}
