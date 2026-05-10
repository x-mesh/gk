package ui

import (
	"strings"
	"testing"

	"github.com/fatih/color"
)

// withNoColor flips color.NoColor to true for the duration of the test
// so the assertion targets the structural output without ANSI bytes.
// The previous value is restored on cleanup so neighbouring tests are
// not affected.
func withNoColor(t *testing.T) {
	t.Helper()
	prev := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prev })
}

func TestRenderSection_Bar_NoSummary(t *testing.T) {
	withNoColor(t)
	got := RenderSection("branch", "", []string{"main → origin/main  ↑3 ↓0"}, SectionOpts{
		Layout: SectionLayoutBar,
	})
	want := "█  BRANCH\n" +
		"   main → origin/main  ↑3 ↓0\n" +
		"\n"
	if got != want {
		t.Fatalf("RenderSection bar mismatch\n--- got\n%s--- want\n%s", got, want)
	}
}

func TestRenderSection_Bar_WithSummary(t *testing.T) {
	// In bar layout the summary sits inline with the title — three
	// spaces of gap, dim styling. The body remains below.
	withNoColor(t)
	got := RenderSection("branch", "main → origin/main  ↑3 ↓0", nil, SectionOpts{
		Layout: SectionLayoutBar,
	})
	want := "█  BRANCH" + sectionSummaryGap + "main → origin/main  ↑3 ↓0\n" +
		"\n"
	if got != want {
		t.Fatalf("RenderSection bar with summary mismatch\n--- got\n%q\n--- want\n%q", got, want)
	}
}

func TestRenderSection_Bar_SummaryAndBody(t *testing.T) {
	withNoColor(t)
	got := RenderSection("activity", "53 commits", []string{"▃ ▁ █", "M T W"}, SectionOpts{
		Layout: SectionLayoutBar,
	})
	want := "█  ACTIVITY" + sectionSummaryGap + "53 commits\n" +
		"   ▃ ▁ █\n" +
		"   M T W\n" +
		"\n"
	if got != want {
		t.Fatalf("bar summary+body mismatch\n--- got\n%q\n--- want\n%q", got, want)
	}
}

func TestRenderSection_Rule_NoSummary(t *testing.T) {
	withNoColor(t)
	got := RenderSection("working tree", "", []string{" M file.go", "?? scratch.txt"}, SectionOpts{
		Layout: SectionLayoutRule,
		Width:  40,
	})
	// Header: "── " + "WORKING TREE" + " " + 40-4-12=24 dashes.
	// Body indent is 3 spaces (matches bar layout).
	want := "── WORKING TREE " + strings.Repeat("─", 24) + "\n" +
		"    M file.go\n" +
		"   ?? scratch.txt\n" +
		"\n"
	if got != want {
		t.Fatalf("RenderSection rule mismatch\n--- got\n%s--- want\n%s", got, want)
	}
}

func TestRenderSection_Rule_WithSummary(t *testing.T) {
	// In rule layout the summary becomes the first body line (dim) so
	// the rule itself stays clean. This keeps the rule's visual width
	// independent of the headline length.
	withNoColor(t)
	got := RenderSection("activity", "53 commits", []string{"▃ ▁ █"}, SectionOpts{
		Layout: SectionLayoutRule,
		Width:  40,
	})
	want := "── ACTIVITY " + strings.Repeat("─", 40-4-8) + "\n" +
		"   53 commits\n" +
		"   ▃ ▁ █\n" +
		"\n"
	if got != want {
		t.Fatalf("rule with summary mismatch\n--- got\n%q\n--- want\n%q", got, want)
	}
}

func TestRenderSection_Rule_AutoWidthClampsToMin(t *testing.T) {
	withNoColor(t)
	// Width=10 falls below sectionRuleMin (32) and should be lifted.
	got := RenderSection("x", "", nil, SectionOpts{
		Layout: SectionLayoutRule,
		Width:  10,
	})
	// 32 - 4 - 1 = 27 dashes
	want := "── X " + strings.Repeat("─", 27) + "\n\n"
	if got != want {
		t.Fatalf("rule did not clamp to min width\n--- got\n%q\n--- want\n%q", got, want)
	}
}

