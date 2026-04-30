// Package diff implements unified diff parsing, rendering, and
// formatting for the `gk diff` command. Parsing and rendering are
// separated into pure functions for testability.
package diff

// DiffResult는 전체 diff 파싱 결과를 담는 최상위 구조체이다.
type DiffResult struct {
	Files []DiffFile
}

// DiffFile은 단일 파일의 diff 정보를 담는다.
type DiffFile struct {
	OldPath      string     // 원본 경로 (rename 시 이전 경로)
	NewPath      string     // 새 경로
	Status       FileStatus // Added, Modified, Deleted, Renamed, Copied, ModeChanged
	IsBinary     bool       // 바이너리 파일 여부
	OldMode      string     // 이전 파일 모드 (예: "100644")
	NewMode      string     // 새 파일 모드
	Hunks        []Hunk
	AddedLines   int // 추가된 라인 총 수
	DeletedLines int // 삭제된 라인 총 수
}

// FileStatus는 파일의 변경 상태를 나타낸다.
type FileStatus int

const (
	StatusModified    FileStatus = iota // 수정됨
	StatusAdded                         // 추가됨
	StatusDeleted                       // 삭제됨
	StatusRenamed                       // 이름 변경됨
	StatusCopied                        // 복사됨
	StatusModeChanged                   // 모드 변경됨
)

// String은 FileStatus의 사람이 읽을 수 있는 문자열 표현을 반환한다.
func (s FileStatus) String() string {
	switch s {
	case StatusModified:
		return "modified"
	case StatusAdded:
		return "added"
	case StatusDeleted:
		return "deleted"
	case StatusRenamed:
		return "renamed"
	case StatusCopied:
		return "copied"
	case StatusModeChanged:
		return "mode changed"
	default:
		return "unknown"
	}
}

// Hunk는 하나의 변경 블록을 나타낸다.
type Hunk struct {
	OldStart int    // 원본 시작 라인
	OldCount int    // 원본 라인 수
	NewStart int    // 새 파일 시작 라인
	NewCount int    // 새 파일 라인 수
	Header   string // @@ 헤더 전체 텍스트
	Lines    []DiffLine
}

// DiffLine은 diff의 개별 라인을 나타낸다.
type DiffLine struct {
	Kind    LineKind // Context, Added, Deleted
	Content string   // 라인 내용 (접두사 제외)
	OldNum  int      // 원본 파일 라인 번호 (Added일 때 0)
	NewNum  int      // 새 파일 라인 번호 (Deleted일 때 0)
}

// LineKind는 diff 라인의 종류를 나타낸다.
type LineKind int

const (
	LineContext LineKind = iota // 컨텍스트 라인
	LineAdded                   // 추가된 라인
	LineDeleted                 // 삭제된 라인
)

// String은 LineKind의 문자열 표현을 반환한다.
func (k LineKind) String() string {
	switch k {
	case LineContext:
		return "context"
	case LineAdded:
		return "added"
	case LineDeleted:
		return "deleted"
	default:
		return "unknown"
	}
}

// DiffSpan은 워드 diff에서 변경된 구간을 나타낸다.
type DiffSpan struct {
	Start   int  // 바이트 오프셋
	End     int  // 바이트 오프셋 (exclusive)
	Changed bool // true면 변경된 구간
}

// ── JSON 출력 구조체 ──────────────────────────────────────────────

// DiffJSON은 --json 출력 형식이다.
type DiffJSON struct {
	Files []DiffFileJSON `json:"files"`
	Stat  DiffStatJSON   `json:"stat"`
}

// DiffFileJSON은 JSON 출력에서 단일 파일의 diff 정보이다.
type DiffFileJSON struct {
	Path         string         `json:"path"`
	OldPath      string         `json:"old_path,omitempty"`
	Status       string         `json:"status"`
	IsBinary     bool           `json:"is_binary"`
	AddedLines   int            `json:"added_lines"`
	DeletedLines int            `json:"deleted_lines"`
	Hunks        []DiffHunkJSON `json:"hunks"`
}

// DiffHunkJSON은 JSON 출력에서 하나의 Hunk 정보이다.
type DiffHunkJSON struct {
	Header string         `json:"header"`
	Lines  []DiffLineJSON `json:"lines"`
}

// DiffLineJSON은 JSON 출력에서 개별 라인 정보이다.
type DiffLineJSON struct {
	Kind    string `json:"kind"` // "context", "added", "deleted"
	Content string `json:"content"`
	OldNum  int    `json:"old_num,omitempty"`
	NewNum  int    `json:"new_num,omitempty"`
}

// DiffStatJSON은 JSON 출력에서 diff 통계 요약이다.
type DiffStatJSON struct {
	TotalFiles   int `json:"total_files"`
	TotalAdded   int `json:"total_added"`
	TotalDeleted int `json:"total_deleted"`
}
