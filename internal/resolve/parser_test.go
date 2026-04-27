package resolve

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Feature: ai-resolve, Property 1: Conflict marker Parse/Print round-trip
// Validates: Requirements 2.1, 2.2, 2.3, 2.4, 2.6, 3.1, 3.2, 3.3
func TestPropertyParsePrintRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cf := genConflictFile(t)
		printed := Print(cf)
		parsed, err := Parse(cf.Path, printed)
		if err != nil {
			t.Fatalf("Parse failed on Print output: %v", err)
		}
		assertConflictFileEqual(t, cf, parsed)
	})
}

// Feature: ai-resolve, Property 9: 유효하지 않은 conflict marker는 에러를 반환한다
// Validates: Requirements 2.5
func TestPropertyInvalidConflictMarkerReturnsError(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		text := genTextWithUnclosedMarker(t)
		_, err := Parse("test.txt", []byte(text))
		if err == nil {
			t.Fatalf("expected error for unclosed conflict marker, got nil.\nInput:\n%s", text)
		}
	})
}

// --- generators ---

// genSafeLine generates a line that does NOT contain any conflict markers.
func genSafeLine(t *rapid.T) string {
	// Use alphanumeric + common code chars, avoid starting with < | = >
	line := rapid.StringMatching(`[a-zA-Z0-9_ \t\.\,\;\:\(\)\{\}\[\]\-\+\*\/\#\!\@\$\%\^\&]{0,60}`).Draw(t, "line")
	// Double-check: reject if it accidentally starts with a marker prefix
	for _, m := range []string{markerOurs, markerBase, markerSep, markerTheirs} {
		if strings.HasPrefix(line, m) {
			// fallback to a guaranteed safe line
			return "safe_line"
		}
	}
	return line
}

// genLabel generates a label string (e.g. "HEAD", "feature-branch").
func genLabel(t *rapid.T) string {
	return rapid.StringMatching(`[a-zA-Z0-9_\-\.\/]{0,30}`).Draw(t, "label")
}

// genLines generates 0-5 safe lines.
func genLines(t *rapid.T, name string) []string {
	n := rapid.IntRange(0, 5).Draw(t, name+"_count")
	lines := make([]string, n)
	for i := range n {
		lines[i] = genSafeLine(t)
	}
	return lines
}

// genContextSegment generates a Context segment with 1-5 safe lines.
func genContextSegment(t *rapid.T) Segment {
	n := rapid.IntRange(1, 5).Draw(t, "ctx_lines")
	lines := make([]string, n)
	for i := range n {
		lines[i] = genSafeLine(t)
	}
	return Segment{Context: lines}
}

// genHunkSegment generates a Hunk segment with random ours/theirs/base lines.
func genHunkSegment(t *rapid.T) Segment {
	h := &ConflictHunk{
		Ours:        genLines(t, "ours"),
		Theirs:      genLines(t, "theirs"),
		OursLabel:   genLabel(t),
		TheirsLabel: genLabel(t),
	}
	// 50% chance of diff3 base
	if rapid.Bool().Draw(t, "has_base") {
		h.Base = genLines(t, "base")
		h.BaseLabel = genLabel(t)
	}
	return Segment{Hunk: h}
}

// genConflictFile generates a valid ConflictFile with 1-5 segments.
// Consecutive Context segments are merged to produce canonical form
// (Parse always merges adjacent context lines into one segment).
func genConflictFile(t *rapid.T) ConflictFile {
	n := rapid.IntRange(1, 5).Draw(t, "seg_count")
	var segs []Segment
	lastWasContext := false
	for range n {
		wantHunk := rapid.Bool().Draw(t, "is_hunk")
		if wantHunk {
			segs = append(segs, genHunkSegment(t))
			lastWasContext = false
		} else if lastWasContext {
			// merge into previous context segment to keep canonical form
			extra := genContextSegment(t)
			segs[len(segs)-1].Context = append(segs[len(segs)-1].Context, extra.Context...)
		} else {
			segs = append(segs, genContextSegment(t))
			lastWasContext = true
		}
	}
	// Ensure at least one segment
	if len(segs) == 0 {
		segs = append(segs, genContextSegment(t))
	}
	return ConflictFile{
		Path:     "test.txt",
		Segments: segs,
	}
}

