package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

// TestFleetLayoutUsesTerminalWidth locks the wide-terminal behaviour: the frame
// spans the real width (it used to collapse back to 80 columns above 120), and
// the slack goes to the elastic columns — a long path renders whole instead of
// being clipped to `ContentView.s…` with half the screen empty beside it.
func TestFleetLayoutUsesTerminalWidth(t *testing.T) {
	if got := fleetWidth(0); got != fleetDefaultWidth {
		t.Errorf("fleetWidth(0) = %d, want %d", got, fleetDefaultWidth)
	}
	if got := fleetWidth(20); got != fleetMinWidth {
		t.Errorf("fleetWidth(20) = %d, want floor %d", got, fleetMinWidth)
	}
	if got := fleetWidth(166); got != 166 {
		t.Errorf("fleetWidth(166) = %d, want 166 (no 120→80 fallback)", got)
	}

	narrow := fleetColumns(80, fleetIndentGrouped)
	wide := fleetColumns(166, fleetIndentGrouped)
	if wide.branch <= narrow.branch || wide.file <= narrow.file {
		t.Errorf("columns did not grow with width: 80=%+v 166=%+v", narrow, wide)
	}
	if huge := fleetColumns(400, fleetIndentGrouped); huge.branch != wide.branch || huge.file != wide.file {
		t.Errorf("columns unbounded at 400 cols: %+v", huge)
	}

	const path = "app/Sources/SpaceMeshApp/ContentView.swift"
	if got := fleetLastChangeLabel(path, wide.file); got != path {
		t.Errorf("wide file column = %q, want the full path", got)
	}
	// Narrow: the basename, as before — no room for directories.
	if got := fleetLastChangeLabel(path, 14); got != "ContentView.s…" {
		t.Errorf("narrow file column = %q, want the clipped basename", got)
	}
	// Mid: the basename fits but the path doesn't — keep the identifying tail.
	if got := fleetLastChangeLabel(path, 30); !strings.HasSuffix(got, "ContentView.swift") || !strings.HasPrefix(got, "…") {
		t.Errorf("mid file column = %q, want a left-clipped tail", got)
	}

	now := time.Date(2026, 6, 23, 15, 4, 5, 0, time.UTC)
	rows := buildFleetRows([]fleetEntryJSON{
		{Repo: "space-mesh", RepoRoot: "/r/sm", Path: "/r/sm", Branch: "phase2-rewire",
			Status: "dirty", Dirty: &contextDirtyJSON{Unstaged: 13}, LastChange: path, lastActive: now},
	}, map[string]bool{})
	out := renderFleetGrouped(rows, 0, now, 166, fleetDetailOff, nil, 1, 1, fleetChurn{}, 0)
	if !strings.Contains(out, path) {
		t.Errorf("grouped render at 166 cols clipped the path:\n%s", out)
	}
	if w := lipgloss.Width(out); w < 160 {
		t.Errorf("grouped render width = %d, want the frame to span the terminal", w)
	}
}

