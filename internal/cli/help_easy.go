package cli

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// easyShortKO maps a command's full path (cobra CommandPath, e.g. "gk branch
// clean") to a plain-Korean one-line description shown in help when Easy Mode
// is on and the language is Korean.
//
// Tone: concise and factual, not chatty — noun/verb-stem endings, no "~요/
// ~어요" politeness. Avoid git jargon or pair it with a one-clause gloss in
// parentheses; keep proper nouns (branch/file names, gk commands) as-is.
// The full path is the key so commands that share a leaf name but differ in
// meaning (gk push vs gk stash push) get distinct text. Commands without an
// entry keep their default English Short.
var easyShortKO = map[string]string{
	// 일상 작업
	"gk status": "작업 상태를 한눈에 표시",
	"gk commit": "바뀐 내용을 메시지와 함께 저장 (commit)",
	"gk push":   "변경을 서버(원격)에 올리기",
	"gk pull":   "서버(원격)의 최신 변경 가져오기",
	"gk sync":   "내 작업을 기준 브랜치 최신 상태로 맞추기",
	"gk switch": "다른 브랜치로 이동 (목록에서 선택)",
	"gk log":    "변경 기록(커밋) 보기",
	"gk diff":   "변경 내용 비교 보기",
	"gk merge":  "다른 브랜치 내용을 현재 브랜치에 합치기",
	"gk next":   "현재 상태와 다음에 할 안전한 작업 안내",
	"gk guide":  "git 작업 흐름 단계별 안내",

	// 진행 중 작업 제어
	"gk continue": "충돌 해결 후 진행 중인 작업 계속",
	"gk abort":    "진행 중인 작업 취소, 이전 상태로 복원",

	// 브랜치
	"gk branch":              "브랜치 관리 (보기·정리)",
	"gk branch list":         "브랜치 목록 (오래됨·합쳐짐 등 필터)",
	"gk branch clean":        "합쳐졌거나 사라진 브랜치 정리",
	"gk branch pick":         "브랜치 골라서 이동",
	"gk branch set-parent":   "이 브랜치의 기준(부모) 브랜치 지정",
	"gk branch unset-parent": "기준(부모) 브랜치 지정 해제",
	"gk branch-check":        "브랜치 이름이 규칙에 맞는지 확인",

	// 작업 공간(worktree)
	"gk worktree":        "여러 작업 공간(worktree) 관리",
	"gk worktree add":    "작업 공간(worktree) 새로 만들기",
	"gk worktree list":   "작업 공간(worktree) 목록",
	"gk worktree remove": "작업 공간(worktree) 제거",
	"gk worktree prune":  "쓰지 않는 worktree 기록 정리",

	// 합치기·충돌
	"gk resolve":       "충돌(같은 부분을 다르게 고침) 해결 (AI)",
	"gk precheck":      "합치기 전에 충돌 여부만 미리 확인",
	"gk edit-conflict": "충돌 난 파일을 편집기로 열기 (첫 충돌 위치로)",

	// 가져오기·되돌리기
	"gk reset":   "현재 브랜치를 서버(원격) 상태로 되돌리기",
	"gk refresh": "main·develop 같은 브랜치를 서버 최신으로 당기기",
	"gk clone":   "원격 저장소를 내 컴퓨터로 복제",

	// 임시 저장(stash)
	"gk stash":       "변경을 임시 보관함에 넣고 빼기",
	"gk stash list":  "임시 보관함 목록",
	"gk stash push":  "현재 변경을 임시 보관함에 넣기",
	"gk stash pop":   "임시 보관함에서 꺼내 적용하고 비우기",
	"gk stash apply": "임시 보관함 내용을 비우지 않고 적용",
	"gk stash drop":  "임시 보관함 항목 버리기",
	"gk stash show":  "임시 보관함 항목 내용 보기",

	// WIP·정리
	"gk wip":   "전부 임시로 저장하는 WIP 커밋 만들기",
	"gk unwip": "wip로 만든 WIP 커밋 되돌리기",
	"gk wipe":  "모든 로컬 변경·새 파일 버리기 (백업 후)",

	// 기록·복구
	"gk forget":                   "파일을 전체 기록에서 제거 (기록이 바뀜)",
	"gk timemachine":              "과거 저장소 상태 둘러보고 복원",
	"gk timemachine list":         "과거 상태 목록 (작업 기록·백업, 최신순)",
	"gk timemachine list-backups": "gk가 만든 백업 지점 목록 (최신순)",
	"gk timemachine restore":      "선택한 과거 상태로 되돌리기 (먼저 백업 생성)",
	"gk timemachine show":         "과거 상태 항목의 커밋·변경 내용 보기",
	"gk undo":                     "최근 작업 기록에서 골라 되돌리기",
	"gk restore":                  "잃어버린 작업(끊긴 커밋) 복구",

	// AI
	"gk ask":       "git·gk 관련 질문에 답변",
	"gk do":        "원하는 작업을 말하면 알맞은 명령 실행",
	"gk explain":   "에러나 마지막 작업을 쉬운 말로 설명",
	"gk pr":        "변경 내용으로 코드 검토 요청(PR) 설명 생성",
	"gk review":    "올린 변경을 AI가 코드 검토",
	"gk changelog": "변경 내역(changelog) 생성",

	// 점검·정책·설정
	"gk doctor":      "설정·환경 점검",
	"gk lint-commit": "커밋 메시지가 규칙(Conventional Commits)에 맞는지 검사",
	"gk preflight":   "올리기 전 정해둔 검사들 실행",
	"gk guard":       "저장소 정책 (비밀정보 검사·서명·커밋 규칙)",
	"gk guard check": "정책 규칙을 모두 검사하고 위반 보고",
	"gk guard init":  "정책 설정 파일(.gk.yaml) 만들기",
	"gk init":        "저장소에 gk 기본 설정 만들기",
	"gk config":      "gk 설정 읽기·변경",
	"gk config get":  "설정 값 하나 출력",
	"gk config show": "전체 설정을 YAML로 출력",
	"gk config init": "기본 설정 파일 만들기",

	// 훅·기타
	"gk hooks":           "gk를 부르는 git 훅 관리",
	"gk hooks install":   "git 훅에 gk 연결 스크립트 설치",
	"gk hooks uninstall": "gk 훅 연결 제거",
	"gk ship":            "릴리스 점검 후 태그 발행",
	"gk update":          "gk를 최신 버전으로 업데이트",
	"gk prompt-info":     "셸 프롬프트용 worktree 표시 출력",
}

