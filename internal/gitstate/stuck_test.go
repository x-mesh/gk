package gitstate

import (
	"path/filepath"
	"testing"
)

// TestRebaseStuckReason_String covers every value plus the invalid sentinel.
func TestRebaseStuckReason_String(t *testing.T) {
	cases := []struct {
		r    RebaseStuckReason
		want string
	}{
		{RebaseStuckNone, "none"},
		{RebaseStuckEmptyCommit, "empty-commit"},
		{RebaseStuckEdit, "edit"},
		{RebaseStuckExec, "exec"},
		{RebaseStuckUnknown, "unknown"},
		{RebaseStuckReason(99), "invalid"},
	}
	for _, c := range cases {
		if got := c.r.String(); got != c.want {
			t.Errorf("RebaseStuckReason(%d).String() = %q, want %q", c.r, got, c.want)
		}
	}
}

func TestClassifyRebaseStuck_NilOrNonRebase(t *testing.T) {
	if got := ClassifyRebaseStuck(nil); got.Reason != RebaseStuckNone {
		t.Errorf("nil state: want None, got %s", got.Reason)
	}
	for _, k := range []StateKind{StateNone, StateMerge, StateCherryPick, StateRevert, StateBisect} {
		s := &State{Kind: k}
		if got := ClassifyRebaseStuck(s); got.Reason != RebaseStuckNone {
			t.Errorf("Kind=%s: want None, got %s", k, got.Reason)
		}
	}
}

// rebaseMergeFixture builds a state pointing at a fresh rebase-merge dir.
func rebaseMergeFixture(t *testing.T) (*State, string) {
	t.Helper()
	commonDir := t.TempDir()
	rbDir := filepath.Join(commonDir, "rebase-merge")
	makeDir(t, rbDir)
	return &State{Kind: StateRebaseMerge, CommonDir: commonDir, GitDir: commonDir}, rbDir
}

// TestClassifyRebaseStuck_EmptyCommit_DropMarker — the canonical mem-mesh
// shape: drop_redundant_commits marker present.
func TestClassifyRebaseStuck_EmptyCommit_DropMarker(t *testing.T) {
	s, rbDir := rebaseMergeFixture(t)
	makeFile(t, filepath.Join(rbDir, "git-rebase-todo"), "")
	makeFile(t, filepath.Join(rbDir, "done"), "pick c613c80 # chore(release): v1.3.1\n")
	makeFile(t, filepath.Join(rbDir, "stopped-sha"), "c613c80fc327d1c90c36c93259332ecb202f79d0\n")
	makeFile(t, filepath.Join(rbDir, "drop_redundant_commits"), "")

	got := ClassifyRebaseStuck(s)
	if got.Reason != RebaseStuckEmptyCommit {
		t.Errorf("Reason: want %s, got %s", RebaseStuckEmptyCommit, got.Reason)
	}
	if got.StoppedSHA != "c613c80fc327d1c90c36c93259332ecb202f79d0" {
		t.Errorf("StoppedSHA: got %q", got.StoppedSHA)
	}
	if got.LastDoneOp != "pick" {
		t.Errorf("LastDoneOp: want pick, got %q", got.LastDoneOp)
	}
}

// TestClassifyRebaseStuck_EmptyCommit_NoDropMarker — older git: pick + empty
// todo + stopped-sha must still classify as EmptyCommit.
func TestClassifyRebaseStuck_EmptyCommit_NoDropMarker(t *testing.T) {
	s, rbDir := rebaseMergeFixture(t)
	makeFile(t, filepath.Join(rbDir, "git-rebase-todo"), "")
	makeFile(t, filepath.Join(rbDir, "done"), "pick deadbeef # noop\n")
	makeFile(t, filepath.Join(rbDir, "stopped-sha"), "deadbeef\n")

	if got := ClassifyRebaseStuck(s).Reason; got != RebaseStuckEmptyCommit {
		t.Errorf("want %s, got %s", RebaseStuckEmptyCommit, got)
	}
}

func TestClassifyRebaseStuck_Edit(t *testing.T) {
	s, rbDir := rebaseMergeFixture(t)
	makeFile(t, filepath.Join(rbDir, "git-rebase-todo"), "pick aaaaaaa # next one\n")
	makeFile(t, filepath.Join(rbDir, "done"), "pick 1111111 # ok\nedit 2222222 # stop here\n")
	makeFile(t, filepath.Join(rbDir, "stopped-sha"), "2222222\n")

	got := ClassifyRebaseStuck(s)
	if got.Reason != RebaseStuckEdit {
		t.Errorf("Reason: want %s, got %s", RebaseStuckEdit, got.Reason)
	}
	if got.LastDoneOp != "edit" {
		t.Errorf("LastDoneOp: want edit, got %q", got.LastDoneOp)
	}
}

