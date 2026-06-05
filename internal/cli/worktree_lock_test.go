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
