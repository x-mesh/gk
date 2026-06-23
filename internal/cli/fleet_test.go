package cli

import (
	"context"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestFleetStatus(t *testing.T) {
	cases := []struct {
		name string
		e    fleetEntryJSON
		want string
	}{
		{"clean", fleetEntryJSON{}, "clean"},
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

	repo.WriteFile("b.txt", "b") // untracked
	entries, err = gatherFleet(ctx, runner)
	if err != nil {
		t.Fatalf("gatherFleet (dirty): %v", err)
	}
	if entries[0].Status != "dirty" {
		t.Errorf("after untracked write status = %q, want dirty", entries[0].Status)
	}
}
