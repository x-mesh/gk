package resolve

// Strategy는 충돌 해결 전략이다.
type Strategy string

const (
	StrategyOurs   Strategy = "ours"
	StrategyTheirs Strategy = "theirs"
	StrategyMerged Strategy = "merged"
)

// ConflictHunk는 하나의 충돌 영역이다.
type ConflictHunk struct {
	Ours        []string // <<<<<<< 와 ======= 사이 라인
	Theirs      []string // ======= 와 >>>>>>> 사이 라인
	Base        []string // ||||||| 와 ======= 사이 라인 (diff3, 없으면 nil)
	OursLabel   string   // <<<<<<< 뒤의 라벨 (e.g. "HEAD")
	TheirsLabel string   // >>>>>>> 뒤의 라벨 (e.g. "feature-branch")
	BaseLabel   string   // ||||||| 뒤의 라벨 (diff3, 없으면 "")
}

// Segment는 파일 내 하나의 영역이다. 충돌 영역이면 Hunk가 non-nil.
type Segment struct {
	Context []string      // 비충돌 라인 (Hunk가 nil일 때)
	Hunk    *ConflictHunk // 충돌 영역 (Context가 nil일 때)
}

// ConflictFile은 하나의 충돌 파일이다.
type ConflictFile struct {
	Path     string
	Segments []Segment // 충돌/비충돌 영역의 순서 보존 목록
}

// HunkResolution은 하나의 충돌 영역에 대한 해결 결과이다.
type HunkResolution struct {
	Strategy      Strategy
	ResolvedLines []string // 해결된 코드 라인
	Rationale     string   // AI 선택 근거 (최대 120자)
}

// FileResolution은 하나의 파일에 대한 전체 해결 결과이다.
type FileResolution struct {
	Path        string
	Resolutions []HunkResolution // ConflictFile.Segments 내 Hunk 순서와 1:1 대응
}

// ResolveOptions는 Resolver의 실행 옵션이다.
type ResolveOptions struct {
	DryRun   bool
	NoAI     bool
	NoBackup bool
	Strategy Strategy // 빈 문자열이면 TUI/interactive 모드
	Files    []string // 빈 슬라이스면 모든 충돌 파일
	Lang     string
}

// ResolveResult는 해결 실행 결과이다.
type ResolveResult struct {
	Resolved []string         // 해결 완료된 파일 경로
	Failed   map[string]error // 실패한 파일과 에러
	Skipped  []string         // 건너뛴 파일 (파싱 에러 등)
	Total    int              // 전체 충돌 파일 수
	AIUsed   bool
	AIModel  string
}
