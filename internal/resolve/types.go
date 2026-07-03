package resolve

// Strategy는 충돌 해결 전략이다.
type Strategy string

const (
	StrategyOurs   Strategy = "ours"
	StrategyTheirs Strategy = "theirs"
	StrategyMerged Strategy = "merged"
	// StrategyUnresolved marks a hunk deliberately left conflicted —
	// ApplyResolutions re-emits its original markers verbatim. Used by the
	// confidence gate for partial file resolution.
	StrategyUnresolved Strategy = "unresolved"
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
	// Confidence는 AI가 보고한 확신도(0.0~1.0). 0 = 미보고.
	Confidence float64
}

// HunkProposal is an AI resolution that was NOT applied — its confidence sat
// below resolve.min_confidence — carried in the paused report so an agent can
// review and act without another "resolve it for me" round-trip.
type HunkProposal struct {
	File       string   `json:"file"`
	Hunk       int      `json:"hunk"` // 1-based conflict-hunk index within the file
	Strategy   string   `json:"strategy"`
	Confidence float64  `json:"confidence"`
	Rationale  string   `json:"rationale,omitempty"`
	Resolved   []string `json:"resolved"`
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
	// UnionFiles overrides the basenames resolved by union merge in the
	// mechanical tier (nil = DefaultUnionFiles).
	UnionFiles []string
	// MinConfidence gates AI resolutions per hunk: below it, the hunk keeps
	// its conflict markers and the AI's answer ships as a proposal instead.
	// 0 disables the gate (an unreported confidence then passes through);
	// with a positive gate, unreported counts as below.
	MinConfidence float64
	// DeferStage: write resolved contents but do NOT `git add` them —
	// the caller stages after its verification gate passes, and can restore
	// the conflict (`git checkout -m`) on failure because the unmerged
	// index stages are still intact.
	DeferStage bool
}

// ResolveResult는 해결 실행 결과이다.
type ResolveResult struct {
	Resolved []string         // 해결 완료된 파일 경로
	Failed   map[string]error // 실패한 파일과 에러
	Skipped  []string         // 건너뛴 파일 (파싱 에러 등)
	Total    int              // 전체 충돌 파일 수
	AIUsed   bool
	AIModel  string
	// Mechanical lists the Resolved subset handled by the deterministic
	// tier (no AI involved).
	Mechanical []string
	// Remaining lists conflicts strategy "safe" deliberately left alone —
	// they need AI or a human, and are still marked/unmerged.
	Remaining []string
	// PendingStage lists resolved paths gk WROTE but did not stage
	// (DeferStage) — the caller stages them after verification, and may
	// restore their conflict with `git checkout -m` on failure.
	PendingStage []string
	// PendingAccept lists markerless files whose existing (user-authored)
	// content was accepted — stage-deferred like PendingStage, but NEVER
	// rolled back: gk did not write them, so restoring markers would
	// destroy the user's manual resolution.
	PendingAccept []string
	// PendingDelete lists delete/modify resolutions whose worktree file was
	// removed but whose index deletion is deferred until the verification
	// gate passes — the intact stages make `git checkout -m` restoration
	// possible on failure.
	PendingDelete []string
	// PendingPartial lists partially resolved files gk WROTE (some hunks
	// fixed, the rest keeping markers). Never staged; on gate failure they
	// are restored with checkout -m like PendingStage.
	PendingPartial []string
	// Proposals carries the AI resolutions the confidence gate did NOT
	// apply — their hunks stay conflicted (partially resolved files land in
	// Remaining). An agent reads these from the paused envelope and either
	// applies them by hand or re-runs after review.
	Proposals []HunkProposal
}
