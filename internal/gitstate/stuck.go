package gitstate

import (
	"os"
	"path/filepath"
	"strings"
)

// RebaseStuckReason classifies why a rebase has paused with no merge conflicts.
type RebaseStuckReason int

const (
	// RebaseStuckNone — not stuck, or not a rebase state. Either the rebase is
	// progressing normally or the state isn't RebaseMerge/RebaseApply.
	RebaseStuckNone RebaseStuckReason = iota
	// RebaseStuckEmptyCommit — picked commit produced an empty/redundant patch
	// against the new base. git stops here so the user can decide
	// (--skip vs --continue with --allow-empty).
	RebaseStuckEmptyCommit
	// RebaseStuckEdit — explicit `edit` / `reword` / `break` line in the todo
	// pauses the rebase for amending.
	RebaseStuckEdit
	// RebaseStuckExec — `exec` line failed; rebase pauses until the user fixes
	// the command output and runs --continue.
	RebaseStuckExec
	// RebaseStuckUnknown — rebase is paused with no unmerged paths but the
	// signals don't match a known category (rebase-apply backend, malformed
	// state dir, future git versions, etc.).
	RebaseStuckUnknown
)

// String returns a stable identifier suitable for messages and tests.
func (r RebaseStuckReason) String() string {
	switch r {
	case RebaseStuckNone:
		return "none"
	case RebaseStuckEmptyCommit:
		return "empty-commit"
	case RebaseStuckEdit:
		return "edit"
	case RebaseStuckExec:
		return "exec"
	case RebaseStuckUnknown:
		return "unknown"
	default:
		return "invalid"
	}
}

// RebaseStuck carries the classification plus the signals that drove it,
// so callers can build a useful message without re-reading the git dir.
type RebaseStuck struct {
	Reason RebaseStuckReason
	// StoppedSHA is the commit at which the rebase paused (empty if unknown).
	StoppedSHA string
	// LastDoneOp is the verb of the last entry in `done` ("pick", "edit",
	// "exec", "reword", "break", ...). Empty if `done` is missing or empty.
	LastDoneOp string
	// LastDoneArg is the rest of the last `done` line after the verb — for
	// `pick` this is a short sha + comment, for `exec` it is the command.
	LastDoneArg string
}

// ClassifyRebaseStuck inspects a known rebase-merge/rebase-apply state and
// returns the reason it is paused. For non-rebase states (or when the rebase
// is progressing normally) it returns RebaseStuckNone.
//
// The caller is expected to have already confirmed there are zero unmerged
// paths — this function does not look at the work tree.
func ClassifyRebaseStuck(state *State) RebaseStuck {
	if state == nil {
		return RebaseStuck{Reason: RebaseStuckNone}
	}
	switch state.Kind {
	case StateRebaseMerge:
		return classifyRebaseMerge(filepath.Join(state.CommonDir, "rebase-merge"))
	case StateRebaseApply:
		// rebase-apply (am backend) signals are different and not yet
		// classified; treat as Unknown so the user still gets the
		// generic guidance instead of a silent exit.
		return classifyRebaseApply(filepath.Join(state.CommonDir, "rebase-apply"))
	default:
		return RebaseStuck{Reason: RebaseStuckNone}
	}
}

func classifyRebaseMerge(dir string) RebaseStuck {
	stoppedSHA := readTrimmed(filepath.Join(dir, "stopped-sha"))
	lastDoneOp, lastDoneArg := lastDoneEntry(filepath.Join(dir, "done"))
	res := RebaseStuck{
		StoppedSHA:  stoppedSHA,
		LastDoneOp:  lastDoneOp,
		LastDoneArg: lastDoneArg,
	}

	// Explicit pause verbs in `done` always indicate stuck — these don't
	// always set stopped-sha (e.g. `break`, failed `exec`).
	switch lastDoneOp {
	case "edit", "reword", "break":
		res.Reason = RebaseStuckEdit
		return res
	case "exec", "x":
		res.Reason = RebaseStuckExec
		return res
	}

	// No pause signal at all — rebase hasn't started or is between picks.
	if stoppedSHA == "" {
		res.Reason = RebaseStuckNone
		return res
	}

	// stopped-sha set: rebase is paused. Classify by remaining signals.
	// Empty/redundant commit marker (git 2.26+).
	if fileExists(filepath.Join(dir, "drop_redundant_commits")) {
		res.Reason = RebaseStuckEmptyCommit
		return res
	}
	// Older git versions or a noop pick: pick + stopped-sha + empty todo.
	todoEmpty := isFileEmpty(filepath.Join(dir, "git-rebase-todo"))
	if (lastDoneOp == "pick" || lastDoneOp == "p") && todoEmpty {
		res.Reason = RebaseStuckEmptyCommit
		return res
	}

	res.Reason = RebaseStuckUnknown
	return res
}

func classifyRebaseApply(dir string) RebaseStuck {
	// am backend: not enough distinct signals to classify yet.
	// `next`/`last` numbers tell us which patch is being applied, but the
	// reason for the pause (conflict vs empty) is harder to pin down without
	// reading the patch itself. Treat as Unknown so the caller can still
	// emit the generic guidance.
	_ = dir
	return RebaseStuck{Reason: RebaseStuckUnknown}
}

// lastDoneEntry returns the verb and remainder of the last non-blank,
// non-comment line of the `done` file, or ("", "") if absent/empty.
func lastDoneEntry(path string) (op, arg string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		op = fields[0]
		if len(fields) == 2 {
			arg = strings.TrimSpace(fields[1])
		}
		return op, arg
	}
	return "", ""
}

// isFileEmpty reports whether the file exists and has zero bytes.
// Returns true for missing files as well — callers treat "no todo left"
// the same as "todo file is empty".
func isFileEmpty(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	return info.Size() == 0
}
