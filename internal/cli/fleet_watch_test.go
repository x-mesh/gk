package cli

import (
	"context"
	"fmt"
	"sync"
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
		{"dirty and recent", fleetEntryJSON{Path: "/w", Dirty: &contextDirtyJSON{Untracked: 1}, lastActive: now.Add(-time.Minute)}, true},
		{"dirty but abandoned", fleetEntryJSON{Path: "/w", Dirty: &contextDirtyJSON{Untracked: 1}, lastActive: now.Add(-60 * 24 * time.Hour)}, false},
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
// granted, the budget divides among actives (not headcount), inactive holders
// are retired whenever a replacement can be considered, and a forced path
// (the zoomed worktree) counts as active regardless of signals.
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

	// An inactive holder is obsolete and is retired even without a new grant.
	have := map[string]bool{"/w/active": true, "/w/idle1": true}
	plan = planFleetWatchers(entries, have, nil, nil, now)
	if len(plan.grant) != 0 || len(plan.revoke) != 1 || plan.revoke[0] != "/w/idle1" {
		t.Errorf("inactive holder must be retired, got grant=%v revoke=%v", plan.grant, plan.revoke)
	}

	// A second entry becomes active while an idle one holds a watcher.
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

// TestPlanFleetWatchers_ReservationsStayWithinBudget locks the process-wide
// invariant. Even a fleet larger than the budget gets only as many one-unit
// reservations as fit; forced worktrees take the available slots first.
func TestPlanFleetWatchers_ReservationsStayWithinBudget(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	budget := fsWatchCostBudget()
	entries := make([]fleetEntryJSON, budget+2)
	for i := range entries {
		entries[i] = fleetEntryJSON{Path: fmt.Sprintf("/w/%04d", i), lastActive: now}
	}
	forced := map[string]bool{entries[len(entries)-1].Path: true}
	plan := planFleetWatchers(entries, nil, forced, nil, now)
	if got := len(plan.grant) * plan.dirCap; got > budget {
		t.Fatalf("reservations = %d, budget = %d", got, budget)
	}
	if len(plan.grant) != budget {
		t.Fatalf("grants = %d, want budget-sized %d", len(plan.grant), budget)
	}
	if plan.grant[0] != entries[len(entries)-1].Path {
		t.Fatalf("forced path must get the first reservation, got %q", plan.grant[0])
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

// --- sync must not hold ws.mu across the filesystem walk ----------------------

func newTestWatchSet() *fleetWatchSet {
	return &fleetWatchSet{
		watchers: map[string]*fsWatcher{},
		events:   make(chan string, 8),
		denied:   map[string]time.Time{},
	}
}

// bigRepo is large enough that establishing a watcher over it takes measurable
// time — which is the whole point: a walk that finishes instantly cannot prove
// anything about who is blocked while it runs.
func bigRepo(t *testing.T) *testutil.Repo {
	t.Helper()
	repo := testutil.NewRepo(t)
	for i := 0; i < 2000; i++ {
		repo.WriteFile(fmt.Sprintf("f%04d.txt", i), "x")
	}
	return repo
}

// The regression: sync ran on a tea.Cmd goroutine and held ws.mu for its whole
// body — including newFSWatcher, which forks git twice and walks the entire
// worktree. View() takes that same mutex on every frame (hasAny), so rendering
// stalled behind the walk: ~30ms per worktree, ~600ms for a whole fleet.
func TestFleetWatchSyncKeepsSetReadableDuringWalk(t *testing.T) {
	repo := bigRepo(t)
	ws := newTestWatchSet()
	defer ws.Close()
	entries := []fleetEntryJSON{{Path: repo.Dir, Current: true}}

	done := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		ws.sync(context.Background(), entries)
		done <- time.Since(start)
	}()

	// What matters is the WORST read, not how many got through: with the lock
	// held across the walk, a reader still completes thousands of fast reads
	// before sync manages to take the lock — and then exactly one read blocks
	// for the entire walk. That single stalled read IS the dropped frame.
	reads := 0
	var worst time.Duration
	for {
		select {
		case took := <-done:
			if took < 3*time.Millisecond {
				t.Skipf("watcher setup took only %v — too fast to prove the lock is free", took)
			}
			if reads == 0 {
				t.Fatal("no reads observed — the probe never ran alongside sync")
			}
			// Generous: a render may legitimately wait for the short locked
			// phases. It may not wait for the walk. (Measured: ~0.3ms free vs
			// ~36ms — the whole walk — when the lock was held across it.)
			if limit := took / 4; worst > limit {
				t.Fatalf("a hasAny() read blocked %v of a %v walk (limit %v) — View() is stalled behind the filesystem walk",
					worst, took, limit)
			}
			t.Logf("sync %v · %d concurrent hasAny() reads · worst %v", took, reads, worst)
			return
		default:
			start := time.Now()
			ws.hasAny() // exactly what View() calls on every frame
			if d := time.Since(start); d > worst {
				worst = d
			}
			reads++
		}
	}
}

// Releasing the lock during the walk means two syncs can race to grant the same
// worktree. The loser's watcher must be closed, not installed twice or leaked —
// it holds one descriptor per file.
func TestFleetWatchSyncConcurrentGrantsDedupe(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "x")
	ws := newTestWatchSet()
	defer ws.Close()
	entries := []fleetEntryJSON{{Path: repo.Dir, Current: true}}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ws.sync(context.Background(), entries)
		}()
	}
	wg.Wait()

	ws.mu.Lock()
	n := len(ws.watchers)
	ws.mu.Unlock()
	if n != 1 {
		t.Fatalf("one worktree must hold exactly one watcher, got %d", n)
	}
}

