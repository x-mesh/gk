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
			h := seg.Hunk
			// <<<<<<< label
			allLines = append(allLines, formatMarker(markerOurs, h.OursLabel))
			allLines = append(allLines, h.Ours...)
			// optional diff3 base
			if h.Base != nil {
				allLines = append(allLines, formatMarker(markerBase, h.BaseLabel))
				allLines = append(allLines, h.Base...)
			}
			// =======
			allLines = append(allLines, markerSep)
			allLines = append(allLines, h.Theirs...)
			// >>>>>>> label
			allLines = append(allLines, formatMarker(markerTheirs, h.TheirsLabel))
		} else {
			allLines = append(allLines, seg.Context...)
		}
	}

	return []byte(strings.Join(allLines, "\n"))
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
			allLines = append(allLines, resolutions[ri].ResolvedLines...)
			ri++
		} else {
			allLines = append(allLines, seg.Context...)
		}
	}

	return []byte(strings.Join(allLines, "\n")), nil
}
