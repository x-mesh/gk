package resolve

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

func twoHunkFile(path string) ConflictFile {
	return ConflictFile{
		Path: path,
		Segments: []Segment{
			{Context: []string{"top"}},
			{Hunk: &ConflictHunk{Ours: []string{"a=1"}, Theirs: []string{"a=2"}, OursLabel: "HEAD", TheirsLabel: "feat"}},
			{Context: []string{"mid"}},
			{Hunk: &ConflictHunk{Ours: []string{"b=1"}, Theirs: []string{"b=2"}, OursLabel: "HEAD", TheirsLabel: "feat"}},
		},
	}
}

// The confidence gate applies the sure hunk, keeps the unsure hunk's markers
// verbatim, never stages the partial file, and ships the withheld answer as
// a proposal — the agent's next move needs no extra round-trip.
func TestRun_ConfidenceGatePartialResolve(t *testing.T) {
	cf := twoHunkFile("m.go")
	content := buildConflictContent(cf)
	written := map[string][]byte{}

	r := &Resolver{
		Runner: &git.FakeRunner{Responses: map[string]git.FakeResponse{
			"status --porcelain=v2": {Stdout: buildPorcelainV2([]string{"m.go"})},
		}},
		Provider: &fakeResolveProvider{resolveRes: provider.ConflictResolutionResult{
			Resolutions: []provider.ConflictResolutionOutput{
				{Index: 0, Strategy: "theirs", Resolved: []string{"a=2"}, Rationale: "clear", Confidence: 0.95},
				{Index: 1, Strategy: "merged", Resolved: []string{"b=3"}, Rationale: "entangled", Confidence: 0.3},
			},
		}},
		ReadFile:  func(string) ([]byte, error) { return content, nil },
		WriteFile: func(p string, d []byte, _ os.FileMode) error { written[p] = d; return nil },
	}

	res, err := r.Run(context.Background(), &gitstate.State{Kind: gitstate.StateMerge}, ResolveOptions{
		Strategy: "ai", MinConfidence: 0.8, DeferStage: true, NoBackup: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Resolved) != 0 || len(res.Remaining) != 1 || res.Remaining[0] != "m.go" {
		t.Fatalf("partial file must land in Remaining only: resolved=%v remaining=%v", res.Resolved, res.Remaining)
	}
	if len(res.PendingStage) != 0 {
		t.Errorf("partial file must never be staged: %v", res.PendingStage)
	}
	if len(res.Proposals) != 1 {
		t.Fatalf("proposals = %+v, want 1", res.Proposals)
	}
	pr := res.Proposals[0]
	if pr.File != "m.go" || pr.Hunk != 2 || pr.Confidence != 0.3 || strings.Join(pr.Resolved, ",") != "b=3" {
		t.Errorf("proposal = %+v", pr)
	}

	out := string(written["m.go"])
	if !strings.Contains(out, "a=2") || strings.Contains(out, "a=1") {
		t.Errorf("confident hunk must be applied (theirs):\n%s", out)
	}
	for _, want := range []string{"<<<<<<< HEAD", "b=1", "=======", "b=2", ">>>>>>> feat"} {
		if !strings.Contains(out, want) {
			t.Errorf("unsure hunk must keep its markers (%q missing):\n%s", want, out)
		}
	}
	if strings.Contains(out, "b=3") {
		t.Errorf("withheld resolution must NOT be written:\n%s", out)
	}
}

// With every hunk above the gate, resolution proceeds exactly as before.
func TestRun_ConfidenceGateAllPass(t *testing.T) {
	cf := twoHunkFile("n.go")
	content := buildConflictContent(cf)
	written := map[string][]byte{}

	r := &Resolver{
		Runner: &git.FakeRunner{Responses: map[string]git.FakeResponse{
			"status --porcelain=v2": {Stdout: buildPorcelainV2([]string{"n.go"})},
		}},
		Provider: &fakeResolveProvider{resolveRes: provider.ConflictResolutionResult{
			Resolutions: []provider.ConflictResolutionOutput{
				{Index: 0, Strategy: "theirs", Resolved: []string{"a=2"}, Confidence: 0.9},
				{Index: 1, Strategy: "ours", Resolved: []string{"b=1"}, Confidence: 0.85},
			},
		}},
		ReadFile:  func(string) ([]byte, error) { return content, nil },
		WriteFile: func(p string, d []byte, _ os.FileMode) error { written[p] = d; return nil },
	}

	res, err := r.Run(context.Background(), &gitstate.State{Kind: gitstate.StateMerge}, ResolveOptions{
		Strategy: "ai", MinConfidence: 0.8, DeferStage: true, NoBackup: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Resolved) != 1 || len(res.Remaining) != 0 || len(res.Proposals) != 0 {
		t.Fatalf("all-pass must fully resolve: %+v", res)
	}
	if out := string(written["n.go"]); strings.Contains(out, "<<<<<<<") {
		t.Errorf("no markers expected:\n%s", out)
	}
}

// A positive gate treats an UNREPORTED confidence (0) as below it — an old
// model that ignores the field cannot slip through an opted-in gate.
func TestRun_ConfidenceGateUnreportedCountsAsBelow(t *testing.T) {
	cf := makeConflictFile("u.go")
	content := buildConflictContent(cf)
	written := map[string][]byte{}

	r := &Resolver{
		Runner: &git.FakeRunner{Responses: map[string]git.FakeResponse{
			"status --porcelain=v2": {Stdout: buildPorcelainV2([]string{"u.go"})},
		}},
		Provider: &fakeResolveProvider{resolveRes: provider.ConflictResolutionResult{
			Resolutions: []provider.ConflictResolutionOutput{
				{Index: 0, Strategy: "merged", Resolved: []string{"x"}}, // no confidence
			},
		}},
		ReadFile:  func(string) ([]byte, error) { return content, nil },
		WriteFile: func(p string, d []byte, _ os.FileMode) error { written[p] = d; return nil },
	}

	res, err := r.Run(context.Background(), &gitstate.State{Kind: gitstate.StateMerge}, ResolveOptions{
		Strategy: "ai", MinConfidence: 0.8, DeferStage: true, NoBackup: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Remaining) != 1 || len(res.Proposals) != 1 {
		t.Fatalf("unreported confidence must be withheld under a positive gate: %+v", res)
	}
}

// Side-picks are canonical: a model that claims "ours" but returns edited
// text must have its payload replaced with the hunk's actual ours lines.
func TestRun_SideTakePayloadIsCanonical(t *testing.T) {
	cf := makeConflictFile("s.go") // ours: "// ours change"
	content := buildConflictContent(cf)
	written := map[string][]byte{}

	r := &Resolver{
		Runner: &git.FakeRunner{Responses: map[string]git.FakeResponse{
			"status --porcelain=v2": {Stdout: buildPorcelainV2([]string{"s.go"})},
		}},
		Provider: &fakeResolveProvider{resolveRes: provider.ConflictResolutionResult{
			Resolutions: []provider.ConflictResolutionOutput{
				{Index: 0, Strategy: "ours", Resolved: []string{"// TAMPERED"}, Confidence: 0.99},
			},
		}},
		ReadFile:  func(string) ([]byte, error) { return content, nil },
		WriteFile: func(p string, d []byte, _ os.FileMode) error { written[p] = d; return nil },
	}

	_, err := r.Run(context.Background(), &gitstate.State{Kind: gitstate.StateMerge}, ResolveOptions{
		Strategy: "ai", DeferStage: true, NoBackup: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := string(written["s.go"])
	if strings.Contains(out, "TAMPERED") || !strings.Contains(out, "// ours change") {
		t.Errorf("side-pick must use the hunk's verbatim ours lines:\n%s", out)
	}
}

// --safe partial resolution: the provable hunk is fixed, the semantic hunk
// keeps its markers, the file stays unmerged and is reported remaining.
func TestRun_SafePartialMechanical(t *testing.T) {
	cf := ConflictFile{
		Path: "p.go",
		Segments: []Segment{
			{Hunk: &ConflictHunk{Ours: []string{"same "}, Theirs: []string{"same"}, OursLabel: "HEAD", TheirsLabel: "feat"}}, // trailing WS only
			{Context: []string{"mid"}},
			{Hunk: &ConflictHunk{Ours: []string{"x = 1"}, Theirs: []string{"x = 2"}, OursLabel: "HEAD", TheirsLabel: "feat"}},
		},
	}
	content := buildConflictContent(cf)
	written := map[string][]byte{}

	r := &Resolver{
		Runner: &git.FakeRunner{Responses: map[string]git.FakeResponse{
			"status --porcelain=v2": {Stdout: buildPorcelainV2([]string{"p.go"})},
		}},
		ReadFile:  func(string) ([]byte, error) { return content, nil },
		WriteFile: func(p string, d []byte, _ os.FileMode) error { written[p] = d; return nil },
	}

	res, err := r.Run(context.Background(), &gitstate.State{Kind: gitstate.StateMerge}, ResolveOptions{
		Strategy: StrategySafe, DeferStage: true, NoBackup: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Remaining) != 1 || len(res.Resolved) != 0 || len(res.PendingStage) != 0 {
		t.Fatalf("partial safe file must stay remaining/unstaged: %+v", res)
	}
	out := string(written["p.go"])
	if !strings.HasPrefix(out, "same ") {
		t.Errorf("trailing-WS hunk must be resolved to ours:\n%s", out)
	}
	for _, want := range []string{"<<<<<<< HEAD", "x = 1", "x = 2", ">>>>>>> feat"} {
		if !strings.Contains(out, want) {
			t.Errorf("semantic hunk must keep markers (%q):\n%s", want, out)
		}
	}
}
