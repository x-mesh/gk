package cli

import (
	"strings"
	"testing"
)

func TestComputeWIPDepths(t *testing.T) {
	isWIP := func(s string) bool { return strings.HasPrefix(s, "WIP") }
	recs := []commitRecord{
		{sha: "a", subject: "WIP: newest"}, // run of 3 — head carries depth
		{sha: "b", subject: "WIP: mid"},
		{sha: "c", subject: "WIP: oldest"},
		{sha: "d", subject: "feat: real"},
		{sha: "e", subject: "WIP: single"}, // singleton — unmapped
		{sha: "f", subject: "fix: other"},
	}
	got := computeWIPDepths(recs, isWIP)
	if got["a"] != 3 {
		t.Errorf("run head depth = %d, want 3", got["a"])
	}
	for _, sha := range []string{"b", "c", "d", "e", "f"} {
		if _, ok := got[sha]; ok {
			t.Errorf("sha %s must not carry a depth, got %d", sha, got[sha])
		}
	}
}

func TestIsBreakingCommit(t *testing.T) {
	breaking := []struct{ subject, body string }{
		{"feat!: drop v1 api", ""},
		{"refactor(core)!: rename", ""},
		{"feat: looks normal", "long body\n\nBREAKING CHANGE: removes flag"},
		{"fix: normal", "BREAKING-CHANGE: hyphen spelling"},
	}
	for _, c := range breaking {
		if !isBreakingCommit(c.subject, c.body) {
			t.Errorf("isBreakingCommit(%q, %q) = false, want true", c.subject, c.body)
		}
	}
	normal := []struct{ subject, body string }{
		{"feat: add login!", ""},          // bang not in header position
		{"feat(api): normal", "details"},
		{"breaking: not a marker", ""},    // type named breaking ≠ ! suffix
	}
	for _, c := range normal {
		if isBreakingCommit(c.subject, c.body) {
			t.Errorf("isBreakingCommit(%q, %q) = true, want false", c.subject, c.body)
		}
	}
}

func TestIsSquashSubject(t *testing.T) {
	isWIP := func(s string) bool { return strings.HasPrefix(s, "WIP") }
	for _, s := range []string{"fixup! feat: x", "squash! y", "WIP: z"} {
		if !isSquashSubject(s, isWIP) {
			t.Errorf("isSquashSubject(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"feat: fixup the parser", "refactor: squash bugs"} {
		if isSquashSubject(s, isWIP) {
			t.Errorf("isSquashSubject(%q) = true, want false", s)
		}
	}
}
