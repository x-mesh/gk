package resolve

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Feature: ai-resolve, Property 3: Strategy 일괄 적용 정확성
// Validates: Requirements 7.1, 7.2
func TestPropertyStrategyBulkApply(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cf := genConflictFile(t)

		// Count hunks; skip if none
		var hunks []*ConflictHunk
		for _, seg := range cf.Segments {
			if seg.Hunk != nil {
				hunks = append(hunks, seg.Hunk)
			}
		}
		if len(hunks) == 0 {
			return
		}

		// Pick a strategy: ours or theirs
		strategy := StrategyOurs
		if rapid.Bool().Draw(t, "use_theirs") {
			strategy = StrategyTheirs
		}

		// Build resolutions matching the chosen strategy
		resolutions := make([]HunkResolution, len(hunks))
		for i, h := range hunks {
			var lines []string
			if strategy == StrategyOurs {
				lines = h.Ours
			} else {
				lines = h.Theirs
			}
			resolutions[i] = HunkResolution{
				Strategy:      strategy,
				ResolvedLines: lines,
			}
		}

		result, err := ApplyResolutions(cf, resolutions)
		if err != nil {
			t.Fatalf("ApplyResolutions failed: %v", err)
		}

		// Verify: rebuild expected output by replacing hunks with strategy lines
		var expected []string
		hi := 0
		for _, seg := range cf.Segments {
			if seg.Hunk != nil {
				expected = append(expected, resolutions[hi].ResolvedLines...)
				hi++
			} else {
				expected = append(expected, seg.Context...)
			}
		}

		got := string(result)
		want := strings.Join(expected, "\n")
		if got != want {
			t.Fatalf("strategy %q: output mismatch\ngot:  %q\nwant: %q", strategy, got, want)
		}
	})
}

// Feature: ai-resolve, Property 4: ApplyResolutions는 conflict marker가 없는 출력을 생성한다
// Validates: Requirements 8.1
func TestPropertyApplyResolutionsNoConflictMarkers(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cf := genConflictFile(t)

		// Count hunks; skip if none
		var hunkCount int
		for _, seg := range cf.Segments {
			if seg.Hunk != nil {
				hunkCount++
			}
		}
		if hunkCount == 0 {
			return
		}

		// Build resolutions with safe lines (no markers)
		resolutions := make([]HunkResolution, hunkCount)
		for i := range hunkCount {
			n := rapid.IntRange(0, 5).Draw(t, "resolved_count")
			lines := make([]string, n)
			for j := range n {
				lines[j] = genSafeLine(t)
			}
			resolutions[i] = HunkResolution{
				Strategy:      StrategyMerged,
				ResolvedLines: lines,
			}
		}

		result, err := ApplyResolutions(cf, resolutions)
		if err != nil {
			t.Fatalf("ApplyResolutions failed: %v", err)
		}

		output := string(result)
		markers := []string{markerOurs, markerSep, markerTheirs, markerBase}
		for _, m := range markers {
			if strings.Contains(output, m) {
				t.Fatalf("output contains conflict marker %q\noutput: %s", m, output)
			}
		}
	})
}
