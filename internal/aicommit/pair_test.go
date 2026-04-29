package aicommit

import (
	"reflect"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

func TestPairTestProdGroupsBasic(t *testing.T) {
	in := []provider.Group{
		{Type: "chore", Files: []string{"internal/cli/switch.go"}},
		{Type: "test", Files: []string{"internal/cli/switch_test.go"}},
	}
	got := PairTestProdGroups(in)
	if len(got) != 1 {
		t.Fatalf("want 1 merged group, got %d: %+v", len(got), got)
	}
	if got[0].Type != "chore" {
		t.Errorf("merged type: want %q (no promotion), got %q", "chore", got[0].Type)
	}
	want := []string{"internal/cli/switch.go", "internal/cli/switch_test.go"}
	if !reflect.DeepEqual(got[0].Files, want) {
		t.Errorf("Files: %+v", got[0].Files)
	}
}

// TestPairTestProdGroupsSingleProdAbsorbsOrphan: when only one
// non-test group exists, orphan tests pile in (no ambiguity).
func TestPairTestProdGroupsSingleProdAbsorbsOrphan(t *testing.T) {
	in := []provider.Group{
		{Type: "chore", Files: []string{"internal/cli/switch.go"}},
		{Type: "test", Files: []string{"internal/cli/integration_test.go"}},
	}
	got := PairTestProdGroups(in)
	if len(got) != 1 {
		t.Fatalf("single-prod fallback: want 1 group, got %d: %+v", len(got), got)
	}
	if got[0].Type != "chore" {
		t.Errorf("type must NOT change (no chore→feat promotion), got %q", got[0].Type)
	}
	if len(got[0].Files) != 2 {
		t.Errorf("Files: %+v", got[0].Files)
	}
}

// TestPairTestProdGroupsMultiProdKeepsOrphan: with multiple prod
// groups orphan ambiguity returns — leave orphans in test group
// (debate verdict C3 — integration_test.go etc. across cross-cutting
// changes).
func TestPairTestProdGroupsMultiProdKeepsOrphan(t *testing.T) {
	in := []provider.Group{
		{Type: "feat", Scope: "switch", Files: []string{"internal/cli/switch.go"}},
		{Type: "feat", Scope: "branch", Files: []string{"internal/cli/branch.go"}},
		{Type: "test", Files: []string{"internal/cli/integration_test.go"}},
	}
	got := PairTestProdGroups(in)
	if len(got) != 3 {
		t.Fatalf("multi-prod: orphan must stay separate, got %d groups: %+v", len(got), got)
	}
	test := findByType(got, "test")
	if test == nil || len(test.Files) != 1 {
		t.Errorf("orphan test group missing: %+v", got)
	}
}

// TestPairTestProdGroupsMixedMultiProd verifies the basename-paired
// test moves to the right scope, while the orphan stays in `test:` —
// only when there's enough prod-group ambiguity (multiple feat scopes)
// to disable the single-prod fallback.
func TestPairTestProdGroupsMixedMultiProd(t *testing.T) {
	in := []provider.Group{
		{Type: "feat", Scope: "switch", Files: []string{"internal/cli/switch.go"}},
		{Type: "feat", Scope: "branch", Files: []string{"internal/cli/branch.go"}},
		{Type: "test", Files: []string{
			"internal/cli/switch_test.go",      // pairs with switch.go
			"internal/cli/integration_test.go", // orphan, multi-prod → stays
		}},
	}
	got := PairTestProdGroups(in)
	if len(got) != 3 {
		t.Fatalf("want 3 groups (paired test moved, orphan stays), got %d: %+v", len(got), got)
	}
	test := findByType(got, "test")
	if test == nil || len(test.Files) != 1 || test.Files[0] != "internal/cli/integration_test.go" {
		t.Errorf("orphan test group: %+v", test)
	}
}

func TestPairTestProdGroupsDropsEmptyTestGroup(t *testing.T) {
	in := []provider.Group{
		{Type: "feat", Files: []string{"x/a.go", "x/b.go"}},
		{Type: "test", Files: []string{"x/a_test.go", "x/b_test.go"}},
	}
	got := PairTestProdGroups(in)
	if len(got) != 1 {
		t.Fatalf("want 1 group after merging, got %d: %+v", len(got), got)
	}
	if len(got[0].Files) != 4 {
		t.Errorf("merged Files: %+v", got[0].Files)
	}
}

func TestPairTestProdGroupsRespectsScope(t *testing.T) {
	// Two prod groups with different scopes — each merge target picks
	// the right destination.
	in := []provider.Group{
		{Type: "feat", Scope: "switch", Files: []string{"internal/cli/switch.go"}},
		{Type: "feat", Scope: "branch", Files: []string{"internal/cli/branch.go"}},
		{Type: "test", Files: []string{
			"internal/cli/switch_test.go",
			"internal/cli/branch_test.go",
		}},
	}
	got := PairTestProdGroups(in)
	if len(got) != 2 {
		t.Fatalf("want 2 prod groups, got %d", len(got))
	}
	for _, g := range got {
		switch g.Scope {
		case "switch":
			if !containsAll(g.Files, []string{"internal/cli/switch.go", "internal/cli/switch_test.go"}) {
				t.Errorf("switch group: %+v", g)
			}
		case "branch":
			if !containsAll(g.Files, []string{"internal/cli/branch.go", "internal/cli/branch_test.go"}) {
				t.Errorf("branch group: %+v", g)
			}
		}
	}
}

// TestPairTestProdGroupsTSBasenameSkipped — basename pairing is
// Go-only (debate verdict C2). With multiple prod groups, TS tests
// stay orphan because we have no way to disambiguate which prod they
// belong to. (Single-prod fallback would merge them, but that's the
// other dimension of pairing — see SingleProdAbsorbsOrphan.)
func TestPairTestProdGroupsTSBasenameSkipped(t *testing.T) {
	in := []provider.Group{
		{Type: "feat", Scope: "foo", Files: []string{"src/foo.ts"}},
		{Type: "feat", Scope: "bar", Files: []string{"src/bar.ts"}},
		{Type: "test", Files: []string{"src/foo.test.ts"}},
	}
	got := PairTestProdGroups(in)
	if len(got) != 3 {
		t.Errorf("TS basename pairing must be skipped under multi-prod; got %+v", got)
	}
}

func TestPairTestProdGroupsNoTestGroup(t *testing.T) {
	in := []provider.Group{
		{Type: "feat", Files: []string{"a.go"}},
		{Type: "chore", Files: []string{"b.go"}},
	}
	got := PairTestProdGroups(in)
	if !reflect.DeepEqual(got, in) {
		t.Errorf("no-op for groups without test type expected; got %+v", got)
	}
}

func TestPairTestProdGroupsSingleGroup(t *testing.T) {
	in := []provider.Group{{Type: "test", Files: []string{"a_test.go"}}}
	got := PairTestProdGroups(in)
	if len(got) != 1 || got[0].Files[0] != "a_test.go" {
		t.Errorf("single-group input: %+v", got)
	}
}

func TestGoProdCounterpart(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"foo_test.go", "foo.go"},
		{"internal/cli/switch_test.go", "internal/cli/switch.go"},
		{"foo.go", ""},
		{"foo.test.ts", ""},
		{"test_foo.py", ""},
		{"_test.go", ".go"}, // edge: empty stem; still a Go test path
	}
	for _, tc := range cases {
		got := goProdCounterpart(tc.in)
		if got != tc.want {
			t.Errorf("goProdCounterpart(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func findByType(groups []provider.Group, t string) *provider.Group {
	for i := range groups {
		if groups[i].Type == t {
			return &groups[i]
		}
	}
	return nil
}

func containsAll(slice, want []string) bool {
	set := map[string]bool{}
	for _, s := range slice {
		set[s] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}
