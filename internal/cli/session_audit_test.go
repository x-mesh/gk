package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/sessionaudit"
)

func TestRenderTurnGraph(t *testing.T) {
	runs := []sessionaudit.CollapsibleRun{
		{Group: "context", GkCommand: "git-kit context", Turns: []int{4, 5, 7}, TurnsSaved: 2},
		{Group: "diff", GkCommand: "git-kit diff", Turns: []int{25, 30}, TurnsSaved: 1},
		{Group: "stash", GkCommand: "git-kit stash", Turns: []int{9}, TurnsSaved: 0}, // not collapsible
	}
	var b bytes.Buffer
	renderTurnGraph(&b, runs)
	out := b.String()

	for _, want := range []string{"context", "git-kit context", "saves 2", "turns 4, 5, 7", "diff", "saves 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("turn graph missing %q in:\n%s", want, out)
		}
	}
	// each run draws one dot per turn: context (3 turns) and diff (2 turns).
	if n := dotCountOnLine(out, "context"); n != 3 {
		t.Errorf("context run dots = %d, want 3:\n%s", n, out)
	}
	if n := dotCountOnLine(out, "diff"); n != 2 {
		t.Errorf("diff run dots = %d, want 2:\n%s", n, out)
	}
	// the 0-saved run must not render.
	if strings.Contains(out, "stash") {
		t.Errorf("a run that saves no turns must not appear:\n%s", out)
	}
}

// dotCountOnLine counts ● on the first non-legend line containing label.
func dotCountOnLine(out, label string) int {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "turns") && strings.Contains(line, label) {
			return strings.Count(line, "●")
		}
	}
	return -1
}

func TestRenderTurnGraph_EmptyWhenNothingSaves(t *testing.T) {
	var b bytes.Buffer
	renderTurnGraph(&b, []sessionaudit.CollapsibleRun{{Group: "diff", Turns: []int{1}, TurnsSaved: 0}})
	if b.Len() != 0 {
		t.Errorf("no savings → no output, got %q", b.String())
	}
}

func TestRenderTrend(t *testing.T) {
	var b bytes.Buffer
	renderTrend(&b, nil, 10)
	if !strings.Contains(b.String(), "no recorded runs") {
		t.Errorf("empty trend should prompt --record, got %q", b.String())
	}

	b.Reset()
	entries := []sessionaudit.HistoryEntry{
		{Timestamp: "2026-06-30T08:00:00Z", GitTurns: 50, EstimatedTurnsSaved: 8, Rate: 0.16, AdoptionRate: 0.60},
		{Timestamp: "2026-06-30T09:00:00Z", GitTurns: 59, EstimatedTurnsSaved: 4, Rate: 0.07, AdoptionRate: 0.61},
	}
	renderTrend(&b, entries, 10)
	out := b.String()
	for _, want := range []string{"last 2 run", "saved 8/50", "saved 4/59", "saveable turns:"} {
		if !strings.Contains(out, want) {
			t.Errorf("trend missing %q in:\n%s", want, out)
		}
	}
}
