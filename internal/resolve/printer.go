package resolve

import (
	"fmt"
	"strings"
)

// Print는 ConflictFile을 conflict marker 형식의 바이트 슬라이스로 변환한다.
// 비충돌 라인(Context)을 그대로 보존한다.
func Print(cf ConflictFile) []byte {
	if len(cf.Segments) == 0 {
		return nil
	}

	var allLines []string

	for _, seg := range cf.Segments {
		if seg.Hunk != nil {
			allLines = append(allLines, hunkMarkerLines(seg.Hunk)...)
		} else {
			allLines = append(allLines, seg.Context...)
		}
	}

	return []byte(strings.Join(allLines, "\n"))
}

// hunkMarkerLines re-emits one conflict hunk verbatim, markers included.
func hunkMarkerLines(h *ConflictHunk) []string {
	var out []string
	out = append(out, formatMarker(markerOurs, h.OursLabel))
	out = append(out, h.Ours...)
	if h.Base != nil {
		out = append(out, formatMarker(markerBase, h.BaseLabel))
		out = append(out, h.Base...)
	}
	out = append(out, markerSep)
	out = append(out, h.Theirs...)
	out = append(out, formatMarker(markerTheirs, h.TheirsLabel))
	return out
}

// formatMarker builds a marker line like "<<<<<<< HEAD" or "<<<<<<< " (empty label → no trailing space).
func formatMarker(prefix, label string) string {
	if label == "" {
		return prefix
	}
	return fmt.Sprintf("%s %s", prefix, label)
}

// ApplyResolutions는 ConflictFile에 해결 결과를 적용하여
// conflict marker가 제거된 최종 파일 내용을 반환한다.
func ApplyResolutions(cf ConflictFile, resolutions []HunkResolution) ([]byte, error) {
	// count hunks
	hunkCount := 0
	for _, seg := range cf.Segments {
		if seg.Hunk != nil {
			hunkCount++
		}
	}
	if hunkCount != len(resolutions) {
		return nil, fmt.Errorf("gk resolve: hunk count %d does not match resolution count %d", hunkCount, len(resolutions))
	}

	var allLines []string
	ri := 0
	for _, seg := range cf.Segments {
		if seg.Hunk != nil {
			if resolutions[ri].Strategy == StrategyUnresolved {
				// The confidence gate kept this hunk — re-emit its markers
				// verbatim so the file stays honestly conflicted there.
				allLines = append(allLines, hunkMarkerLines(seg.Hunk)...)
			} else {
				allLines = append(allLines, resolutions[ri].ResolvedLines...)
			}
			ri++
		} else {
			allLines = append(allLines, seg.Context...)
		}
	}

	return []byte(strings.Join(allLines, "\n")), nil
}
