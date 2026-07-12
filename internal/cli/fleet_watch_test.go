package cli

import (
	"context"
	"fmt"
	"syscall"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// TestFleetEntryActive locks the activity signals: current checkout, dirty
// work, a paused operation, and recency all count; stale/clean/error do not.
func TestFleetEntryActive(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		e    fleetEntryJSON
		want bool
	}{
		{"current checkout", fleetEntryJSON{Path: "/w", Current: true}, true},
		{"dirty", fleetEntryJSON{Path: "/w", Dirty: &contextDirtyJSON{Untracked: 1}}, true},
		{"paused op", fleetEntryJSON{Path: "/w", Operation: "rebase 2/5"}, true},
		{"recent activity", fleetEntryJSON{Path: "/w", lastActive: now.Add(-30 * time.Minute)}, true},
		{"stale clean", fleetEntryJSON{Path: "/w", lastActive: now.Add(-2 * time.Hour)}, false},
		{"no signals", fleetEntryJSON{Path: "/w"}, false},
		{"error entry", fleetEntryJSON{Path: "/w", Status: "error", Current: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fleetEntryActive(tc.e, now); got != tc.want {
				t.Errorf("fleetEntryActive = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPlanFleetWatchers covers the allocation policy: only active entries are
// granted, the budget divides among actives (not headcount), idle holders are
// grandfathered without pressure and revoked under it, and a forced path (the
// zoomed worktree) counts as active regardless of signals.
func TestPlanFleetWatchers(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-3 * time.Hour)
	entries := []fleetEntryJSON{
		{Path: "/w/active", Dirty: &contextDirtyJSON{Unstaged: 1}, lastActive: now},
		{Path: "/w/idle1", lastActive: stale},
		{Path: "/w/idle2", lastActive: stale},
		{Path: "/w/error", Status: "error"},
	}

	// No watchers yet: only the active entry is granted; budget = full/1.
	plan := planFleetWatchers(entries, map[string]bool{}, nil, nil, now)
	if len(plan.grant) != 1 || plan.grant[0] != "/w/active" {
		t.Errorf("grant = %v, want [/w/active]", plan.grant)
	}
	if len(plan.revoke) != 0 {
		t.Errorf("no idle holders → no revokes, got %v", plan.revoke)
	}
	if plan.dirCap != fsWatchCostBudget() {
		t.Errorf("one active gets the whole budget, dirCap = %d", plan.dirCap)
	}

	// Idle holder without pressure (active already has its watcher): kept.
	have := map[string]bool{"/w/active": true, "/w/idle1": true}
	plan = planFleetWatchers(entries, have, nil, nil, now)
	if len(plan.grant) != 0 || len(plan.revoke) != 0 {
		t.Errorf("steady state must be a no-op, got grant=%v revoke=%v", plan.grant, plan.revoke)
	}

	// Pressure: a second entry becomes active while an idle one holds a
	// watcher → the idle holder is revoked to free budget.
	entries[1].lastActive = now // idle1 wakes up
	have = map[string]bool{"/w/active": true, "/w/idle2": true}
	plan = planFleetWatchers(entries, have, nil, nil, now)
	if len(plan.grant) != 1 || plan.grant[0] != "/w/idle1" {
		t.Errorf("woken entry must be granted, got %v", plan.grant)
	}
	if len(plan.revoke) != 1 || plan.revoke[0] != "/w/idle2" {
		t.Errorf("idle holder must be revoked under pressure, got %v", plan.revoke)
	}
	if plan.dirCap != fsWatchCostBudget()/2 {
		t.Errorf("two actives split the budget, dirCap = %d", plan.dirCap)
	}

	// Forced path (zoom target) counts as active despite no signals.
	entries[1].lastActive = stale
	plan = planFleetWatchers(entries, map[string]bool{}, map[string]bool{"/w/idle2": true}, nil, now)
	found := false
	for _, p := range plan.grant {
		if p == "/w/idle2" {
			found = true
		}
	}
	if !found {
		t.Errorf("forced path must be granted, grant = %v", plan.grant)
	}
}

// TestIsFDExhausted: EMFILE/ENFILE (wrapped or bare) are the hard-abort
// signals; anything else stays best-effort.
func TestIsFDExhausted(t *testing.T) {
	if !isFDExhausted(syscall.EMFILE) || !isFDExhausted(fmt.Errorf("add: %w", syscall.ENFILE)) {
		t.Error("EMFILE/ENFILE must be detected, wrapped or not")
	}
	if isFDExhausted(syscall.EACCES) || isFDExhausted(nil) {
		t.Error("non-exhaustion errors must not trigger the hard abort")
	}
}

// TestFSWatchCostBudget: the budget is always within sane bounds — at least
// the floor, at most the cap (kqueue) or the historical dir constant.
func TestFSWatchCostBudget(t *testing.T) {
	got := fsWatchCostBudget()
	if fsWatchCostPerFile {
		if got < 256 || got > 1<<16 {
			t.Errorf("kqueue budget out of bounds: %d", got)
		}
	} else if got != fsWatchMaxDirs {
		t.Errorf("non-kqueue budget = %d, want %d", got, fsWatchMaxDirs)
	}
}

// TestNewFSWatcherCostCap (kqueue platforms): a tree whose FILE count exceeds
// the cost cap is refused outright — files, not just directories, are the
// descriptor cost that melted the dashboard once.
func TestNewFSWatcherCostCap(t *testing.T) {
	if !fsWatchCostPerFile {
		t.Skip("file-cost accounting only applies to kqueue platforms")
	}
	repo := testutil.NewRepo(t)
	for i := 0; i < 10; i++ {
		repo.WriteFile(fmt.Sprintf("f%02d.txt", i), "x")
	}
	runner := &git.ExecRunner{Dir: repo.Dir}

	if fw, ok := newFSWatcher(context.Background(), runner, fsWatchDebounce, 5); ok {
		fw.Close()
		t.Fatal("10 files must exceed a cost cap of 5")
	}
	fw, ok := newFSWatcher(context.Background(), runner, fsWatchDebounce, 500)
	if !ok {
		t.Fatal("500 cost must fit a 10-file tree")
	}
	fw.Close()
}

// TestPlanFleetWatchers_CooldownExertsNoPressure (F4): when every missing
// grant is blocked by the retry cooldown, idle holders must NOT be revoked —
// otherwise the fleet sheds watchers for grants that won't even be attempted.
func TestPlanFleetWatchers_CooldownExertsNoPressure(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	entries := []fleetEntryJSON{
		{Path: "/w/active", Dirty: &contextDirtyJSON{Unstaged: 1}, lastActive: now},
		{Path: "/w/idle", lastActive: now.Add(-3 * time.Hour)},
	}
	have := map[string]bool{"/w/idle": true}
	denied := map[string]time.Time{"/w/active": now.Add(-30 * time.Second)} // cooling down

	plan := planFleetWatchers(entries, have, nil, denied, now)
	if len(plan.grant) != 0 {
		t.Errorf("cooldown-blocked path must not be granted, got %v", plan.grant)
	}
	if len(plan.revoke) != 0 {
		t.Errorf("no attemptable grant → no revoke, got %v", plan.revoke)
	}

	// Cooldown expired → grant resumes and pressure applies again.
	denied["/w/active"] = now.Add(-3 * time.Minute)
	plan = planFleetWatchers(entries, have, nil, denied, now)
	if len(plan.grant) != 1 || len(plan.revoke) != 1 {
		t.Errorf("expired cooldown must restore grant+pressure, got grant=%v revoke=%v", plan.grant, plan.revoke)
	}
}
