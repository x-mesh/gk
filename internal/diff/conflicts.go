package diff

import "strings"

// 충돌 마커는 git의 표준 3-way merge 마커이다. 정확히 7개의 동일 문자로
// 시작하고, 그 뒤가 공백/탭/EOL 중 하나(또는 식별자가 따라오는 경우는
// `<<<<<<< HEAD` 같은 형태)이다. 부분 일치(예: `<<<<<<<<` 8개)는
// 의도적으로 제외 — git 자신이 7개 정확히만 마커로 인식하기 때문.
const (
	conflictMarkerStart    = "<<<<<<<"
	conflictMarkerSplit    = "======="
	conflictMarkerEnd      = ">>>>>>>"
	conflictMarkerAncestor = "|||||||" // diff3 스타일 ancestor 마커
)

// IsConflictMarkerLine은 한 줄이 git merge conflict 마커인지 판단한다.
// hunk line의 prefix(+/-/space)는 이미 제거된 Content 기준이다.
//
// 정확히 7개 문자 + (EOL | space | tab)만 인정 → git 본인 동작과 일치.
// 8개 이상이거나 6개 이하는 우연한 코드 토큰(예: shell heredoc, 코드
// 주석 구분선)으로 간주해서 false.
func IsConflictMarkerLine(content string) bool {
	for _, marker := range []string{
		conflictMarkerStart,
		conflictMarkerSplit,
		conflictMarkerEnd,
		conflictMarkerAncestor,
	} {
		if !strings.HasPrefix(content, marker) {
			continue
		}
		rest := content[len(marker):]
		if rest == "" {
			return true
		}
		switch rest[0] {
		case ' ', '\t', '\r', '\n':
			return true
		}
	}
	return false
}

// HunkHasConflictMarker는 hunk의 라인 중 하나라도 conflict marker가
// 포함되어 있으면 true를 반환한다. 마커는 보통 컨텍스트 라인으로 보이지만
// 신뢰성을 위해 Kind와 무관하게 검사한다.
func HunkHasConflictMarker(h Hunk) bool {
	for _, line := range h.Lines {
		if IsConflictMarkerLine(line.Content) {
			return true
		}
	}
	return false
}

// FileHasConflictMarker는 파일의 어느 hunk에라도 conflict marker가
// 포함되어 있는지 검사한다.
func FileHasConflictMarker(f DiffFile) bool {
	for _, h := range f.Hunks {
		if HunkHasConflictMarker(h) {
			return true
		}
	}
	return false
}

// ConflictFileIndices는 result 안에서 conflict marker를 포함한 파일의
// 인덱스 슬라이스를 반환한다 (오름차순). TUI 내비게이션의 "다음 충돌
// 파일로 점프" 동작이 이 결과를 사용한다.
func ConflictFileIndices(result *DiffResult) []int {
	if result == nil {
		return nil
	}
	var out []int
	for i, f := range result.Files {
		if FileHasConflictMarker(f) {
			out = append(out, i)
		}
	}
	return out
}

