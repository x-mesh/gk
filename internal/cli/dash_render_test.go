package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/ui"
)

const dashLongTitle = "refactor(tm-agent): move the wait command into tm_agent/wait.py and keep the old entry point"

func renderSnapshot(t *testing.T, snap dashSnapshot, width int) string {
	t.Helper()
	var buf bytes.Buffer
	renderDash(&buf, snap, width)
	return buf.String()
}

func dashTableSnapshot(scope, repo string) dashSnapshot {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	return dashSnapshot{
		Scope: scope, FetchedAt: now,
		PRs: dashPRSection{Counts: []dashCount{{Relation: dashRelMine, Total: 1, Shown: 1}}, Items: []dashPRRow{{
			Number: 897, Title: dashLongTitle, Repo: repo, Relation: dashRelMine, State: "OPEN",
			Review: dashReview{Source: dashReviewNone}, CI: dashCI{State: dashCIFailure},
			Action: dashAction{Rule: 3, Text: "CI failed: lint"},
		}}},
		Issues: dashIssueSection{Counts: []dashCount{{Relation: dashRelMentioned, Total: 1, Shown: 1}}, Items: []dashIssueRow{{
			Number: 74, Title: "rca-agent process_tree output", Repo: repo, Relation: dashRelMentioned,
			Action: dashAction{Rule: 3, Text: "reply needed"}, UpdatedAt: now.Add(-time.Hour),
		}}},
	}
}

func TestRenderDashTitleUsesTheWidth(t *testing.T) {
	setNoColor(t, true, func() {})
	snap := dashTableSnapshot("repo:x-mesh/gk", "x-mesh/gk")
	if wide := renderSnapshot(t, snap, 200); !strings.Contains(wide, dashLongTitle) {
		t.Errorf("a 200-column table cut the title:\n%s", wide)
	}
	narrow := renderSnapshot(t, snap, 80)
	if strings.Contains(narrow, dashLongTitle) || !strings.Contains(narrow, "refactor(tm-agent)") || !strings.Contains(narrow, "…") {
		t.Errorf("an 80-column table did not shorten the title:\n%s", narrow)
	}
	for _, line := range strings.Split(narrow, "\n") {
		if strings.Contains(line, "#897") && len([]rune(line)) > 80 {
			t.Errorf("row is %d columns at width 80: %q", len([]rune(line)), line)
		}
	}
}

func TestRenderDashOrgShowsTheRepo(t *testing.T) {
	setNoColor(t, true, func() {})
	org := renderSnapshot(t, dashTableSnapshot("org:x-mesh", "x-mesh/tm-agent"), 200)
	if !strings.Contains(org, "repo") || !strings.Contains(org, "tm-agent") || strings.Contains(org, "x-mesh/tm-agent") {
		t.Errorf("org table has no short repo column:\n%s", org)
	}
	repo := renderSnapshot(t, dashTableSnapshot("repo:x-mesh/gk", "x-mesh/gk"), 200)
	for _, line := range strings.Split(repo, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#  ") && strings.Contains(line, "repo") {
			t.Errorf("repo table has a repo column: %q", line)
		}
	}
}

func TestRenderDashColors(t *testing.T) {
	setNoColor(t, false, func() {})
	out := renderSnapshot(t, dashTableSnapshot("repo:x-mesh/gk", "x-mesh/gk"), 200)
	for name, want := range map[string]string{
		"failed check in bold red": cellRedBold("CI failed: lint"),
		"reply in yellow":          cellYellow("reply needed"),
		"mine in blue":             cellBlue("mine"),
		"mention in magenta":       cellMagenta("mentioned"),
		"number in cyan":           cellCyan("#897"),
		"a fresh update in green":  cellGreen("1h ago"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%s: output has no %q", name, want)
		}
	}
	if plain := ui.StripANSI(out); !strings.Contains(plain, "mine 1") {
		t.Errorf("header counts lost:\n%s", plain)
	}
}

// Text from other people's PRs never reaches the terminal as a control
// sequence.
func TestRenderDashEscapesControlCharacters(t *testing.T) {
	setNoColor(t, true, func() {})
	snap := dashTableSnapshot("repo:x-mesh/gk", "x-mesh/gk")
	snap.PRs.Items[0].Title = "evil\x1b]0;pwned\x07title"
	snap.PRs.Items[0].Action = dashAction{Rule: 2, Text: "conflicts with main: a\x1b[2Jb.go"}
	snap.Forecast = []dashForecastJSON{{Base: "main", Total: 2, Considered: 2,
		Pairs: []dashForecastPairJSON{{A: 1, B: 2, Files: []string{"x\x1b[31m\ny.go"}}}}}
	out := renderSnapshot(t, snap, 200)
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("output carries a control character: %q", out)
	}
	for _, want := range []string{"evil?]0;pwned?title", "a?[2Jb.go", "x?[31m?y.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("output has no %q:\n%s", want, out)
		}
	}
	if snap.PRs.Items[0].Title != "evil\x1b]0;pwned\x07title" {
		t.Errorf("rendering changed the snapshot")
	}
}
