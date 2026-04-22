package timemachine

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestDiffStat_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("c1")
	repo.WriteFile("b.txt", "bb\ncc\n")
	sha := repo.Commit("c2")

	out, err := DiffStat(context.Background(), &git.ExecRunner{Dir: repo.Dir}, sha)
	if err != nil {
		t.Fatalf("DiffStat: %v", err)
	}
	if !strings.Contains(out, "b.txt") {
		t.Errorf("expected b.txt in stat output, got: %q", out)
	}
}

func TestDiffStat_EmptyOid(t *testing.T) {
	_, err := DiffStat(context.Background(), &git.FakeRunner{}, "")
	if err == nil {
		t.Error("expected error for empty oid")
	}
}

func TestDiffCache_HitsAvoidRepeatFetch(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"show --stat --format= abc": {Stdout: "a.txt | 2 +-\n"},
		},
	}
	cache := NewDiffCache(fake, 4)

	v1, err := cache.Stat(context.Background(), "abc")
	if err != nil {
		t.Fatalf("first Stat: %v", err)
	}
	v2, err := cache.Stat(context.Background(), "abc")
	if err != nil {
		t.Fatalf("second Stat: %v", err)
	}
	if v1 != v2 {
		t.Errorf("cache miss? v1=%q v2=%q", v1, v2)
	}
	// FakeRunner.Calls must show exactly ONE show invocation for "abc".
	count := 0
	for _, c := range fake.Calls {
		if len(c.Args) >= 1 && c.Args[0] == "show" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 show call (cache hit), got %d (Calls=%+v)", count, fake.Calls)
	}
}

func TestDiffCache_StatAndPatch_DontCollide(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"show --stat --format= abc":         {Stdout: "STAT_OUTPUT"},
			"show --patch --stat --format= abc": {Stdout: "PATCH_OUTPUT"},
		},
	}
	cache := NewDiffCache(fake, 4)

	s, err := cache.Stat(context.Background(), "abc")
	if err != nil || s != "STAT_OUTPUT" {
		t.Fatalf("Stat = %q, err=%v", s, err)
	}

	p, err := cache.Patch(context.Background(), "abc")
	if err != nil || p != "PATCH_OUTPUT" {
		t.Fatalf("Patch = %q, err=%v", p, err)
	}

	// Both stored.
	if cache.Len() != 2 {
		t.Errorf("cache.Len = %d, want 2", cache.Len())
	}
}

func TestDiffCache_EvictsOldestPastCapacity(t *testing.T) {
	fake := &git.FakeRunner{DefaultResp: git.FakeResponse{Stdout: "x"}}
	cache := NewDiffCache(fake, 2)

	_, _ = cache.Stat(context.Background(), "a")
	_, _ = cache.Stat(context.Background(), "b")
	_, _ = cache.Stat(context.Background(), "c") // should evict "a"

	if cache.Len() != 2 {
		t.Errorf("cache.Len = %d, want 2 after eviction", cache.Len())
	}

	// Accessing "a" again must re-fetch (cache miss).
	before := len(fake.Calls)
	_, _ = cache.Stat(context.Background(), "a")
	after := len(fake.Calls)
	if after <= before {
		t.Errorf("expected an additional fetch after eviction; before=%d after=%d", before, after)
	}
}

func TestDiffCache_MRUReorderOnHit(t *testing.T) {
	fake := &git.FakeRunner{DefaultResp: git.FakeResponse{Stdout: "x"}}
	cache := NewDiffCache(fake, 2)

	_, _ = cache.Stat(context.Background(), "a")
	_, _ = cache.Stat(context.Background(), "b")
	// Touch "a" again → a is now MRU; "b" is LRU.
	_, _ = cache.Stat(context.Background(), "a")
	// Add "c" → should evict "b", not "a".
	_, _ = cache.Stat(context.Background(), "c")

	// Re-accessing "a" should NOT trigger a fetch (still in cache).
	before := len(fake.Calls)
	_, _ = cache.Stat(context.Background(), "a")
	after := len(fake.Calls)
	if after != before {
		t.Errorf("MRU reorder broken: 'a' was fetched again (before=%d after=%d)", before, after)
	}
}
