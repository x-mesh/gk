package cli

import (
	"context"
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

func TestParsePorcelainZPaths(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", nil},
		{"modified + untracked", " M foo.go\x00?? bar.txt\x00", []string{"foo.go", "bar.txt"}},
		{"rename skips source", "R  new.go\x00old.go\x00", []string{"new.go"}},
		{"staged add", "A  added.go\x00", []string{"added.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePorcelainZPaths(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("parsePorcelainZPaths = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("path[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
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
	out := renderFleet(fleetView{entries: entries, cursor: 1, now: now, width: 100, detail: true})
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
	paused := renderFleet(fleetView{entries: entries, cursor: 2, now: now, width: 100, detail: true})
	for _, want := range []string{"rebase 2/5", "gk continue", "18m ago"} {
		if !strings.Contains(paused, want) {
			t.Errorf("paused render missing %q in:\n%s", want, paused)
		}
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

	entries, err := gatherFleet(ctx, runner)
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
	entries, err = gatherFleet(ctx, runner)
	if err != nil {
		t.Fatalf("gatherFleet (dirty): %v", err)
	}
	if entries[0].Status != "dirty" {
		t.Errorf("after untracked write status = %q, want dirty", entries[0].Status)
	}
}
