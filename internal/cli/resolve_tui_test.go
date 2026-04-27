package cli

import (
	"strings"
	"testing"

	"github.com/fatih/color"
	"pgregory.net/rapid"

	"github.com/x-mesh/gk/internal/resolve"
)

func init() {
	color.NoColor = true // for test predictability
}

// ---------------------------------------------------------------------------
// Generators
// ---------------------------------------------------------------------------

// genNonEmptyLine generates a non-empty line without newlines.
func genNonEmptyLine(t *rapid.T, label string) string {
	return rapid.StringMatching(`[a-zA-Z0-9_ \t\.\,\;\:\(\)\{\}\[\]\-\+\*\/\#]{1,60}`).Draw(t, label)
}

// genNonEmptyLines generates 1-5 non-empty lines.
func genNonEmptyLines(t *rapid.T, label string) []string {
	n := rapid.IntRange(1, 5).Draw(t, label+"_count")
	lines := make([]string, n)
	for i := range n {
		lines[i] = genNonEmptyLine(t, label)
	}
	return lines
}

// genConflictHunkForDiff generates a ConflictHunk with non-empty Ours and Theirs.
func genConflictHunkForDiff(t *rapid.T) resolve.ConflictHunk {
	return resolve.ConflictHunk{
		Ours:   genNonEmptyLines(t, "ours"),
		Theirs: genNonEmptyLines(t, "theirs"),
	}
}

// ---------------------------------------------------------------------------
// Feature: ai-resolve, Property 8: FormatHunkDiff는 hunk 내용을 포함한다
// ---------------------------------------------------------------------------

// TestPropertyFormatHunkDiff verifies that FormatHunkDiff output contains
// all Ours lines and all Theirs lines from the input hunk.
// **Validates: Requirements 6.2**
func TestPropertyFormatHunkDiff(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		hunk := genConflictHunkForDiff(rt)
		result := FormatHunkDiff(hunk)

		for i, line := range hunk.Ours {
			if !strings.Contains(result, line) {
				rt.Fatalf("result does not contain Ours[%d] %q\nresult:\n%s", i, line, result)
			}
		}
		for i, line := range hunk.Theirs {
			if !strings.Contains(result, line) {
				rt.Fatalf("result does not contain Theirs[%d] %q\nresult:\n%s", i, line, result)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

func TestFormatHunkDiff_Basic(t *testing.T) {
	hunk := resolve.ConflictHunk{
		Ours:   []string{"local line 1", "local line 2"},
		Theirs: []string{"remote line 1"},
	}
	result := FormatHunkDiff(hunk)

	if !strings.Contains(result, "- local line 1") {
		t.Errorf("expected ours line with - prefix: %s", result)
	}
	if !strings.Contains(result, "- local line 2") {
		t.Errorf("expected ours line with - prefix: %s", result)
	}
	if !strings.Contains(result, "+ remote line 1") {
		t.Errorf("expected theirs line with + prefix: %s", result)
	}
}

func TestFormatHunkDiff_Empty(t *testing.T) {
	hunk := resolve.ConflictHunk{}
	result := FormatHunkDiff(hunk)
	if result != "" {
		t.Errorf("expected empty string for empty hunk, got: %q", result)
	}
}
