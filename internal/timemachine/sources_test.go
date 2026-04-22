package timemachine

import (
	"context"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestMerge_NewestFirst(t *testing.T) {
	a := []Event{{OID: "a", When: time.Unix(100, 0), Kind: KindReflog}}
	b := []Event{{OID: "b", When: time.Unix(300, 0), Kind: KindBackup}}
	c := []Event{{OID: "c", When: time.Unix(200, 0), Kind: KindReflog}}

	got := Merge(a, b, c)
	wantOrder := []string{"b", "c", "a"} // 300, 200, 100
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, w := range wantOrder {
		if got[i].OID != w {
			t.Errorf("got[%d].OID = %q, want %q", i, got[i].OID, w)
		}
	}
}

func TestMerge_ZeroTimesSortToEnd(t *testing.T) {
	zero := Event{OID: "zero"}
	dated := Event{OID: "dated", When: time.Unix(100, 0)}

	got := Merge([]Event{zero, dated})
	if got[0].OID != "dated" {
		t.Errorf("got[0] = %q, want dated", got[0].OID)
	}
	if got[1].OID != "zero" {
		t.Errorf("got[1] = %q, want zero", got[1].OID)
	}
}

func TestLimit(t *testing.T) {
	in := []Event{{OID: "1"}, {OID: "2"}, {OID: "3"}}

	if got := Limit(in, 0); len(got) != 3 {
		t.Errorf("Limit(0) truncated: %d", len(got))
	}
	if got := Limit(in, 100); len(got) != 3 {
		t.Errorf("Limit(100) truncated: %d", len(got))
	}
	if got := Limit(in, 2); len(got) != 2 || got[0].OID != "1" || got[1].OID != "2" {
		t.Errorf("Limit(2) = %+v", got)
	}
}

func TestFilterByKind(t *testing.T) {
	in := []Event{
		{OID: "1", Kind: KindReflog},
		{OID: "2", Kind: KindBackup},
		{OID: "3", Kind: KindReflog},
	}

	if got := FilterByKind(in); len(got) != 3 {
		t.Errorf("empty filter altered input: %d", len(got))
	}

	got := FilterByKind(in, "reflog")
	if len(got) != 2 || got[0].OID != "1" || got[1].OID != "3" {
		t.Errorf("filter=reflog got %+v", got)
	}

	got = FilterByKind(in, "backup")
	if len(got) != 1 || got[0].OID != "2" {
		t.Errorf("filter=backup got %+v", got)
	}

	got = FilterByKind(in, "unknown-kind")
	if len(got) != 0 {
		t.Errorf("unknown kind should filter everything: got %+v", got)
	}
}

func TestFilterByBranch(t *testing.T) {
	in := []Event{
		{Kind: KindReflog, Ref: "HEAD@{0}"},
		{Kind: KindReflog, Ref: "refs/heads/main@{0}"},
		{Kind: KindReflog, Ref: "refs/heads/feature@{2}"},
		{Kind: KindBackup, Branch: "main"},
		{Kind: KindBackup, Branch: "feature"},
		{Kind: KindStash, Ref: "stash@{0}"},
	}

	// Empty branch is a no-op.
	if got := FilterByBranch(in, ""); len(got) != 6 {
		t.Errorf("empty branch filter altered input: %d", len(got))
	}

	// branch=main: should match main reflog + main backup.
	got := FilterByBranch(in, "main")
	if len(got) != 2 {
		t.Fatalf("main filter: got %d, want 2 (%+v)", len(got), got)
	}

	// branch=HEAD: only matches HEAD reflog entries.
	got = FilterByBranch(in, "HEAD")
	if len(got) != 1 || got[0].Ref != "HEAD@{0}" {
		t.Errorf("HEAD filter: got %+v", got)
	}

	// branch=feature: matches feature reflog + backup.
	got = FilterByBranch(in, "feature")
	if len(got) != 2 {
		t.Errorf("feature filter: got %d, want 2", len(got))
	}

	// Stash events never match a specific branch.
	got = FilterByBranch(in, "nonexistent")
	if len(got) != 0 {
		t.Errorf("nonexistent filter: got %d, want 0", len(got))
	}
}

func TestFilterBySince(t *testing.T) {
	old := time.Unix(100, 0)
	mid := time.Unix(200, 0)
	recent := time.Unix(300, 0)
	zero := time.Time{}

	in := []Event{
		{OID: "old", When: old},
		{OID: "mid", When: mid},
		{OID: "recent", When: recent},
		{OID: "no-time", When: zero},
	}

	// Zero cutoff = no-op.
	if got := FilterBySince(in, time.Time{}); len(got) != 4 {
		t.Errorf("zero cutoff altered input: %d", len(got))
	}

	// cutoff = 200: recent + mid pass, old + no-time drop.
	got := FilterBySince(in, mid)
	if len(got) != 2 {
		t.Fatalf("cutoff=200: got %d, want 2 (%+v)", len(got), got)
	}
	for _, ev := range got {
		if ev.OID != "mid" && ev.OID != "recent" {
			t.Errorf("unexpected event in filtered: %+v", ev)
		}
	}

	// cutoff in the future: all events drop.
	if got := FilterBySince(in, time.Unix(999, 0)); len(got) != 0 {
		t.Errorf("future cutoff: got %d, want 0", len(got))
	}
}

// --- integration ----------------------------------------------------------

func TestReadBranches_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("c1")
	repo.CreateBranch("feature")
	repo.Checkout("feature")
	repo.WriteFile("b.txt", "b")
	repo.Commit("c2")

	evs, err := ReadBranches(context.Background(), &git.ExecRunner{Dir: repo.Dir}, 10)
	if err != nil {
		t.Fatalf("ReadBranches: %v", err)
	}
	if len(evs) < 2 {
		t.Errorf("expected >=2 branch reflog events, got %d", len(evs))
	}
	for i, ev := range evs {
		if ev.Kind != KindReflog {
			t.Errorf("ev[%d].Kind = %v, want reflog", i, ev.Kind)
		}
	}
}

