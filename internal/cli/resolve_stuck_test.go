package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/resolve"
)

// stuckEmptyCommitState builds a rebase-merge fixture that ClassifyRebaseStuck
// will tag as RebaseStuckEmptyCommit — the same shape as the mem-mesh repro.
func stuckEmptyCommitState(t *testing.T) *gitstate.State {
	t.Helper()
	commonDir := t.TempDir()
	rb := filepath.Join(commonDir, "rebase-merge")
	if err := os.MkdirAll(rb, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mustWrite := func(name, content string) {
		if err := os.WriteFile(filepath.Join(rb, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	mustWrite("git-rebase-todo", "")
	mustWrite("done", "pick c613c80 # release\n")
	mustWrite("stopped-sha", "c613c80fc327d1c90c36c93259332ecb202f79d0\n")
	mustWrite("drop_redundant_commits", "")
	return &gitstate.State{
		Kind:      gitstate.StateRebaseMerge,
		CommonDir: commonDir,
		GitDir:    commonDir,
	}
}

// TestRunResolveInteractive_StuckRebase verifies the interactive entrypoint
// emits a *resolve.StuckError instead of swallowing the situation as
// "no conflicted files found" — the exact bug this branch fixes.
func TestRunResolveInteractive_StuckRebase(t *testing.T) {
	state := stuckEmptyCommitState(t)

	fr := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2": {Stdout: ""},
		},
	}
	stderr := &bytes.Buffer{}

	r := &resolve.Resolver{
		Runner: fr,
		Stderr: stderr,
		Stdout: &bytes.Buffer{},
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetErr(stderr)
	cmd.SetOut(&bytes.Buffer{})

	err := runResolveInteractive(context.Background(), cmd, r, state, resolve.ResolveOptions{})

	var se *resolve.StuckError
	if !errors.As(err, &se) {
		t.Fatalf("want *resolve.StuckError, got %T: %v", err, err)
	}
	if se.Stuck.Reason != gitstate.RebaseStuckEmptyCommit {
		t.Errorf("Reason: want EmptyCommit, got %s", se.Stuck.Reason)
	}
	if strings.Contains(stderr.String(), "no conflicted files found") {
		t.Errorf("legacy message must not leak when stuck:\n%s", stderr.String())
	}
}

// TestRunResolveInteractive_NonStuckRebase — when the rebase is between
// picks (todo non-empty, no stopped-sha), behaviour must match the legacy
// path: print the friendly message and return nil.
func TestRunResolveInteractive_NonStuckRebase(t *testing.T) {
	commonDir := t.TempDir()
	rb := filepath.Join(commonDir, "rebase-merge")
	_ = os.MkdirAll(rb, 0o755)
	_ = os.WriteFile(filepath.Join(rb, "git-rebase-todo"), []byte("pick aaaa # next\n"), 0o644)
	_ = os.WriteFile(filepath.Join(rb, "done"), []byte("pick 1111 # ok\n"), 0o644)

	state := &gitstate.State{
		Kind:      gitstate.StateRebaseMerge,
		CommonDir: commonDir,
		GitDir:    commonDir,
	}

	fr := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2": {Stdout: ""},
		},
	}
	stderr := &bytes.Buffer{}

	r := &resolve.Resolver{Runner: fr, Stderr: stderr, Stdout: &bytes.Buffer{}}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetErr(stderr)
	cmd.SetOut(&bytes.Buffer{})

	err := runResolveInteractive(context.Background(), cmd, r, state, resolve.ResolveOptions{})
	if err != nil {
		t.Fatalf("non-stuck rebase: want nil err, got %v", err)
	}
	if !strings.Contains(stderr.String(), "no conflicted files found") {
		t.Errorf("legacy message must still print, got %q", stderr.String())
	}
}

// TestRunResolveInteractive_NonRebaseEmpty — merge / cherry-pick with no
// unmerged paths must keep the original "no conflicted files found" output.
func TestRunResolveInteractive_NonRebaseEmpty(t *testing.T) {
	for _, kind := range []gitstate.StateKind{gitstate.StateMerge, gitstate.StateCherryPick} {
		state := &gitstate.State{Kind: kind}
		fr := &git.FakeRunner{
			Responses: map[string]git.FakeResponse{
				"status --porcelain=v2": {Stdout: ""},
			},
		}
		stderr := &bytes.Buffer{}
		r := &resolve.Resolver{Runner: fr, Stderr: stderr, Stdout: &bytes.Buffer{}}

		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		cmd.SetErr(stderr)
		cmd.SetOut(&bytes.Buffer{})

		err := runResolveInteractive(context.Background(), cmd, r, state, resolve.ResolveOptions{})
		if err != nil {
			t.Errorf("Kind=%s: want nil err, got %v", kind, err)
		}
		if !strings.Contains(stderr.String(), "no conflicted files found") {
			t.Errorf("Kind=%s: legacy message missing, got %q", kind, stderr.String())
		}
	}
}
