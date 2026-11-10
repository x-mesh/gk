package gitleaksrules

import (
	"encoding/json"
	"strings"
	"testing"
)

// The matcher must agree with strings.Contains for every keyword, including
// keywords that overlap or sit inside one another.
func TestKeywordMatcher_AgreesWithContains(t *testing.T) {
	words := []string{"he", "she", "his", "hers", "ey", "key", "api", "api_key", "sk", "s."}
	var rs ruleset
	if err := json.Unmarshal(rulesJSON, &rs); err != nil {
		t.Fatal(err)
	}
	for _, r := range rs.Rules {
		words = append(words, lowerAll(r.Keywords)...)
	}
	m := newKeywordMatcher(words)
	texts := []string{
		"",
		"ushers",
		"my api_key = abc",
		"monkey business s. sk",
		"no match here",
		strings.ToLower("AWS_SECRET xoxb-123 ghp_abc glpat- hf_ sk_live_ -----begin private key"),
	}
	for _, text := range texts {
		seen := m.newSet()
		m.find(text, seen)
		for id, w := range words {
			if got, want := seen.has(int32(id)), strings.Contains(text, w); got != want {
				t.Errorf("text %q keyword %q: matcher=%v contains=%v", text, w, got, want)
			}
		}
	}
}
