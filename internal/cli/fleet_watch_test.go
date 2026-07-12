package cli

import (
	"testing"
	"time"
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
	plan := planFleetWatchers(entries, map[string]bool{}, nil, now)
	if len(plan.grant) != 1 || plan.grant[0] != "/w/active" {
		t.Errorf("grant = %v, want [/w/active]", plan.grant)
	}
	if len(plan.revoke) != 0 {
		t.Errorf("no idle holders → no revokes, got %v", plan.revoke)
	}
	if plan.dirCap != fsWatchMaxDirs {
		t.Errorf("one active gets the whole budget, dirCap = %d", plan.dirCap)
	}

	// Idle holder without pressure (active already has its watcher): kept.
	have := map[string]bool{"/w/active": true, "/w/idle1": true}
	plan = planFleetWatchers(entries, have, nil, now)
	if len(plan.grant) != 0 || len(plan.revoke) != 0 {
		t.Errorf("steady state must be a no-op, got grant=%v revoke=%v", plan.grant, plan.revoke)
	}

	// Pressure: a second entry becomes active while an idle one holds a
	// watcher → the idle holder is revoked to free budget.
	entries[1].lastActive = now // idle1 wakes up
	have = map[string]bool{"/w/active": true, "/w/idle2": true}
	plan = planFleetWatchers(entries, have, nil, now)
	if len(plan.grant) != 1 || plan.grant[0] != "/w/idle1" {
		t.Errorf("woken entry must be granted, got %v", plan.grant)
	}
	if len(plan.revoke) != 1 || plan.revoke[0] != "/w/idle2" {
		t.Errorf("idle holder must be revoked under pressure, got %v", plan.revoke)
	}
	if plan.dirCap != fsWatchMaxDirs/2 {
		t.Errorf("two actives split the budget, dirCap = %d", plan.dirCap)
	}

	// Forced path (zoom target) counts as active despite no signals.
	entries[1].lastActive = stale
	plan = planFleetWatchers(entries, map[string]bool{}, map[string]bool{"/w/idle2": true}, now)
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