func TestReadStash_EmptyStash_ReturnsEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")

	// No stash pushed; refs/stash does not exist.
	evs, err := ReadStash(context.Background(), &git.ExecRunner{Dir: repo.Dir})
	if err != nil {
		t.Fatalf("ReadStash: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("expected 0 stash events, got %d", len(evs))
	}
}

func TestReadStash_AfterStashPush_ReturnsEntry(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")
	// Dirty the tree and push a stash.
	repo.WriteFile("a.txt", "dirty")
	if _, err := repo.TryGit("stash", "push", "-m", "test-stash"); err != nil {
		t.Fatalf("stash push: %v", err)
	}

	evs, err := ReadStash(context.Background(), &git.ExecRunner{Dir: repo.Dir})
	if err != nil {
		t.Fatalf("ReadStash: %v", err)
	}
	if len(evs) == 0 {
		t.Fatalf("expected >=1 stash event, got 0")
	}
	for _, ev := range evs {
		if ev.Kind != KindStash {
			t.Errorf("ev.Kind = %v, want KindStash", ev.Kind)
		}
		if ev.OID == "" {
			t.Errorf("ev.OID empty: %+v", ev)
		}
	}
}

func TestReadBackups_Wraps_ListBackups(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")

	// Empty repo → empty result.
	evs, err := ReadBackups(context.Background(), &git.ExecRunner{Dir: repo.Dir})
	if err != nil {
		t.Fatalf("ReadBackups: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("expected 0 backups, got %d", len(evs))
	}

	// Create a backup via gitsafe.Restorer.Backup.
	r := gitsafe.NewRestorer(&git.ExecRunner{Dir: repo.Dir},
		func() time.Time { return time.Unix(1700000000, 0) }, "undo")
	if _, err := r.Backup(context.Background(), "main"); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	evs, err = ReadBackups(context.Background(), &git.ExecRunner{Dir: repo.Dir})
	if err != nil {
		t.Fatalf("ReadBackups: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("expected 1 backup event, got %d", len(evs))
	}
	if evs[0].Kind != KindBackup || evs[0].BackupKind != "undo" {
		t.Errorf("unexpected event: %+v", evs[0])
	}
}