// TestFleetChurn covers the session-volume accumulator: a worktree's first poll
// is a baseline (whatever was already dirty did not happen on our watch), a
// re-touch counts only the increment, and a commit — which resets the counts
// against HEAD — contributes nothing rather than going negative.
func TestFleetChurn(t *testing.T) {
	const wt = "/r/wt"
	entry := func(sigs map[string]fileSig) []fleetEntryJSON {
		return []fleetEntryJSON{{Path: wt, sigs: sigs}}
	}
	var c fleetChurn
	prev := map[string]map[string]fileSig{}

	// Poll 1: first sight of the worktree — baseline, no churn.
	first := map[string]fileSig{"a.go": {xy: " M", added: 10, removed: 2}}
	c.accumulate(prev, entry(first))
	if c.any() {
		t.Errorf("baseline poll produced churn: %+v", c)
	}
	prev[wt] = first

	// Poll 2: a.go grows by 5/1, b.go appears with 7 — churn is the increment.
	second := map[string]fileSig{
		"a.go": {xy: " M", added: 15, removed: 3},
		"b.go": {xy: "??", added: 7},
	}
	c.accumulate(prev, entry(second))
	if c.added != 5+7 || c.removed != 1 {
		t.Errorf("churn = +%d −%d, want +12 −1", c.added, c.removed)
	}
	if c.touched() != 2 {
		t.Errorf("touched = %d, want 2 (a.go, b.go)", c.touched())
	}
	prev[wt] = second

	// Poll 3: the agent commits — sigs go empty. Nothing negative, nothing new.
	c.accumulate(prev, entry(map[string]fileSig{}))
	if c.added != 12 || c.removed != 1 || c.touched() != 2 {
		t.Errorf("a commit changed the churn: %+v", c)
	}
	prev[wt] = map[string]fileSig{}

	// Poll 4: fresh work after the commit counts in full (baseline is HEAD again).
	c.accumulate(prev, entry(map[string]fileSig{"c.go": {xy: " M", added: 4}}))
	if c.added != 16 || c.touched() != 3 {
		t.Errorf("post-commit work = %+v, want +16 over 3 files", c)
	}
}

// TestFleetVolumeHeadline: the header carries both readings — the uncommitted
// diffstat and the session Δ — and drops Δ before the clock when the terminal
// is too narrow to hold both.
func TestFleetVolumeHeadline(t *testing.T) {
	now := time.Date(2026, 7, 13, 8, 22, 59, 0, time.UTC)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	entries := []fleetEntryJSON{
		{Branch: "develop", Status: "dirty", Files: 7, Added: 31, Removed: 3},
		{Branch: "phase2", Status: "dirty", Files: 27, Added: 12890, Removed: 261, Ahead: 2},
	}
	churn := fleetChurn{added: 3142, removed: 210, files: map[string]bool{"a": true, "b": true}}

	// Wide: uncommitted (~ 34 files +…), unpushed (↑2), the │ divider, then Δ.
	// The Δ reading no longer carries "over N files" — it is +/− and elapsed.
	wide := renderFleetHeadline("2 worktrees", entries, now, 166, dim, churn, 17*time.Minute)
	for _, want := range []string{"~ 34 files", "+12,921", "−264", "↑2", "unpushed", "│", "Δ", "+3,142", "17m", "08:22:59"} {
		if !strings.Contains(wide, want) {
			t.Errorf("headline missing %q in:\n%s", want, wide)
		}
	}
	if strings.Contains(wide, "over") {
		t.Errorf("Δ should no longer print 'over N files':\n%s", wide)
	}

	// Narrow: Δ (flow) drops first, then the pending segments; the count never.
	narrow := renderFleetHeadline("2 worktrees", entries, now, 80, dim, churn, 17*time.Minute)
	if strings.Contains(narrow, "Δ") {
		t.Errorf("narrow headline kept Δ:\n%s", narrow)
	}
	for _, want := range []string{"2 worktrees", "34 files", "08:22:59"} {
		if !strings.Contains(narrow, want) {
			t.Errorf("narrow headline missing %q in:\n%s", want, narrow)
		}
	}

	// Δ present but nothing pending: the divider must not appear (it only
	// separates pending work from flow, and there is no pending work here).
	flowOnly := renderFleetHeadline("2 worktrees", nil, now, 166, dim, churn, 17*time.Minute)
	if strings.Contains(flowOnly, "│") {
		t.Errorf("divider drawn with no pending segments:\n%s", flowOnly)
	}
	if !strings.Contains(flowOnly, "Δ") {
		t.Errorf("flow-only headline dropped Δ:\n%s", flowOnly)
	}

	// Pending present but no churn yet: still no divider (nothing to divide
	// from), and the unpushed reading stands alone.
	pendingOnly := renderFleetHeadline("2 worktrees", entries, now, 166, dim, fleetChurn{}, 0)
	if strings.Contains(pendingOnly, "│") {
		t.Errorf("divider drawn with no flow segment:\n%s", pendingOnly)
	}
	if !strings.Contains(pendingOnly, "↑2") {
		t.Errorf("pending-only headline dropped the unpushed reading:\n%s", pendingOnly)
	}

	// Nothing dirty and nothing seen yet: just the count and the clock.
	idle := renderFleetHeadline("2 worktrees", nil, now, 166, dim, fleetChurn{}, 0)
	if strings.Contains(idle, "files") || strings.Contains(idle, "Δ") {
		t.Errorf("idle headline should carry no volume:\n%s", idle)
	}
}

