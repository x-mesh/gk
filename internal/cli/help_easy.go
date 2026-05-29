package cli

import (
	"strings"

	"github.com/spf13/cobra"
)

// easyShortKO maps a command name to a plain-Korean one-line description
// shown in `gk --help` when Easy Mode is on and the language is Korean.
// The goal is a non-developer-friendly summary: everyday words, git jargon
// either avoided or paired with a one-clause gloss, command/branch names
// kept as-is. Commands without an entry keep their default English Short.
var easyShortKO = map[string]string{
	"status":    "지금 내 작업이 어떤 상태인지 한눈에 보여줘요",
	"commit":    "바뀐 내용을 메시지와 함께 저장해요 (commit)",
	"push":      "내 변경을 서버(원격)에 올려요",
	"pull":      "서버(원격)의 최신 변경을 가져와요",
	"sync":      "내 작업을 기준 브랜치의 최신 상태에 맞춰요",
	"switch":    "다른 브랜치로 옮기거나, 목록에서 골라 들어가요",
	"branch":    "브랜치를 살펴보고 정리해요",
	"log":       "지금까지의 변경 기록(커밋)을 보여줘요",
	"merge":     "다른 브랜치의 내용을 지금 브랜치로 합쳐요",
	"worktree":  "여러 작업 공간(worktree)을 만들고 관리해요",
	"resolve":   "충돌(같은 부분을 서로 다르게 고친 것)을 풀도록 도와줘요",
	"pr":        "변경 내용으로 코드 검토 요청(PR) 설명을 만들어줘요",
	"ask":       "git이나 gk에 대한 궁금한 점에 답해줘요",
	"do":        "하고 싶은 일을 말하면 알맞은 명령을 찾아 실행해요",
	"explain":   "에러 메시지나 방금 한 작업을 쉬운 말로 풀어 설명해요",
	"doctor":    "설정과 환경이 제대로 돼 있는지 점검해요",
	"reset":     "현재 브랜치를 서버(원격) 상태로 되돌려요",
	"clone":     "원격 저장소를 내 컴퓨터로 복제해요",
	"changelog": "변경 기록을 정리한 변경 내역(changelog)을 만들어요",
}

// installEasyHelp wraps the root help function so that, when Easy Mode is on
// and the language is Korean, each command's one-line description is swapped
// for its plain-Korean version (easyShortKO) just for the duration of the
// help render, then restored. This keeps the static cobra Short fields
// intact for every other code path.
func installEasyHelp(root *cobra.Command) {
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

// swapEasyShorts replaces Short on every command that has an easyShortKO
// entry, returning a function that restores the originals.
func swapEasyShorts(root *cobra.Command) func() {
	var restore []func()
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if s, ok := easyShortKO[c.Name()]; ok && c.Short != "" {
			orig := c.Short
			c.Short = s
			restore = append(restore, func() { c.Short = orig })
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(root)
	return func() {
		for _, r := range restore {
			r()
		}
	}
}
