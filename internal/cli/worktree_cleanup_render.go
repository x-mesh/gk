package cli

import (
	"fmt"
	"io"
	"strings"
)

var cleanupSkipNotes = map[cleanupReason]string{
	reasonCurrent:        "the worktree you are in",
	reasonBare:           "bare repository",
	reasonDetached:       "detached HEAD, no branch to judge",
	reasonNoBranch:       "no branch checked out",
	reasonProtected:      "protected branch",
	reasonLockedLive:     "locked by a running process",
	reasonLockedStale:    "locked; --force-stale-locks to override",
	reasonFresh:          "newer than --stale",
	reasonAgeUnknown:     "branch age unknown, cannot apply --stale",
	reasonNoMergeTarget:  "no parent/base to check the merge against",
	reasonTargetIsSelf:   "its merge target is itself",
	reasonUnmerged:       "not merged into its parent/base",
	reasonUnstarted:      "branch never moved since it was created; may be a workspace in use",
	reasonRepoUnreadable: "repository could not be read",
}

func renderWorktreeCleanup(w io.Writer, report worktreeCleanupJSON) {
	if report.DryRun {
		fmt.Fprintf(w, "worktree cleanup (dry-run): %d would be removed, %d kept\n", len(report.Candidates), len(report.Skipped))
		for _, c := range report.Candidates {
			if c.Action == cleanupActionPruneRecord {
				fmt.Fprintf(w, "  would prune record %s (%s; directory is gone, branch kept)\n", c.Path, c.Branch)
				continue
			}
			fmt.Fprintf(w, "  would remove %s (%s)", c.Path, cleanupBranchNote(c))
			if len(c.Disposable) > 0 {
				fmt.Fprintf(w, " + delete regenerable untracked: %s", strings.Join(c.Disposable, ", "))
			}
			fmt.Fprintln(w)
		}
	} else {
		fmt.Fprintf(w, "worktree cleanup: removed %d, failed %d, kept %d\n", len(report.Removed), len(report.Failed), len(report.Skipped))
	}
	for _, c := range report.Failed {
		fmt.Fprintf(w, "  failed %s: %s\n", c.Path, c.Error)
	}
	renderCleanupKept(w, report.Skipped)
	if report.DryRun && len(report.Candidates) > 0 {
		fmt.Fprintln(w, "(dry-run) nothing was removed; re-run with -y to apply.")
	}
}

func renderCleanupKept(w io.Writer, skipped []worktreeCleanupEntry) {
	if len(skipped) == 0 {
		return
	}
	counts := map[cleanupReason]int{}
	var order []cleanupReason
	dirtyKept := false
	for _, s := range skipped {
		reason := cleanupPrimaryReason(s)
		if counts[reason] == 0 {
			order = append(order, reason)
		}
		counts[reason]++
		if reason == reasonDirty {
			dirtyKept = true
		}
	}
	summary := make([]string, 0, len(order))
	for _, reason := range order {
		summary = append(summary, fmt.Sprintf("%d %s", counts[reason], reason))
	}
	fmt.Fprintf(w, "kept (%s):\n", strings.Join(summary, ", "))
	for _, s := range skipped {
		fmt.Fprintf(w, "  %s (%s): %s\n", s.Path, s.Branch, cleanupSkipNote(s))
	}
	if dirtyKept {
		fmt.Fprintln(w, "dirty worktrees are kept because removing them loses uncommitted work.")
		fmt.Fprintln(w, "  inspect: git -C <path> status   force (destructive): --discard-dirty")
	}
}

// cleanupPrimaryReason names why a worktree was kept: the verdict the
// classifier recorded, or, for a row built from the JSON report, the last
// reason, which the classifier appends last.
func cleanupPrimaryReason(s worktreeCleanupEntry) cleanupReason {
	if s.verdict != "" {
		return s.verdict
	}
	if len(s.Reasons) == 0 {
		return "skipped"
	}
	return cleanupReason(s.Reasons[len(s.Reasons)-1])
}

func cleanupBranchNote(c worktreeCleanupEntry) string {
	note := c.Branch
	if c.Target != "" {
		note += " -> " + c.Target
	}
	if len(c.Reasons) > 0 {
		note += "; " + strings.Join(c.Reasons, ", ")
	}
	return note
}

func cleanupSkipNote(s worktreeCleanupEntry) string {
	reason := cleanupPrimaryReason(s)
	if reason == reasonDirty {
		return "dirty: " + cleanupDirtyNote(s)
	}
	if s.Error != "" {
		return string(reason) + ": " + s.Error
	}
	if note, ok := cleanupSkipNotes[reason]; ok {
		return string(reason) + " (" + note + ")"
	}
	return string(reason)
}

func cleanupDirtyNote(s worktreeCleanupEntry) string {
	d := s.Dirty
	if d == nil {
		return "uncommitted changes"
	}
	return formatDirtyCounts(*d, s.Untracked)
}
