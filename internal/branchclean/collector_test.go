package branchclean

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"pgregory.net/rapid"
)

// ---------------------------------------------------------------------------
// Unit tests: CollectMerged
// ---------------------------------------------------------------------------

func TestCollectMerged_Basic(t *testing.T) {
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"branch --merged main --format=%(refname:short)": {
				Stdout: "feat/done\nfix/old\nmain\n",
			},
		},
	}
	c := &Collector{Runner: runner, Client: git.NewClient(runner)}

	protected := map[string]bool{"main": true}
	entries, err := c.CollectMerged(context.Background(), "main", protected)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(entries), entries)
	}
	for _, e := range entries {
		if e.Name == "main" {
			t.Fatal("main should be excluded as protected")
		}
		if e.Status != StatusMerged {
			t.Fatalf("expected status merged, got %v", e.Status)
		}
	}
}

func TestCollectMerged_EmptyOutput(t *testing.T) {
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"branch --merged main --format=%(refname:short)": {Stdout: ""},
		},
	}
	c := &Collector{Runner: runner, Client: git.NewClient(runner)}

	entries, err := c.CollectMerged(context.Background(), "main", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestCollectMerged_GitError(t *testing.T) {
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"branch --merged main --format=%(refname:short)": {
				Stderr:   "fatal: not a git repository",
				ExitCode: 128,
			},
		},
	}
	c := &Collector{Runner: runner, Client: git.NewClient(runner)}

	_, err := c.CollectMerged(context.Background(), "main", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// Unit tests: CollectGone
// ---------------------------------------------------------------------------

func TestCollectGone_Basic(t *testing.T) {
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track) refs/heads": {
				Stdout: "feat/a\x00origin/feat/a\x001700000000\x00[gone]\n" +
					"feat/b\x00origin/feat/b\x001700000000\x00\n" +
					"main\x00origin/main\x001700000000\x00\n",
			},
		},
	}
	c := &Collector{Runner: runner, Client: git.NewClient(runner)}

	entries, err := c.CollectGone(context.Background(), map[string]bool{"main": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "feat/a" {
		t.Fatalf("expected feat/a, got %s", entries[0].Name)
	}
	if entries[0].Status != StatusGone {
		t.Fatalf("expected status gone, got %v", entries[0].Status)
	}
}

// ---------------------------------------------------------------------------
// Unit tests: CollectStale
// ---------------------------------------------------------------------------

func TestCollectStale_Basic(t *testing.T) {
	now := time.Now()
	old := now.AddDate(0, 0, -60) // 60 days ago
	recent := now.AddDate(0, 0, -5) // 5 days ago

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track) refs/heads": {
				Stdout: fmt.Sprintf("feat/old\x00\x00%d\x00\nfeat/recent\x00\x00%d\x00\nmain\x00\x00%d\x00\n",
					old.Unix(), recent.Unix(), now.Unix()),
			},
		},
	}
	c := &Collector{Runner: runner, Client: git.NewClient(runner)}

	entries, err := c.CollectStale(context.Background(), 30, map[string]bool{"main": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "feat/old" {
		t.Fatalf("expected feat/old, got %s", entries[0].Name)
	}
	if entries[0].Status != StatusStale {
		t.Fatalf("expected status stale, got %v", entries[0].Status)
	}
}

// ---------------------------------------------------------------------------
// Unit tests: CollectAll
// ---------------------------------------------------------------------------

func TestCollectAll_MergedAndGone(t *testing.T) {
	now := time.Now()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short HEAD": {Stdout: "main\n"},
			"symbolic-ref --short refs/remotes/origin/HEAD": {Stdout: "origin/main\n"},
			"branch --merged main --format=%(refname:short)": {
				Stdout: "feat/merged\nmain\n",
			},
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track) refs/heads": {
				Stdout: fmt.Sprintf("feat/gone\x00origin/feat/gone\x00%d\x00[gone]\nfeat/merged\x00\x00%d\x00\nmain\x00origin/main\x00%d\x00\n",
					now.Unix(), now.Unix(), now.Unix()),
			},
		},
	}
	c := &Collector{Runner: runner, Client: git.NewClient(runner)}

	opts := CleanOptions{
		Gone: true,
		All:  false,
	}
	entries, err := c.CollectAll(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["feat/merged"] {
		t.Fatal("expected feat/merged in results")
	}
	if !names["feat/gone"] {
		t.Fatal("expected feat/gone in results")
	}
	if names["main"] {
		t.Fatal("main should be excluded")
	}
}

