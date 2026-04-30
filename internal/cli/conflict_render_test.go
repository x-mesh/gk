package cli

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateLabelRunes_CJKSafe verifies the CJK regression — a Korean
// label longer than the rune cap must truncate at a rune boundary, not
// in the middle of a 3-byte UTF-8 sequence. Prior to the rune-aware
// fix this slice produced invalid UTF-8 in the rendered terminal.
func TestTruncateLabelRunes_CJKSafe(t *testing.T) {
	long := strings.Repeat("한글", 40) // 80 runes, 240 bytes
	got := truncateLabelRunes(long, 60)

	if !utf8.ValidString(got) {
		t.Fatalf("output is not valid UTF-8 (rune cut): %q", got)
	}
	gotRunes := utf8.RuneCountInString(got)
	if gotRunes != 60 {
		t.Errorf("rune count = %d, want 60 (59 + ellipsis)", gotRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected trailing ellipsis, got %q", got)
	}
}

func TestTruncateLabelRunes_NoTruncation(t *testing.T) {
	cases := []struct {
		in  string
		max int
	}{
		{"main", 60},
		{"메인", 60},
		{"", 60},
		{strings.Repeat("a", 60), 60}, // exactly at cap
	}
	for _, c := range cases {
		if got := truncateLabelRunes(c.in, c.max); got != c.in {
			t.Errorf("truncateLabelRunes(%q,%d) = %q, want unchanged", c.in, c.max, got)
		}
	}
}

func TestTruncateLabelRunes_AsciiBoundary(t *testing.T) {
	// 61 ASCII runes triggers truncation: 59 + "…" = 60 runes total.
	in := strings.Repeat("x", 61)
	got := truncateLabelRunes(in, 60)
	if utf8.RuneCountInString(got) != 60 {
		t.Errorf("rune count = %d, want 60", utf8.RuneCountInString(got))
	}
	if got != strings.Repeat("x", 59)+"…" {
		t.Errorf("got %q", got)
	}
}
