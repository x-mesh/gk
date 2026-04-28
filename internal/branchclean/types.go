package branchclean

import "time"

// BranchStatus는 브랜치의 현재 상태를 나타낸다.
type BranchStatus string

const (
	StatusMerged       BranchStatus = "merged"
	StatusGone         BranchStatus = "gone"
	StatusStale        BranchStatus = "stale"
	StatusSquashMerged BranchStatus = "squash-merged"
	StatusAmbiguous    BranchStatus = "ambiguous"
	StatusActive       BranchStatus = "active"
	StatusRemoteOnly   BranchStatus = "remote-only"
)

// BranchEntry는 수집된 브랜치 하나의 정보를 담는다.
type BranchEntry struct {
	Name           string
	LastCommitMsg  string
	DiffStat       string
	LastCommitDate time.Time
	// CreatedAt is the timestamp of the first reflog entry for the
	// branch — i.e. when the branch ref itself was first written.
	// Differs from LastCommitDate when the branch was created from an
	// older base commit. Zero when reflog is unavailable / expired.
	CreatedAt time.Time
	Status    BranchStatus
	Upstream  string
	Gone      bool
	// IsRemote = true marks remote-only branches (e.g. origin/feat-x
	// with no local counterpart). The cleaner uses `git push <remote>
	// --delete <name>` for these, never `git branch -d`.
	IsRemote   bool
	RemoteName string // populated when IsRemote (e.g. "origin")
}

// CleanCandidate는 삭제 후보 브랜치와 AI 분석 결과를 결합한 구조체이다.
type CleanCandidate struct {
	BranchEntry
	AICategory string // "completed", "experiment", "in_progress", "preserve", "" (AI 미사용)
	AISummary  string // 최대 80자
	SafeDelete bool   // AI 권장 삭제 여부
	Selected   bool   // TUI에서 기본 선택 여부
}

// CleanOptions는 Branch_Cleaner의 실행 옵션이다.
type CleanOptions struct {
	DryRun       bool
	Force        bool
	Yes          bool
	NoAI         bool
	Gone         bool
	Stale        int  // 0이면 비활성
	All          bool
	SquashMerged bool
	Remote       bool // remote-tracking refs 정리 (git remote prune)
	// IncludeRemote가 true면 remote-only 브랜치(로컬에 동일 이름이 없는
	// origin/X 등)도 후보에 포함된다. 확정 시 git push <remote> --delete로
	// 삭제되므로 의도가 분명할 때만 사용한다.
	IncludeRemote bool
	BaseBranch    string // 빈 문자열이면 자동 감지
	RemoteName    string // 빈 문자열이면 "origin"
	Protected     []string
	StaleDays     int // config에서 가져온 기본값
	Lang          string
}

// CleanResult는 정리 실행 결과이다.
type CleanResult struct {
	Deleted []string
	Failed  map[string]error
	DryRun  []CleanCandidate
	Pruned  bool
	AIUsed  bool
	AIModel string
}