func TestCollectAll_Deduplicates(t *testing.T) {
	now := time.Now()
	old := now.AddDate(0, 0, -60)
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short HEAD": {Stdout: "main\n"},
			"symbolic-ref --short refs/remotes/origin/HEAD": {Stdout: "origin/main\n"},
			"branch --merged main --format=%(refname:short)": {
				Stdout: "feat/both\nmain\n",
			},
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track) refs/heads": {
				Stdout: fmt.Sprintf("feat/both\x00origin/feat/both\x00%d\x00[gone]\nmain\x00origin/main\x00%d\x00\n",
					old.Unix(), now.Unix()),
			},
		},
	}
	c := &Collector{Runner: runner, Client: git.NewClient(runner)}

	opts := CleanOptions{All: true, StaleDays: 30}
	entries, err := c.CollectAll(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// feat/both는 merged + gone + stale 모두에 해당하지만 한 번만 나와야 함
	count := 0
	for _, e := range entries {
		if e.Name == "feat/both" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected feat/both once, got %d times", count)
	}
}

// ---------------------------------------------------------------------------
// Unit tests: FilterProtected
// ---------------------------------------------------------------------------

func TestFilterProtected_Basic(t *testing.T) {
	entries := []BranchEntry{
		{Name: "main"},
		{Name: "feat/a"},
		{Name: "develop"},
		{Name: "feat/b"},
	}
	protected := map[string]bool{"main": true, "develop": true}

	result := FilterProtected(entries, protected)
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
	for _, e := range result {
		if protected[e.Name] {
			t.Fatalf("protected branch %s should not be in result", e.Name)
		}
	}
}

func TestFilterProtected_EmptyProtected(t *testing.T) {
	entries := []BranchEntry{{Name: "a"}, {Name: "b"}}
	result := FilterProtected(entries, nil)
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
}

