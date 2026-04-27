package resolve

import (
	"bytes"
	"fmt"
	"strings"
)

// conflict marker prefixes
const (
	markerOurs   = "<<<<<<<"
	markerBase   = "|||||||"
	markerSep    = "======="
	markerTheirs = ">>>>>>>"
)

// parser states
type parserState int

const (
	stateContext parserState = iota
	stateOurs
	stateBase
	stateTheirs
)

// Parse는 충돌 파일의 내용을 ConflictFile로 파싱한다.
// conflict marker가 없으면 빈 Hunk 슬라이스를 가진 ConflictFile을 반환한다.
// 유효하지 않은 marker (열린 마커에 닫는 마커 없음)는 에러를 반환한다.
// diff3 형식의 ||||||| base marker를 지원한다.
func Parse(path string, content []byte) (ConflictFile, error) {
	cf := ConflictFile{Path: path}

	// nil content = truly empty file, no segments
	if content == nil {
		return cf, nil
	}

	lines := splitLines(content)

	state := stateContext
	var contextLines []string
	var hunk ConflictHunk

	for _, line := range lines {
		switch state {
		case stateContext:
			if strings.HasPrefix(line, markerOurs) {
				// flush accumulated context
				if len(contextLines) > 0 {
					cf.Segments = append(cf.Segments, Segment{Context: contextLines})
					contextLines = nil
				}
				hunk = ConflictHunk{
					OursLabel: extractLabel(line, markerOurs),
				}
				state = stateOurs
			} else {
				contextLines = append(contextLines, line)
			}

		case stateOurs:
			if strings.HasPrefix(line, markerBase) {
				hunk.BaseLabel = extractLabel(line, markerBase)
				state = stateBase
			} else if strings.HasPrefix(line, markerSep) && !strings.HasPrefix(line, markerSep+"=") {
				state = stateTheirs
			} else {
				hunk.Ours = append(hunk.Ours, line)
			}

		case stateBase:
			if strings.HasPrefix(line, markerSep) && !strings.HasPrefix(line, markerSep+"=") {
				state = stateTheirs
			} else {
				hunk.Base = append(hunk.Base, line)
			}

		case stateTheirs:
			if strings.HasPrefix(line, markerTheirs) {
				hunk.TheirsLabel = extractLabel(line, markerTheirs)
				cf.Segments = append(cf.Segments, Segment{Hunk: &ConflictHunk{
					Ours:        hunk.Ours,
					Theirs:      hunk.Theirs,
					Base:        hunk.Base,
					OursLabel:   hunk.OursLabel,
					TheirsLabel: hunk.TheirsLabel,
					BaseLabel:   hunk.BaseLabel,
				}})
				hunk = ConflictHunk{}
				state = stateContext
			} else {
				hunk.Theirs = append(hunk.Theirs, line)
			}
		}
	}

	// EOF in a conflict state → invalid marker
	if state != stateContext {
		return ConflictFile{}, fmt.Errorf("gk resolve: %s: unclosed conflict marker (missing >>>>>>> marker)", path)
	}

	// flush trailing context
	if len(contextLines) > 0 {
		cf.Segments = append(cf.Segments, Segment{Context: contextLines})
	}

	return cf, nil
}

// splitLines splits content by newlines, preserving the structure.
// A trailing newline produces an empty final element which we keep
// so that Print can reconstruct the exact byte sequence.
func splitLines(content []byte) []string {
	raw := bytes.Split(content, []byte("\n"))
	lines := make([]string, len(raw))
	for i, r := range raw {
		lines[i] = string(r)
	}
	return lines
}

// extractLabel returns the text after the marker prefix, trimmed of leading space.
func extractLabel(line, prefix string) string {
	rest := strings.TrimPrefix(line, prefix)
	if len(rest) > 0 && rest[0] == ' ' {
		rest = rest[1:]
	}
	return rest
}