func TestRenderSection_BodyLinesPreservedVerbatim(t *testing.T) {
	// Body containing wide CJK + emoji + ANSI bytes must be emitted
	// without padding or truncation. This is the property the box
	// layout violated and the user complained about.
	withNoColor(t)
	body := []string{
		"한글 라벨: ↑3 ↓0",
		"🚀 deploy ready",
		"plain ascii",
	}
	got := RenderSection("status", "", body, SectionOpts{Layout: SectionLayoutBar})
	for _, ln := range body {
		if !strings.Contains(got, sectionIndent+ln+"\n") {
			t.Errorf("body line not preserved verbatim: %q\nfull:\n%s", ln, got)
		}
	}
}

func TestRenderSection_EmptyAll(t *testing.T) {
	withNoColor(t)
	got := RenderSection("clean", "", nil, SectionOpts{Layout: SectionLayoutBar})
	want := "█  CLEAN\n\n"
	if got != want {
		t.Fatalf("empty body mismatch\n--- got\n%q\n--- want\n%q", got, want)
	}
}

func TestRenderSection_TrailingBlankLineSeparatesSections(t *testing.T) {
	// Stacking two sections must produce exactly one blank line
	// between them — the header of the second section directly
	// follows the trailing newline of the first.
	withNoColor(t)
	a := RenderSection("a", "", []string{"x"}, SectionOpts{Layout: SectionLayoutBar})
	b := RenderSection("b", "", []string{"y"}, SectionOpts{Layout: SectionLayoutBar})
	got := a + b
	want := "█  A\n   x\n\n" +
		"█  B\n   y\n\n"
	if got != want {
		t.Fatalf("stacking mismatch\n--- got\n%q\n--- want\n%q", got, want)
	}
}

func TestRenderNextAction_Bar(t *testing.T) {
	// next goes inline with the title (summary slot); why becomes the
	// body. This inverts the legacy "→ next / why: …" pair so the
	// command is the very first thing the eye lands on.
	withNoColor(t)
	got := RenderNextAction("gk push", "3 commits ahead of origin/main", SectionOpts{
		Layout: SectionLayoutBar,
	})
	want := "█  NEXT" + sectionSummaryGap + "→ gk push\n" +
		"   why: 3 commits ahead of origin/main\n" +
		"\n"
	if got != want {
		t.Fatalf("next action bar mismatch\n--- got\n%s--- want\n%s", got, want)
	}
}

func TestRenderNextAction_OmitsWhyWhenEmpty(t *testing.T) {
	withNoColor(t)
	got := RenderNextAction("gk fetch", "", SectionOpts{Layout: SectionLayoutBar})
	want := "█  NEXT" + sectionSummaryGap + "→ gk fetch\n\n"
	if got != want {
		t.Fatalf("next action without why mismatch\n--- got\n%q\n--- want\n%q", got, want)
	}
}

func TestRenderNextAction_Rule(t *testing.T) {
	withNoColor(t)
	got := RenderNextAction("gk push", "ahead", SectionOpts{
		Layout: SectionLayoutRule,
		Width:  40,
	})
	want := "── NEXT " + strings.Repeat("─", 40-4-4) + "\n" +
		"   → gk push\n" +
		"   why: ahead\n" +
		"\n"
	if got != want {
		t.Fatalf("next action rule mismatch\n--- got\n%s--- want\n%s", got, want)
	}
}

