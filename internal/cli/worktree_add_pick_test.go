package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

const (
	localRefsCmd  = "for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track)%00%(objectname:short) refs/heads"
	remoteRefsCmd = "for-each-ref --format=%(refname:short)%00%(committerdate:unix)%00%(symref)%00%(objectname:short) refs/remotes"
)

// newBranchPickRunner wires the four probes loadWorktreeBranchRows makes.
func newBranchPickRunner(locals, remotes, worktrees, toplevel string) *git.FakeRunner {
	return &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			localRefsCmd:                {Stdout: locals},
			remoteRefsCmd:               {Stdout: remotes},
			"worktree list --porcelain": {Stdout: worktrees},
			"rev-parse --show-toplevel": {Stdout: toplevel + "\n"},
		},
	}
}

// Recency is the whole point of this picker: it answers "what was I
// working on", which alphabetical order cannot.
func TestLoadWorktreeBranchRows_SortsNewestFirst(t *testing.T) {
	runner := newBranchPickRunner(
		"zeta\x00\x001700000300\x00\x00aaaaaaa\nalpha\x00\x001700000100\x00\x00bbbbbbb\nmid\x00\x001700000200\x00\x00ccccccc\n",
		"", "worktree /repo\nbranch refs/heads/zeta\n", "/repo",
	)
	local, _, err := loadWorktreeBranchRows(context.Background(), runner)
	if err != nil {
		t.Fatalf("loadWorktreeBranchRows: %v", err)
	}
	var got []string
	for _, r := range local {
		got = append(got, r.Name)
	}
	want := []string{"zeta", "mid", "alpha"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("want newest-first %v, got %v", want, got)
	}
}

// The branch we are standing on is just as unavailable as one held by
// another worktree — git refuses a second checkout either way. This is
// the case loadSwitchWorktrees alone gets wrong, since it deliberately
// excludes the current worktree from byBranch.
func TestLoadWorktreeBranchRows_CurrentWorktreeCountsAsOccupied(t *testing.T) {
	runner := newBranchPickRunner(
		"develop\x00\x001700000200\x00\x00aaaaaaa\nfeat/x\x00\x001700000100\x00\x00bbbbbbb\n",
		"",
		"worktree /repo\nbranch refs/heads/develop\n\nworktree /wt/featx\nbranch refs/heads/feat/x\n",
		"/repo",
	)
	local, _, err := loadWorktreeBranchRows(context.Background(), runner)
	if err != nil {
		t.Fatalf("loadWorktreeBranchRows: %v", err)
	}
	occupied := map[string]string{}
	for _, r := range local {
		occupied[r.Name] = r.OccupiedBy
	}
	if occupied["develop"] != "repo" {
		t.Errorf("current worktree's branch should read as occupied, got %q", occupied["develop"])
	}
	if occupied["feat/x"] != "featx" {
		t.Errorf("other worktree's branch should name it, got %q", occupied["feat/x"])
	}
}

// Remote-only rows come back separately so the caller can keep them out
// of the default view while still exposing them to the filter.
func TestLoadWorktreeBranchRows_SplitsRemoteOnly(t *testing.T) {
	runner := newBranchPickRunner(
		"develop\x00origin/develop\x001700000200\x00\x00aaaaaaa\n",
		"origin/develop\x001700000200\x00\x00aaaaaaa\norigin/feat/new\x001700000900\x00\x00ddddddd\n",
		"worktree /repo\nbranch refs/heads/develop\n", "/repo",
	)
	local, remote, err := loadWorktreeBranchRows(context.Background(), runner)
	if err != nil {
		t.Fatalf("loadWorktreeBranchRows: %v", err)
	}
	if len(local) != 1 || local[0].Name != "develop" {
		t.Fatalf("unexpected local rows: %+v", local)
	}
	if len(remote) != 1 || remote[0].Name != "feat/new" {
		t.Fatalf("origin/develop duplicates a local branch and must not repeat: %+v", remote)
	}
	if remote[0].TrackRef != "origin/feat/new" {
		t.Errorf("remote row must carry its tracking ref, got %q", remote[0].TrackRef)
	}
	if local[0].TrackRef != "" {
		t.Errorf("local row must not look like it needs creating, got %q", local[0].TrackRef)
	}
}

func TestWorktreeBranchSource(t *testing.T) {
	cases := []struct {
		name string
		in   branchInfo
		want string
	}{
		{"upstream", branchInfo{Upstream: "origin/x"}, "↑ origin/x"},
		{"inferred", branchInfo{Upstream: "origin/x", UpstreamInferred: true}, "~ origin/x"},
		{"gone", branchInfo{Upstream: "origin/x", Gone: true}, "(gone)"},
		{"fork", branchInfo{ForkBranch: "main", ForkPoint: "abc1234"}, "from main@abc1234"},
		{"bare", branchInfo{}, "(local)"},
	}
	for _, c := range cases {
		if got := worktreeBranchSource(c.in); got != c.want {
			t.Errorf("%s: want %q, got %q", c.name, c.want, got)
		}
	}
}