func TestFilterProtected_EmptyEntries(t *testing.T) {
	result := FilterProtected(nil, map[string]bool{"main": true})
	if len(result) != 0 {
		t.Fatalf("expected 0, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// Unit tests: DeduplicateEntries
// ---------------------------------------------------------------------------

func TestDeduplicateEntries_Basic(t *testing.T) {
	entries := []BranchEntry{
		{Name: "a", Status: StatusMerged},
		{Name: "b", Status: StatusGone},
		{Name: "a", Status: StatusStale},
		{Name: "c", Status: StatusMerged},
		{Name: "b", Status: StatusMerged},
	}
	result := DeduplicateEntries(entries)
	if len(result) != 3 {
		t.Fatalf("expected 3, got %d", len(result))
	}
	// 첫 번째 occurrence 유지
	if result[0].Name != "a" || result[0].Status != StatusMerged {
		t.Fatalf("expected first 'a' with merged status, got %v", result[0])
	}
	if result[1].Name != "b" || result[1].Status != StatusGone {
		t.Fatalf("expected first 'b' with gone status, got %v", result[1])
	}
}

func TestDeduplicateEntries_NoDuplicates(t *testing.T) {
	entries := []BranchEntry{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	result := DeduplicateEntries(entries)
	if len(result) != 3 {
		t.Fatalf("expected 3, got %d", len(result))
	}
}

func TestDeduplicateEntries_Empty(t *testing.T) {
	result := DeduplicateEntries(nil)
	if len(result) != 0 {
		t.Fatalf("expected 0, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// Property tests
// ---------------------------------------------------------------------------

// branchNameGen generates valid git-like branch names.
func branchNameGen() *rapid.Generator[string] {
	return rapid.Custom[string](func(t *rapid.T) string {
		prefix := rapid.SampledFrom([]string{"feat/", "fix/", "chore/", "release/", ""}).Draw(t, "prefix")
		suffix := rapid.StringMatching(`[a-z][a-z0-9\-]{1,15}`).Draw(t, "suffix")
		return prefix + suffix
	})
}

// Feature: ai-branch-clean, Property 5: Stale 필터링 정확성
// **Validates: Requirements 5.1, 5.2**
func TestProperty5_StaleFilteringAccuracy(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		now := time.Now()
		staleDays := rapid.IntRange(1, 365).Draw(rt, "staleDays")

		n := rapid.IntRange(0, 30).Draw(rt, "branchCount")

		// 브랜치 목록 생성 (for-each-ref 출력 형식)
		// 경계값 문제를 피하기 위해 "확실히 stale"과 "확실히 fresh"만 생성한다.
		// staleDays 경계에서 ±1일 버퍼를 둔다.
		var lines []string
		type branchExpect struct {
			name    string
			isStale bool
		}
		var expected []branchExpect

		usedNames := make(map[string]bool)
		for i := 0; i < n; i++ {
			name := branchNameGen().Draw(rt, fmt.Sprintf("name_%d", i))
			if usedNames[name] {
				continue
			}
			usedNames[name] = true

			// 확실히 stale (staleDays+1 이상) 또는 확실히 fresh (staleDays-1 이하, 최소 0)
			isStale := rapid.Bool().Draw(rt, fmt.Sprintf("isStale_%d", i))
			var commitDate time.Time
			if isStale {
				extraDays := rapid.IntRange(1, 365).Draw(rt, fmt.Sprintf("extraStale_%d", i))
				commitDate = now.AddDate(0, 0, -(staleDays + extraDays))
			} else {
				freshDays := rapid.IntRange(0, max(staleDays-1, 0)).Draw(rt, fmt.Sprintf("freshDays_%d", i))
				commitDate = now.AddDate(0, 0, -freshDays)
			}

			expected = append(expected, branchExpect{name: name, isStale: isStale})
			line := fmt.Sprintf("%s\x00\x00%d\x00", name, commitDate.Unix())
			lines = append(lines, line)
		}

		output := strings.Join(lines, "\n")
		runner := &git.FakeRunner{
			Responses: map[string]git.FakeResponse{
				"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track) refs/heads": {
					Stdout: output,
				},
			},
		}
		c := &Collector{Runner: runner, Client: git.NewClient(runner)}

		entries, err := c.CollectStale(context.Background(), staleDays, nil)
		if err != nil {
			rt.Fatalf("unexpected error: %v", err)
		}

		resultNames := make(map[string]bool)
		for _, e := range entries {
			resultNames[e.Name] = true
		}

		for _, exp := range expected {
			if exp.isStale && !resultNames[exp.name] {
				rt.Fatalf("stale branch %q not in result", exp.name)
			}
			if !exp.isStale && resultNames[exp.name] {
				rt.Fatalf("non-stale branch %q should not be in result", exp.name)
			}
		}
	})
}

// Feature: ai-branch-clean, Property 6: DeduplicateEntries 유일성
// **Validates: Requirements 6.3**
func TestProperty6_DeduplicateEntriesUniqueness(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(0, 50).Draw(rt, "entryCount")

		// 이름 풀을 먼저 생성하고, 중복을 포함하여 entries 생성
		poolSize := rapid.IntRange(1, max(n, 1)).Draw(rt, "poolSize")
		namePool := make([]string, poolSize)
		for i := 0; i < poolSize; i++ {
			namePool[i] = branchNameGen().Draw(rt, fmt.Sprintf("poolName_%d", i))
		}

		var entries []BranchEntry
		for i := 0; i < n; i++ {
			idx := rapid.IntRange(0, poolSize-1).Draw(rt, fmt.Sprintf("idx_%d", i))
			entries = append(entries, BranchEntry{
				Name:   namePool[idx],
				Status: BranchStatus(rapid.SampledFrom([]string{"merged", "gone", "stale"}).Draw(rt, fmt.Sprintf("status_%d", i))),
			})
		}

		result := DeduplicateEntries(entries)

		// 1. 결과의 모든 이름은 유일해야 함
		seen := make(map[string]bool)
		for _, e := range result {
			if seen[e.Name] {
				rt.Fatalf("duplicate name in result: %s", e.Name)
			}
			seen[e.Name] = true
		}

		// 2. 원본의 모든 고유 이름이 결과에 포함되어야 함
		inputUnique := make(map[string]bool)
		for _, e := range entries {
			inputUnique[e.Name] = true
		}
		for name := range inputUnique {
			if !seen[name] {
				rt.Fatalf("unique name %q from input missing in result", name)
			}
		}

		// 3. 결과 크기 == 입력의 고유 이름 수
		if len(result) != len(inputUnique) {
			rt.Fatalf("result size %d != unique input names %d", len(result), len(inputUnique))
		}
	})
}

// Feature: ai-branch-clean, Property 8: FilterProtected 안전성
// **Validates: Requirements 1.4, 1.5, 5.3, 5.4, 9.1, 9.2, 9.3**
func TestProperty8_FilterProtectedSafety(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(0, 30).Draw(rt, "entryCount")
		protectedCount := rapid.IntRange(0, 10).Draw(rt, "protectedCount")

		// 이름 풀 생성
		allNames := make([]string, 0, n+protectedCount)
		for i := 0; i < n+protectedCount; i++ {
			allNames = append(allNames, branchNameGen().Draw(rt, fmt.Sprintf("name_%d", i)))
		}

		// entries 생성
		var entries []BranchEntry
		for i := 0; i < n; i++ {
			idx := rapid.IntRange(0, len(allNames)-1).Draw(rt, fmt.Sprintf("entryIdx_%d", i))
			entries = append(entries, BranchEntry{Name: allNames[idx]})
		}

		// protected set 생성
		protected := make(map[string]bool)
		for i := 0; i < protectedCount; i++ {
			idx := rapid.IntRange(0, len(allNames)-1).Draw(rt, fmt.Sprintf("protIdx_%d", i))
			protected[allNames[idx]] = true
		}

		result := FilterProtected(entries, protected)

		// 결과에 protected 이름이 없어야 함
		for _, e := range result {
			if protected[e.Name] {
				rt.Fatalf("protected branch %q found in result", e.Name)
			}
		}
	})
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
