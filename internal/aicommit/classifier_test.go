package aicommit

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// classifyGroups is a test shim for the pre-ClassifyResult call style: it runs
// Classify and returns just the groups, so the existing tests stay terse now
// that Classify returns model/token metadata alongside the groups.
func classifyGroups(ctx context.Context, p provider.Provider, files []FileChange, opts ClassifyOptions) ([]provider.Group, error) {
	res, err := Classify(ctx, p, files, opts)
	return res.Groups, err
}

func TestClassifyPassesLineDeltasToProvider(t *testing.T) {
	p := provider.NewFake()
	p.ClassifyResponses = []provider.ClassifyResult{{
		Groups: []provider.Group{{Type: "feat", Files: []string{"src/large.go", "src/small.go"}}},
	}}
	var got []provider.FileChange
	p.OnClassify = func(in provider.ClassifyInput) { got = in.Files }

	_, err := Classify(context.Background(), p, []FileChange{
		{Path: "src/large.go", Status: "modified", Added: 312, Deleted: 17},
		{Path: "src/small.go", Status: "modified", Added: 1, Deleted: 1},
	}, ClassifyOptions{AllowedTypes: []string{"feat"}, HybridFileLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("provider files = %d, want 2", len(got))
	}
	if got[0].Added != 312 || got[0].Deleted != 17 || got[1].Added != 1 || got[1].Deleted != 1 {
		t.Fatalf("line deltas lost: %+v", got)
	}
}

func TestHeuristicType(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"internal/cli/root.go", ""},
		{"internal/cli/root_test.go", "test"},
		{"components/App.test.tsx", "test"},
		{"tests/integration/fixtures/a.txt", "test"},
		{"docs/api.md", "docs"},
		{"README.md", "docs"},
		{".github/workflows/ci.yml", "ci"},
		{"Makefile", "build"},
		{"go.sum", "build"},
		{"package-lock.json", "build"},
		{"cmd/gk/main.go", ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := heuristicType(tc.path); got != tc.want {
				t.Errorf("heuristicType(%s): want %q, got %q", tc.path, tc.want, got)
			}
		})
	}
}