// Markers are the at-a-glance signal for "can I pick this row at all".
func TestBuildWorktreeBranchItems_Markers(t *testing.T) {
	rows := []worktreeBranchRow{
		{Name: "free", LastCommit: time.Unix(1700000000, 0)},
		{Name: "remote-only", TrackRef: "origin/remote-only", LastCommit: time.Unix(1700000000, 0)},
		{Name: "taken", OccupiedBy: "other-wt", LastCommit: time.Unix(1700000000, 0)},
	}
	items := buildWorktreeBranchItems(rows, true)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	for i, want := range []string{"●", "○", "⊗"} {
		if !strings.Contains(items[i].Cells[0], want) {
			t.Errorf("row %d should carry %q, got %q", i, want, items[i].Cells[0])
		}
	}
	if !strings.Contains(items[2].Cells[2], "other-wt") {
		t.Errorf("occupied row should name its worktree, got %q", items[2].Cells[2])
	}
}

// The occupancy column is only worth its width when something is
// occupied, so the cell count has to track the header count.
func TestBuildWorktreeBranchItems_ColumnCountTracksWorktreeCol(t *testing.T) {
	rows := []worktreeBranchRow{{Name: "x", LastCommit: time.Unix(1700000000, 0)}}
	if got := len(buildWorktreeBranchItems(rows, false)[0].Cells); got != 4 {
		t.Errorf("without the WORKTREE column want 4 cells, got %d", got)
	}
	if got := len(buildWorktreeBranchItems(rows, true)[0].Cells); got != 5 {
		t.Errorf("with the WORKTREE column want 5 cells, got %d", got)
	}
}

// The subtitle is where the remote question gets asked, so it must state
// both the current scope and the way to widen it.
func TestBuildWorktreeBranchSubtitle(t *testing.T) {
	hidden := buildWorktreeBranchSubtitle(37, 28, false)
	if !strings.Contains(hidden, "37 local") || !strings.Contains(hidden, "ctrl+r to include 28 remote") {
		t.Errorf("hidden-remotes subtitle should offer the toggle, got %q", hidden)
	}
	shown := buildWorktreeBranchSubtitle(37, 28, true)
	if !strings.Contains(shown, "37 local + 28 remote") || !strings.Contains(shown, "local only") {
		t.Errorf("shown-remotes subtitle should offer the way back, got %q", shown)
	}
	none := buildWorktreeBranchSubtitle(37, 0, false)
	if strings.Contains(none, "ctrl+r") {
		t.Errorf("with no remotes there is nothing to toggle, got %q", none)
	}
}

// An empty list must still say what to do next rather than render blank.
func TestWorktreeBranchNoneItem_NamesTheWayOut(t *testing.T) {
	withRemotes := worktreeBranchNoneItem(4, 28, false)
	if len(withRemotes.Cells) != 4 {
		t.Fatalf("placeholder must fill the header count, got %d", len(withRemotes.Cells))
	}
	if !strings.Contains(withRemotes.Cells[0], "ctrl+r") {
		t.Errorf("should point at the remotes toggle, got %q", withRemotes.Cells[0])
	}
	noRemotes := worktreeBranchNoneItem(4, 0, false)
	if strings.Contains(noRemotes.Cells[0], "ctrl+r") {
		t.Errorf("no remotes to widen to — should not offer it, got %q", noRemotes.Cells[0])
	}
	if !strings.Contains(noRemotes.Cells[0], "new branch") {
		t.Errorf("should fall back to the create path, got %q", noRemotes.Cells[0])
	}
}

func TestSuggestWorktreeName(t *testing.T) {
	cases := map[string]string{
		"feat/relay-agent-notify": "relay-agent-notify",
		"develop":                 "develop",
		"a/b/c":                   "c",
		"  feat/x  ":              "x",
		"":                        "",
	}
	for in, want := range cases {
		if got := suggestWorktreeName(in); got != want {
			t.Errorf("%q: want %q, got %q", in, want, got)
		}
	}
}

// AGE must outlive UPSTREAM/HASH when the terminal narrows, and WORKTREE
// must outrank UPSTREAM here because occupancy decides selectability.
func TestWorktreeBranchColumnPriority_RecencyAndOccupancyWin(t *testing.T) {
	p := worktreeBranchColumnPriority()
	if p["AGE"] <= p["UPSTREAM"] || p["AGE"] <= p["HASH"] {
		t.Errorf("AGE must survive longer than UPSTREAM/HASH, got %v", p)
	}
	if p["WORKTREE"] <= p["UPSTREAM"] {
		t.Errorf("WORKTREE must outrank UPSTREAM in the add picker, got %v", p)
	}
	if p["BRANCH"] <= p["AGE"] {
		t.Errorf("BRANCH must be the last column standing, got %v", p)
	}
}

// The remotes toggle carries a ctrl alias so it stays reachable while the
// filter prompt is focused.
func TestWorktreeBranchExtras_RemotesHasFilterAlias(t *testing.T) {
	extras := worktreeBranchExtras()
	if len(extras) != 1 || extras[0].Key != "r" {
		t.Fatalf("expected a single remotes toggle, got %+v", extras)
	}
	if extras[0].FilterKey != "ctrl+r" {
		t.Errorf("remotes toggle must work while filtering, got %q", extras[0].FilterKey)
	}
}
