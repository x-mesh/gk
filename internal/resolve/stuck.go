package resolve

import (
	"fmt"
	"strings"

	"github.com/x-mesh/gk/internal/gitstate"
)

// FormatStuckGuidance returns a human-readable message explaining why a
// rebase is paused with no merge conflicts and how to resume it.
//
// The message has a stable prefix that callers and tests can match against:
//
//	gk resolve: rebase is in progress but no conflicted files found.
//
// Only RebaseStuckEmptyCommit / Edit / Exec / Unknown are formatted.
// For RebaseStuckNone the function returns an empty string — callers
// should not invoke it in that case.
func FormatStuckGuidance(stuck gitstate.RebaseStuck) string {
	if stuck.Reason == gitstate.RebaseStuckNone {
		return ""
	}

	var b strings.Builder
	b.WriteString("gk resolve: rebase is in progress but no conflicted files found.\n")

	reasonLine, recommended := stuckReasonLine(stuck)
	fmt.Fprintf(&b, "  reason: %s\n", reasonLine)

	b.WriteString("  next:\n")
	for _, opt := range stuckOptions(recommended) {
		b.WriteString("    ")
		b.WriteString(opt)
		b.WriteString("\n")
	}
	return b.String()
}

// stuckReasonLine builds the "reason: ..." text and returns the action verb
// (skip / continue / abort / "") that the caller should highlight as
// recommended in the options list.
func stuckReasonLine(stuck gitstate.RebaseStuck) (line, recommended string) {
	short := shortSHA(stuck.StoppedSHA)
	switch stuck.Reason {
	case gitstate.RebaseStuckEmptyCommit:
		if short != "" {
			return fmt.Sprintf("empty/redundant commit at %s — its changes are already in the new base", short), "skip"
		}
		return "empty/redundant commit — its changes are already in the new base", "skip"
	case gitstate.RebaseStuckEdit:
		if short != "" {
			return fmt.Sprintf("paused for editing at %s (%s)", short, displayOp(stuck.LastDoneOp)), "continue"
		}
		return fmt.Sprintf("paused for editing (%s)", displayOp(stuck.LastDoneOp)), "continue"
	case gitstate.RebaseStuckExec:
		arg := stuck.LastDoneArg
		if arg == "" {
			arg = "exec command failed"
		}
		return fmt.Sprintf("exec failed: %s", arg), "continue"
	case gitstate.RebaseStuckUnknown:
		fallthrough
	default:
		return "rebase paused for an unrecognized reason", ""
	}
}

// stuckOptions returns the three resume commands. The recommended one
// (when non-empty) is moved to the top and tagged "(recommended)".
func stuckOptions(recommended string) []string {
	type opt struct{ verb, line string }
	all := []opt{
		{"skip", "git rebase --skip      # drop the current commit and move on"},
		{"continue", "git rebase --continue  # use the current state as-is (creates an empty commit if needed)"},
		{"abort", "git rebase --abort     # cancel the rebase and return to the original branch position"},
	}

	gkLine := "gk continue            # equivalent to `git rebase --continue` for this state"

	out := make([]string, 0, len(all)+1)
	if recommended != "" {
		for _, o := range all {
			if o.verb == recommended {
				out = append(out, o.line+"  (recommended)")
				break
			}
		}
		for _, o := range all {
			if o.verb != recommended {
				out = append(out, o.line)
			}
		}
	} else {
		for _, o := range all {
			out = append(out, o.line)
		}
	}
	out = append(out, gkLine)
	return out
}

func shortSHA(sha string) string {
	if len(sha) < 7 {
		return sha
	}
	return sha[:7]
}

func displayOp(op string) string {
	switch op {
	case "":
		return "edit/break"
	default:
		return op
	}
}