func TestSectionColor_KnownNames(t *testing.T) {
	// The registry maps gk-wide section vocabulary to specific intents.
	// Locking the mapping prevents accidental drift — a `divergence`
	// section in `gk pull` must keep showing up in violet, otherwise
	// the cross-command muscle memory breaks.
	cases := map[string]*color.Color{
		"branch":               SectionInfo,
		"BRANCH":               SectionInfo,
		"  branch  ":           SectionInfo,
		"working tree":         SectionCaution,
		"working tree · clean": SectionCaution,
		"divergence":           SectionDiverged,
		"diverged":             SectionDiverged,
		"activity 7d":          SectionHealth,
		"activity":             SectionHealth,
		"next":                 SectionAction,
		"environment":          SectionInfo,
		"repository state":     SectionHealth,
	}
	for in, want := range cases {
		got := SectionColor(in)
		if got != want {
			t.Errorf("SectionColor(%q) returned %p, want %p", in, got, want)
		}
	}
}

func TestSectionColor_UnknownFallsBackToMuted(t *testing.T) {
	if SectionColor("totally-unknown-name") != SectionMuted {
		t.Error("unknown section name should fall back to SectionMuted")
	}
	if SectionColor("") != SectionMuted {
		t.Error("empty section name should fall back to SectionMuted")
	}
}

func TestSectionColor_AllIntentsRender(t *testing.T) {
	// Each intent must produce a non-empty styled string when colour
	// is on — guards against a nil `*color.Color` slipping through.
	prev := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = prev })

	intents := []*color.Color{
		SectionInfo, SectionCaution, SectionDiverged,
		SectionHealth, SectionAction, SectionMuted,
	}
	for i, c := range intents {
		if c == nil {
			t.Errorf("intent #%d is nil", i)
			continue
		}
		if got := c.Sprint("X"); !strings.Contains(got, "\x1b[") {
			t.Errorf("intent #%d emitted no ANSI prefix: %q", i, got)
		}
	}
}

func TestParseSectionLayout(t *testing.T) {
	cases := map[string]SectionLayout{
		"":        SectionLayoutBar,
		"bar":     SectionLayoutBar,
		"BAR":     SectionLayoutBar,
		"  bar  ": SectionLayoutBar,
		"rule":    SectionLayoutRule,
		"RULE":    SectionLayoutRule,
		"unknown": SectionLayoutBar, // unknown falls back to bar
	}
	for in, want := range cases {
		if got := ParseSectionLayout(in); got != want {
			t.Errorf("ParseSectionLayout(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestRenderSection_NoChromeBleedFromBody is the regression test for
// the original report: a body line wider than the rule must NOT cause
// the chrome to be redrawn or split. Since our rule width is fixed and
// the body is emitted verbatim with no per-line border, this is
// trivially true — but the assertion locks it in.
func TestRenderSection_NoChromeBleedFromBody(t *testing.T) {
	withNoColor(t)
	wide := strings.Repeat("한", 80) // 240 bytes, ~80 visual cells
	got := RenderSection("wide", "", []string{wide}, SectionOpts{
		Layout: SectionLayoutRule,
		Width:  40,
	})
	headerCount := strings.Count(got, sectionRuleGlyph+sectionRuleGlyph+" ")
	if headerCount != 1 {
		t.Errorf("expected exactly 1 header chrome occurrence, got %d", headerCount)
	}
	if !strings.Contains(got, sectionIndent+wide+"\n") {
		t.Errorf("body line was modified or padded; full output:\n%s", got)
	}
}

func TestRenderSection_TitleSharesChromeColor(t *testing.T) {
	// With colour enabled, both the bar glyph AND the title text must
	// be wrapped in the chrome's ANSI prefix — regression guard for
	// improvement #1 (title and bar same colour).
	prev := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = prev })

	got := RenderSection("x", "", []string{"plain"}, SectionOpts{
		Layout: SectionLayoutBar,
		Color:  color.New(color.FgRed),
	})
	// Two red ANSI prefixes expected: one for the bar, one for the title.
	prefixes := strings.Count(got, "\x1b[31m")
	if prefixes < 2 {
		t.Errorf("expected ≥2 red ANSI prefixes (bar + title), got %d in %q", prefixes, got)
	}
	if !strings.Contains(got, sectionIndent+"plain\n") {
		t.Errorf("body should be emitted unstyled; got %q", got)
	}
}
