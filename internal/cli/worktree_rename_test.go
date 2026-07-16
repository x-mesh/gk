package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

// resolveWorktreeTarget is the pure reference-resolution logic behind
// `gk worktree rename <target>`: it maps a user-typed reference (managed
// name, absolute/relative path, or branch name) to a registered worktree.
// A FakeRunner with no worktree.base config means resolveWorktreePath is a
// no-op, so managed-name resolution collapses to the basename/branch paths —
// which is exactly what we exercise here.
func TestResolveWorktreeTarget(t *testing.T) {
	entries := []WorktreeEntry{
		{Path: "/repo", Branch: "main"},
		{Path: "/base/proj/feat-x", Branch: "feat/x"},
		{Path: "/base/proj/hotfix", Branch: "hotfix", Detached: false},
		{Path: "/base/proj/detached", Detached: true},
	}
	runner := &git.FakeRunner{}
	ctx := context.Background()

	cases := []struct {
		name     string
		target   string
		wantPath string
		wantOK   bool
	}{
		{"managed basename", "feat-x", "/base/proj/feat-x", true},
		{"absolute path", "/base/proj/hotfix", "/base/proj/hotfix", true},
		{"branch name fallback", "feat/x", "/base/proj/feat-x", true},
		{"main by branch name", "main", "/repo", true},
		{"detached by basename", "detached", "/base/proj/detached", true},
		{"unknown", "nope", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveWorktreeTarget(ctx, runner, nil, tc.target, entries)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (entry %+v)", ok, tc.wantOK, got)
			}
			if ok && got.Path != tc.wantPath {
				t.Errorf("path = %q, want %q", got.Path, tc.wantPath)
			}
		})
	}
}

// TestResolveWorktreeTargetPrefersPathOverBranch guards the match ordering:
// an exact/basename path match must win over a coincidental branch-name
// collision so `rename` never grabs the wrong worktree.
func TestResolveWorktreeTargetPrefersPathOverBranch(t *testing.T) {
	// Worktree A's directory basename is "shared"; worktree B's *branch* is
	// also "shared". Targeting "shared" must resolve A (basename beats branch).
	entries := []WorktreeEntry{
		{Path: "/base/proj/shared", Branch: "feat/a"},
		{Path: "/base/proj/other", Branch: "shared"},
	}
	got, ok := resolveWorktreeTarget(context.Background(), &git.FakeRunner{}, nil, "shared", entries)
	if !ok {
		t.Fatal("expected a match")
	}
	if got.Path != "/base/proj/shared" {
		t.Errorf("basename match must win over branch match: got %q", got.Path)
	}
}

// TestResolveWorktreeTargetAbsolutePathMatch confirms an absolute path
// resolves regardless of basename/branch, canonicalised for comparison.
func TestResolveWorktreeTargetAbsolutePathMatch(t *testing.T) {
	dir := t.TempDir()
	entries := []WorktreeEntry{{Path: dir, Branch: "feat/x"}}
	got, ok := resolveWorktreeTarget(context.Background(), &git.FakeRunner{}, nil, filepath.Join(dir, "."), entries)
	if !ok || got.Path != dir {
		t.Fatalf("absolute path did not match: ok=%v path=%q", ok, got.Path)
	}
}

// TestCheckWorktreeLockGate covers the shared lock policy used by both
// `gk worktree remove` and `rename`: a live holder needs --force-locked, a
// stale one needs --force, and an unlocked worktree always passes.
func TestCheckWorktreeLockGate(t *testing.T) {
	cases := []struct {
		name        string
		lock        worktreeLock
		force       bool
		forceLocked bool
		wantErr     bool
	}{
		{"unlocked always ok", worktreeLock{Locked: false}, false, false, false},
		{"live holder blocked", worktreeLock{Locked: true, Alive: true}, false, false, true},
		{"live holder needs --force (insufficient)", worktreeLock{Locked: true, Alive: true}, true, false, true},
		{"live holder --force-locked passes", worktreeLock{Locked: true, Alive: true}, false, true, false},
		{"stale holder blocked", worktreeLock{Locked: true, Alive: false}, false, false, true},
		{"stale holder --force passes", worktreeLock{Locked: true, Alive: false}, true, false, false},
		{"stale holder --force-locked passes", worktreeLock{Locked: true, Alive: false}, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkWorktreeLockGate(tc.lock, tc.force, tc.forceLocked)
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestRewriteChildParents is the regression test for the dangling-gk-parent
// bug: after a branch is renamed, children recording the OLD name as their
// gk-parent must be repointed at the new name; unrelated parents stay put.
func TestRewriteChildParents(t *testing.T) {
	// git config --get-regexp returns the full gk-parent map; SetParent
	// issues one `git config` write per repointed child.
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"config --get-regexp ^branch\\..*\\.gk-parent$": {
				Stdout: "branch.feat/sub.gk-parent feat/base\n" +
					"branch.feat/other.gk-parent main\n" +
					"branch.feat/sub2.gk-parent feat/base\n",
			},
		},
	}
	client := git.NewClient(fake)
	rewriteChildParents(context.Background(), client, "feat/base", "feat/base-v2")

	// Collect the config-write calls.
	var writes []string
	for _, c := range fake.Calls {
		if len(c.Args) >= 3 && c.Args[0] == "config" && strings.HasSuffix(c.Args[1], ".gk-parent") {
			writes = append(writes, c.Args[1]+"="+c.Args[2])
		}
	}
	// Both children of feat/base repointed; feat/other (parent main) untouched.
	wantSub := map[string]bool{
		"branch.feat/sub.gk-parent=feat/base-v2":  false,
		"branch.feat/sub2.gk-parent=feat/base-v2": false,
	}
	for _, wgot := range writes {
		if _, ok := wantSub[wgot]; ok {
			wantSub[wgot] = true
		}
		if strings.Contains(wgot, "feat/other") {
			t.Errorf("unrelated parent should not be rewritten: %s", wgot)
		}
	}
	for want, seen := range wantSub {
		if !seen {
			t.Errorf("expected child repoint %q, writes=%v", want, writes)
		}
	}
}
