package aicommit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

func TestComposeAllFirstAttemptClean(t *testing.T) {
	p := provider.NewFake()
	p.ComposeResponses = []provider.ComposeResult{
		{Subject: "add classifier", Model: "fake-v1"},
	}
	groups := []provider.Group{{Type: "feat", Files: []string{"a.go"}}}
	msgs, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
	})
	if err != nil {
		t.Fatalf("ComposeAll: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs: %+v", msgs)
	}
	if msgs[0].Attempts != 1 {
		t.Errorf("Attempts: want 1, got %d", msgs[0].Attempts)
	}
	if msgs[0].Subject != "add classifier" {
		t.Errorf("Subject: %q", msgs[0].Subject)
	}
}

func TestComposeAllRetriesOnLintFail(t *testing.T) {
	p := provider.NewFake()
	// Attempt 1: subject way too long → lint fails.
	longSubj := strings.Repeat("x", 200)
	p.ComposeResponses = []provider.ComposeResult{
		{Subject: longSubj},
		{Subject: "short and clean"},
	}
	groups := []provider.Group{{Type: "feat", Files: []string{"a.go"}}}
	msgs, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
	})
	if err != nil {
		t.Fatalf("ComposeAll: %v", err)
	}
	if msgs[0].Attempts != 2 {
		t.Errorf("Attempts: want 2 (retry), got %d", msgs[0].Attempts)
	}
	if msgs[0].Subject != "short and clean" {
		t.Errorf("Subject: %q", msgs[0].Subject)
	}
}

func TestComposeAllFailsAfterMaxAttempts(t *testing.T) {
	p := provider.NewFake()
	// All three attempts return lint-violating subjects.
	badSubj := strings.Repeat("y", 200)
	p.ComposeResponses = []provider.ComposeResult{
		{Subject: badSubj},
		{Subject: badSubj},
		{Subject: badSubj},
	}
	groups := []provider.Group{{Type: "feat", Files: []string{"a.go"}}}
	_, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		MaxAttempts:      3,
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
	})
	if err == nil {
		t.Fatal("want error after max retries")
	}
	if !strings.Contains(err.Error(), "commitlint failed after 3 attempts") {
		t.Errorf("err: %v", err)
	}
}

func TestComposeAllFeedsRetryContext(t *testing.T) {
	p := provider.NewFake()
	var capturedAttempts [][]provider.AttemptFeedback
	p.OnCompose = func(in provider.ComposeInput) {
		capturedAttempts = append(capturedAttempts, in.PreviousAttempts)
	}
	p.ComposeResponses = []provider.ComposeResult{
		{Subject: strings.Repeat("z", 200)}, // triggers retry
		{Subject: "clean subject"},
	}
	groups := []provider.Group{{Type: "feat", Files: []string{"a.go"}}}
	_, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
	})
	if err != nil {
		t.Fatalf("ComposeAll: %v", err)
	}
	if len(capturedAttempts) != 2 {
		t.Fatalf("compose invocations: %d", len(capturedAttempts))
	}
	if len(capturedAttempts[0]) != 0 {
		t.Errorf("first call should have no history, got %+v", capturedAttempts[0])
	}
	if len(capturedAttempts[1]) != 1 {
		t.Fatalf("second call should have 1 history entry, got %d", len(capturedAttempts[1]))
	}
	if !strings.Contains(strings.Join(capturedAttempts[1][0].Issues, " "), "subject-max-length") {
		t.Errorf("issues not threaded into retry: %+v", capturedAttempts[1][0].Issues)
	}
}

// TestComposeAllPreservesOrderConcurrent guards the parallel fan-out:
// even though groups are composed concurrently, out[i] must correspond
// to groups[i]. Run under `go test -race` to also catch data races on
// the shared output slice / provider cursors.
func TestComposeAllPreservesOrderConcurrent(t *testing.T) {
	p := provider.NewFake()
	p.ComposeResponses = []provider.ComposeResult{
		{Subject: "first"},
		{Subject: "second"},
		{Subject: "third"},
	}
	groups := []provider.Group{
		{Type: "feat", Files: []string{"a.go"}},
		{Type: "fix", Files: []string{"b.go"}},
		{Type: "docs", Files: []string{"c.md"}},
	}
	msgs, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"feat", "fix", "docs"},
		MaxSubjectLength: 72,
	})
	if err != nil {
		t.Fatalf("ComposeAll: %v", err)
	}
	if len(msgs) != len(groups) {
		t.Fatalf("len(msgs)=%d, want %d", len(msgs), len(groups))
	}
	for i, g := range groups {
		if msgs[i].Group.Type != g.Type {
			t.Errorf("msgs[%d].Group.Type=%q, want %q (order not preserved)", i, msgs[i].Group.Type, g.Type)
		}
	}
}