// koUsageTemplate is cobra's default usage template with the structural
// labels translated to Korean (Usage→사용법, Flags→옵션, …). The {{...}}
// actions are left untouched. Installed on the root (and inherited by every
// subcommand) only when Easy Mode + Korean is active.
const koUsageTemplate = `사용법:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

다른 이름:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

예시:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

사용할 수 있는 명령:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

옵션:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

공통 옵션:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

추가 도움말:{{range .Commands}}{{if .IsAdditionalHelpTopic}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

자세한 내용은 "{{.CommandPath}} [command] --help" 를 실행하세요.{{end}}
`

// globalFlagKO maps a persistent (global) flag name to its plain-Korean
// description, shown under "공통 옵션" in every command's help.
var globalFlagKO = map[string]string{
	"debug":        "진단 로그 출력 (실행한 하위 명령·재시도 이유·소요 시간)",
	"dry-run":      "실제로 바꾸지 않고 무엇을 할지만 출력",
	"easy":         "이번 실행에 한해 쉬운 모드 켜기",
	"no-easy":      "설정·환경이 켜놨어도 쉬운 모드 끄기",
	"json":         "지원하는 경우 JSON으로 출력",
	"no-color":     "색상 출력 끄기",
	"repo":         "git 저장소 경로 (기본: 현재 폴더)",
	"show-prompt":  "AI로 보내는 내용(가린 상태) 표시",
	"skip-privacy": "민감정보 차단 기준 건너뛰기 (가림 처리는 유지)",
	"verbose":      "자세히 출력",
}

// easyLongKO maps a command's full path to a plain-Korean long description.
// Filled incrementally (parallel translation); empty entries keep English.
var easyLongKO = map[string]string{}

// easyFlagKO maps "<command path> --<flag>" to a plain-Korean flag
// description for command-local flags. Filled incrementally; missing entries
// keep the English flag usage.
var easyFlagKO = map[string]string{}

// installEasyHelp wires the Easy-Mode Korean help. When Easy Mode + Korean
// is active it installs a Korean usage template (structural labels) and a
// help-func wrapper that swaps each command's one-line description for its
// plain-Korean version (easyShortKO) just for the duration of the render,
// then restores it — keeping the static cobra fields intact elsewhere.
func installEasyHelp(root *cobra.Command) {
	if easyHelpActive() {
		root.SetUsageTemplate(koUsageTemplate)
	}
	base := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if easyHelpActive() {
			defer swapEasyShorts(root)()
		}
		base(cmd, args)
	})
}

// easyHelpActive reports whether the plain-Korean help should be shown:
// Easy Mode enabled and the resolved language is Korean.
func easyHelpActive() bool {
	e := EasyEngine()
	return e.IsEnabled() && strings.HasPrefix(strings.ToLower(e.Lang()), "ko")
}

// swapEasyShorts replaces the Korean-translatable help text — command Short
// and Long, command-local flag usages, and global (persistent) flag usages —
// with their plain-Korean versions, returning a function that restores the
// originals. Keyed on CommandPath() (and "path --flag") so shared leaf names
// under different parents map distinctly. Missing entries keep English.
func swapEasyShorts(root *cobra.Command) func() {
	var restore []func()
	swapStr := func(get func() string, set func(string), val string) {
		if val == "" || get() == "" {
			return
		}
		orig := get()
		set(val)
		restore = append(restore, func() { set(orig) })
	}

	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		path := c.CommandPath()
		swapStr(func() string { return c.Short }, func(s string) { c.Short = s }, easyShortKO[path])
		swapStr(func() string { return c.Long }, func(s string) { c.Long = s }, easyLongKO[path])
		// Command-local flags.
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			ff := f
			swapStr(func() string { return ff.Usage }, func(s string) { ff.Usage = s }, easyFlagKO[path+" --"+ff.Name])
		})
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(root)

	// Global (persistent) flags live on the root and appear under "공통 옵션"
	// in every command's help; swap them once by flag name.
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) {
		ff := f
		swapStr(func() string { return ff.Usage }, func(s string) { ff.Usage = s }, globalFlagKO[ff.Name])
	})

	return func() {
		for _, r := range restore {
			r()
		}
	}
}
