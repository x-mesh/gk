package resolve

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

// stuckRebaseMergeState builds a State pointing at a fresh rebase-merge
// fixture so CheckStuck has files to inspect. The signal preset ("empty",
// "edit", "exec", "unknown", "none") controls which classification will fire.
func stuckRebaseMergeState(t *testing.T, preset string) *gitstate.State {
	t.Helper()
	commonDir := t.TempDir()
	rb := filepath.Join(commonDir, "rebase-merge")
	if err := os.MkdirAll(rb, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(rb, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	switch preset {
	case "empty":
		write("git-rebase-todo", "")
		write("done", "pick c613c80 # release\n")
		write("stopped-sha", "c613c80fc327d1c90c36c93259332ecb202f79d0\n")
		write("drop_redundant_commits", "")
	case "edit":
		write("git-rebase-todo", "pick aaaa # next\n")
		write("done", "edit bbbb # stop\n")
		write("stopped-sha", "bbbb\n")
	case "exec":
		write("git-rebase-todo", "pick aaaa # next\n")
		write("done", "exec make test\n")
	case "unknown":
		write("git-rebase-todo", "")
		write("stopped-sha", "abc123\n")
	case "none":
		write("git-rebase-todo", "pick aaaa # next\n")
		write("done", "pick 1111 # ok\n")
	default:
		t.Fatalf("unknown preset %q", preset)
	}
	return &gitstate.State{
		Kind:      gitstate.StateRebaseMerge,
		CommonDir: commonDir,
		GitDir:    commonDir,
	}
}

// fakeRunnerNoConflicts returns a runner whose `git status --porcelain=v2`
// produces no unmerged paths — exercising the "no conflicted files" branch.
func fakeRunnerNoConflicts() *git.FakeRunner {
	return &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2": {Stdout: ""},
		},
	}
}

func TestResolverRun_StuckEmptyCommit(t *testing.T) {
	state := stuckRebaseMergeState(t, "empty")
	stderr := &bytes.Buffer{}

	r := &Resolver{Runner: fakeRunnerNoConflicts(), Stderr: stderr}
	res, err := r.Run(context.Background(), state, ResolveOptions{Strategy: StrategyOurs})

	if res != nil {
		t.Errorf("res should be nil on stuck error, got %+v", res)
	}
	var se *StuckError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StuckError, got %T: %v", err, err)
	}
	if se.Stuck.Reason != gitstate.RebaseStuckEmptyCommit {
		t.Errorf("Reason: want EmptyCommit, got %s", se.Stuck.Reason)
	}
	// stderr must NOT carry the "no conflicted files found" line — that
	// would re-mask the stuck state we're trying to surface.
	if strings.Contains(stderr.String(), "no conflicted files found") {
		t.Errorf("stderr leaked masked message: %q", stderr.String())
	}
}

func TestResolverRun_StuckEdit(t *testing.T) {
	state := stuckRebaseMergeState(t, "edit")
	r := &Resolver{Runner: fakeRunnerNoConflicts(), Stderr: &bytes.Buffer{}}
	_, err := r.Run(context.Background(), state, ResolveOptions{Strategy: StrategyOurs})
	var se *StuckError
	if !errors.As(err, &se) || se.Stuck.Reason != gitstate.RebaseStuckEdit {
		t.Errorf("want StuckError(Edit), got %v", err)
	}
}

func TestResolverRun_StuckExec(t *testing.T) {
	state := stuckRebaseMergeState(t, "exec")
	r := &Resolver{Runner: fakeRunnerNoConflicts(), Stderr: &bytes.Buffer{}}
	_, err := r.Run(context.Background(), state, ResolveOptions{Strategy: StrategyOurs})
	var se *StuckError
	if !errors.As(err, &se) || se.Stuck.Reason != gitstate.RebaseStuckExec {
		t.Errorf("want StuckError(Exec), got %v", err)
	}
}

func TestResolverRun_StuckUnknown(t *testing.T) {
	state := stuckRebaseMergeState(t, "unknown")
	r := &Resolver{Runner: fakeRunnerNoConflicts(), Stderr: &bytes.Buffer{}}
	_, err := r.Run(context.Background(), state, ResolveOptions{Strategy: StrategyOurs})
	var se *StuckError
	if !errors.As(err, &se) || se.Stuck.Reason != gitstate.RebaseStuckUnknown {
		t.Errorf("want StuckError(Unknown), got %v", err)
	}
}

// TestResolverRun_NotStuck_KeepsLegacyMessage — when rebase is mid-stream
// (todo non-empty, no stopped-sha) Run must still emit the original
// "no conflicted files found" message and return nil error.
func TestResolverRun_NotStuck_KeepsLegacyMessage(t *testing.T) {
	state := stuckRebaseMergeState(t, "none")
	stderr := &bytes.Buffer{}
	r := &Resolver{Runner: fakeRunnerNoConflicts(), Stderr: stderr}
	res, err := r.Run(context.Background(), state, ResolveOptions{Strategy: StrategyOurs})
	if err != nil {
		t.Fatalf("non-stuck rebase: want nil err, got %v", err)
	}
	if res == nil {
		t.Fatal("non-stuck rebase: want empty result, got nil")
	}
	if !strings.Contains(stderr.String(), "no conflicted files found") {
		t.Errorf("stderr should keep legacy message, got %q", stderr.String())
	}
}

// TestResolverRun_NonRebaseStateUnchanged — merge / cherry-pick must never
// trigger StuckError even when CollectConflictedFiles returns empty.
func TestResolverRun_NonRebaseStateUnchanged(t *testing.T) {
	for _, kind := range []gitstate.StateKind{gitstate.StateMerge, gitstate.StateCherryPick} {
		state := &gitstate.State{Kind: kind}
		r := &Resolver{Runner: fakeRunnerNoConflicts(), Stderr: &bytes.Buffer{}}
		_, err := r.Run(context.Background(), state, ResolveOptions{Strategy: StrategyOurs})
		if err != nil {
			t.Errorf("Kind=%s: want nil err, got %v", kind, err)
		}
	}
}
