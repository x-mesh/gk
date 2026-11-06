package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestPidFromLockReason(t *testing.T) {
	t.Parallel()
	cases := map[string]int{
		"claude agent agent-xyz (pid 12070)": 12070,
		"locked by some tool pid 4242":       4242,
		"no pid in this reason":              0,
		"":                                   0,
	}
	for in, want := range cases {
		if got := pidFromLockReason(in); got != want {
			t.Errorf("pidFromLockReason(%q): want %d, got %d", in, want, got)
		}
	}
}

func TestPidAlive(t *testing.T) {
	t.Parallel()
	if !pidAlive(os.Getpid()) {
		t.Error("the current process should read as alive")
	}
	if pidAlive(0) || pidAlive(-1) {
		t.Error("pid <= 0 must be dead")
	}
	if pidAlive(999999) {
		t.Skip("pid 999999 happens to be live on this host; skipping the dead-pid assertion")
	}
}

func TestWorktreeLockInfo_StaleVsAlive(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	r := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	// Alive: lock reason names this test process.
	wtAlive := filepath.Join(t.TempDir(), "alive")
	repo.RunGit("worktree", "add", wtAlive, "-b", "a")
	repo.RunGit("worktree", "lock", "--reason", fmt.Sprintf("claude agent (pid %d)", os.Getpid()), wtAlive)
	if lk := worktreeLockInfo(ctx, r, wtAlive); !lk.Locked || !lk.Alive {
		t.Errorf("alive lock: want locked+alive, got %+v", lk)
	}

	// Stale: lock reason names a dead pid.
	wtStale := filepath.Join(t.TempDir(), "stale")
	repo.RunGit("worktree", "add", wtStale, "-b", "s")
	repo.RunGit("worktree", "lock", "--reason", "claude agent (pid 999999)", wtStale)
	if lk := worktreeLockInfo(ctx, r, wtStale); !lk.Locked || lk.Alive {
		t.Errorf("stale lock: want locked+!alive, got %+v", lk)
	}

	// Unlocked worktree: not reported as locked.
	wtFree := filepath.Join(t.TempDir(), "free")
	repo.RunGit("worktree", "add", wtFree, "-b", "f")
	if lk := worktreeLockInfo(ctx, r, wtFree); lk.Locked {
		t.Errorf("unlocked worktree should not be Locked, got %+v", lk)
	}
}

func TestRunWorktreeRemove_StaleLockGate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	wt := filepath.Join(t.TempDir(), "stale")
	repo.RunGit("worktree", "add", wt, "-b", "s")
	repo.RunGit("worktree", "lock", "--reason", "claude agent (pid 999999)", wt)

	// Without --force: refused (stale lock).
	root, buf := buildWorktreeCmd(repo.Dir, "remove", wt)
	if err := root.Execute(); err == nil {
		t.Errorf("stale-locked remove without --force should be refused\n%s", buf.String())
	}
	// With --force: unlock + remove succeeds.
	root2, buf2 := buildWorktreeCmd(repo.Dir, "remove", wt, "--force")
	if err := root2.Execute(); err != nil {
		t.Fatalf("stale-locked remove with --force should succeed: %v\n%s", err, buf2.String())
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be gone after removal")
	}
}

func TestRunWorktreeRemove_LiveLockRequiresForceLocked(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	wt := filepath.Join(t.TempDir(), "alive")
	repo.RunGit("worktree", "add", wt, "-b", "a")
	repo.RunGit("worktree", "lock", "--reason", fmt.Sprintf("claude agent (pid %d)", os.Getpid()), wt)

	// --force is NOT enough for a live lock.
	root, buf := buildWorktreeCmd(repo.Dir, "remove", wt, "--force")
	if err := root.Execute(); err == nil {
		t.Errorf("live-locked remove with only --force should be refused\n%s", buf.String())
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree must survive a refused removal: %v", err)
	}
	// --force-locked overrides.
	root2, buf2 := buildWorktreeCmd(repo.Dir, "remove", wt, "--force-locked")
	if err := root2.Execute(); err != nil {
		t.Fatalf("live-locked remove with --force-locked should succeed: %v\n%s", err, buf2.String())
	}
}

// git resolves a relative path against the repo dir while `worktree list`
// reports absolute paths, so the lock gate must compare on the same footing
// (toss-20261106-e1e389c2: a relative path skipped the gate entirely).
func TestRunWorktreeRemove_LiveLockRelativePath(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	rel := filepath.Join(".claude", "worktrees", "agent-x")
	repo.RunGit("worktree", "add", rel, "-b", "agent-x")
	repo.RunGit("worktree", "lock", "--reason", fmt.Sprintf("claude agent agent-x (pid %d start Fri Nov  6 08:38:01 2026)", os.Getpid()), rel)
	abs := filepath.Join(repo.Dir, rel)

	root, buf := buildWorktreeCmd(repo.Dir, "remove", rel, "--force")
	err := root.Execute()
	if err == nil {
		t.Fatalf("live-locked remove with only --force should be refused\n%s", buf.String())
	}
	if got := errorCodeFromError(err); got != "worktree-locked" {
		t.Errorf("refusal should come from the lock gate (code worktree-locked), got %q: %v", got, err)
	}

	root2, buf2 := buildWorktreeCmd(repo.Dir, "remove", rel, "--force-locked")
	if err := root2.Execute(); err != nil {
		t.Fatalf("live-locked remove by relative path with --force-locked should succeed: %v\n%s", err, buf2.String())
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be gone after removal")
	}

	// git also identifies a worktree by its unique trailing component; the
	// lock gate must see through that spelling too.
	byName := filepath.Join(t.TempDir(), "agent-y")
	repo.RunGit("worktree", "add", byName, "-b", "agent-y")
	repo.RunGit("worktree", "lock", "--reason", fmt.Sprintf("claude agent agent-y (pid %d)", os.Getpid()), byName)
	root3, buf3 := buildWorktreeCmd(repo.Dir, "remove", "agent-y", "--force-locked")
	if err := root3.Execute(); err != nil {
		t.Fatalf("live-locked remove by name with --force-locked should succeed: %v\n%s", err, buf3.String())
	}
	if _, err := os.Stat(byName); !os.IsNotExist(err) {
		t.Errorf("worktree named agent-y should be gone after removal")
	}
}

func TestUniqueWorktreeBySuffix(t *testing.T) {
	t.Parallel()
	entries := []WorktreeEntry{
		{Path: "/r"},
		{Path: "/r/.claude/worktrees/agent-x"},
		{Path: "/tmp/a/dup"},
		{Path: "/tmp/b/dup"},
	}
	cases := []struct {
		target string
		want   string
		ok     bool
	}{
		{"agent-x", "/r/.claude/worktrees/agent-x", true},
		{"worktrees/agent-x", "/r/.claude/worktrees/agent-x", true},
		{"nt-x", "", false},
		{"dup", "", false},
		{"a/dup", "/tmp/a/dup", true},
	}
	for _, tc := range cases {
		got, ok := uniqueWorktreeBySuffix(entries, tc.target)
		if got != tc.want || ok != tc.ok {
			t.Errorf("uniqueWorktreeBySuffix(%q) = (%q, %v), want (%q, %v)", tc.target, got, ok, tc.want, tc.ok)
		}
	}
}
