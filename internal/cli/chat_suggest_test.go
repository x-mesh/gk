package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// topMatch runs a lookup against the real command tree and returns the
// highest-scoring command path ("" when nothing matched).
func topMatch(t *testing.T, intent string) (string, suggestResult) {
	t.Helper()
	out, err := chatSuggestLookup(rootCmd)(context.Background(), intent)
	if err != nil {
		t.Fatalf("lookup(%q): %v", intent, err)
	}
	var res suggestResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("lookup(%q): result is not valid JSON: %v\n%s", intent, err, out)
	}
	if len(res.Matches) == 0 {
		return "", res
	}
	return res.Matches[0].Command, res
}

// The point of gk_suggest is that a suggestion names a command that exists in
// THIS build. These intents are the follow-up actions a chat answer most
// plausibly ends on.
func TestChatSuggestLookupRanksIntendedCommand(t *testing.T) {
	cases := []struct {
		intent string
		want   string
	}{
		{"delete merged branches", "gk branch clean"},
		{"branches whose upstream is gone", "gk branch clean"},
		{"list branches", "gk branch list"},
		{"find the commit that introduced a regression", "gk bisect"},
		{"search commit history for a keyword", "gk find"},
		{"restore lost work", "gk restore"},
	}
	for _, tc := range cases {
		t.Run(tc.intent, func(t *testing.T) {
			got, res := topMatch(t, tc.intent)
			if got != tc.want {
				t.Errorf("intent %q: top match = %q, want %q\nall: %+v", tc.intent, got, tc.want, res.Matches)
			}
		})
	}
}

// A suggestion must be actionable, so a match carries the flags and example
// the model would otherwise have to recall — which is exactly what it gets
// wrong when left to prior knowledge.
func TestChatSuggestLookupCarriesFlagsAndSummary(t *testing.T) {
	_, res := topMatch(t, "delete merged branches")
	m := res.Matches[0]
	if m.Summary == "" {
		t.Error("match carries no summary")
	}
	var hasGone bool
	for _, f := range m.Flags {
		if f.Flag == "--gone" {
			hasGone = true
		}
		// Global flags apply to every command and say nothing about this one.
		if f.Flag == "--repo" || f.Flag == "--json" {
			t.Errorf("inherited global flag %q leaked into notable flags", f.Flag)
		}
	}
	if !hasGone {
		t.Errorf("branch clean match is missing --gone; got %+v", m.Flags)
	}
}

// No match must be an explicit "gk has nothing here" rather than an empty
// array the model can read as "I'll fill this in myself".
func TestChatSuggestLookupNoMatchIsExplicit(t *testing.T) {
	out, err := chatSuggestLookup(rootCmd)(context.Background(), "deploy to kubernetes and order pizza")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	var res suggestResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(res.Matches) != 0 {
		t.Fatalf("expected no matches, got %+v", res.Matches)
	}
	if !strings.Contains(res.Note, "invent") {
		t.Errorf("note does not warn against inventing a command: %q", res.Note)
	}
	if !strings.Contains(out, `"matches":[]`) {
		t.Errorf("empty matches must serialize as [] not null: %s", out)
	}
}

// Hidden, deprecated, and cobra's own scaffolding are never actionable
// suggestions; a bare group command ("gk branch") has no behaviour of its own.
func TestChatSuggestSkipsNonActionableCommands(t *testing.T) {
	for _, intent := range []string{"help", "completion script for zsh", "branch"} {
		got, res := topMatch(t, intent)
		switch got {
		case "gk help", "gk completion", "gk branch":
			t.Errorf("intent %q surfaced non-actionable command %q", intent, got)
		}
		for _, m := range res.Matches {
			if strings.HasSuffix(m.Command, " help") || strings.HasSuffix(m.Command, " completion") {
				t.Errorf("intent %q surfaced scaffolding command %q", intent, m.Command)
			}
		}
	}
}

// Same question, same list — a suggestion that reshuffles between identical
// turns reads as nondeterminism in the answer itself.
func TestChatSuggestLookupIsDeterministic(t *testing.T) {
	first, err := chatSuggestLookup(rootCmd)(context.Background(), "clean up branches")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := chatSuggestLookup(rootCmd)(context.Background(), "clean up branches")
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		if again != first {
			t.Fatalf("result %d differs from first run:\n%s\n%s", i, first, again)
		}
	}
}

func TestChatSuggestLookupCapsMatches(t *testing.T) {
	// A term that appears all over the tree must still return a short list.
	_, res := topMatch(t, "commit branch file remote change")
	if len(res.Matches) > suggestMaxMatches {
		t.Errorf("got %d matches, cap is %d", len(res.Matches), suggestMaxMatches)
	}
}

func TestSuggestTermsTokenizesMixedScripts(t *testing.T) {
	got := suggestTerms("Delete merged branches — 브랜치 정리, v2")
	want := map[string]bool{"delete": true, "merged": true, "branches": true, "브랜치": true, "정리": true, "v2": true}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected token %q in %v", g, got)
		}
		delete(want, g)
	}
	if len(want) > 0 {
		t.Errorf("missing tokens %v in %v", want, got)
	}
}

// Prefix matching is what lets "branches" find "branch"; the length floor is
// what keeps "log" from matching "login"/"logic".
func TestContainsTokenPrefixRules(t *testing.T) {
	if !containsToken([]string{"branch"}, "branches") {
		t.Error(`"branches" should match token "branch"`)
	}
	if !containsToken([]string{"branches"}, "branch") {
		t.Error(`"branch" should match token "branches"`)
	}
	if containsToken([]string{"login"}, "log") {
		t.Error(`short term "log" must not prefix-match "login"`)
	}
	if !containsToken([]string{"log"}, "log") {
		t.Error(`"log" should match itself exactly`)
	}
}

func TestFirstLinesKeepsLeadingNonEmptyLines(t *testing.T) {
	got := firstLines("\n  gk a\n\n  gk b\n  gk c\n  gk d\n", 3)
	if got != "gk a\ngk b\ngk c" {
		t.Errorf("got %q", got)
	}
}
