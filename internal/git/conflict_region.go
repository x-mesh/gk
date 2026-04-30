package git

import (
	"bufio"
	"os"
	"strings"
)

// ConflictRegion describes one git conflict block — a contiguous slice
// of a file delimited by `<<<<<<<`, `=======`, and `>>>>>>>` markers.
// Line numbers are 1-indexed and refer to the file as it sits on disk
// while the conflict is unresolved (so callers can quote them back to
// the user, e.g. "lines 188–200").
type ConflictRegion struct {
	StartMarkerLine int    // line carrying `<<<<<<<`
	MidMarkerLine   int    // line carrying `=======`
	EndMarkerLine   int    // line carrying `>>>>>>>`
	OursLabel       string // text after `<<<<<<<` (typically "HEAD")
	TheirsLabel     string // text after `>>>>>>>` (typically the commit subject or branch)

	Ours   []ConflictLine // content between Start and Mid markers (exclusive)
	Theirs []ConflictLine // content between Mid and End markers (exclusive)

	// ContextBefore/After carry one line of unchanged code surrounding
	// the region. They are nil at the very top/bottom of the file. The
	// renderer uses them to anchor the conflict in its surroundings so
	// the user can recognise where the change lives.
	ContextBefore *ConflictLine
	ContextAfter  *ConflictLine
}

// ConflictLine is one line from inside (or adjacent to) a conflict
// region. LineNum is 1-indexed.
type ConflictLine struct {
	LineNum int
	Text    string
}

// ParseConflictMarkers reads path and returns one ConflictRegion per
// `<<<<<<<` … `=======` … `>>>>>>>` block found. Malformed regions
// (missing mid or end marker) are silently skipped — pre-existing
// markers in test fixtures or generated code shouldn't crash the
// renderer. Files larger than ~16MiB are truncated at the scanner's
// buffer; callers concerned with very large files should fall back to
// the simple "files with conflicts" listing.
func ParseConflictMarkers(path string) ([]ConflictRegion, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return parseConflictLines(lines), nil
}

// parseConflictLines is the testable core split out from
// ParseConflictMarkers — accepts an already-loaded slice of lines so
// tests don't need to write fixtures to disk.
func parseConflictLines(lines []string) []ConflictRegion {
	var regions []ConflictRegion
	i := 0
	for i < len(lines) {
		line := lines[i]
		if !isStartMarker(line) {
			i++
			continue
		}
		startIdx := i
		oursLabel := strings.TrimSpace(strings.TrimPrefix(line, "<<<<<<<"))

		midIdx := -1
		for j := i + 1; j < len(lines); j++ {
			if lines[j] == "=======" {
				midIdx = j
				break
			}
			if isStartMarker(lines[j]) {
				// Nested or unmatched start — abandon this region and
				// resume scanning from the inner start.
				break
			}
		}
		if midIdx < 0 {
			i++
			continue
		}

		endIdx := -1
		theirsLabel := ""
		for j := midIdx + 1; j < len(lines); j++ {
			if isEndMarker(lines[j]) {
				endIdx = j
				theirsLabel = strings.TrimSpace(strings.TrimPrefix(lines[j], ">>>>>>>"))
				break
			}
			if isStartMarker(lines[j]) {
				break
			}
		}
		if endIdx < 0 {
			i++
			continue
		}

		region := ConflictRegion{
			StartMarkerLine: startIdx + 1,
			MidMarkerLine:   midIdx + 1,
			EndMarkerLine:   endIdx + 1,
			OursLabel:       oursLabel,
			TheirsLabel:     theirsLabel,
			Ours:            collectLines(lines, startIdx+1, midIdx),
			Theirs:          collectLines(lines, midIdx+1, endIdx),
		}
		if startIdx > 0 {
			region.ContextBefore = &ConflictLine{
				LineNum: startIdx,
				Text:    lines[startIdx-1],
			}
		}
		if endIdx+1 < len(lines) {
			region.ContextAfter = &ConflictLine{
				LineNum: endIdx + 2,
				Text:    lines[endIdx+1],
			}
		}
		regions = append(regions, region)
		i = endIdx + 1
	}
	return regions
}

func isStartMarker(line string) bool {
	return strings.HasPrefix(line, "<<<<<<< ") || line == "<<<<<<<"
}

func isEndMarker(line string) bool {
	return strings.HasPrefix(line, ">>>>>>> ") || line == ">>>>>>>"
}

// collectLines slices [from, to) of lines into ConflictLine entries
// with 1-indexed line numbers (LineNum = slice index + 1).
func collectLines(lines []string, from, to int) []ConflictLine {
	if from >= to {
		return nil
	}
	out := make([]ConflictLine, 0, to-from)
	for j := from; j < to; j++ {
		out = append(out, ConflictLine{LineNum: j + 1, Text: lines[j]})
	}
	return out
}

// TotalConflictLines is a convenience for renderers — sums the line
// counts on both sides across every region.
func TotalConflictLines(regions []ConflictRegion) int {
	total := 0
	for _, r := range regions {
		total += len(r.Ours) + len(r.Theirs)
	}
	return total
}
