package aicommit

import (
	"context"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

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

func TestClassifyHeuristicOnlyShortCircuitsLLM(t *testing.T) {
	p := provider.NewFake()
	// Rig fake to misbehave if it's called.
	p.ClassifyErrs = []error{errUnexpected{}}

	files := []FileChange{
		{Path: "internal/cli/root_test.go", Status: "modified"},
		{Path: "internal/cli/root.go", Status: "modified"},
	}
	groups, err := Classify(context.Background(), p, files, ClassifyOptions{
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
	groups, err := Classify(context.Background(), p, files, ClassifyOptions{
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
	groups, err := Classify(context.Background(), p, files, ClassifyOptions{
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

func TestClassifyPathRuleOverrideMovesTestOutOfFeat(t *testing.T) {
	p := provider.NewFake()
	// Provider deliberately lumps a test file into feat.
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
	groups, err := Classify(context.Background(), p, files, ClassifyOptions{
		HybridFileLimit: 1, // force LLM path even for small set
		AllowedTypes:    []string{"feat", "test"},
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	// After override: one feat group with main.go, one test group with *_test.go
	var featFiles, testFiles []string
	for _, g := range groups {
		switch g.Type {
		case "feat":
			featFiles = g.Files
		case "test":
			testFiles = g.Files
		}
	}
	if len(featFiles) != 1 || featFiles[0] != "cmd/gk/main.go" {
		t.Errorf("feat group files: %+v", featFiles)
	}
	if len(testFiles) != 1 || testFiles[0] != "internal/cli/foo_test.go" {
		t.Errorf("test group files: %+v (want foo_test.go moved here)", testFiles)
	}
}

func TestClassifyDropsDeniedFiles(t *testing.T) {
	p := provider.NewFake()
	files := []FileChange{
		{Path: ".env", DeniedBy: ".env"},
		{Path: "cmd/gk/main.go"},
	}
	groups, err := Classify(context.Background(), p, files, ClassifyOptions{HeuristicOnly: true})
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

// errUnexpected is returned by fake hooks that should never fire.
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
