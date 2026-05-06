package forget

import (
	"strings"
	"testing"
)

func TestParseBarMode(t *testing.T) {
	cases := []struct {
		in   string
		want BarMode
		err  bool
	}{
		{"", BarAuto, false},
		{"auto", BarAuto, false},
		{"AUTO", BarAuto, false},
		{"filled", BarFilled, false},
		{"block", BarBlock, false},
		{"none", BarNone, false},
		{"plain", BarNone, false},
		{"off", BarNone, false},
		{"weird", BarAuto, true},
	}
	for _, tc := range cases {
		got, err := ParseBarMode(tc.in)
		if (err != nil) != tc.err {
			t.Errorf("ParseBarMode(%q) error=%v, wantErr=%v", tc.in, err, tc.err)
		}
		if !tc.err && got != tc.want {
			t.Errorf("ParseBarMode(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestRenderAuditEmpty(t *testing.T) {
	out := RenderAudit(nil, RenderOpts{Mode: BarNone, Width: 100})
	if !strings.Contains(out, "no blobs") {
		t.Errorf("empty render = %q, want a no-blobs message", out)
	}
}

func TestRenderAuditPlainHasFlag(t *testing.T) {
	entries := []AuditEntry{
		{Path: "src/foo", UniqueBlobs: 2, TotalBytes: 1024, LargestBytes: 512, InHEAD: true},
		{Path: "ghost/old", UniqueBlobs: 1, TotalBytes: 256, LargestBytes: 256, InHEAD: false},
	}
	out := RenderAudit(entries, RenderOpts{Mode: BarNone, Width: 120})
	if !strings.Contains(out, "src/foo") {
		t.Errorf("missing src/foo in output:\n%s", out)
	}
	if !strings.Contains(out, "(history-only)") {
		t.Errorf("history-only entry not flagged:\n%s", out)
	}
}

func TestRenderAuditBlockBar(t *testing.T) {
	entries := []AuditEntry{
		{Path: "big", UniqueBlobs: 1, TotalBytes: 1000, LargestBytes: 1000, InHEAD: true},
		{Path: "small", UniqueBlobs: 1, TotalBytes: 250, LargestBytes: 250, InHEAD: true},
	}
	out := RenderAudit(entries, RenderOpts{Mode: BarBlock, Width: 120})
	// Both rows should carry block glyphs; the heaviest row should
	// contain a full-width bar (multiple full-block chars in a row).
	if !strings.Contains(out, "█") {
		t.Errorf("block mode output missing block char:\n%s", out)
	}
	// The smaller row must include the empty-cell glyph somewhere.
	if !strings.Contains(out, "░") {
		t.Errorf("block mode output missing empty-cell char (proportions broken?):\n%s", out)
	}
}

func TestRenderAuditFilledIncludesLabel(t *testing.T) {
	// We do not assert ANSI escape presence here because lipgloss
	// strips colour when the test runner is not a TTY — the rendered
	// output collapses to a plain string. What we *can* assert is
	// that the label content survives intact, including the
	// history-only marker used for monochrome distinction.
	entries := []AuditEntry{
		{Path: "src/foo", UniqueBlobs: 2, TotalBytes: 1024, LargestBytes: 1024, InHEAD: true},
		{Path: "ghost/leftover", UniqueBlobs: 1, TotalBytes: 256, LargestBytes: 256, InHEAD: false},
	}
	out := RenderAudit(entries, RenderOpts{Mode: BarFilled, Width: 100, NoColor: true})
	if !strings.Contains(out, "src/foo") {
		t.Errorf("filled label missing path src/foo:\n%s", out)
	}
	if !strings.Contains(out, "(history)") {
		t.Errorf("filled label missing history marker for ghost row:\n%s", out)
	}
}

func TestTruncatePathMiddleEllipsis(t *testing.T) {
	got := truncatePath("aaaaa/bbbbb/ccccc/ddddd/eeeee.txt", 16)
	if !strings.Contains(got, "…") {
		t.Errorf("truncatePath should middle-ellipsis long paths; got %q", got)
	}
	if len([]rune(got)) > 16 {
		t.Errorf("truncatePath %q exceeds width 16", got)
	}
}

func TestTruncatePathPassthroughShort(t *testing.T) {
	got := truncatePath("short", 50)
	if got != "short" {
		t.Errorf("truncatePath unchanged short = %q, want short", got)
	}
}

func TestBlockBarSubCell(t *testing.T) {
	// 0.50 with width 8 should produce four full blocks; 0.0 produces
	// none; 1.0 produces all eight.
	if got := blockBar(0.5, 8); !strings.HasPrefix(got, "████") {
		t.Errorf("blockBar(0.5, 8) = %q, want 4 full blocks first", got)
	}
	if got := blockBar(0, 4); strings.Contains(got, "█") {
		t.Errorf("blockBar(0, 4) = %q, want no full blocks", got)
	}
	if got := blockBar(1, 4); !strings.Contains(got, "████") {
		t.Errorf("blockBar(1, 4) = %q, want full bar", got)
	}
}
