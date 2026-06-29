package cli

import "testing"

func TestParseBisectCulprit(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"short sha", "a1b2c3d is the first bad commit\nAuthor: x", "a1b2c3d"},
		{
			"full sha amid progress",
			"running test\nBisecting: 2 revisions left to test after this\n" +
				"abc1234def5678901234567890123456789abcd0 is the first bad commit\n",
			"abc1234def5678901234567890123456789abcd0",
		},
		{"no culprit", "Bisecting: 1 revision left\nstill searching", ""},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseBisectCulprit(c.in); got != c.want {
				t.Errorf("parseBisectCulprit() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseBisectRemaining(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"Bisecting: 3 revisions left to test after this (roughly 2 steps)", 3},
		{"Bisecting: 1 revision left to test after this", 1},
		{"deadbeef is the first bad commit", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := parseBisectRemaining(c.in); got != c.want {
			t.Errorf("parseBisectRemaining(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestCountBisectSteps(t *testing.T) {
	out := "Bisecting: 3 revisions left to test after this\nrun\n" +
		"Bisecting: 1 revision left to test after this\nrun\n" +
		"deadbeef is the first bad commit"
	if got := countBisectSteps(out); got != 2 {
		t.Errorf("countBisectSteps() = %d, want 2", got)
	}
	if got := countBisectSteps("no steps here"); got != 0 {
		t.Errorf("countBisectSteps(none) = %d, want 0", got)
	}
}