// genTextWithUnclosedMarker generates text that has <<<<<<< but no >>>>>>>.
func genTextWithUnclosedMarker(t *rapid.T) string {
	// Generate some safe lines before the marker
	beforeCount := rapid.IntRange(0, 3).Draw(t, "before_count")
	var parts []string
	for range beforeCount {
		parts = append(parts, genSafeLine(t))
	}
	// Add the opening marker
	label := genLabel(t)
	if label != "" {
		parts = append(parts, markerOurs+" "+label)
	} else {
		parts = append(parts, markerOurs)
	}
	// Add some ours lines (no closing marker)
	oursCount := rapid.IntRange(0, 3).Draw(t, "ours_count")
	for range oursCount {
		parts = append(parts, genSafeLine(t))
	}
	// Optionally add ======= but never >>>>>>>
	if rapid.Bool().Draw(t, "has_sep") {
		parts = append(parts, markerSep)
		theirsCount := rapid.IntRange(0, 3).Draw(t, "theirs_count")
		for range theirsCount {
			parts = append(parts, genSafeLine(t))
		}
	}
	// Add some trailing safe lines
	afterCount := rapid.IntRange(0, 2).Draw(t, "after_count")
	for range afterCount {
		parts = append(parts, genSafeLine(t))
	}
	return strings.Join(parts, "\n")
}

// --- assertion helpers ---

func assertConflictFileEqual(t *rapid.T, expected, actual ConflictFile) {
	t.Helper()
	if expected.Path != actual.Path {
		t.Fatalf("Path mismatch: %q vs %q", expected.Path, actual.Path)
	}
	if len(expected.Segments) != len(actual.Segments) {
		t.Fatalf("Segment count mismatch: %d vs %d", len(expected.Segments), len(actual.Segments))
	}
	for i := range expected.Segments {
		es := expected.Segments[i]
		as := actual.Segments[i]
		if es.Hunk != nil {
			if as.Hunk == nil {
				t.Fatalf("Segment[%d]: expected Hunk, got Context", i)
			}
			assertHunkEqual(t, i, es.Hunk, as.Hunk)
		} else {
			if as.Hunk != nil {
				t.Fatalf("Segment[%d]: expected Context, got Hunk", i)
			}
			assertStringSliceEqual(t, i, "Context", es.Context, as.Context)
		}
	}
}

func assertHunkEqual(t *rapid.T, idx int, expected, actual *ConflictHunk) {
	t.Helper()
	assertStringSliceEqual(t, idx, "Ours", expected.Ours, actual.Ours)
	assertStringSliceEqual(t, idx, "Theirs", expected.Theirs, actual.Theirs)
	assertStringSliceEqual(t, idx, "Base", expected.Base, actual.Base)
	if expected.OursLabel != actual.OursLabel {
		t.Fatalf("Segment[%d] OursLabel: %q vs %q", idx, expected.OursLabel, actual.OursLabel)
	}
	if expected.TheirsLabel != actual.TheirsLabel {
		t.Fatalf("Segment[%d] TheirsLabel: %q vs %q", idx, expected.TheirsLabel, actual.TheirsLabel)
	}
	if expected.BaseLabel != actual.BaseLabel {
		t.Fatalf("Segment[%d] BaseLabel: %q vs %q", idx, expected.BaseLabel, actual.BaseLabel)
	}
}

func assertStringSliceEqual(t *rapid.T, segIdx int, name string, expected, actual []string) {
	t.Helper()
	if len(expected) != len(actual) {
		t.Fatalf("Segment[%d] %s length: %d vs %d\nexpected: %v\nactual:   %v",
			segIdx, name, len(expected), len(actual), expected, actual)
	}
	for i := range expected {
		if expected[i] != actual[i] {
			t.Fatalf("Segment[%d] %s[%d]: %q vs %q", segIdx, name, i, expected[i], actual[i])
		}
	}
}
