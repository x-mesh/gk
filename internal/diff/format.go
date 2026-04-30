package diff

import (
	"fmt"
	"strings"
)

// FormatUnifiedDiff는 DiffResult를 다시 unified diff 텍스트로 변환한다.
// 라운드트립 속성 검증에 사용된다: FormatUnifiedDiff → ParseUnifiedDiff가
// 원본 구조를 보존해야 한다.
func FormatUnifiedDiff(result *DiffResult) string {
	if result == nil || len(result.Files) == 0 {
		return ""
	}

	var b strings.Builder
	// 파일 간 구분 개행은 이전 파일 마지막에 이미 포함되므로 추가 처리 불필요.
	for i := range result.Files {
		formatFile(&b, &result.Files[i])
	}
	return b.String()
}

// formatFile은 단일 DiffFile을 unified diff 텍스트로 변환한다.
func formatFile(b *strings.Builder, f *DiffFile) {
	// 1. diff --git 헤더
	fmt.Fprintf(b, "diff --git a/%s b/%s\n", f.OldPath, f.NewPath)

	// 2. 확장 헤더 (상태에 따라)
	formatExtendedHeaders(b, f)

	// 3. 바이너리 파일 처리
	if f.IsBinary {
		fmt.Fprintf(b, "Binary files a/%s and b/%s differ\n", f.OldPath, f.NewPath)
		return
	}

	// Hunk가 없으면 (모드 변경만 있는 경우 등) ---/+++ 및 hunk 출력 생략
	if len(f.Hunks) == 0 {
		return
	}

	// 4. --- / +++ 헤더
	formatFileHeaders(b, f)

	// 5. Hunk 출력
	for _, h := range f.Hunks {
		formatHunk(b, &h)
	}
}

// formatExtendedHeaders는 diff --git과 ---/+++ 사이의 확장 헤더를 출력한다.
func formatExtendedHeaders(b *strings.Builder, f *DiffFile) {
	switch f.Status {
	case StatusAdded:
		if f.NewMode != "" {
			fmt.Fprintf(b, "new file mode %s\n", f.NewMode)
		}
	case StatusDeleted:
		if f.OldMode != "" {
			fmt.Fprintf(b, "deleted file mode %s\n", f.OldMode)
		}
	case StatusRenamed:
		fmt.Fprintf(b, "rename from %s\n", f.OldPath)
		fmt.Fprintf(b, "rename to %s\n", f.NewPath)
	case StatusCopied:
		fmt.Fprintf(b, "copy from %s\n", f.OldPath)
		fmt.Fprintf(b, "copy to %s\n", f.NewPath)
	case StatusModeChanged:
		if f.OldMode != "" {
			fmt.Fprintf(b, "old mode %s\n", f.OldMode)
		}
		if f.NewMode != "" {
			fmt.Fprintf(b, "new mode %s\n", f.NewMode)
		}
	}
}

// formatFileHeaders는 --- 및 +++ 파일 경로 헤더를 출력한다.
func formatFileHeaders(b *strings.Builder, f *DiffFile) {
	switch f.Status {
	case StatusAdded:
		b.WriteString("--- /dev/null\n")
		fmt.Fprintf(b, "+++ b/%s\n", f.NewPath)
	case StatusDeleted:
		fmt.Fprintf(b, "--- a/%s\n", f.OldPath)
		b.WriteString("+++ /dev/null\n")
	default:
		fmt.Fprintf(b, "--- a/%s\n", f.OldPath)
		fmt.Fprintf(b, "+++ b/%s\n", f.NewPath)
	}
}

// formatHunk은 단일 Hunk를 unified diff 텍스트로 변환한다.
func formatHunk(b *strings.Builder, h *Hunk) {
	// @@ 헤더
	fmt.Fprintf(b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldCount, h.NewStart, h.NewCount)

	// 라인 출력
	for _, dl := range h.Lines {
		switch dl.Kind {
		case LineAdded:
			fmt.Fprintf(b, "+%s\n", dl.Content)
		case LineDeleted:
			fmt.Fprintf(b, "-%s\n", dl.Content)
		case LineContext:
			fmt.Fprintf(b, " %s\n", dl.Content)
		}
	}
}
