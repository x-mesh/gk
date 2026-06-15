package aicommit

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/ui"
)

type scriptedPrompter struct {
	decisions []ReviewDecision
	idx       int
}

func (s *scriptedPrompter) Prompt(_, _ int, _ Message) (ReviewDecision, error) {
	if s.idx >= len(s.decisions) {
		return ReviewDecision{}, errors.New("scripted prompter ran out of decisions")
	}
	d := s.decisions[s.idx]
	s.idx++
	return d, nil
}

func TestPrintSummaryWithStats(t *testing.T) {
	out := &bytes.Buffer{}
	messages := []Message{
		{Group: provider.Group{Type: "feat", Files: []string{"a.go", "b.go"}}, Subject: "do a"},
		{Group: provider.Group{Type: "docs", Files: []string{"README.md"}}, Subject: "note it"},
	}
	stats := map[string]FileStat{
		"a.go":      {Glyph: "M", Added: 10, Deleted: 2, Symbols: "Foo"},
		"b.go":      {Glyph: "A", Added: 30, Deleted: 0},
		"README.md": {Glyph: "M", Added: 4, Deleted: 1},
	}
	if err := printSummary(out, messages, stats); err != nil {
		t.Fatalf("printSummary: %v", err)
	}
	got := out.String()
	// Header totals: 3 files, +44 −3.
	if !strings.Contains(got, "Proposed 2 commit(s) · 3 file(s) · +44 −3") {
		t.Errorf("totals header missing:\n%s", got)
	}
	// Per-file line carries glyph, delta, and symbol.
	if !strings.Contains(got, "A  b.go") {
		t.Errorf("added-file glyph missing:\n%s", got)
	}
	if !strings.Contains(got, "+10 −2") || !strings.Contains(got, "Foo") {
		t.Errorf("delta/symbol missing:\n%s", got)
	}
	// No plain bullet when stats cover the file.
	if strings.Contains(got, "• a.go") {
		t.Errorf("expected annotated line, got plain bullet:\n%s", got)
	}
}

func TestPrintSummaryFallsBackWithoutStats(t *testing.T) {
	out := &bytes.Buffer{}
	messages := []Message{{Group: provider.Group{Type: "feat", Files: []string{"a.go"}}, Subject: "do a"}}
	if err := printSummary(out, messages, nil); err != nil {
		t.Fatalf("printSummary: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "Proposed 1 commit(s):") {
		t.Errorf("plain header missing:\n%s", got)
	}
	if !strings.Contains(got, "• a.go") {
		t.Errorf("plain bullet missing:\n%s", got)
	}
	if strings.Contains(got, "· 1 file(s)") {
		t.Errorf("totals tail should be absent without stats:\n%s", got)
	}
}

func TestReviewPlanForceSkipsPrompter(t *testing.T) {
	out := &bytes.Buffer{}
	messages := []Message{
		{Group: provider.Group{Type: "feat", Files: []string{"a.go"}}, Subject: "add a"},
		{Group: provider.Group{Type: "test", Files: []string{"a_test.go"}}, Subject: "cover a"},
	}
	decisions, err := ReviewPlan(messages, ReviewOptions{
		Out:      out,
		Force:    true,
		Prompter: nil, // would fail if invoked
	})
	if err != nil {
		t.Fatalf("ReviewPlan: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("decisions: %+v", decisions)
	}
	for i, d := range decisions {
		if !d.Keep {
			t.Errorf("decision[%d] should be Keep in force mode: %+v", i, d)
		}
	}
	if !strings.Contains(out.String(), "Proposed 2 commit(s)") {
		t.Errorf("summary missing: %q", out.String())
	}
}

func TestReviewPlanNonInteractiveWithoutForceErrors(t *testing.T) {
	_, err := ReviewPlan([]Message{{}}, ReviewOptions{NonInteractive: true})
	if !errors.Is(err, ui.ErrNonInteractive) {
		t.Errorf("want ErrNonInteractive, got %v", err)
	}
}

func TestReviewPlanPropagatesScriptedDecisions(t *testing.T) {
	out := &bytes.Buffer{}
	messages := []Message{
		{Group: provider.Group{Type: "feat", Files: []string{"a.go"}}, Subject: "keep"},
		{Group: provider.Group{Type: "chore", Files: []string{"b.go"}}, Subject: "drop me"},
	}
	script := &scriptedPrompter{decisions: []ReviewDecision{
		{Keep: true},
		{Drop: true},
	}}
	decisions, err := ReviewPlan(messages, ReviewOptions{Out: out, Prompter: script})
	if err != nil {
		t.Fatalf("ReviewPlan: %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("decisions: %+v", decisions)
	}
	if !decisions[0].Keep || decisions[0].Drop {
		t.Errorf("first decision: %+v", decisions[0])
	}
	if decisions[1].Keep || !decisions[1].Drop {
		t.Errorf("second decision: %+v", decisions[1])
	}
}

func TestReviewPlanEmptyPlanPrintsNoCommitsMessage(t *testing.T) {
	out := &bytes.Buffer{}
	decisions, err := ReviewPlan(nil, ReviewOptions{Out: out, Force: true})
	if err != nil {
		t.Fatalf("ReviewPlan: %v", err)
	}
	if len(decisions) != 0 {
		t.Errorf("decisions: %+v", decisions)
	}
	if !strings.Contains(out.String(), "no commit groups proposed") {
		t.Errorf("expected empty-state message, got %q", out.String())
	}
}
