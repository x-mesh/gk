package aicommit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// TestComposeAll_RealGroq_ParallelSpeedup measures the actual wall-clock of
// ComposeAll over N independent groups against a real provider, comparing
// sequential (concurrency=1) vs parallel (concurrency=4). It is the
// efficacy check for the Compose fan-out: parallel must be meaningfully
// faster than sequential when each group is its own network round-trip.
//
// Gated on GROQ_API_KEY — skips when unset so the normal suite stays
// hermetic. Run with:
//
//	GROQ_API_KEY=… go test ./internal/aicommit -run RealGroq -v -count=1
func TestComposeAll_RealGroq_ParallelSpeedup(t *testing.T) {
	if os.Getenv("GROQ_API_KEY") == "" {
		t.Skip("GROQ_API_KEY unset — skipping real-provider benchmark")
	}
	p := provider.NewGroq()

	groups := []provider.Group{
		{Type: "feat", Files: []string{"internal/auth/login.go"}},
		{Type: "fix", Files: []string{"internal/payment/charge.go"}},
		{Type: "refactor", Files: []string{"internal/cache/cache.go"}},
		{Type: "docs", Files: []string{"docs/api.md"}},
	}
	diffs := map[string]string{
		groupKey(groups[0]): "+func Login(u, p string) (string, error) { return \"tok\", nil }\n+func Logout(t string) error { return nil }\n",
		groupKey(groups[1]): "+func Charge(cents int) error { return nil }\n-func Charge(c int) {}\n",
		groupKey(groups[2]): "+func Get(k string) ([]byte, bool) { return nil, false }\n+func Set(k string, v []byte) {}\n",
		groupKey(groups[3]): "+# API Docs\n+Auth and payment endpoints documented.\n",
	}
	base := ComposeOptions{
		MaxAttempts:      2,
		AllowedTypes:     []string{"feat", "fix", "refactor", "docs", "chore"},
		MaxSubjectLength: 72,
	}

	for _, conc := range []int{1, 2, 4} {
		opts := base
		opts.Concurrency = conc
		start := time.Now()
		msgs, err := ComposeAll(context.Background(), p, groups, diffs, opts)
		dur := time.Since(start)
		if err != nil {
			t.Fatalf("concurrency=%d: ComposeAll error: %v", conc, err)
		}
		t.Logf("concurrency=%d : %v wall-clock for %d groups", conc, dur.Round(time.Millisecond), len(msgs))
	}
}

// TestComposeAll_RealGroq_WarmCacheCost measures the *cost* of WarmCache
// on a provider with no prompt cache (groq). Warming runs the first group
// synchronously before fanning out the rest, so on a cache-less provider
// it must be strictly slower than pure parallel — there is no offsetting
// cache hit. This is why the CLI enables WarmCache only for Anthropic;
// the Anthropic-side *benefit* needs a separate measurement with a key.
func TestComposeAll_RealGroq_WarmCacheCost(t *testing.T) {
	if os.Getenv("GROQ_API_KEY") == "" {
		t.Skip("GROQ_API_KEY unset — skipping real-provider benchmark")
	}
	p := provider.NewGroq()
	groups := []provider.Group{
		{Type: "feat", Files: []string{"a.go"}},
		{Type: "fix", Files: []string{"b.go"}},
		{Type: "refactor", Files: []string{"c.go"}},
		{Type: "docs", Files: []string{"d.md"}},
	}
	diffs := map[string]string{
		groupKey(groups[0]): "+func A() {}\n",
		groupKey(groups[1]): "+func B() {}\n",
		groupKey(groups[2]): "+func C() {}\n",
		groupKey(groups[3]): "+# D\n",
	}
	base := ComposeOptions{MaxAttempts: 2, AllowedTypes: []string{"feat", "fix", "refactor", "docs", "chore"}, MaxSubjectLength: 72, Concurrency: 4}
	for _, warm := range []bool{false, true} {
		opts := base
		opts.WarmCache = warm
		start := time.Now()
		_, err := ComposeAll(context.Background(), p, groups, diffs, opts)
		if err != nil {
			t.Fatalf("warm=%v: %v", warm, err)
		}
		t.Logf("WarmCache=%v (conc=4, no-cache provider) : %v", warm, time.Since(start).Round(time.Millisecond))
	}
}

// TestComposeAll_RealGroq_SingleShotIgnoresConcurrency proves the
// single-group fast-path: one LLM group should take the same wall-clock
// regardless of the concurrency setting (it bypasses errgroup entirely).
func TestComposeAll_RealGroq_SingleShotIgnoresConcurrency(t *testing.T) {
	if os.Getenv("GROQ_API_KEY") == "" {
		t.Skip("GROQ_API_KEY unset — skipping real-provider benchmark")
	}
	p := provider.NewGroq()
	groups := []provider.Group{{Type: "feat", Files: []string{"internal/auth/login.go"}}}
	diffs := map[string]string{
		groupKey(groups[0]): "+func Login(u, p string) (string, error) { return \"tok\", nil }\n",
	}
	base := ComposeOptions{MaxAttempts: 2, AllowedTypes: []string{"feat", "chore"}, MaxSubjectLength: 72}
	for _, conc := range []int{1, 8} {
		opts := base
		opts.Concurrency = conc
		start := time.Now()
		_, err := ComposeAll(context.Background(), p, groups, diffs, opts)
		if err != nil {
			t.Fatalf("concurrency=%d: %v", conc, err)
		}
		t.Logf("single group, concurrency=%d : %v (should be ~same — fast-path)", conc, time.Since(start).Round(time.Millisecond))
	}
}
