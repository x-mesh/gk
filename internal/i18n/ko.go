package i18n

// koMessages contains Korean message variants for normal and easy modes.
// Key naming convention: "<command>.<situation>.<item>"
//
// Every key MUST have a ModeNormal variant. ModeEasy variants are optional
// and provide beginner-friendly Korean translations with emoji.
var koMessages = map[string]map[Mode]string{
	// ── Status section headers ──────────────────────────────────────────

	"status.staged.header": {
		ModeNormal: "Staged changes",
		ModeEasy:   "📦 커밋 준비된 변경사항 (staged)",
	},
	"status.unstaged.header": {
		ModeNormal: "Unstaged changes",
		ModeEasy:   "✏️ 아직 준비 안 된 변경사항 (unstaged)",
	},
	"status.untracked.header": {
		ModeNormal: "Untracked files",
		ModeEasy:   "🆕 새로 만든 파일 (untracked)",
	},
	"status.conflict.header": {
		ModeNormal: "Unmerged paths",
		ModeEasy:   "💥 충돌이 발생한 파일 (conflict)",
	},

	// ── Hint messages for status ────────────────────────────────────────

	"hint.status.has_staged": {
		ModeNormal: "try: gk commit",
		ModeEasy:   "💡 다음 단계: 변경사항을 저장하려면 → gk commit",
	},
	"hint.status.has_staged.minimal": {
		ModeNormal: "gk commit",
		ModeEasy:   "gk commit",
	},
	"hint.status.has_unstaged": {
		ModeNormal: "try: git add <file>",
		ModeEasy:   "💡 다음 단계: 변경사항을 준비하려면 → gk add <파일>",
	},
	"hint.status.has_unstaged.minimal": {
		ModeNormal: "gk add <file>",
		ModeEasy:   "gk add <파일>",
	},
	"hint.status.has_untracked": {
		ModeNormal: "try: git add <file> to track",
		ModeEasy:   "💡 다음 단계: 새 파일을 추적하려면 → gk add <파일>",
	},
	"hint.status.has_untracked.minimal": {
		ModeNormal: "gk add <file>",
		ModeEasy:   "gk add <파일>",
	},
	"hint.status.has_conflict": {
		ModeNormal: "fix conflicts and run: git add <file>",
		ModeEasy:   "💡 다음 단계: 충돌을 해결한 뒤 → gk add <파일> → gk commit",
	},
	"hint.status.has_conflict.minimal": {
		ModeNormal: "gk add <file> && gk commit",
		ModeEasy:   "gk add <파일> → gk commit",
	},

	// ── Error messages ──────────────────────────────────────────────────

	"error.push_failed": {
		ModeNormal: "push failed: remote has new changes",
		ModeEasy:   "❌ 서버에 올리기 실패: 원격 저장소에 새로운 변경사항이 있습니다",
	},
	"error.pull_failed": {
		ModeNormal: "pull failed: local changes would be overwritten",
		ModeEasy:   "❌ 서버에서 가져오기 실패: 로컬 변경사항이 덮어씌워질 수 있습니다",
	},
	"error.merge_conflict": {
		ModeNormal: "merge conflict detected",
		ModeEasy:   "💥 같은 부분을 다르게 수정해서 충돌이 발생했습니다",
	},

	// ── Error hints ─────────────────────────────────────────────────────

	"hint.error.push_failed": {
		ModeNormal: "try: gk pull first",
		ModeEasy:   "💡 먼저 서버에서 가져오기를 실행하세요 → gk pull",
	},
	"hint.error.push_failed.minimal": {
		ModeNormal: "gk pull",
		ModeEasy:   "gk pull",
	},
	"hint.error.pull_failed": {
		ModeNormal: "try: gk commit or gk stash first",
		ModeEasy:   "💡 먼저 변경사항을 저장하세요 → gk commit 또는 gk stash",
	},
	"hint.error.pull_failed.minimal": {
		ModeNormal: "gk commit or gk stash",
		ModeEasy:   "gk commit 또는 gk stash",
	},
	"hint.error.merge_conflict": {
		ModeNormal: "fix conflicts, then: git add <file> && git commit",
		ModeEasy:   "💡 충돌 파일을 편집한 뒤 → gk add <파일> → gk commit",
	},
	"hint.error.merge_conflict.minimal": {
		ModeNormal: "gk add <file> && gk commit",
		ModeEasy:   "gk add <파일> → gk commit",
	},

	// ── General messages ────────────────────────────────────────────────

	"general.success": {
		ModeNormal: "Success",
		ModeEasy:   "✅ 성공",
	},
	"general.warning": {
		ModeNormal: "Warning",
		ModeEasy:   "⚠️ 주의",
	},
	"general.error": {
		ModeNormal: "Error",
		ModeEasy:   "❌ 오류",
	},
	"general.nothing_to_commit": {
		ModeNormal: "nothing to commit, working tree clean",
		ModeEasy:   "✅ 저장할 변경사항이 없습니다. 작업 폴더가 깨끗합니다!",
	},
	"general.branch_info": {
		ModeNormal: "On branch %s",
		ModeEasy:   "🌿 현재 브랜치: %s",
	},

	// ── Guide workflow names and descriptions ───────────────────────────

	"guide.workflow.save.name": {
		ModeNormal: "Save changes",
		ModeEasy:   "변경사항 저장하기",
	},
	"guide.workflow.save.description": {
		ModeNormal: "Stage, commit, and push your changes",
		ModeEasy:   "파일 수정 → 커밋 → 서버에 올리기",
	},
	"guide.workflow.update.name": {
		ModeNormal: "Update from remote",
		ModeEasy:   "서버에서 최신 코드 가져오기",
	},
	"guide.workflow.update.description": {
		ModeNormal: "Pull latest changes from the remote repository",
		ModeEasy:   "원격 저장소의 최신 변경사항을 내 코드에 반영",
	},
	"guide.workflow.branch-work.name": {
		ModeNormal: "Branch workflow",
		ModeEasy:   "브랜치 만들고 작업하기",
	},
	"guide.workflow.branch-work.description": {
		ModeNormal: "Create a branch, work, and merge back",
		ModeEasy:   "새 작업 갈래를 만들어 독립적으로 작업",
	},
	"guide.workflow.resolve-conflict.name": {
		ModeNormal: "Resolve conflicts",
		ModeEasy:   "충돌 해결하기",
	},
	"guide.workflow.resolve-conflict.description": {
		ModeNormal: "Fix merge conflicts and continue",
		ModeEasy:   "같은 파일을 다르게 수정했을 때 해결하는 방법",
	},
	"guide.workflow.undo.name": {
		ModeNormal: "Undo mistakes",
		ModeEasy:   "실수 되돌리기",
	},
	"guide.workflow.undo.description": {
		ModeNormal: "Safely undo recent changes",
		ModeEasy:   "잘못된 작업을 안전하게 되돌리기",
	},

	// ── Guide step titles ───────────────────────────────────────────────

	"guide.step.check_status": {
		ModeNormal: "Check current status",
		ModeEasy:   "현재 상태 확인",
	},
	"guide.step.save_changes": {
		ModeNormal: "Save changes",
		ModeEasy:   "변경사항 저장",
	},
	"guide.step.push_to_remote": {
		ModeNormal: "Push to remote",
		ModeEasy:   "서버에 올리기",
	},
	"guide.step.pull_latest": {
		ModeNormal: "Pull latest changes",
		ModeEasy:   "최신 코드 가져오기",
	},
	"guide.step.create_branch": {
		ModeNormal: "Create a new branch",
		ModeEasy:   "새 브랜치 만들기",
	},
	"guide.step.merge_branch": {
		ModeNormal: "Merge branch",
		ModeEasy:   "브랜치 합치기",
	},
	"guide.step.edit_conflict": {
		ModeNormal: "Edit conflicting files",
		ModeEasy:   "충돌 파일 편집",
	},
	"guide.step.continue_after_resolve": {
		ModeNormal: "Continue after resolving",
		ModeEasy:   "해결 후 계속",
	},
	"guide.step.undo": {
		ModeNormal: "Undo last action",
		ModeEasy:   "되돌리기",
	},
	"guide.step.timemachine": {
		ModeNormal: "Or use timemachine",
		ModeEasy:   "또는 타임머신",
	},

	// ── Commit messages ─────────────────────────────────────────────────

	"commit.success": {
		ModeNormal: "Committed: %s",
		ModeEasy:   "✅ 변경사항이 저장되었습니다: %s",
	},
	"hint.commit.push": {
		ModeNormal: "try: gk push",
		ModeEasy:   "💡 다음 단계: 서버에 올리려면 → gk push",
	},
	"hint.commit.push.minimal": {
		ModeNormal: "gk push",
		ModeEasy:   "gk push",
	},

	// ── Push messages ───────────────────────────────────────────────────

	"push.success": {
		ModeNormal: "Pushed to %s",
		ModeEasy:   "🚀 서버에 올리기 완료: %s",
	},

	// ── Pull messages ───────────────────────────────────────────────────

	"pull.success": {
		ModeNormal: "Pulled from %s",
		ModeEasy:   "📥 서버에서 가져오기 완료: %s",
	},

	// ── Easy Mode system messages ───────────────────────────────────────

	"easy.catalog_load_failed": {
		ModeNormal: "gk: Easy Mode catalog load failed, falling back to normal mode",
		ModeEasy:   "gk: Easy Mode 카탈로그 로딩 실패, 일반 모드로 전환합니다",
	},
	"easy.lang_not_found": {
		ModeNormal: "gk: language %q not found, falling back to English",
		ModeEasy:   "gk: 언어 %q를 찾을 수 없습니다. 영어로 전환합니다",
	},
}

func init() {
	RegisterMessages("ko", koMessages)
}
