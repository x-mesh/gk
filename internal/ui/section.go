package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/fatih/color"
)

// SectionLayout selects how RenderSection draws a titled block.
//
// Both layouts share two properties that the legacy box layout did not:
// the chrome (bar or rule) is independent of body width, so wide-character
// content cannot push it out of alignment, and the body is rendered
// verbatim with a fixed three-space indent — no per-line padding.
type SectionLayout int

const (
	// SectionLayoutBar prefixes the title with a thick coloured block
	// and renders the body indented below it. The block fills one full
	// terminal cell and is followed by two spaces so the title text
	// always lands at column 4, which the body indent matches:
	//
	//   █  TITLE   summary
	//      line1
	//      line2
	SectionLayoutBar SectionLayout = iota

	// SectionLayoutRule places the title between two horizontal rule
	// segments sized to min(64, TTY width). Summary, when provided,
	// is emitted as the first body line under the rule:
	//
	//   ── TITLE ──────────────────────
	//      summary
	//      line1
	//      line2
	SectionLayoutRule
)

// SectionLayoutBar / SectionLayoutRule chrome glyphs. Kept as constants
// so tests can assert on them without re-deriving the literal.
//
// The bar glyph is a full block (U+2588) so the prefix carries real
// visual weight — a thin ▎ disappears at small zoom levels. It is
// followed by two spaces so the title text starts at column 4; the
// body indent below matches that so titles and content stack on the
// same column.
const (
	sectionBarGlyph  = "█"
	sectionRuleGlyph = "─"

	// SectionIndent is the leading whitespace applied to body lines.
	// Exported so callers that need to emit body content outside of
	// RenderSection (e.g. when stitching pre-rendered text under a
	// previously emitted section header) can match the same column
	// without re-deriving the literal.
	SectionIndent = "   "
	sectionIndent = SectionIndent

	sectionRuleMin = 32
	sectionRuleMax = 64

	// sectionSummaryGap is the literal padding between the title text
	// and the inline summary in bar layout. Kept as a constant so the
	// summary always sits a predictable distance from the title even
	// when callers wrap the summary in their own ANSI styling.
	sectionSummaryGap = "   "
)

// SectionOpts customises RenderSection / RenderNextAction output.
//
// Color paints the chrome (bar glyph or rule segments) and — because
// the title shares the same colour — the title text. fatih/color
// honours color.NoColor automatically, so callers that want plain
// output only need to set color.NoColor=true. When nil, faint cyan is
// used as a safe default.
//
// Width overrides the rule width for SectionLayoutRule. Zero means
// "auto" — fall back to min(sectionRuleMax, TTYWidth()). Tests use
// this to pin a deterministic width.
//
// KeepCase suppresses the default ToUpper applied to the title. The
// upper-cased label reads as a section name for short tags ("BRANCH",
// "NEXT") but mangles proper nouns and file paths
// ("INTERNAL/CLI/STATUS.GO" is unreadable), so callers whose title is
// content-bearing should set this flag.
type SectionOpts struct {
	Layout   SectionLayout
	Color    *color.Color
	Width    int
	KeepCase bool
}

// Solarized-inspired chrome colours for canonical section "intents".
// Use these directly when a section name doesn't fit the built-in
// registry; SectionColor takes a name and resolves to the right intent.
//
// xterm-256 codes:
//
//	Info      33  (#268BD2 steel blue)   — neutral status / branch
//	Caution  136  (#B58900 mustard)      — needs-attention / dirty tree
//	Diverged  61  (#6C71C4 violet)       — branch divergence / merge state
//	Health    64  (#859900 olive)        — historical / passing checks
//	Action   166  (#CB4B16 orange)       — call-to-action / next step
//	Muted     -                         — fallback (faint cyan)
//
// All intents are bold so the title text reads cleanly at small font
// sizes. fatih/color honours color.NoColor automatically.
var (
	SectionInfo     = color.New(38, 5, 33, color.Bold)
	SectionCaution  = color.New(38, 5, 136, color.Bold)
	SectionDiverged = color.New(38, 5, 61, color.Bold)
	SectionHealth   = color.New(38, 5, 64, color.Bold)
	SectionAction   = color.New(38, 5, 166, color.Bold)
	SectionMuted    = color.New(color.FgCyan, color.Faint)
)