// TestFleetStatColumn: the row diffstat column exists when there are counts to
// put in it, and yields (rather than printing a column of `·`) when the counts
// were never gathered — `--feed-stats=false`.
func TestFleetStatColumn(t *testing.T) {
	withStats := []fleetEntryJSON{{Branch: "develop", Files: 7, Added: 31, Removed: 3}}
	noStats := []fleetEntryJSON{{Branch: "develop", Files: 7}}

	if cols := fleetStatCols(166, fleetIndentFlat, withStats); cols.stat == 0 {
		t.Error("stat column dropped even though the entries carry counts")
	}
	if cols := fleetStatCols(166, fleetIndentFlat, noStats); cols.stat != 0 {
		t.Error("stat column kept with no counts to show (--feed-stats=false)")
	}
	if cols := fleetColumns(80, fleetIndentGrouped); cols.stat != 0 {
		t.Errorf("stat column claimed %d cols on an 80-col terminal", cols.stat)
	}

	now := time.Date(2026, 7, 13, 8, 22, 59, 0, time.UTC)
	out := renderFleetTable(withStats, 0, now, fleetStatCols(166, fleetIndentFlat, withStats))
	if !strings.Contains(out, "+31") || !strings.Contains(out, "−3") {
		t.Errorf("row missing its diffstat:\n%s", out)
	}
}

