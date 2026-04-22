package gitsafe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/testutil"
)

// --- Report unit tests (no I/O) -------------------------------------------

func TestReport_OK(t *testing.T) {
	tests := []struct {
		name string
		r    Report
		want bool
	}{
		{"clean", Report{}, true},
		{"dirty", Report{Dirty: true}, false},
		{"rebasing", Report{InProgress: gitstate.StateRebaseMerge}, false},
		{"rebasing-and-dirty", Report{InProgress: gitstate.StateRebaseMerge, Dirty: true}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.r.OK(); got != tc.want {
				t.Errorf("OK() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReport_Err(t *testing.T) {
	if err := (Report{}).Err(); err != nil {
		t.Errorf("clean Report.Err() = %v, want nil", err)
	}

	err := Report{Dirty: true}.Err()
	if err == nil || !strings.Contains(err.Error(), "working tree has uncommitted changes") {
		t.Errorf("dirty Report.Err() = %v, want uncommitted-changes error", err)
	}

	err = Report{InProgress: gitstate.StateRebaseMerge}.Err()
	if err == nil || !strings.Contains(err.Error(), "in-progress") {
		t.Errorf("rebasing Report.Err() = %v, want in-progress error", err)
	}

	// In-progress takes precedence over dirty.
	err = Report{InProgress: gitstate.StateRebaseMerge, Dirty: true}.Err()
	if err == nil || !strings.Contains(err.Error(), "in-progress") {
		t.Errorf("combined Report.Err() = %v, want in-progress precedence", err)
	}
}

func TestReport_AllowDirty(t *testing.T) {
	r := Report{Dirty: true}.AllowDirty()
	if r.Dirty {
		t.Errorf("AllowDirty() did not clear Dirty flag")
	}
	if err := r.Err(); err != nil {
		t.Errorf("dirty-then-allowed Report.Err() = %v, want nil", err)
	}

	// In-progress still fires after AllowDirty.
	r = Report{InProgress: gitstate.StateRebaseMerge, Dirty: true}.AllowDirty()
	if r.InProgress != gitstate.StateRebaseMerge {
		t.Errorf("AllowDirty() clobbered InProgress: %v", r.InProgress)
	}
	if err := r.Err(); err == nil {
		t.Errorf("rebasing Report.AllowDirty().Err() = nil, want in-progress error")
	}
}

// --- Check integration tests (real repo) ----------------------------------

func TestCheck_Clean(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hello")
	repo.RunGit("add", "a.txt")
	repo.RunGit("commit", "-m", "init")

	runner := &git.ExecRunner{Dir: repo.Dir}
	rep, err := Check(context.Background(), runner, WithWorkDir(repo.Dir))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !rep.OK() {
		t.Errorf("clean repo OK()=false; report=%+v", rep)
	}
}

func TestCheck_Dirty(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hello")
	repo.RunGit("add", "a.txt")
	repo.RunGit("commit", "-m", "init")
	repo.WriteFile("a.txt", "modified") // dirty

	runner := &git.ExecRunner{Dir: repo.Dir}
	rep, err := Check(context.Background(), runner, WithWorkDir(repo.Dir))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !rep.Dirty {
		t.Fatalf("expected Dirty=true; report=%+v", rep)
	}
	if err := rep.Err(); err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("dirty Report.Err() = %v, want uncommitted error", err)
	}
	// AllowDirty path — wipe's case.
	if err := rep.AllowDirty().Err(); err != nil {
		t.Errorf("Report.AllowDirty().Err() = %v, want nil for dirty-only state", err)
	}
}

func TestCheck_Rebasing(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "v1")
	repo.RunGit("add", "a.txt")
	repo.RunGit("commit", "-m", "init")

	// Fabricate rebase-merge marker directory — faster + more reliable than
	// staging a real rebase conflict. gitstate.Detect only checks for the
	// directory's existence to classify the state.
	rebaseDir := filepath.Join(repo.Dir, ".git", "rebase-merge")
	if err := os.MkdirAll(rebaseDir, 0o755); err != nil {
		t.Fatalf("mkdir rebase-merge: %v", err)
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	rep, err := Check(context.Background(), runner, WithWorkDir(repo.Dir))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep.InProgress == gitstate.StateNone {
		t.Fatalf("expected in-progress state; report=%+v", rep)
	}
	if err := rep.Err(); err == nil || !strings.Contains(err.Error(), "in-progress") {
		t.Errorf("in-progress Report.Err() = %v, want in-progress error", err)
	}
	// AllowDirty does NOT suppress in-progress — the critical invariant.
	if err := rep.AllowDirty().Err(); err == nil {
		t.Errorf("Report.AllowDirty().Err() = nil during rebase, want in-progress error")
	}
}