func TestFleetWatchForwardRemovesStoppedWatcher(t *testing.T) {
	fw := &fsWatcher{events: make(chan struct{})}
	ws := newTestWatchSet()
	ws.watchers["/w"] = fw
	ws.health = fleetWatchHealth{Eligible: 1}
	finished := make(chan struct{})
	go func() {
		ws.forward("/w", fw)
		close(finished)
	}()
	close(fw.events) // loop closes this after an EMFILE/ENFILE fallback
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("forward did not release a stopped watcher")
	}
	if ws.hasWatcher("/w") || ws.hasAny() {
		t.Fatal("stopped watcher must not remain live in the set")
	}
	health := ws.snapshotHealth()
	if health.Watched != 0 || !health.PollingFallback {
		t.Fatalf("health after watcher stop = %+v, want watched=0 polling fallback", health)
	}
	plan := planFleetWatchers([]fleetEntryJSON{{Path: "/w", Current: true}}, map[string]bool{}, nil, nil, time.Now())
	if len(plan.grant) != 1 || plan.grant[0] != "/w" {
		t.Fatalf("next sync must reacquire stopped watcher, grant=%v", plan.grant)
	}
}

// A newly active worktree must not be added beside an old full-budget watcher.
// sync retires and rebuilds the equal split, then retires the obsolete watcher
// again when the fleet shrinks.
func TestFleetWatchSyncRebalancesAndRetiresInactive(t *testing.T) {
	first := testutil.NewRepo(t)
	second := testutil.NewRepo(t)
	first.WriteFile("a.txt", "x")
	second.WriteFile("b.txt", "x")
	ws := newTestWatchSet()
	defer ws.Close()

	ws.sync(context.Background(), []fleetEntryJSON{{Path: first.Dir, Current: true}})
	ws.sync(context.Background(), []fleetEntryJSON{
		{Path: first.Dir, Current: true},
		{Path: second.Dir, Current: true},
	})

	ws.mu.Lock()
	if got := len(ws.watchers); got != 2 {
		ws.mu.Unlock()
		t.Fatalf("watchers after rebalance = %d, want 2", got)
	}
	reserved := 0
	for _, fw := range ws.watchers {
		reserved += fw.costCap
	}
	ws.mu.Unlock()
	if reserved > fsWatchCostBudget() {
		t.Fatalf("reservations = %d, budget = %d", reserved, fsWatchCostBudget())
	}

	ws.sync(context.Background(), []fleetEntryJSON{{Path: first.Dir, Current: true}})
	if ws.hasWatcher(second.Dir) {
		t.Fatal("inactive watcher must be retired on sync")
	}
}

// The dashboard can quit while sync is still walking. A watcher that finishes
// after Close must not be installed into a closed set (nor leaked).
func TestFleetWatchSyncRacingCloseInstallsNothing(t *testing.T) {
	repo := bigRepo(t)
	ws := newTestWatchSet()
	entries := []fleetEntryJSON{{Path: repo.Dir, Current: true}}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ws.sync(context.Background(), entries)
	}()
	ws.Close() // lands during the walk
	wg.Wait()

	if ws.hasAny() {
		t.Fatal("a closed watch set must not gain a watcher from an in-flight sync")
	}
}