// TestFleetFeedPaneFillsHeight: the feed tail takes whatever height the
// dashboard leaves (the pane was capped at 8 lines while the rest of the
// terminal sat blank), and still yields entirely when there is no room.
func TestFleetFeedPaneFillsHeight(t *testing.T) {
	m := fleetModel{height: 48}
	if got := m.feedPaneLines(6); got != 48-6-3 {
		t.Errorf("feedPaneLines(6) = %d, want the remaining height %d", got, 48-6-3)
	}
	if got := m.feedPaneLines(46); got != 0 {
		t.Errorf("feedPaneLines(46) = %d, want the pane dropped", got)
	}
	if got := (fleetModel{}).feedPaneLines(6); got != 8 {
		t.Errorf("unknown height = %d, want the static default 8", got)
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
	// An EMPTY set (all-idle fleet) must keep the configured interval — the
	// heartbeat only makes sense when something actually feeds events.
	empty := &fleetWatchSet{watchers: map[string]*fsWatcher{}}
	if got := fleetTickInterval(2*time.Second, empty); got != 2*time.Second {
		t.Errorf("empty set: %v, want 2s (keep polling)", got)
	}
	ws := &fleetWatchSet{watchers: map[string]*fsWatcher{"/w": nil}}
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

	busy := fleetFilterEntries(entries, fleetFilterBusy, time.Time{})
	if len(busy) != 3 {
		t.Errorf("busy filter = %d entries, want 3 (dirty/paused/error): %+v", len(busy), busy)
	}
	stuck := fleetFilterEntries(entries, fleetFilterStuck, time.Time{})
	if len(stuck) != 2 {
		t.Errorf("stuck filter = %d entries, want 2 (paused/error): %+v", len(stuck), stuck)
	}
	if got := fleetFilterEntries(entries, fleetFilterAll, time.Time{}); len(got) != 4 {
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

// --- fs-triggered poll rate limit -------------------------------------------
//
// Backpressure (one poll in flight) does not bound the poll RATE: before
// fsPollGap, a worktree under continuous churn re-triggered a full fleet scan
// the instant the previous one landed. On a 21-worktree fleet — where one poll
// costs ~1.8 CPU-seconds — that pinned the process at >100% CPU indefinitely.

func fsRateModel(interval time.Duration, sinceLastPoll time.Duration) fleetModel {
	return fleetModel{
		interval:      interval,
		lastPollStart: time.Now().Add(-sinceLastPoll),
	}
}

func TestFleetFSEventPollsImmediatelyWhenGapElapsed(t *testing.T) {
	m := fsRateModel(5*time.Second, 10*time.Second) // last poll long past
	got, cmd := m.Update(fleetFSMsg{path: "/w"})
	fm := got.(fleetModel)
	if !fm.polling {
		t.Fatal("an event after the gap must poll immediately (fs latency is the feature)")
	}
	if fm.deferred {
		t.Error("deferred should stay false when the poll ran now")
	}
	if cmd == nil {
		t.Error("expected a poll command")
	}
}

func TestFleetFSEventInsideGapDefersInsteadOfPolling(t *testing.T) {
	m := fsRateModel(5*time.Second, 1*time.Second) // 4s of the gap still to run
	got, cmd := m.Update(fleetFSMsg{path: "/w"})
	fm := got.(fleetModel)
	if fm.polling {
		t.Fatal("an event inside the rate-limit window must NOT start a poll")
	}
	if !fm.deferred {
		t.Fatal("the event must be queued (deferred), never dropped")
	}
	if cmd == nil {
		t.Error("expected the cooldown timer command")
	}
}

func TestFleetFSEventWhileDeferredCoalesces(t *testing.T) {
	m := fsRateModel(5*time.Second, 1*time.Second)
	m.deferred = true
	got, _ := m.Update(fleetFSMsg{path: "/w"})
	fm := got.(fleetModel)
	if fm.polling {
		t.Error("a second event must not jump the queued poll")
	}
	if !fm.deferred {
		t.Error("still exactly one queued poll")
	}
}

// The regression that mattered: a poll landing with fsPending set used to
// re-poll with zero cooldown, chaining full scans back to back.
func TestFleetPendingEventRepollsThroughRateLimit(t *testing.T) {
	m := fsRateModel(5*time.Second, 0) // this poll started just now
	m.polling, m.fsPending = true, true
	got, _ := m.Update(fleetDataMsg{})
	fm := got.(fleetModel)
	if fm.polling {
		t.Fatal("re-poll must wait out the gap, not start immediately (this was the 116% CPU bug)")
	}
	if !fm.deferred {
		t.Fatal("the pending event must still be honoured — queued, not dropped")
	}
	if fm.fsPending {
		t.Error("fsPending consumed into the queued poll")
	}
}

func TestFleetPendingEventPollsNowWhenGapAlreadyElapsed(t *testing.T) {
	m := fsRateModel(5*time.Second, 9*time.Second) // slow poll outran the gap
	m.polling, m.fsPending = true, true
	got, _ := m.Update(fleetDataMsg{})
	fm := got.(fleetModel)
	if !fm.polling {
		t.Fatal("a poll slower than the gap owes no further wait")
	}
}

func TestFleetManualRefreshOverridesRateLimit(t *testing.T) {
	m := fsRateModel(5*time.Second, 0)
	m.deferred = true
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	fm := got.(fleetModel)
	if !fm.polling {
		t.Fatal("a keypress is not the runaway loop the limit exists to stop")
	}
	if fm.deferred {
		t.Error("the armed cooldown must be retired, not left to fire a second poll")
	}
}

func TestFleetStaleCooldownIgnored(t *testing.T) {
	m := fsRateModel(5*time.Second, 10*time.Second)
	m.tickSeq = 3
	got, cmd := m.Update(fleetCooldownMsg{seq: 2}) // superseded generation
	fm := got.(fleetModel)
	if fm.polling || cmd != nil {
		t.Error("a cooldown from a superseded chain must not poll")
	}
}