// SectionColor returns the canonical chrome colour for a named section.
// Names are matched case-insensitively and trimmed; unknown names fall
// back to SectionMuted so a typo never crashes the renderer.
//
// The registry covers the gk-wide section vocabulary so the same name
// yields the same colour regardless of which command renders it — the
// "divergence" violet appears in `gk status`, `gk pull`, and `gk merge`,
// reinforcing the visual association.
func SectionColor(name string) *color.Color {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "branch":
		return SectionInfo
	case "working tree", "working tree · clean":
		return SectionCaution
	case "divergence", "diverged":
		return SectionDiverged
	case "activity 7d", "activity":
		return SectionHealth
	case "next":
		return SectionAction
	case "environment", "environ":
		return SectionInfo
	case "repository state", "repo state", "repository":
		return SectionHealth
	default:
		return SectionMuted
	}
}

// ParseSectionLayout maps a config string ("bar" / "rule") to a layout
// value. Empty or unknown input falls back to bar.
func ParseSectionLayout(s string) SectionLayout {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "rule":
		return SectionLayoutRule
	default:
		return SectionLayoutBar
	}
}

// RenderSection returns a section block titled `title` with an optional
// `summary` headline and `lines` as the body. Output always ends with a
// trailing blank line so callers can stack sections without inserting
// their own separators.
//
// `summary` is the one-line headline that complements the section title
// — e.g. "5M 2? · 7 files" for a working-tree section. In the bar
// layout it sits inline with the title (separated by sectionSummaryGap)
// so the eye gets the headline without descending into the body. In
// the rule layout it is emitted as the first body line so the rule
// itself stays clean. Empty summary is a no-op.
//
// Body lines are written verbatim — no padding, no truncation, no
// per-line border. Wide-character glyphs and embedded ANSI escape
// sequences cannot misalign the chrome because the chrome never
// depends on body width.
func RenderSection(title, summary string, lines []string, opts SectionOpts) string {
	chrome := opts.Color
	if chrome == nil {
		chrome = color.New(color.FgCyan, color.Faint)
	}
	dim := color.New(color.Faint).SprintFunc()
	upper := title
	if !opts.KeepCase {
		upper = strings.ToUpper(title)
	}

	var b strings.Builder
	switch opts.Layout {
	case SectionLayoutRule:
		w := opts.Width
		if w <= 0 {
			w = sectionRuleMax
			if tw, ok := TTYWidth(); ok && tw > 0 && tw < w {
				w = tw
			}
		}
		if w < sectionRuleMin {
			w = sectionRuleMin
		}
		// Layout: "── TITLE ─────…"  (4 + len(upper) + fill = w)
		fill := w - 4 - runeLen(upper)
		if fill < 3 {
			fill = 3
		}
		b.WriteString(chrome.Sprint(sectionRuleGlyph + sectionRuleGlyph + " "))
		b.WriteString(chrome.Sprint(upper))
		b.WriteString(" ")
		b.WriteString(chrome.Sprint(strings.Repeat(sectionRuleGlyph, fill)))
		b.WriteString("\n")
		if summary != "" {
			b.WriteString(sectionIndent)
			b.WriteString(dim(summary))
			b.WriteString("\n")
		}
	default: // SectionLayoutBar
		b.WriteString(chrome.Sprint(sectionBarGlyph))
		b.WriteString("  ")
		b.WriteString(chrome.Sprint(upper))
		if summary != "" {
			b.WriteString(sectionSummaryGap)
			b.WriteString(dim(summary))
		}
		b.WriteString("\n")
	}

	for _, ln := range lines {
		b.WriteString(sectionIndent)
		b.WriteString(ln)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// RenderNextAction returns the highlighted footer that closes a
// rich-mode section sequence. The next-command sits in the title row's
// summary slot so the eye lands on it immediately, and the optional
// `why` explanation goes in the body — the inverse of the legacy
// "→ next / why: …" pair which buried the command on the second line.
//
// Bar:
//
//	█  NEXT   → gk push
//	   why: 3 commits ahead of origin/main
//
// Rule:
//
//	── NEXT ─────────────────────────
//	   → gk push
//	   why: 3 commits ahead of origin/main
//
// `why` may be empty; in that case only the next-command line is
// emitted.
func RenderNextAction(next, why string, opts SectionOpts) string {
	arrow := color.New(color.FgMagenta, color.Bold).Sprint("→")
	dim := color.New(color.Faint).SprintFunc()

	summary := arrow + " " + next
	var body []string
	if why != "" {
		body = []string{dim("why: " + why)}
	}
	return RenderSection("next", summary, body, opts)
}

// runeLen returns the rune count of s. Used to size the rule fill
// without pulling in a wide-character measurement library — section
// titles are always short ASCII labels in practice ("BRANCH", "WORKING
// TREE", etc.), and a slight under-count for hypothetical CJK titles
// only makes the rule a few cells longer than intended, never shorter.
func runeLen(s string) int {
	n := 0
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:])
		n++
		i += size
	}
	return n
}
