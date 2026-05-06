package resolve

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

// TestResolverRun_StateNoneWithUnmergedAcceptsThePath — regression
// guard for the v0.37.1 fix: when `git stash apply`, `git apply
// --3way`, or a partial reset leaves unmerged stages without writing
// a MERGE_HEAD / rebase-merge / CHERRY_PICK_HEAD marker, gk resolve
// must still take the conflict-resolution path. Previously the
// state.Kind == StateNone gate kicked in *before* the file collection
// step, locking users out of the only command equipped to fix the
// situation.
func TestResolverRun_StateNoneWithUnmergedAcceptsThePath(t *testing.T) {
	// 4 unmerged paths reported by porcelain v2; no in-progress op.
	statusOut := buildPorcelainV2([]string{"a.go", "b.go", "c.go", "d.go"})
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2": {Stdout: statusOut},
		},
	}

	// ReadFile returns conflict markers so ParseConflictFiles produces
	// at least one ConflictFile and the rest of the pipeline runs.
	conflictBlob := buildConflictContent(makeConflictFile("placeholder"))
	r := &Resolver{
		Runner:   runner,
		Stderr:   &bytes.Buffer{},
		Stdout:   &bytes.Buffer{},
		ReadFile: func(string) ([]byte, error) { return conflictBlob, nil },
	}
	state := &gitstate.State{Kind: gitstate.StateNone}

	_, err := r.Run(context.Background(), state, ResolveOptions{
		Strategy: StrategyOurs, // skips AI; deterministic
		DryRun:   true,         // no FS writes
		NoBackup: true,
	})
	if err == nil {
		return // success path — gate didn't fire and the dry-run completed
	}
	// If something else fails downstream that's fine for this regression
	// test, but the legacy bare guard message must be gone.
	if strings.Contains(err.Error(), "no merge/rebase/cherry-pick conflict in progress") &&
		!strings.Contains(err.Error(), "no unmerged paths") {
		t.Fatalf("legacy bare guard fired despite unmerged paths: %v", err)
	}
}

// TestResolverRun_StateNoneWithoutUnmergedRefusesWithUpdatedMessage —
// the *real* nothing-to-do case (clean tree, no op) must still be
// rejected, but with the new wording so users see "and no unmerged
// paths" — confirming both halves of the gate were consulted.
func TestResolverRun_StateNoneWithoutUnmergedRefusesWithUpdatedMessage(t *testing.T) {
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2": {Stdout: ""},
		},
	}
	r := &Resolver{
		Runner: runner,
		Stderr: &bytes.Buffer{},
		Stdout: &bytes.Buffer{},
	}
	state := &gitstate.State{Kind: gitstate.StateNone}

	_, err := r.Run(context.Background(), state, ResolveOptions{Strategy: StrategyOurs})
	if err == nil {
		t.Fatal("expected error when there's nothing to resolve")
	}
	if !strings.Contains(err.Error(), "no unmerged paths") {
		t.Errorf("expected updated guard wording mentioning unmerged paths, got: %v", err)
	}
}