func TestClassifyRebaseStuck_Reword(t *testing.T) {
	s, rbDir := rebaseMergeFixture(t)
	makeFile(t, filepath.Join(rbDir, "git-rebase-todo"), "")
	makeFile(t, filepath.Join(rbDir, "done"), "reword 3333333 # rename\n")
	makeFile(t, filepath.Join(rbDir, "stopped-sha"), "3333333\n")

	if got := ClassifyRebaseStuck(s).Reason; got != RebaseStuckEdit {
		t.Errorf("reword should classify as Edit, got %s", got)
	}
}

func TestClassifyRebaseStuck_Break(t *testing.T) {
	s, rbDir := rebaseMergeFixture(t)
	makeFile(t, filepath.Join(rbDir, "git-rebase-todo"), "pick 4444444 # later\n")
	makeFile(t, filepath.Join(rbDir, "done"), "break\n")
	// `break` does not set stopped-sha but still pauses the rebase.
	if got := ClassifyRebaseStuck(s).Reason; got != RebaseStuckEdit {
		t.Errorf("break should classify as Edit, got %s", got)
	}
}

func TestClassifyRebaseStuck_Exec(t *testing.T) {
	s, rbDir := rebaseMergeFixture(t)
	makeFile(t, filepath.Join(rbDir, "git-rebase-todo"), "pick 5555555 # next\n")
	makeFile(t, filepath.Join(rbDir, "done"), "pick 4444444 # ok\nexec make test\n")
	// exec failure does not set stopped-sha.
	got := ClassifyRebaseStuck(s)
	if got.Reason != RebaseStuckExec {
		t.Errorf("Reason: want %s, got %s", RebaseStuckExec, got.Reason)
	}
	if got.LastDoneArg != "make test" {
		t.Errorf("LastDoneArg: want %q, got %q", "make test", got.LastDoneArg)
	}
}

// TestClassifyRebaseStuck_NotStuck — stopped-sha empty, todo non-empty.
// Mid-rebase between conflict-free picks: not stuck, just transient.
func TestClassifyRebaseStuck_NotStuck(t *testing.T) {
	s, rbDir := rebaseMergeFixture(t)
	makeFile(t, filepath.Join(rbDir, "git-rebase-todo"), "pick aaaa # later\npick bbbb # last\n")
	makeFile(t, filepath.Join(rbDir, "done"), "pick 1111 # done\n")

	if got := ClassifyRebaseStuck(s).Reason; got != RebaseStuckNone {
		t.Errorf("want None (mid-rebase), got %s", got)
	}
}

// TestClassifyRebaseStuck_Unknown — paused with no recognized signal.
func TestClassifyRebaseStuck_Unknown(t *testing.T) {
	s, rbDir := rebaseMergeFixture(t)
	makeFile(t, filepath.Join(rbDir, "git-rebase-todo"), "")
	// `done` missing entirely: nothing to anchor on.
	makeFile(t, filepath.Join(rbDir, "stopped-sha"), "abc1234\n")

	if got := ClassifyRebaseStuck(s).Reason; got != RebaseStuckUnknown {
		t.Errorf("want Unknown, got %s", got)
	}
}

// TestClassifyRebaseStuck_RebaseApply — am backend always falls back to
// Unknown. The caller still gets non-empty Reason so it can emit guidance.
func TestClassifyRebaseStuck_RebaseApply(t *testing.T) {
	commonDir := t.TempDir()
	makeDir(t, filepath.Join(commonDir, "rebase-apply"))
	s := &State{Kind: StateRebaseApply, CommonDir: commonDir, GitDir: commonDir}

	if got := ClassifyRebaseStuck(s).Reason; got != RebaseStuckUnknown {
		t.Errorf("rebase-apply: want Unknown, got %s", got)
	}
}

// TestLastDoneEntry_SkipsBlanksAndComments verifies the parser ignores
// `done` files with trailing newlines or comment lines.
func TestLastDoneEntry_SkipsBlanksAndComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "done")
	makeFile(t, path, "pick aaaaaaa # first\n# a comment\nedit bbbbbbb # second\n\n\n")

	op, arg := lastDoneEntry(path)
	if op != "edit" {
		t.Errorf("op: want edit, got %q", op)
	}
	if arg != "bbbbbbb # second" {
		t.Errorf("arg: want %q, got %q", "bbbbbbb # second", arg)
	}
}

func TestLastDoneEntry_Missing(t *testing.T) {
	op, arg := lastDoneEntry(filepath.Join(t.TempDir(), "nope"))
	if op != "" || arg != "" {
		t.Errorf("missing file: want empty, got %q/%q", op, arg)
	}
}
