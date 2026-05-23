package aicommit

import (
	"context"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

func classifiedPaths(groups []provider.Group) map[string]bool {
	got := map[string]bool{}
	for _, g := range groups {
		for _, p := range g.Files {
			got[p] = true
		}
	}
	return got
}

// A file the LLM omits from every group must still be classified (swept
// into a fallback group) — otherwise it is silently dropped and never
// committed, forcing the user to re-run `gk commit`.
func TestClassify_SweepsUncoveredFiles(t *testing.T) {
	// Heterogeneous top dirs force the LLM path (not the homogeneous heuristic).
	files := []FileChange{
		{Path: "src/a.go", Status: "M"},
		{Path: "web/b.js", Status: "M"},
		{Path: "extra/orphan.txt", Status: "M"},
	}
	fake := &provider.Fake{
		NameVal:     "fake",
		LocalityVal: provider.LocalityLocal,
		ClassifyResponses: []provider.ClassifyResult{{
			Groups: []provider.Group{
				{Type: "feat", Files: []string{"src/a.go", "web/b.js"}}, // orphan.txt omitted
			},
		}},
	}
	groups, err := Classify(context.Background(), fake, files, ClassifyOptions{
		AllowedTypes:    []string{"feat", "chore"},
		HybridFileLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := classifiedPaths(groups)
	for _, want := range []string{"src/a.go", "web/b.js", "extra/orphan.txt"} {
		if !got[want] {
			t.Errorf("file %q was dropped from classification: %v", want, got)
		}
	}
}

// A path the LLM invents (not among the gathered files) must be ignored, so
// a later `git commit -- <path>` never fails on a phantom path.
func TestClassify_IgnoresPhantomLLMPaths(t *testing.T) {
	files := []FileChange{
		{Path: "src/real.go", Status: "M"},
		{Path: "web/x.js", Status: "M"},
	}
	fake := &provider.Fake{
		NameVal:     "fake",
		LocalityVal: provider.LocalityLocal,
		ClassifyResponses: []provider.ClassifyResult{{
			Groups: []provider.Group{
				{Type: "feat", Files: []string{"src/real.go", "web/x.js", "ghost/phantom.go"}},
			},
		}},
	}
	groups, err := Classify(context.Background(), fake, files, ClassifyOptions{
		AllowedTypes:    []string{"feat"},
		HybridFileLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := classifiedPaths(groups)
	if got["ghost/phantom.go"] {
		t.Error("phantom path invented by the LLM should be ignored")
	}
	for _, want := range []string{"src/real.go", "web/x.js"} {
		if !got[want] {
			t.Errorf("real file %q missing from classification: %v", want, got)
		}
	}
}