// BuildConflictHunks는 conflict marker가 박힌 파일의 원시 라인을 받아,
// marker block마다 한 hunk를 만들어 반환한다 — 파일 전체를 한 hunk로
// 출력하면 사용자가 변경 영역을 찾기 어렵고 정상 diff UI와도 결이 다르다.
//
// 라인 분류:
//   - <<<<<<<와 ======= 사이 ("ours") → LineDeleted (◀, red)
//   - =======와 >>>>>>> 사이 ("theirs") → LineAdded (▶, green)
//   - ||||||| (diff3 ancestor) ~ ======= → LineContext (사용자가 직접 판단)
//   - marker 라인 자체 → LineContext
//   - block 바깥의 ±context 라인 → LineContext
//
// 각 hunk는 block 위/아래로 contextLines 만큼의 컨텍스트를 포함한다.
// 인접한 block의 context가 겹치면 두 block을 한 hunk로 합친다 — 일반
// `git diff -U3`이 인접 변경을 합치는 동작과 일치.
func BuildConflictHunks(rawLines []string, contextLines int) []Hunk {
	if contextLines < 0 {
		contextLines = 0
	}

	// 1) marker 위치 스캔: 각 block의 시작(<<<<<<<)과 끝(>>>>>>>) 인덱스.
	type block struct {
		start, end int // 0-based, inclusive
	}
	var blocks []block
	cur := -1
	for i, line := range rawLines {
		switch {
		case strings.HasPrefix(line, conflictMarkerStart) && IsConflictMarkerLine(line):
			cur = i
		case strings.HasPrefix(line, conflictMarkerEnd) && IsConflictMarkerLine(line) && cur >= 0:
			blocks = append(blocks, block{start: cur, end: i})
			cur = -1
		}
	}
	if len(blocks) == 0 {
		return nil
	}

	// 2) context 영역까지 확장한 뒤 인접 영역 병합.
	type span struct {
		from, to int // 0-based, inclusive
	}
	spans := make([]span, 0, len(blocks))
	for _, b := range blocks {
		from := b.start - contextLines
		if from < 0 {
			from = 0
		}
		to := b.end + contextLines
		if to >= len(rawLines) {
			to = len(rawLines) - 1
		}
		if n := len(spans); n > 0 && from <= spans[n-1].to+1 {
			if to > spans[n-1].to {
				spans[n-1].to = to
			}
			continue
		}
		spans = append(spans, span{from: from, to: to})
	}

	// 3) span마다 hunk 생성.
	hunks := make([]Hunk, 0, len(spans))
	for _, s := range spans {
		lines := make([]DiffLine, 0, s.to-s.from+1)
		state := conflictStateOutside
		oldNum, newNum := s.from+1, s.from+1
		for i := s.from; i <= s.to; i++ {
			content := rawLines[i]
			kind, advanceOld, advanceNew, nextState := classifyConflictLine(content, state)
			line := DiffLine{Kind: kind, Content: content}
			if advanceOld {
				line.OldNum = oldNum
			}
			if advanceNew {
				line.NewNum = newNum
			}
			lines = append(lines, line)
			if advanceOld {
				oldNum++
			}
			if advanceNew {
				newNum++
			}
			state = nextState
		}
		hunks = append(hunks, Hunk{
			OldStart: s.from + 1,
			OldCount: oldNum - (s.from + 1),
			NewStart: s.from + 1,
			NewCount: newNum - (s.from + 1),
			Header:   formatConflictHunkHeader(s.from+1, oldNum-(s.from+1), s.from+1, newNum-(s.from+1)),
			Lines:    lines,
		})
	}
	return hunks
}

// conflict line 분류 상태 머신. ours/theirs/ancestor 안에서는 라인을
// 다르게 분류해야 한다.
const (
	conflictStateOutside = iota
	conflictStateOurs
	conflictStateAncestor
	conflictStateTheirs
)

// classifyConflictLine은 (LineKind, oldNum 증가 여부, newNum 증가 여부,
// 다음 state)를 반환한다.
func classifyConflictLine(content string, state int) (LineKind, bool, bool, int) {
	if IsConflictMarkerLine(content) {
		switch {
		case strings.HasPrefix(content, conflictMarkerStart):
			return LineContext, true, true, conflictStateOurs
		case strings.HasPrefix(content, conflictMarkerAncestor):
			return LineContext, true, true, conflictStateAncestor
		case strings.HasPrefix(content, conflictMarkerSplit):
			return LineContext, true, true, conflictStateTheirs
		case strings.HasPrefix(content, conflictMarkerEnd):
			return LineContext, true, true, conflictStateOutside
		}
	}
	switch state {
	case conflictStateOurs:
		return LineDeleted, true, false, state
	case conflictStateTheirs:
		return LineAdded, false, true, state
	default:
		return LineContext, true, true, state
	}
}

func formatConflictHunkHeader(oldStart, oldCount, newStart, newCount int) string {
	if oldCount == 0 {
		oldCount = 1
	}
	if newCount == 0 {
		newCount = 1
	}
	// 일반 git diff와 같은 모양 + conflict 표식.
	return "@@ -" + itoa(oldStart) + "," + itoa(oldCount) + " +" + itoa(newStart) + "," + itoa(newCount) + " @@ conflict"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// FilterConflictHunks는 result의 깊은 복사본을 반환하되, 각 파일에서
// conflict marker가 없는 hunk를 제거하고, 결과적으로 hunk가 0개가 된
// 파일도 함께 제거한다. 원본 result는 변경하지 않는다.
//
// "충돌만 보기" 모드(--conflicts 플래그 / TUI c 단축키)가 이 함수의
// 결과를 렌더한다.
func FilterConflictHunks(result *DiffResult) *DiffResult {
	if result == nil {
		return &DiffResult{}
	}
	out := &DiffResult{}
	for _, f := range result.Files {
		var kept []Hunk
		for _, h := range f.Hunks {
			if HunkHasConflictMarker(h) {
				kept = append(kept, h)
			}
		}
		if len(kept) == 0 {
			continue
		}
		fCopy := f
		fCopy.Hunks = kept
		out.Files = append(out.Files, fCopy)
	}
	return out
}