// The space-mesh incident guard: a huge working tree is classified in
// disjoint chunks — each provider call sees at most classifyChunkSize
// files — and same-typed groups merge back into one result that still
// covers every file, with token usage summed across calls.
func TestClassifyChunksLargeFileSets(t *testing.T) {
	p := provider.NewFake()
	var callSizes []int
	p.OnClassify = func(in provider.ClassifyInput) {
		callSizes = append(callSizes, len(in.Files))
	}
	total := classifyChunkSize*2 + 25 // → 3 chunks
	files := make([]FileChange, total)
	for i := range files {
		files[i] = FileChange{Path: fmt.Sprintf("src/f%04d.go", i), Status: "modified"}
	}
	for start := 0; start < total; start += classifyChunkSize {
		end := min(start+classifyChunkSize, total)
		var paths []string
		for _, f := range files[start:end] {
			paths = append(paths, f.Path)
		}
		p.ClassifyResponses = append(p.ClassifyResponses, provider.ClassifyResult{
			Groups:     []provider.Group{{Type: "feat", Files: paths, Rationale: "chunk"}},
			Model:      "fake",
			TokensUsed: 10,
		})
	}

	res, err := Classify(context.Background(), p, files, ClassifyOptions{AllowedTypes: []string{"feat"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := count(p.Calls, "Classify"); got != 3 {
		t.Fatalf("provider calls = %d, want 3", got)
	}
	for i, n := range callSizes {
		if n > classifyChunkSize {
			t.Errorf("call %d saw %d files, exceeds chunk size %d", i, n, classifyChunkSize)
		}
	}
	if len(res.Groups) != 1 {
		t.Fatalf("groups = %d, want 1 merged feat group", len(res.Groups))
	}
	if len(res.Groups[0].Files) != total {
		t.Errorf("merged group has %d files, want %d", len(res.Groups[0].Files), total)
	}
	if res.TokensUsed != 30 {
		t.Errorf("tokens = %d, want summed 30", res.TokensUsed)
	}
}

func TestClassifyHeuristicOnlyShortCircuitsLLM(t *testing.T) {
	p := provider.NewFake()
	// Rig fake to misbehave if it's called.
	p.ClassifyErrs = []error{errUnexpected{}}

	files := []FileChange{
		{Path: "internal/cli/root_test.go", Status: "modified"},
		{Path: "internal/cli/root.go", Status: "modified"},
	}
	groups, err := classifyGroups(context.Background(), p, files, ClassifyOptions{
		HeuristicOnly: true,
		AllowedTypes:  []string{"feat", "test"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups: %+v", groups)
	}
	if count(p.Calls, "Classify") != 0 {
		t.Errorf("HeuristicOnly must not call the provider")
	}
}

func TestClassifySmallHomogeneousShortCircuitsLLM(t *testing.T) {
	p := provider.NewFake()
	files := []FileChange{
		{Path: "internal/cli/a.go"},
		{Path: "internal/cli/b.go"},
	}
	groups, err := classifyGroups(context.Background(), p, files, ClassifyOptions{
		HybridFileLimit: 5,
		AllowedTypes:    []string{"feat", "chore"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(groups) != 1 || groups[0].Type != "chore" {
		t.Errorf("want single chore group, got %+v", groups)
	}
	if count(p.Calls, "Classify") != 0 {
		t.Error("small homogeneous set must use heuristic only")
	}
}

// TestClassifyDefiniteSingleGroupSkipsLLM proves the net-new fast path:
// a change that resolves to exactly one DEFINITE-kind group skips the LLM
// Classify round-trip even when isSmallHomogeneous would NOT (here: >5
// files spanning multiple top-dirs). classifyCalls must stay 0.
func TestClassifyDefiniteSingleGroupSkipsLLM(t *testing.T) {
	classifyCalls := 0
	p := provider.NewFake()
	p.OnClassify = func(provider.ClassifyInput) { classifyCalls++ }
	// Rig the fake to misbehave if it IS called.
	p.ClassifyErrs = []error{errUnexpected{}}

	// 6 docs files (> HybridFileLimit) across two top-dirs (docs/, .) —
	// isSmallHomogeneous returns false, so without the definite fast path
	// this would hit the LLM. All map to the single "docs" heuristic type.
	files := []FileChange{
		{Path: "README.md"},
		{Path: "docs/a.md"},
		{Path: "docs/b.md"},
		{Path: "docs/c.md"},
		{Path: "docs/d.md"},
		{Path: "docs/e.md"},
	}
	groups, err := classifyGroups(context.Background(), p, files, ClassifyOptions{
		HybridFileLimit: 5,
		AllowedTypes:    []string{"feat", "docs", "chore"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(groups) != 1 || groups[0].Type != "docs" {
		t.Fatalf("want single docs group, got %+v", groups)
	}
	if classifyCalls != 0 {
		t.Errorf("definite single-group input must NOT call Classify, got %d call(s)", classifyCalls)
	}
	if count(p.Calls, "Classify") != 0 {
		t.Errorf("definite single-group input must skip LLM, calls=%v", p.Calls)
	}
}

// TestClassifyChoreSingleGroupStillCallsLLM guards the exclusion: a single
// "chore" group is a mixed source change the LLM must be allowed to split
// into feat/fix/refactor. The definite fast path must NOT swallow it.
func TestClassifyChoreSingleGroupStillCallsLLM(t *testing.T) {
	classifyCalls := 0
	p := provider.NewFake()
	p.OnClassify = func(provider.ClassifyInput) { classifyCalls++ }
	p.ClassifyResponses = []provider.ClassifyResult{{
		Groups: []provider.Group{
			{Type: "feat", Files: []string{"internal/a.go", "internal/b.go", "internal/c.go"}},
			{Type: "fix", Files: []string{"internal/d.go", "internal/e.go", "internal/f.go"}},
		},
	}}
	// 6 plain source files → one "chore" heuristic group, but spread across
	// >5 files so isSmallHomogeneous does NOT short-circuit either.
	files := []FileChange{
		{Path: "internal/a.go"}, {Path: "internal/b.go"}, {Path: "internal/c.go"},
		{Path: "internal/d.go"}, {Path: "internal/e.go"}, {Path: "internal/f.go"},
	}
	groups, err := classifyGroups(context.Background(), p, files, ClassifyOptions{
		HybridFileLimit: 5,
		AllowedTypes:    []string{"feat", "fix", "chore"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if classifyCalls != 1 {
		t.Errorf("single chore group MUST still call Classify, got %d call(s)", classifyCalls)
	}
	if len(groups) != 2 {
		t.Fatalf("LLM split should yield 2 groups, got %+v", groups)
	}
}

// TestClassifyScopeRequiredDisablesDefiniteFastPath guards the review fix:
// heuristic groups carry no scope, so when a scope is mandatory the
// definite-kind fast path must NOT fire — otherwise a scopeless message
// would hard-fail commitlint. The same docs input that skips the LLM
// without ScopeRequired must call it WITH ScopeRequired.
func TestClassifyScopeRequiredDisablesDefiniteFastPath(t *testing.T) {
	classifyCalls := 0
	p := provider.NewFake()
	p.OnClassify = func(provider.ClassifyInput) { classifyCalls++ }
	p.ClassifyResponses = []provider.ClassifyResult{{
		Groups: []provider.Group{{Type: "docs", Scope: "api", Files: []string{
			"README.md", "docs/a.md", "docs/b.md", "docs/c.md", "docs/d.md", "docs/e.md",
		}}},
	}}
	files := []FileChange{
		{Path: "README.md"}, {Path: "docs/a.md"}, {Path: "docs/b.md"},
		{Path: "docs/c.md"}, {Path: "docs/d.md"}, {Path: "docs/e.md"},
	}
	if _, err := classifyGroups(context.Background(), p, files, ClassifyOptions{
		HybridFileLimit: 5,
		AllowedTypes:    []string{"feat", "docs", "chore"},
		ScopeRequired:   true,
	}); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if classifyCalls != 1 {
		t.Errorf("ScopeRequired must disable the definite fast path and call the LLM, got %d call(s)", classifyCalls)
	}
}

func TestClassifyLLMInvokedForDiverseSet(t *testing.T) {
	p := provider.NewFake()
	p.ClassifyResponses = []provider.ClassifyResult{{
		Groups: []provider.Group{
			{Type: "feat", Files: []string{"cmd/gk/main.go"}, Rationale: "new flag"},
			{Type: "test", Files: []string{"internal/cli/foo_test.go"}, Rationale: "coverage"},
		},
	}}
	files := []FileChange{
		{Path: "cmd/gk/main.go", Status: "modified"},
		{Path: "internal/cli/foo_test.go", Status: "added"},
	}
	groups, err := classifyGroups(context.Background(), p, files, ClassifyOptions{
		HybridFileLimit: 5, // diverse top-dirs → LLM path
		AllowedTypes:    []string{"feat", "test", "chore"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if count(p.Calls, "Classify") != 1 {
		t.Errorf("provider should be called once, calls=%v", p.Calls)
	}
	if len(groups) != 2 {
		t.Fatalf("groups: %+v", groups)
	}
}

func TestClassifyKeepsAuxiliaryTestWithFeatureGroup(t *testing.T) {
	p := provider.NewFake()
	p.ClassifyResponses = []provider.ClassifyResult{{
		Groups: []provider.Group{{
			Type:  "feat",
			Files: []string{"cmd/gk/main.go", "internal/cli/foo_test.go"},
		}},
	}}
	files := []FileChange{
		{Path: "cmd/gk/main.go"},
		{Path: "internal/cli/foo_test.go"},
	}
	groups, err := classifyGroups(context.Background(), p, files, ClassifyOptions{
		HybridFileLimit: 1, // force LLM path even for small set
		AllowedTypes:    []string{"feat", "test"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups: %+v", groups)
	}
	if groups[0].Type != "feat" {
		t.Fatalf("group type = %q, want feat", groups[0].Type)
	}
	if len(groups[0].Files) != 2 {
		t.Fatalf("files = %+v, want implementation + test together", groups[0].Files)
	}
}

func TestClassifyPathRuleOverrideMovesStandaloneTestOutOfFeat(t *testing.T) {
	p := provider.NewFake()
	p.ClassifyResponses = []provider.ClassifyResult{{
		Groups: []provider.Group{{
			Type:  "feat",
			Files: []string{"internal/cli/foo_test.go"},
		}},
	}}
	files := []FileChange{{Path: "internal/cli/foo_test.go"}}
	groups, err := classifyGroups(context.Background(), p, files, ClassifyOptions{
		HybridFileLimit: 0,
		AllowedTypes:    []string{"feat", "test"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(groups) != 1 || groups[0].Type != "test" {
		t.Fatalf("groups = %+v, want single test group", groups)
	}
}

func TestClassifyKeepsAuxiliaryDocsWithFeatureGroup(t *testing.T) {
	p := provider.NewFake()
	p.ClassifyResponses = []provider.ClassifyResult{{
		Groups: []provider.Group{{
			Type:  "feat",
			Files: []string{"internal/cli/ship.go", "README.md", "docs/commands.md"},
		}},
	}}
	files := []FileChange{
		{Path: "internal/cli/ship.go"},
		{Path: "README.md"},
		{Path: "docs/commands.md"},
	}
	groups, err := classifyGroups(context.Background(), p, files, ClassifyOptions{
		HybridFileLimit: 1,
		AllowedTypes:    []string{"feat", "docs"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(groups) != 1 || groups[0].Type != "feat" {
		t.Fatalf("groups = %+v, want single feat group", groups)
	}
}

func TestClassifyDropsDeniedFiles(t *testing.T) {
	p := provider.NewFake()
	files := []FileChange{
		{Path: ".env", DeniedBy: ".env"},
		{Path: "cmd/gk/main.go"},
	}
	groups, err := classifyGroups(context.Background(), p, files, ClassifyOptions{HeuristicOnly: true})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	for _, g := range groups {
		for _, f := range g.Files {
			if f == ".env" {
				t.Error(".env must not be forwarded to provider")
			}
		}
	}
}

func TestToProviderFilesPropagatesOrigPath(t *testing.T) {
	files := []FileChange{
		{Path: "new.go", Status: "renamed", OrigPath: "old.go", IsBinary: false},
		{Path: "regular.go", Status: "modified"},
	}
	out := toProviderFiles(files)

	if len(out) != 2 {
		t.Fatalf("want 2 files, got %d", len(out))
	}
	if out[0].OrigPath != "old.go" {
		t.Errorf("OrigPath: want %q, got %q", "old.go", out[0].OrigPath)
	}
	if out[1].OrigPath != "" {
		t.Errorf("OrigPath for regular file: want empty, got %q", out[1].OrigPath)
	}
}

// errUnexpected is returned by fake hooks that should never fire.
// TestConstrainTypes covers the fold policy directly: a type the repo's
// commit.types rejects becomes "chore" (merging with an existing chore
// group of the same scope), while an empty allow-list, an allowed type,
// or a config without chore leave the groups untouched.
func TestConstrainTypes(t *testing.T) {
	cases := []struct {
		name    string
		groups  []provider.Group
		allowed []string
		want    []string // expected group types, in order
	}{
		{
			name:    "empty-allowed-passes-through",
			groups:  []provider.Group{{Type: "build", Files: []string{"go.sum"}}},
			allowed: nil,
			want:    []string{"build"},
		},
		{
			name:    "allowed-type-untouched",
			groups:  []provider.Group{{Type: "build", Files: []string{"go.sum"}}},
			allowed: []string{"feat", "build", "chore"},
			want:    []string{"build"},
		},
		{
			name:    "disallowed-folds-to-chore",
			groups:  []provider.Group{{Type: "build", Files: []string{"go.sum"}}},
			allowed: []string{"fix", "docs", "feat", "chore"},
			want:    []string{"chore"},
		},
		{
			name: "fold-merges-into-existing-chore",
			groups: []provider.Group{
				{Type: "chore", Files: []string{"a.go"}},
				{Type: "build", Files: []string{"go.sum", "Makefile"}},
			},
			allowed: []string{"fix", "feat", "chore"},
			want:    []string{"chore"},
		},
		{
			name:    "no-chore-fallback-leaves-groups-for-failfast",
			groups:  []provider.Group{{Type: "build", Files: []string{"go.sum"}}},
			allowed: []string{"fix", "feat"},
			want:    []string{"build"},
		},
		{
			name:    "case-insensitive-allowed",
			groups:  []provider.Group{{Type: "BUILD", Files: []string{"go.sum"}}},
			allowed: []string{"Fix", "Chore"},
			want:    []string{"chore"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := constrainTypes(tc.groups, tc.allowed)
			if len(got) != len(tc.want) {
				t.Fatalf("constrainTypes = %+v, want types %v", got, tc.want)
			}
			for i, w := range tc.want {
				if !strings.EqualFold(got[i].Type, w) {
					t.Errorf("group[%d].Type = %q, want %q", i, got[i].Type, w)
				}
			}
		})
	}

	// The merge case must keep every file and note the fold in the rationale.
	merged := constrainTypes([]provider.Group{
		{Type: "chore", Files: []string{"a.go"}},
		{Type: "build", Files: []string{"go.sum"}, Rationale: "heuristic path-based"},
	}, []string{"chore"})
	if len(merged) != 1 || len(merged[0].Files) != 2 {
		t.Fatalf("merge lost files: %+v", merged)
	}
}

// TestClassifyFoldsDisallowedHeuristicType is the user-reported failure in
// miniature: a repo narrows commit.types to (fix, docs, feat, chore), yet the
// path heuristic stamps go.sum as "build" — a label the composer is
// guaranteed to reject. Classify must hand back only allowed types.
func TestClassifyFoldsDisallowedHeuristicType(t *testing.T) {
	p := provider.NewFake()
	p.ClassifyErrs = []error{errUnexpected{}}

	files := []FileChange{
		{Path: "go.sum", Status: "modified"},
		{Path: "internal/cli/root.go", Status: "modified"},
	}
	groups, err := classifyGroups(context.Background(), p, files, ClassifyOptions{
		HeuristicOnly: true,
		AllowedTypes:  []string{"fix", "docs", "feat", "chore"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	// build folds into the chore group root.go already produced — one group.
	if len(groups) != 1 || groups[0].Type != "chore" || len(groups[0].Files) != 2 {
		t.Fatalf("want one chore group holding both files, got %+v", groups)
	}
}

// TestClassifyDefiniteFastPathFoldsDisallowedBuild guards the interplay of
// fold and fast path: definiteness is judged on the RAW heuristic kind, so a
// lockfile/build-only change still skips the LLM even when "build" is not
// allowed — it just ships under the repo's catch-all type.
func TestClassifyDefiniteFastPathFoldsDisallowedBuild(t *testing.T) {
	p := provider.NewFake()
	p.ClassifyErrs = []error{errUnexpected{}}

	// 6 build-kind files (> HybridFileLimit) so isSmallHomogeneous does not
	// short-circuit; only the definite fast path can keep the LLM out.
	files := []FileChange{
		{Path: "go.sum"},
		{Path: "Makefile"},
		{Path: "Dockerfile"},
		{Path: "package-lock.json"},
		{Path: "yarn.lock"},
		{Path: "pnpm-lock.yaml"},
	}
	groups, err := classifyGroups(context.Background(), p, files, ClassifyOptions{
		HybridFileLimit: 5,
		AllowedTypes:    []string{"fix", "docs", "feat", "chore"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if count(p.Calls, "Classify") != 0 {
		t.Errorf("definite build-only input must still skip the LLM, calls=%v", p.Calls)
	}
	if len(groups) != 1 || groups[0].Type != "chore" || len(groups[0].Files) != 6 {
		t.Fatalf("want one folded chore group with 6 files, got %+v", groups)
	}
}

// TestClassifyLLMOverrideFoldsDisallowedType closes the LLM path: even when
// the model answers within the allowed set, overrideWithPathRules stamps the
// heuristic "build" back onto go.sum — the fold must catch that too.
func TestClassifyLLMOverrideFoldsDisallowedType(t *testing.T) {
	p := provider.NewFake()
	p.ClassifyResponses = []provider.ClassifyResult{{
		Groups: []provider.Group{
			{Type: "feat", Files: []string{
				"internal/a.go", "internal/b.go", "internal/c.go",
				"internal/d.go", "internal/e.go", "internal/f.go", "go.sum",
			}},
		},
	}}
	files := []FileChange{
		{Path: "internal/a.go"}, {Path: "internal/b.go"}, {Path: "internal/c.go"},
		{Path: "internal/d.go"}, {Path: "internal/e.go"}, {Path: "internal/f.go"},
		{Path: "go.sum"},
	}
	groups, err := classifyGroups(context.Background(), p, files, ClassifyOptions{
		HybridFileLimit: 5,
		AllowedTypes:    []string{"fix", "docs", "feat", "chore"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	byType := map[string][]string{}
	for _, g := range groups {
		byType[g.Type] = append(byType[g.Type], g.Files...)
	}
	if len(byType["feat"]) != 6 {
		t.Errorf("feat group should keep the 6 source files, got %+v", groups)
	}
	if len(byType["chore"]) != 1 || byType["chore"][0] != "go.sum" {
		t.Errorf("go.sum must land in a folded chore group, got %+v", groups)
	}
	if len(byType["build"]) != 0 {
		t.Errorf("no build group may survive a narrowed commit.types, got %+v", groups)
	}
}

type errUnexpected struct{}

func (errUnexpected) Error() string { return "classifier called provider when it shouldn't have" }

func count(slice []string, want string) int {
	n := 0
	for _, s := range slice {
		if s == want {
			n++
		}
	}
	return n
}
