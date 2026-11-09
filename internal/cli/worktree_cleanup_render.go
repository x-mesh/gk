package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/fatih/color"
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

var (
	cleanupRemoveColor = color.New(color.FgGreen, color.Bold)
	cleanupFailColor   = color.New(color.FgRed, color.Bold)
	cleanupFaint       = color.New(color.Faint)
	cleanupBold        = color.New(color.Bold)
)

// cleanupReasonColor groups kept verdicts by what the reader must do: yellow
// holds work or a lock that needs a decision, cyan is unfinished branch work,
// red is a failure, and faint marks rows that are kept by design.
func cleanupReasonColor(reason cleanupReason) *color.Color {
	switch reason {
	case reasonDirty, reasonLockedStale:
		return color.New(color.FgYellow)
	case reasonUnmerged, reasonFresh:
		return color.New(color.FgCyan)
	case reasonRepoUnreadable:
		return color.New(color.FgRed)
	case reasonCurrent, reasonBare, reasonDetached, reasonNoBranch, reasonProtected,
		reasonLockedLive, reasonUnstarted:
		return cleanupFaint
	default:
		return color.New(color.Reset)
	}
}

func renderWorktreeCleanup(w io.Writer, report worktreeCleanupJSON) {
	if report.DryRun {
		fmt.Fprintf(w, "%s %s, %d kept\n",
			cleanupBold.Sprint("worktree cleanup (dry-run):"),
			cleanupRemoveColor.Sprintf("%d would be removed", len(report.Candidates)), len(report.Skipped))
		for _, c := range report.Candidates {
			if c.Action == cleanupActionPruneRecord {
				fmt.Fprintf(w, "  %s %s %s\n", cleanupRemoveColor.Sprint("would prune record"), c.Path,
					cleanupFaint.Sprintf("(%s; directory is gone, branch kept)", c.Branch))
				continue
			}
			fmt.Fprintf(w, "  %s %s %s", cleanupRemoveColor.Sprint("would remove"), c.Path,
				cleanupFaint.Sprintf("(%s)", cleanupBranchNote(c)))
			if len(c.Disposable) > 0 {
				fmt.Fprintf(w, " + delete regenerable untracked: %s", strings.Join(c.Disposable, ", "))
			}
			fmt.Fprintln(w)
		}
	} else {
		failed := fmt.Sprintf("failed %d", len(report.Failed))
		if len(report.Failed) > 0 {
			failed = cleanupFailColor.Sprint(failed)
		}
		fmt.Fprintf(w, "%s %s, %s, kept %d\n", cleanupBold.Sprint("worktree cleanup:"),
			cleanupRemoveColor.Sprintf("removed %d", len(report.Removed)), failed, len(report.Skipped))
	}
	for _, c := range report.Failed {
		fmt.Fprintf(w, "  %s %s: %s\n", cleanupFailColor.Sprint("failed"), c.Path, c.Error)
	}
	renderCleanupKept(w, report.Skipped)
	if report.DryRun && len(report.Candidates) > 0 {
		fmt.Fprintf(w, "(dry-run) nothing was removed; re-run with %s to apply.\n", cleanupBold.Sprint("-y"))
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
		summary = append(summary, cleanupReasonColor(reason).Sprintf("%d %s", counts[reason], reason))
	}
	fmt.Fprintf(w, "%s (%s):\n", cleanupBold.Sprint("kept"), strings.Join(summary, ", "))
	for _, s := range skipped {
		fmt.Fprintf(w, "  %s %s: %s\n", s.Path, cleanupFaint.Sprintf("(%s)", s.Branch),
			cleanupReasonColor(cleanupPrimaryReason(s)).Sprint(cleanupSkipNote(s)))
	}
	if dirtyKept {
		fmt.Fprintln(w, cleanupFaint.Sprint("dirty worktrees are kept because removing them loses uncommitted work."))
		fmt.Fprintf(w, "  inspect: git -C <path> status   force %s: --discard-dirty\n", cleanupFailColor.Sprint("(destructive)"))
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
