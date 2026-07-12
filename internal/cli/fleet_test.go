package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestFleetStatus(t *testing.T) {
	cases := []struct {
		name string
		e    fleetEntryJSON
		want string
	}{
		{"clean", fleetEntryJSON{}, "clean"},
		{"paused beats conflict", fleetEntryJSON{Operation: "rebase 2/5", Dirty: &contextDirtyJSON{Conflicts: 2}}, "paused"},
		{"conflict beats dirty", fleetEntryJSON{Dirty: &contextDirtyJSON{Unstaged: 3, Conflicts: 1}}, "conflict"},
		{"dirty untracked", fleetEntryJSON{Dirty: &contextDirtyJSON{Untracked: 1}}, "dirty"},
		{"ahead", fleetEntryJSON{Ahead: 2}, "ahead"},
		{"behind", fleetEntryJSON{Behind: 1}, "behind"},
		{"diverged", fleetEntryJSON{Ahead: 1, Behind: 1}, "diverged"},
		{"dirty beats ahead", fleetEntryJSON{Ahead: 2, Dirty: &contextDirtyJSON{Staged: 1}}, "dirty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fleetStatus(tc.e); got != tc.want {
				t.Errorf("fleetStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFleetLabels(t *testing.T) {
	if got := fleetDiffLabel(2, 1); got != "↑2 ↓1" {
		t.Errorf("diff(2,1) = %q", got)
	}
	if got := fleetDiffLabel(3, 0); got != "↑3" {
		t.Errorf("diff(3,0) = %q", got)
	}
	if got := fleetDiffLabel(0, 0); got != "·" {
		t.Errorf("diff(0,0) = %q, want ·", got)
	}
	if got := fleetDirtyLabel(nil); got != "·" {
		t.Errorf("dirty(nil) = %q, want ·", got)
	}
	if got := fleetDirtyLabel(&contextDirtyJSON{Staged: 1, Untracked: 2}); got != "S1 ?2" {
		t.Errorf("dirty = %q, want 'S1 ?2'", got)
	}
	if got := fleetDirtyLabel(&contextDirtyJSON{Conflicts: 1}); got != "✗1" {
		t.Errorf("dirty conflict = %q", got)
	}
}

func TestFleetOperationLabel(t *testing.T) {
	cases := []struct {
		name  string
		state gitstate.State
		want  string
	}{
		{"rebase with progress", gitstate.State{Kind: gitstate.StateRebaseMerge, Current: 2, Total: 5}, "rebase 2/5"},
		{"rebase apply", gitstate.State{Kind: gitstate.StateRebaseApply, Current: 1, Total: 3}, "rebase 1/3"},
		{"rebase no progress", gitstate.State{Kind: gitstate.StateRebaseMerge}, "rebase"},
		{"merge", gitstate.State{Kind: gitstate.StateMerge}, "merge"},
		{"cherry-pick", gitstate.State{Kind: gitstate.StateCherryPick}, "cherry-pick"},
		{"revert", gitstate.State{Kind: gitstate.StateRevert}, "revert"},
		{"none", gitstate.State{Kind: gitstate.StateNone}, ""},
		{"bisect", gitstate.State{Kind: gitstate.StateBisect}, "bisect"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fleetOperationLabel(&tc.state); got != tc.want {
				t.Errorf("fleetOperationLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFleetActiveLabel(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		e    fleetEntryJSON
		now  time.Time
		want string
	}{
		{"unknown", fleetEntryJSON{}, now, "·"},
		{"live now", fleetEntryJSON{lastActive: now}, now, "now"},
		{"live ticks up", fleetEntryJSON{lastActive: now.Add(-5 * time.Minute)}, now, "5m"},
		{"live hours", fleetEntryJSON{lastActive: now.Add(-3 * time.Hour)}, now, "3h"},
		// Static/JSON path: no live clock, fall back to the ActiveAgoS snapshot.
		{"snapshot minutes", fleetEntryJSON{ActiveAgoS: 120}, time.Time{}, "2m"},
		{"snapshot sub-minute reads now", fleetEntryJSON{ActiveAgoS: 0, lastActive: now}, time.Time{}, "now"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fleetActiveLabel(tc.e, tc.now); got != tc.want {
				t.Errorf("fleetActiveLabel = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSplitPorcelainZ(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []porcelainZRecord
	}{
		{"empty", "", nil},
		{"modified + untracked", " M foo.go\x00?? bar.txt\x00",
			[]porcelainZRecord{{" M", "foo.go"}, {"??", "bar.txt"}}},
		{"rename skips source", "R  new.go\x00old.go\x00",
			[]porcelainZRecord{{"R ", "new.go"}}},
		{"staged add", "A  added.go\x00", []porcelainZRecord{{"A ", "added.go"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitPorcelainZ(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("splitPorcelainZ = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("record[%d] = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestParseWorktreeScan covers the consolidated scan: dirty tallies match the
// countContextDirty rules, signatures carry every changed path, and the newest
// on-disk mtime picks the last-changed file.
func TestParseWorktreeScan(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "older.go")
	newer := filepath.Join(dir, "newer.go")
	for _, p := range []string{older, newer} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatal(err)
	}

	raw := " M older.go\x00?? newer.go\x00UU conflicted.go\x00A  staged.go\x00"
	s := parseWorktreeScan(raw, dir)

	if s.dirty.Unstaged != 1 || s.dirty.Untracked != 1 || s.dirty.Conflicts != 1 || s.dirty.Staged != 1 {
		t.Errorf("dirty = %+v, want 1 each", s.dirty)
	}
	if len(s.sigs) != 4 {
		t.Errorf("sigs = %d entries, want 4: %v", len(s.sigs), s.sigs)
	}
	if s.newestPath != "newer.go" {
		t.Errorf("newestPath = %q, want newer.go", s.newestPath)
	}
	if sig := s.sigs["older.go"]; sig.xy != " M" || sig.mtime == 0 {
		t.Errorf("older.go sig = %+v, want xy ' M' with mtime", sig)
	}
	// Paths missing on disk (e.g. staged delete) still get a signature, with a
	// zero mtime — they must not win the newest-change slot.
	if sig := s.sigs["conflicted.go"]; sig.mtime != 0 {
		t.Errorf("conflicted.go sig mtime = %d, want 0 (not on disk)", sig.mtime)
	}
}

// TestFleetRenderDetail smoke-tests the live TUI layout: the header carries the
// wall-clock, the cursor row gets the master-detail panel with its enrichment
// fields, and a paused worktree shows the ⏸ marker in the table.
func TestFleetRenderDetail(t *testing.T) {
	now := time.Date(2026, 6, 23, 15, 4, 5, 0, time.UTC)
	entries := []fleetEntryJSON{
		{Branch: "develop", Current: true, Ahead: 13, Status: "ahead", lastActive: now.Add(-2 * time.Minute)},
		{Branch: "feat/auth", Ahead: 3, Behind: 1, Dirty: &contextDirtyJSON{Staged: 2, Unstaged: 1},
			Status: "dirty", Parent: "develop", ParentBehind: 1, lastActive: now},
		{Branch: "fix/race", Operation: "rebase 2/5", Resume: "gk continue",
			Dirty: &contextDirtyJSON{Conflicts: 2}, Status: "paused", lastActive: now.Add(-18 * time.Minute)},
	}

	// Cursor on the dirty branch: panel shows its parent/land fields; the paused
	// worktree still flags ⏸ in the table.
	out := renderFleet(fleetView{entries: entries, cursor: 1, now: now, width: 100, detail: fleetDetailFields})
	for _, want := range []string{
		"15:04:05",    // live wall-clock in the header
		"feat/auth",   // cursor row title in the detail panel
		"parent",      // detail field
		"develop  ↓1", // parent + behind
		"land",        // land-readiness field
		"⏸",           // paused marker shows in the table
		"just now",    // active detail for the cursor row (lastActive == now)
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}

	// Cursor on the paused worktree: the panel carries the op label + resume.
	paused := renderFleet(fleetView{entries: entries, cursor: 2, now: now, width: 100, detail: fleetDetailFields})
	for _, want := range []string{"rebase 2/5", "gk continue", "18m ago"} {
		if !strings.Contains(paused, want) {
			t.Errorf("paused render missing %q in:\n%s", want, paused)
		}
	}
}

// TestFleetRenderDetailFeed covers the detail panel's live-feed mode: only the
// cursor worktree's events appear (with stats/notes), and an event-less
// worktree shows the waiting placeholder instead.
func TestFleetRenderDetailFeed(t *testing.T) {
	now := time.Date(2026, 6, 23, 15, 4, 5, 0, time.UTC)
	entries := []fleetEntryJSON{
		{Path: "/wt/a", Branch: "feat/auth", Dirty: &contextDirtyJSON{Unstaged: 2}, Status: "dirty", lastActive: now},
		{Path: "/wt/b", Branch: "fix/race", Status: "clean"},
	}
	feed := []fleetFeedEvent{
		{ts: now.Add(-30 * time.Second), wt: "/wt/a", path: "internal/auth/auth.go", glyph: "~", added: 12, removed: 3, symbols: "validateToken"},
		{ts: now.Add(-10 * time.Second), wt: "/wt/a", path: "internal/auth/auth_test.go", glyph: "+", note: "new"},
		{ts: now.Add(-5 * time.Second), wt: "/wt/b", path: "other.go", glyph: "~"},
	}

	out := renderFleet(fleetView{entries: entries, cursor: 0, now: now, width: 100, detail: fleetDetailFeed, feed: feed})
	for _, want := range []string{
		"feat/auth",     // panel title
		"auth.go",       // this worktree's event
		"auth_test.go",  // and the second one
		"validateToken", // changed-function name on the event line
		"+12",           // stats on the event line (watch-style green/red ± form)
		"−3",
		"new", // note marker
	} {
		if !strings.Contains(out, want) {
			t.Errorf("feed panel missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "other.go") {
		t.Errorf("feed panel leaked another worktree's event:\n%s", out)
	}

	// No events for the cursor worktree → placeholder, not an empty box.
	quiet := renderFleet(fleetView{entries: entries, cursor: 1, now: now, width: 100, detail: fleetDetailFeed, feed: feed[:2]})
	if !strings.Contains(quiet, "no changes yet") {
		t.Errorf("quiet feed panel missing placeholder in:\n%s", quiet)
	}
}

func TestFleetCommandRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"fleet"})
	if err != nil {
		t.Fatalf("find fleet: %v", err)
	}
	if cmd.Name() != "fleet" {
		t.Errorf("resolved to %q, want fleet", cmd.Name())
	}
}

// TestGatherFleet exercises the real worktree-enrichment path against a temp
// repo: a clean repo reports one current worktree with status "clean"; after an
// untracked write the status rolls up to "dirty".
func TestGatherFleet(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	entries, err := gatherFleet(ctx, runner, false)
	if err != nil {
		t.Fatalf("gatherFleet: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(entries), entries)
	}
	if entries[0].Branch == "" {
		t.Errorf("entry branch is empty")
	}
	if !entries[0].Current {
		t.Errorf("the only worktree should be current")
	}
	if entries[0].Status != "clean" {
		t.Errorf("clean repo status = %q, want clean", entries[0].Status)
	}
	// Activity is anchored on the HEAD commit, so a just-committed worktree has
	// a non-zero lastActive (the live TUI re-derives the age from it each tick).
	if entries[0].lastActive.IsZero() {
		t.Errorf("lastActive should be set from the HEAD commit time")
	}

	repo.WriteFile("b.txt", "b") // untracked
	entries, err = gatherFleet(ctx, runner, false)
	if err != nil {
		t.Fatalf("gatherFleet (dirty): %v", err)
	}
	if entries[0].Status != "dirty" {
		t.Errorf("after untracked write status = %q, want dirty", entries[0].Status)
	}
	if entries[0].LastChange != "b.txt" {
		t.Errorf("LastChange = %q, want b.txt", entries[0].LastChange)
	}
	if len(entries[0].sigs) != 1 {
		t.Errorf("sigs = %v, want the one untracked path", entries[0].sigs)
	}
}

// TestApplyFeedDiff covers the fleet-wide feed accumulation: the first scan is
// a silent baseline, later polls emit events, cleared files close out, and a
// vanished worktree drops from the signature state.
func TestApplyFeedDiff(t *testing.T) {
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	wt := func(path, branch string, sigs map[string]fileSig) fleetEntryJSON {
		return fleetEntryJSON{Path: path, Branch: branch, sigs: sigs}
	}

	// Poll 1: baseline — already-dirty files produce NO events.
	feed, state := applyFeedDiff(map[string]map[string]fileSig{},
		[]fleetEntryJSON{wt("/a", "feat/x", map[string]fileSig{"f.go": {xy: " M", mtime: 1}})},
		nil, now)
	if len(feed) != 0 {
		t.Fatalf("baseline emitted events: %+v", feed)
	}

	// Poll 2: f.go re-touched, g.go new — two events tagged with the branch.
	feed, state = applyFeedDiff(state,
		[]fleetEntryJSON{wt("/a", "feat/x", map[string]fileSig{
			"f.go": {xy: " M", mtime: 2},
			"g.go": {xy: "??", mtime: 2},
		})}, feed, now.Add(time.Second))
	if len(feed) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(feed), feed)
	}
	for _, ev := range feed {
		if ev.branch != "feat/x" {
			t.Errorf("event branch = %q, want feat/x", ev.branch)
		}
	}

	// Poll 3: worktree went clean — both files clear; a second worktree with a
	// nil scan (error entry) must not fabricate a baseline reset.
	feed, state = applyFeedDiff(state,
		[]fleetEntryJSON{
			wt("/a", "feat/x", map[string]fileSig{}),
			{Path: "/b", Branch: "feat/y", Status: "error"},
		}, feed, now.Add(2*time.Second))
	cleared := 0
	for _, ev := range feed {
		if ev.cleared {
			cleared++
		}
	}
	if cleared != 2 {
		t.Errorf("cleared events = %d, want 2 (feed: %+v)", cleared, feed)
	}
	if _, ok := state["/b"]; ok {
		t.Errorf("error entry must not enter signature state")
	}

	// Poll 4: worktree /a vanished from the fleet — state drops it.
	_, state = applyFeedDiff(state, nil, feed, now.Add(3*time.Second))
	if len(state) != 0 {
		t.Errorf("state should be empty after all worktrees vanish: %v", state)
	}
}

// TestFleetWatchBudget: N active watchers split the process-wide watch
// budget (fd-aware on kqueue platforms), with a floor so a crowded fleet
// still gets usable watchers.
func TestFleetWatchBudget(t *testing.T) {
	total := fsWatchCostBudget()
	if got := fleetWatchBudget(1); got != total {
		t.Errorf("budget(1) = %d, want %d", got, total)
	}
	if got := fleetWatchBudget(4); got != total/4 {
		t.Errorf("budget(4) = %d, want %d", got, total/4)
	}
	if got := fleetWatchBudget(10 * total); got != 64 {
		t.Errorf("budget(huge N) = %d, want the 64 floor", got)
	}
	if got := fleetWatchBudget(0); got != total {
		t.Errorf("budget(0) = %d, want %d", got, total)
	}
}

// TestFleetTickInterval: fsnotify presence demotes the poll to the heartbeat;
// an interval slower than the heartbeat is respected.
func TestFleetTickInterval(t *testing.T) {
	if got := fleetTickInterval(2*time.Second, nil); got != 2*time.Second {
		t.Errorf("no watcher: %v, want 2s", got)
	}
	ws := &fleetWatchSet{}
	if got := fleetTickInterval(2*time.Second, ws); got != fsHeartbeatInterval {
		t.Errorf("watcher active: %v, want heartbeat %v", got, fsHeartbeatInterval)
	}
	if got := fleetTickInterval(30*time.Second, ws); got != 30*time.Second {
		t.Errorf("slow interval: %v, want 30s (respected)", got)
	}
}

// TestFleetViewFilterSort covers the 'f'/'s' view controls: busy keeps working
// trees, stuck keeps blocked ones, activity sorts most-recent first, status
// sorts most-urgent first — all without touching the underlying order.
func TestFleetViewFilterSort(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	entries := []fleetEntryJSON{
		{Path: "/clean", Branch: "a", Status: "clean", lastActive: now.Add(-3 * time.Hour)},
		{Path: "/dirty", Branch: "b", Status: "dirty", lastActive: now},
		{Path: "/paused", Branch: "c", Status: "paused", lastActive: now.Add(-time.Hour)},
		{Path: "/err", Branch: "", Status: "error"},
	}

	busy := fleetFilterEntries(entries, fleetFilterBusy)
	if len(busy) != 3 {
		t.Errorf("busy filter = %d entries, want 3 (dirty/paused/error): %+v", len(busy), busy)
	}
	stuck := fleetFilterEntries(entries, fleetFilterStuck)
	if len(stuck) != 2 {
		t.Errorf("stuck filter = %d entries, want 2 (paused/error): %+v", len(stuck), stuck)
	}
	if got := fleetFilterEntries(entries, fleetFilterAll); len(got) != 4 {
		t.Errorf("all filter must keep everything")
	}

	byActivity := fleetSortEntries(entries, fleetSortActivity)
	if byActivity[0].Path != "/dirty" {
		t.Errorf("activity sort first = %s, want /dirty (most recent)", byActivity[0].Path)
	}
	byStatus := fleetSortEntries(entries, fleetSortStatus)
	if byStatus[0].Status != "error" || byStatus[1].Status != "paused" {
		t.Errorf("status sort = %v, want error,paused first", []string{byStatus[0].Status, byStatus[1].Status})
	}
	// The input slice order must be untouched (sort works on a copy).
	if entries[0].Path != "/clean" {
		t.Errorf("original slice was reordered")
	}
}

// TestFleetEventTail: newest N events for one worktree, oldest-first.
func TestFleetEventTail(t *testing.T) {
	feed := []fleetFeedEvent{
		{wt: "/a", path: "1.go"},
		{wt: "/b", path: "x.go"},
		{wt: "/a", path: "2.go"},
		{wt: "/a", path: "3.go"},
		{wt: "/a", path: "4.go"},
	}
	tail := fleetEventTail(feed, "/a", 3)
	if len(tail) != 3 || tail[0].path != "2.go" || tail[2].path != "4.go" {
		t.Errorf("tail = %+v, want 2.go..4.go oldest-first", tail)
	}
	if got := fleetEventTail(feed, "/none", 3); len(got) != 0 {
		t.Errorf("unknown worktree should yield no tail: %+v", got)
	}
}

// TestFleetTransitions covers the --events state diff: status flips, op
// start/end, land-ready edges; new worktrees and error entries stay silent.
func TestFleetTransitions(t *testing.T) {
	ts := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	prev := []fleetEntryJSON{
		{Path: "/a", Branch: "feat/x", Status: "clean"},
		{Path: "/b", Branch: "feat/y", Status: "dirty"},
		{Path: "/c", Branch: "feat/z", Status: "paused", Operation: "rebase 1/3"},
	}
	curr := []fleetEntryJSON{
		{Path: "/a", Branch: "feat/x", Status: "conflict"},               // status flip
		{Path: "/b", Branch: "feat/y", Status: "dirty", LandReady: true}, // land-ready edge
		{Path: "/c", Branch: "feat/z", Status: "clean"},                  // op ended
		{Path: "/d", Branch: "feat/new", Status: "dirty"},                // new worktree: silent
		{Path: "/e", Branch: "", Status: "error", Error: "timeout"},      // error: silent
	}
	evs := fleetTransitions(prev, curr, ts)
	kinds := map[string]fleetStreamEvent{}
	for _, ev := range evs {
		kinds[ev.Kind+":"+ev.Path] = ev
	}
	if ev, ok := kinds["status-changed:/a"]; !ok || ev.From != "clean" || ev.To != "conflict" {
		t.Errorf("missing/wrong status-changed for /a: %+v", evs)
	}
	if _, ok := kinds["land-ready:/b"]; !ok {
		t.Errorf("missing land-ready for /b: %+v", evs)
	}
	if ev, ok := kinds["op-end:/c"]; !ok || ev.Operation != "rebase 1/3" {
		t.Errorf("missing/wrong op-end for /c: %+v", evs)
	}
	// /c also flips paused→clean — that's a legitimate second event.
	for _, ev := range evs {
		if ev.Path == "/d" || ev.Path == "/e" {
			t.Errorf("baseline/error worktree emitted an event: %+v", ev)
		}
	}
}

// TestFleetFeedRingCap: the feed never grows past fleetFeedCap.
func TestFleetFeedRingCap(t *testing.T) {
	feed := make([]fleetFeedEvent, 0, fleetFeedCap)
	for i := 0; i < fleetFeedCap; i++ {
		feed = append(feed, fleetFeedEvent{path: "old"})
	}
	prev := map[string]map[string]fileSig{"/a": {"f.go": {xy: " M", mtime: 1}}}
	entries := []fleetEntryJSON{{Path: "/a", Branch: "b", sigs: map[string]fileSig{"f.go": {xy: " M", mtime: 2}}}}
	feed, _ = applyFeedDiff(prev, entries, feed, time.Now())
	if len(feed) != fleetFeedCap {
		t.Errorf("feed length = %d, want capped at %d", len(feed), fleetFeedCap)
	}
	if feed[len(feed)-1].path != "f.go" {
		t.Errorf("newest event should survive the cap, got %+v", feed[len(feed)-1])
	}
	if feed[0].path != "old" && len(feed) > 0 {
		// oldest entries were dropped from the front — the first remaining
		// element is still an "old" filler unless exactly at the boundary.
		t.Logf("front of ring: %+v", feed[0])
	}
}
