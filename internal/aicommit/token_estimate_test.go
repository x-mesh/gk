package aicommit

import (
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

func TestEstimateComposeTokensHeuristicBypass(t *testing.T) {
	g := provider.Group{
		Type:  "build",
		Files: []string{"go.sum", "package-lock.json"},
	}
	got := EstimateComposeTokens(g, "diff --git ...", "en")
	if got != 0 {
		t.Errorf("heuristic-bypass group must report 0 tokens, got %d", got)
	}
}

func TestEstimateComposeTokensFeatGroup(t *testing.T) {
	g := provider.Group{
		Type:  "feat",
		Files: []string{"main.go"},
	}
	diff := strings.Repeat("+ var x int\n", 100) // ~1200 chars
	got := EstimateComposeTokens(g, diff, "en")
	if got <= composePromptOverhead {
		t.Errorf("expected estimate > overhead, got %d", got)
	}
	// Sanity: rough bound — overhead + ~300 tokens for the diff
	if got > 1000 {
		t.Errorf("estimate %d unexpectedly high for ~1200 char diff", got)
	}
}

func TestEstimateComposeTokensScalesWithDiff(t *testing.T) {
	g := provider.Group{Type: "feat", Files: []string{"main.go"}}
	small := EstimateComposeTokens(g, strings.Repeat("x", 1000), "en")
	big := EstimateComposeTokens(g, strings.Repeat("x", 50000), "en")
	if big <= small*5 {
		t.Errorf("estimate should scale with diff size — small=%d big=%d", small, big)
	}
}

func TestEstimateClassifyTokens(t *testing.T) {
	files := []FileChange{
		{Path: "main.go", Status: "modified"},
		{Path: "internal/foo/bar.go", Status: "added"},
	}
	got := EstimateClassifyTokens(files)
	if got <= 200 {
		t.Errorf("estimate should include overhead + per-file bytes, got %d", got)
	}
}

func TestEstimateClassifyTokensEmpty(t *testing.T) {
	got := EstimateClassifyTokens(nil)
	if got != 200 {
		t.Errorf("zero-file estimate should equal overhead, got %d", got)
	}
}
