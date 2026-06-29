package cli

import "testing"

func TestFleetGroupRollup(t *testing.T) {
	cases := []struct {
		name   string
		states []string
		want   string
	}{
		{"paused beats conflict", []string{"clean", "conflict", "paused"}, "paused"},
		{"conflict beats dirty", []string{"dirty", "conflict"}, "conflict"},
		{"ahead over clean", []string{"clean", "ahead"}, "ahead"},
		{"error tops all", []string{"paused", "error"}, "error"},
		{"all clean", []string{"clean", "clean"}, "clean"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := make([]fleetEntryJSON, len(tc.states))
			for i, s := range tc.states {
				g[i] = fleetEntryJSON{Status: s}
			}
			if got := fleetGroupRollup(g); got != tc.want {
				t.Errorf("fleetGroupRollup(%v) = %q, want %q", tc.states, got, tc.want)
			}
		})
	}
}

func TestBuildFleetRows(t *testing.T) {
	entries := []fleetEntryJSON{
		{Repo: "a", RepoRoot: "/a", Branch: "main", Status: "clean", Path: "/a"},
		{Repo: "a", RepoRoot: "/a", Branch: "feat", Status: "dirty", Path: "/a/wt"},
		{Repo: "b", RepoRoot: "/b", Branch: "main", Status: "paused", Path: "/b"},
	}

	rows := buildFleetRows(entries, nil)
	// header a + 2 worktrees + header b + 1 worktree = 5
	if len(rows) != 5 {
		t.Fatalf("expanded: want 5 rows, got %d (%+v)", len(rows), rows)
	}
	if !rows[0].header || rows[0].label != "a" || rows[0].count != 2 {
		t.Errorf("row 0 should be header a(2), got %+v", rows[0])
	}
	if rows[0].rollup != "dirty" {
		t.Errorf("repo a roll-up = %q, want dirty (clean+dirty)", rows[0].rollup)
	}
	if !rows[3].header || rows[3].rollup != "paused" {
		t.Errorf("row 3 should be header b/paused, got %+v", rows[3])
	}

	// collapse /a: its worktrees disappear, header remains and is marked folded.
	rows2 := buildFleetRows(entries, map[string]bool{"/a": true})
	if len(rows2) != 3 {
		t.Fatalf("collapsed: want 3 rows (header a, header b, wt b), got %d", len(rows2))
	}
	if !rows2[0].header || !rows2[0].collapsed {
		t.Errorf("row 0 should be a collapsed header, got %+v", rows2[0])
	}
	if rows2[1].header || rows2[1].repoRoot != "/a" {
		// row 1 must NOT be a worktree of /a (it's folded); it should be header b.
		if !rows2[1].header || rows2[1].repoRoot != "/b" {
			t.Errorf("row 1 should be header b after folding a, got %+v", rows2[1])
		}
	}
}

func TestFleetCursorWatchTarget(t *testing.T) {
	m := fleetModel{
		multi:     true,
		collapsed: map[string]bool{},
		entries: []fleetEntryJSON{
			{Repo: "a", RepoRoot: "/a", Branch: "main", Path: "/a", Current: true},
			{Repo: "a", RepoRoot: "/a", Branch: "feat", Path: "/a/wt"},
			{Repo: "b", RepoRoot: "/b", Branch: "main", Path: "/b", Status: "error"},
		},
	}
	m.rebuildRows()
	// rows: [0 h:a, 1 wt:/a, 2 wt:/a/wt, 3 h:b, 4 wt:/b(error)]

	// On a worktree row → that worktree's path.
	m.cursor = 2
	if got := m.cursorWatchTarget(); got != "/a/wt" {
		t.Errorf("worktree row: got %q, want /a/wt", got)
	}
	// On a header → that repo's current worktree.
	m.cursor = 0
	if got := m.cursorWatchTarget(); got != "/a" {
		t.Errorf("header row: got %q, want /a (current worktree)", got)
	}
	// On an unreachable (error) worktree → nothing to watch.
	m.cursor = 4
	if got := m.cursorWatchTarget(); got != "" {
		t.Errorf("error row: got %q, want empty", got)
	}
}

func TestFleetRebuildCursorStable(t *testing.T) {
	m := fleetModel{
		multi:     true,
		collapsed: map[string]bool{},
		entries: []fleetEntryJSON{
			{Repo: "a", RepoRoot: "/a", Branch: "main", Path: "/a"},
			{Repo: "a", RepoRoot: "/a", Branch: "feat", Path: "/a/wt"},
			{Repo: "b", RepoRoot: "/b", Branch: "main", Path: "/b"},
		},
	}
	m.rebuildRows()
	// rows: [h:a, wt:/a, wt:/a/wt, h:b, wt:/b] — put cursor on /a/wt.
	m.cursor = 2
	want := fleetRowKeyOf(m.rows[2])
	if want.path != "/a/wt" {
		t.Fatalf("setup: cursor row = %+v, want path /a/wt", want)
	}

	// Folding repo b must not move the cursor off /a/wt.
	m.collapsed["/b"] = true
	m.rebuildRows()
	if got := fleetRowKeyOf(m.rows[m.cursor]); got != want {
		t.Errorf("cursor jumped to %+v after folding b, want %+v", got, want)
	}
}
