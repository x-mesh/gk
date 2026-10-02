package cli

import (
	"fmt"
	"io"
	"strings"
)

var cleanupSkipNotes = map[string]string{
	"current":         "the worktree you are in",
	"bare":            "bare repository",
	"detached":        "detached HEAD, no branch to judge",
	"no-branch":       "no branch checked out",
	"protected":       "protected branch",
	"locked-live":     "locked by a running process",
	"locked-stale":    "locked; --force-stale-locks to override",
	"fresh":           "newer than --stale",
	"age-unknown":     "branch age unknown, cannot apply --stale",
	"no-merge-target": "no parent/base to check the merge against",
	"target-is-self":  "its merge target is itself",
	"unmerged":        "not merged into its parent/base",
	"repo-unreadable": "repository could not be read",
}

func renderWorktreeCleanup(w io.Writer, report worktreeCleanupJSON) {
	if report.DryRun {
		fmt.Fprintf(w, "worktree cleanup (dry-run): %d would be removed, %d kept\n", len(report.Candidates), len(report.Skipped))
		for _, c := range report.Candidates {
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
	counts := map[string]int{}
	var order []string
	dirtyKept := false
	for _, s := range skipped {
		reason := cleanupPrimaryReason(s)
		if counts[reason] == 0 {
			order = append(order, reason)
		}
		counts[reason]++
		if reason == "dirty" {
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

func cleanupPrimaryReason(s worktreeCleanupEntry) string {
	if len(s.Reasons) == 0 {
		return "skipped"
	}
	return s.Reasons[0]
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
	if reason == "dirty" {
		return "dirty: " + cleanupDirtyNote(s)
	}
	if s.Error != "" {
		return reason + ": " + s.Error
	}
	if note, ok := cleanupSkipNotes[reason]; ok {
		return reason + " (" + note + ")"
	}
	return reason
}

func cleanupDirtyNote(s worktreeCleanupEntry) string {
	d := s.Dirty
	if d == nil {
		return "uncommitted changes"
	}
	return formatDirtyCounts(*d, s.Untracked)
}
