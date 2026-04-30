package diff

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// 파싱에 사용되는 정규식 패턴
var (
	// diffHeaderRE는 "diff --git a/path b/path" 헤더를 매칭한다.
	diffGitHeaderRE = regexp.MustCompile(`^diff --git a/(.+?) b/(.+)$`)

	// hunkHeaderRE는 "@@ -old,count +new,count @@" 헤더를 매칭한다.
	// count가 생략된 경우(단일 라인)도 처리한다.
	hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$`)

	// binaryFileRE는 "Binary files ... differ" 메시지를 매칭한다.
	binaryFileRE = regexp.MustCompile(`^Binary files .+ and .+ differ$`)
)

// scannerMaxLineBytes caps the longest single line we'll buffer when
// parsing diff output. Real-world generated files (minified bundles,
// vendored lockfiles) routinely produce single lines well past 1 MB.
// 64 MB easily absorbs those while keeping bounded memory.
const scannerMaxLineBytes = 64 * 1024 * 1024

// ParseUnifiedDiff는 git diff의 unified diff 출력을 파싱하여 DiffResult를 반환한다.
// 순수 함수로 구현되어 io.Reader에서 읽는다.
//
// 라인 길이 한계는 scannerMaxLineBytes(64MB). 일반 소스 파일의 한 줄은
// 수십~수백 바이트지만, 생성된 lockfile, minified bundle, 또는 한 줄에
// 수백만 토큰이 들어간 vendored 파일은 1MB도 쉽게 넘는다. 한계를 넘으면
// `bufio.ErrTooLong` 으로 파싱 자체가 실패하므로 caller가 raw bytes
// 출력으로 fallback해 무엇이 잘못됐는지조차 모르게 된다 — 64MB로 두어
// 현실의 거대 입력은 다 흡수하면서도 무제한 메모리는 막는다.
func ParseUnifiedDiff(r io.Reader) (*DiffResult, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), scannerMaxLineBytes)

	result := &DiffResult{}
	var currentFile *DiffFile
	var currentHunk *Hunk

	// 현재 hunk 내 라인 번호 추적
	var oldNum, newNum int

	for scanner.Scan() {
		line := scanner.Text()

		// 1. diff --git 헤더 감지 → 새 파일 시작
		if m := diffGitHeaderRE.FindStringSubmatch(line); m != nil {
			// 이전 파일 마무리
			if currentFile != nil {
				finishFile(currentFile, currentHunk)
				result.Files = append(result.Files, *currentFile)
			}
			currentFile = &DiffFile{
				OldPath: m[1],
				NewPath: m[2],
				Status:  StatusModified, // 기본값, 이후 헤더에서 업데이트
			}
			currentHunk = nil
			continue
		}

		// 파일 컨텍스트가 없으면 나머지 처리 건너뛰기
		if currentFile == nil {
			continue
		}

		// 2. 확장 헤더 파싱 (diff --git과 --- 사이의 라인들)
		if currentHunk == nil {
			if parseExtendedHeader(currentFile, line) {
				continue
			}
		}

		// 3. --- 파일 경로 파싱
		if strings.HasPrefix(line, "--- ") {
			path := line[4:]
			if path == "/dev/null" {
				currentFile.Status = StatusAdded
			} else {
				currentFile.OldPath = stripABPrefix(path)
			}
			continue
		}

		// 4. +++ 파일 경로 파싱
		if strings.HasPrefix(line, "+++ ") {
			path := line[4:]
			if path == "/dev/null" {
				currentFile.Status = StatusDeleted
			} else {
				currentFile.NewPath = stripABPrefix(path)
			}
			continue
		}

		// 5. @@ hunk 헤더 파싱
		if m := hunkHeaderRE.FindStringSubmatch(line); m != nil {
			// 이전 hunk 마무리
			if currentHunk != nil {
				currentFile.Hunks = append(currentFile.Hunks, *currentHunk)
			}

			oldStart, _ := strconv.Atoi(m[1])
			oldCount := 1
			if m[2] != "" {
				oldCount, _ = strconv.Atoi(m[2])
			}
			newStart, _ := strconv.Atoi(m[3])
			newCount := 1
			if m[4] != "" {
				newCount, _ = strconv.Atoi(m[4])
			}

			currentHunk = &Hunk{
				OldStart: oldStart,
				OldCount: oldCount,
				NewStart: newStart,
				NewCount: newCount,
				Header:   line,
			}
			oldNum = oldStart
			newNum = newStart
			continue
		}

		// 6. 바이너리 파일 감지
		if binaryFileRE.MatchString(line) {
			currentFile.IsBinary = true
			continue
		}

		// 7. hunk 내 라인 파싱
		if currentHunk != nil {
			dl, consumed := parseDiffLine(line, &oldNum, &newNum)
			if consumed {
				currentHunk.Lines = append(currentHunk.Lines, dl)
				switch dl.Kind {
				case LineAdded:
					currentFile.AddedLines++
				case LineDeleted:
					currentFile.DeletedLines++
				}
			}
			// "\ No newline at end of file" 등은 무시
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("diff 파싱 중 읽기 오류: %w", err)
	}

	// 마지막 파일 마무리
	if currentFile != nil {
		finishFile(currentFile, currentHunk)
		result.Files = append(result.Files, *currentFile)
	}

	return result, nil
}

// parseExtendedHeader는 diff --git과 ---/+++ 사이의 확장 헤더를 파싱한다.
// 처리된 경우 true를 반환한다.
func parseExtendedHeader(f *DiffFile, line string) bool {
	switch {
	case strings.HasPrefix(line, "old mode "):
		f.OldMode = strings.TrimPrefix(line, "old mode ")
		return true
	case strings.HasPrefix(line, "new mode "):
		f.NewMode = strings.TrimPrefix(line, "new mode ")
		if f.OldMode != "" {
			f.Status = StatusModeChanged
		}
		return true
	case strings.HasPrefix(line, "rename from "):
		f.OldPath = strings.TrimPrefix(line, "rename from ")
		f.Status = StatusRenamed
		return true
	case strings.HasPrefix(line, "rename to "):
		f.NewPath = strings.TrimPrefix(line, "rename to ")
		f.Status = StatusRenamed
		return true
	case strings.HasPrefix(line, "similarity index "):
		f.Status = StatusRenamed
		return true
	case strings.HasPrefix(line, "copy from "):
		f.OldPath = strings.TrimPrefix(line, "copy from ")
		f.Status = StatusCopied
		return true
	case strings.HasPrefix(line, "copy to "):
		f.NewPath = strings.TrimPrefix(line, "copy to ")
		f.Status = StatusCopied
		return true
	case strings.HasPrefix(line, "new file mode "):
		f.NewMode = strings.TrimPrefix(line, "new file mode ")
		f.Status = StatusAdded
		return true
	case strings.HasPrefix(line, "deleted file mode "):
		f.OldMode = strings.TrimPrefix(line, "deleted file mode ")
		f.Status = StatusDeleted
		return true
	case strings.HasPrefix(line, "index "):
		return true
	case strings.HasPrefix(line, "dissimilarity index "):
		return true
	}
	return false
}

// parseDiffLine은 hunk 내의 단일 라인을 파싱한다.
// 유효한 diff 라인이면 DiffLine과 true를 반환한다.
func parseDiffLine(line string, oldNum, newNum *int) (DiffLine, bool) {
	if len(line) == 0 {
		// 빈 라인은 컨텍스트 라인으로 처리 (일부 diff 출력에서 발생)
		dl := DiffLine{
			Kind:    LineContext,
			Content: "",
			OldNum:  *oldNum,
			NewNum:  *newNum,
		}
		*oldNum++
		*newNum++
		return dl, true
	}

	switch line[0] {
	case '+':
		dl := DiffLine{
			Kind:    LineAdded,
			Content: line[1:],
			OldNum:  0,
			NewNum:  *newNum,
		}
		*newNum++
		return dl, true
	case '-':
		dl := DiffLine{
			Kind:    LineDeleted,
			Content: line[1:],
			OldNum:  *oldNum,
			NewNum:  0,
		}
		*oldNum++
		return dl, true
	case ' ':
		dl := DiffLine{
			Kind:    LineContext,
			Content: line[1:],
			OldNum:  *oldNum,
			NewNum:  *newNum,
		}
		*oldNum++
		*newNum++
		return dl, true
	case '\\':
		// "\ No newline at end of file" — 무시
		return DiffLine{}, false
	default:
		return DiffLine{}, false
	}
}

// finishFile은 현재 파일의 마지막 hunk를 추가하고 마무리한다.
func finishFile(f *DiffFile, lastHunk *Hunk) {
	if lastHunk != nil {
		f.Hunks = append(f.Hunks, *lastHunk)
	}
}

// stripABPrefix는 "a/" 또는 "b/" 접두사를 제거한다.
func stripABPrefix(path string) string {
	if strings.HasPrefix(path, "a/") {
		return path[2:]
	}
	if strings.HasPrefix(path, "b/") {
		return path[2:]
	}
	return path
}