// TestComposeAllHeuristicSlotAlignment verifies that an inline heuristic
// group (no LLM round-trip) lands in its original index alongside the
// concurrently-composed LLM groups.
func TestComposeAllHeuristicSlotAlignment(t *testing.T) {
	p := provider.NewFake()
	p.ComposeResponses = []provider.ComposeResult{
		{Subject: "implement a"},
		{Subject: "fix b"},
	}
	groups := []provider.Group{
		{Type: "feat", Files: []string{"a.go"}},    // LLM
		{Type: "build", Files: []string{"go.sum"}}, // heuristic — no LLM
		{Type: "fix", Files: []string{"b.go"}},     // LLM
	}
	msgs, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"feat", "build", "fix"},
		MaxSubjectLength: 72,
	})
	if err != nil {
		t.Fatalf("ComposeAll: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("len(msgs)=%d, want 3", len(msgs))
	}
	if msgs[1].Model != heuristicModel {
		t.Errorf("msgs[1].Model=%q, want %q (heuristic group lost its slot)", msgs[1].Model, heuristicModel)
	}
	if msgs[0].Group.Type != "feat" || msgs[2].Group.Type != "fix" {
		t.Errorf("LLM groups misaligned: msgs[0]=%q msgs[2]=%q", msgs[0].Group.Type, msgs[2].Group.Type)
	}
	// The heuristic group must NOT have consumed an LLM call.
	composeCalls := 0
	for _, c := range p.Calls {
		if c == "Compose" {
			composeCalls++
		}
	}
	if composeCalls != 2 {
		t.Errorf("Compose calls=%d, want 2 (heuristic should skip the LLM)", composeCalls)
	}
}

// TestComposeConcurrency pins the worker-limit resolution: configured
// <= 0 falls back to the default, the result never exceeds the group
// count, and it is always >= 1 (errgroup.SetLimit(0) would deadlock).
func TestComposeConcurrency(t *testing.T) {
	cases := []struct {
		groupCount, configured, want int
	}{
		{groupCount: 10, configured: 0, want: DefaultComposeConcurrency}, // default
		{groupCount: 10, configured: 2, want: 2},                         // configured wins
		{groupCount: 3, configured: 8, want: 3},                          // clamp to groups
		{groupCount: 1, configured: 0, want: 1},                          // single group
		{groupCount: 5, configured: -1, want: DefaultComposeConcurrency}, // negative → default
	}
	for _, c := range cases {
		if got := composeConcurrency(c.groupCount, c.configured); got != c.want {
			t.Errorf("composeConcurrency(%d, %d)=%d, want %d", c.groupCount, c.configured, got, c.want)
		}
	}
}

// TestComposeAllSingleGroupFastPath documents that a lone LLM group takes
// the no-errgroup path and composes exactly once.
func TestComposeAllSingleGroupFastPath(t *testing.T) {
	p := provider.NewFake()
	p.ComposeResponses = []provider.ComposeResult{{Subject: "do the thing"}}
	groups := []provider.Group{{Type: "feat", Files: []string{"a.go"}}}
	msgs, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
	})
	if err != nil {
		t.Fatalf("ComposeAll: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Subject != "do the thing" {
		t.Fatalf("msgs: %+v", msgs)
	}
	if count(p.Calls, "Compose") != 1 {
		t.Errorf("Compose calls=%d, want 1", count(p.Calls, "Compose"))
	}
}

// TestComposeAllWarmCachePreservesContract verifies the warm-up path
// (first group synchronous, rest parallel) still preserves input order
// and composes every group exactly once.
func TestComposeAllWarmCachePreservesContract(t *testing.T) {
	p := provider.NewFake()
	p.ComposeResponses = []provider.ComposeResult{
		{Subject: "one"}, {Subject: "two"}, {Subject: "three"},
	}
	groups := []provider.Group{
		{Type: "feat", Files: []string{"a.go"}},
		{Type: "fix", Files: []string{"b.go"}},
		{Type: "refactor", Files: []string{"c.go"}},
	}
	msgs, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"feat", "fix", "refactor"},
		MaxSubjectLength: 72,
		WarmCache:        true,
		Concurrency:      2,
	})
	if err != nil {
		t.Fatalf("ComposeAll: %v", err)
	}
	for i, g := range groups {
		if msgs[i].Group.Type != g.Type {
			t.Errorf("msgs[%d].Group.Type=%q, want %q", i, msgs[i].Group.Type, g.Type)
		}
	}
	if count(p.Calls, "Compose") != 3 {
		t.Errorf("Compose calls=%d, want 3", count(p.Calls, "Compose"))
	}
}

func TestComposeAllProviderErrorBubbles(t *testing.T) {
	p := provider.NewFake()
	p.ComposeErrs = []error{errors.New("provider down")}
	groups := []provider.Group{{Type: "feat", Files: []string{"a.go"}}}
	_, err := ComposeAll(context.Background(), p, groups, nil, ComposeOptions{
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
	})
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "provider down") {
		t.Errorf("err: %v", err)
	}
}
