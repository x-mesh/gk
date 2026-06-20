package aicommit

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// TestComposeAll_RealGroq_DiffContextQuality is the lever #2 quality gate.
// It composes commit messages from REAL repo diffs rendered at -U3 (git's
// old default) and at -U1 (the new compose context), against the live Groq
// provider, and reports the Message.Attempts==1 rate for each. A drop in
// first-try success at -U1 vs -U3 would be a quality regression signal —
// the lower context starving the model of anchor lines.
//
// Gated on GROQ_API_KEY. Run with:
//
//	GROQ_API_KEY=… go test ./internal/aicommit -run RealGroq_DiffContext -v -count=1
func TestComposeAll_RealGroq_DiffContextQuality(t *testing.T) {
	if os.Getenv("GROQ_API_KEY") == "" {
		t.Skip("GROQ_API_KEY unset — skipping real-provider quality gate")
	}

	// Real commit ranges from this repo; each is a single-file .go change so
	// the diff is a realistic compose payload (not a synthetic one-liner).
	samples := []struct {
		name string
		rev  string
		file string
		typ  string
	}{
		{"diff_budget", "7505e05", "internal/aicommit/diff_budget.go", "feat"},
		{"land", "cc8e11e", "internal/cli/land.go", "feat"},
		{"merge", "bffeeaf", "internal/cli/merge.go", "feat"},
		{"pull_base", "8887ec3", "internal/cli/pull_base.go", "chore"},
	}

	p := provider.NewGroq()
	opts := ComposeOptions{
		MaxAttempts:      3,
		AllowedTypes:     []string{"feat", "fix", "refactor", "docs", "chore"},
		MaxSubjectLength: 72,
	}

	gitDiff := func(u, rev, file string) (string, bool) {
		// Run from the package's repo root regardless of cwd.
		root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return "", false
		}
		cmd := exec.Command("git", "-C", strings.TrimSpace(string(root)),
			"diff", u, rev+"~1", rev, "--", file)
		out, err := cmd.Output()
		if err != nil || len(out) == 0 {
			return "", false
		}
		return string(out), true
	}

	type result struct {
		first int // Attempts==1 count
		total int
	}
	run := func(u string) result {
		var r result
		for _, s := range samples {
			d, ok := gitDiff(u, s.rev, s.file)
			if !ok {
				t.Logf("[%s] %s: diff unavailable, skipping", u, s.name)
				continue
			}
			g := provider.Group{Type: s.typ, Files: []string{s.file}}
			msgs, err := ComposeAll(context.Background(), p,
				[]provider.Group{g}, map[string]string{groupKey(g): d}, opts)
			if err != nil {
				t.Logf("[%s] %s: compose error: %v", u, s.name, err)
				continue
			}
			r.total++
			if len(msgs) > 0 && msgs[0].Attempts == 1 {
				r.first++
			}
			if len(msgs) > 0 {
				t.Logf("[%s] %s: attempts=%d subject=%q", u, s.name, msgs[0].Attempts, msgs[0].Subject)
			}
		}
		return r
	}

	u3 := run("-U3")
	u1 := run("-U1")

	t.Logf("Attempts==1 rate  -U3: %d/%d", u3.first, u3.total)
	t.Logf("Attempts==1 rate  -U1: %d/%d", u1.first, u1.total)
	if u3.total == 0 || u1.total == 0 {
		t.Skip("no diffs collected (shallow clone?) — quality gate inconclusive")
	}
	// We do not hard-fail on a single-sample regression (LLM nondeterminism);
	// the numbers are logged so the operator can judge. But a total collapse
	// (-U1 produces zero first-try successes while -U3 produced some) is a
	// strong signal worth failing on.
	if u3.first > 0 && u1.first == 0 {
		t.Errorf("quality regression: -U1 first-try success collapsed to 0 (was %d/%d at -U3)", u3.first, u3.total)
	}
}
