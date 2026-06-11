package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/ui"
)

// This file is the wizard's display-settings step: the user picks the default
// `gk log` / `gk status` layers from a checkbox list whose live preview
// re-renders a canned sample on every toggle. The samples are static text —
// faithful in glyphs and shape to the real renderers, but independent of the
// repo the wizard happens to run in (setup usually targets the GLOBAL config,
// and must work outside a repository).

// logVisChoices lists the selectable `gk log` layers in display order. The
// pseudo-key "graph" is in the same list (it reads as just another layer) but
// is stored as the log.graph bool, not a log.vis element.
var logVisChoices = []ui.MultiSelectItem{
	{Key: "cc", Display: "cc — Conventional-Commits 글리프 + 타입 집계"},
	{Key: "safety", Display: "safety — push 경계 마커 ◇"},
	{Key: "tags-rule", Display: "tags-rule — 태그/unpushed 구분선"},
	{Key: "impact", Display: "impact — 커밋별 변경량 막대"},
	{Key: "graph", Display: "graph — 토폴로지 그래프 선"},
	{Key: "pulse", Display: "pulse — 기간 커밋 빈도 스파크라인"},
	{Key: "calendar", Display: "calendar — 요일×주 히트 캘린더"},
	{Key: "hotspots", Display: "hotspots — 자주 바뀌는 파일 마커 ◉"},
	{Key: "trailers", Display: "trailers — 커밋 트레일러 표시"},
	{Key: "lanes", Display: "lanes — 작성자별 타임라인 레인"},
	{Key: "breaking", Display: "breaking — BREAKING CHANGE 마커 ‼"},
	{Key: "squash", Display: "squash — fixup!/WIP 정리 후보 ⊟"},
	{Key: "wip", Display: "wip — WIP 연속 구간 깊이 ≡N"},
}

// statusVisChoices lists the selectable `gk status` layers in display order.
var statusVisChoices = []ui.MultiSelectItem{
	{Key: "gauge", Display: "gauge — upstream 대비 ↑↓ 게이지"},
	{Key: "base", Display: "base — base 브랜치 대비 한 줄 + 다음 행동"},
	{Key: "bar", Display: "bar — 트리 상태 구성 막대"},
	{Key: "progress", Display: "progress — 정리 진행률 + 남은 작업"},
	{Key: "types", Display: "types — 확장자별 변경 집계"},
	{Key: "tree", Display: "tree — 변경 파일 트리"},
	{Key: "staleness", Display: "staleness — 파일별 마지막 수정 나이"},
	{Key: "local", Display: "local — BRANCH 줄 작업트리 배지"},
	{Key: "since-push", Display: "since-push — 미push 커밋 나이/개수"},
	{Key: "conflict", Display: "conflict — 충돌 파일 강조"},
	{Key: "churn", Display: "churn — 최근 자주 바뀐 파일"},
	{Key: "risk", Display: "risk — 변경 위험도 요약"},
	{Key: "stash", Display: "stash — stash 개수/나이 한 줄"},
	{Key: "heatmap", Display: "heatmap — 파일별 변경 강도"},
	{Key: "wip", Display: "wip — HEAD의 WIP 체인 깊이 배지"},
	{Key: "squash", Display: "squash — 미정리 fixup!/WIP 카운트 ◈"},
	{Key: "ancestry", Display: "ancestry — 브랜치 스택 depth (parent 체인)"},
	{Key: "collision", Display: "collision — 다른 worktree와 같은 파일 더티 ⊠"},
}

// xyStyleChoices is the three-way per-entry state column pick, each option
// carrying its own sample so the choice is visual, not nominal.
var xyStyleChoices = []ui.ScrollSelectOption{
	{Key: "l", Value: "labels", Display: "labels — new / mod / staged / conflict"},
	{Key: "g", Value: "glyphs", Display: "glyphs — + ~ ● ⚔ #"},
	{Key: "r", Value: "raw", Display: "raw — ?? .M A. UU (git porcelain)"},
}

// wizardDisplay collects the log/status display settings. Any display flag
// (--log-vis / --log-graph / --status-vis / --xy-style) switches the whole
// step to flag-only mode — scripts state exactly what they want and get no
// prompts. Interactively the step sits behind one confirm, then offers the
// two layer pickers with live previews and the xy-style pick.
func wizardDisplay(cmd *cobra.Command, ctx context.Context, cur *config.Config, changes map[string]string, lists map[string][]string) error {
	flagged := false
	if cmd.Flags().Changed("log-vis") {
		v, _ := cmd.Flags().GetStringSlice("log-vis")
		if slices.Contains(v, "graph") {
			return fmt.Errorf("gk config setup: --log-vis: graph는 레이어가 아니라 --log-graph 플래그로 지정하세요")
		}
		if err := validateVisNames(v, logVisChoices); err != nil {
			return fmt.Errorf("gk config setup: --log-vis: %w", err)
		}
		lists["log.vis"] = v
		flagged = true
	}
	if cmd.Flags().Changed("log-graph") {
		v, _ := cmd.Flags().GetBool("log-graph")
		changes["log.graph"] = strconv.FormatBool(v)
		flagged = true
	}
	if cmd.Flags().Changed("status-vis") {
		v, _ := cmd.Flags().GetStringSlice("status-vis")
		if err := validateVisNames(v, statusVisChoices); err != nil {
			return fmt.Errorf("gk config setup: --status-vis: %w", err)
		}
		lists["status.vis"] = v
		flagged = true
	}
	if cmd.Flags().Changed("xy-style") {
		v, _ := cmd.Flags().GetString("xy-style")
		switch v {
		case "labels", "glyphs", "raw":
			changes["status.xy_style"] = v
		default:
			return fmt.Errorf("gk config setup: --xy-style: %q (labels/glyphs/raw 중 하나)", v)
		}
		flagged = true
	}
	if flagged || !ui.IsTerminal() {
		return nil
	}

	want, err := ui.ConfirmTUI(ctx, "log/status 표시 옵션을 미리보기로 골라볼까요?",
		"gk log / gk status가 기본으로 그리는 시각화 레이어를 선택합니다", true)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return errWizardAborted
		}
		return nil
	}
	if !want {
		return nil
	}

	// gk log layers. Current values pre-checked; Enter without touching
	// anything round-trips to a no-op (pruned before writing).
	preLog := map[string]bool{}
	for _, v := range cur.Log.Vis {
		preLog[v] = true
	}
	if cur.Log.Graph {
		preLog["graph"] = true
	}
	sel, err := ui.MultiSelectPreviewTUI(ctx, "gk log 기본 레이어", logVisChoices, preLog, previewLogVis)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return errWizardAborted
		}
		return nil
	}
	graph := false
	logVis := make([]string, 0, len(sel))
	for _, k := range sel {
		if k == "graph" {
			graph = true
			continue
		}
		logVis = append(logVis, k)
	}
	lists["log.vis"] = logVis
	changes["log.graph"] = strconv.FormatBool(graph)

	// gk status layers.
	preSt := map[string]bool{}
	for _, v := range cur.Status.Vis {
		preSt[v] = true
	}
	stVis, err := ui.MultiSelectPreviewTUI(ctx, "gk status 기본 레이어", statusVisChoices, preSt, previewStatusVis)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return errWizardAborted
		}
		return nil
	}
	lists["status.vis"] = stVis

	// Per-entry state column style — each option's sample is in its label.
	opts := make([]ui.ScrollSelectOption, len(xyStyleChoices))
	copy(opts, xyStyleChoices)
	curStyle := cur.Status.XYStyle
	if curStyle == "" {
		curStyle = "labels"
	}
	for i := range opts {
		opts[i].IsDefault = opts[i].Value == curStyle
	}
	style, err := ui.ScrollSelectTUI(ctx, "gk status 항목 상태 표기",
		previewXYStyles(), opts)
	if err == nil && style != "" {
		changes["status.xy_style"] = style
	} else if errors.Is(err, ui.ErrPickerAborted) {
		return errWizardAborted
	}
	return nil
}

// validateVisNames rejects layer names outside the choice list, so a typo in
// --log-vis/--status-vis fails the wizard instead of landing in the config.
func validateVisNames(names []string, choices []ui.MultiSelectItem) error {
	valid := make(map[string]bool, len(choices))
	for _, c := range choices {
		valid[c.Key] = true
	}
	for _, n := range names {
		if !valid[n] {
			return fmt.Errorf("알 수 없는 레이어 %q", n)
		}
	}
	return nil
}

// previewLogVis renders the canned `gk log` sample for the selected layers.
// Three commits — an unpushed feat, a pushed fix, a tagged chore — exercise
// every layer's glyph.
func previewLogVis(selected []string) string {
	on := map[string]bool{}
	for _, k := range selected {
		on[k] = true
	}

	var b strings.Builder
	if on["cc"] {
		b.WriteString("scope: 3 commits · feat=1 fix=1 chore=1\n")
	}
	if on["pulse"] {
		b.WriteString("pulse 30d ▁▇▄▂·▂▁▅█▅·▂▁▄  (128 commits, peak Wed)\n")
	}
	if on["calendar"] {
		b.WriteString("     W1  W2  W3  W4\n")
		b.WriteString("Mon  ░   ▓   ·   █\n")
		b.WriteString("Wed  █   ░   ▒   ░\n")
	}

	// One sample commit row, columns assembled from the selected layers.
	row := func(unpushed bool, ccGlyph, hash, age, author, subject, impact string, hot bool) {
		b.WriteString("●")
		if on["safety"] && unpushed {
			b.WriteString(" ◇")
		}
		if on["cc"] {
			b.WriteString(" " + ccGlyph)
		}
		fmt.Fprintf(&b, " %s (%s) <%s> %s", hash, age, author, subject)
		if on["impact"] {
			b.WriteString("  " + impact)
		}
		if on["hotspots"] && hot {
			b.WriteString("  ◉")
		}
		b.WriteString("\n")
	}

	if on["wip"] || on["squash"] {
		mark := ""
		if on["squash"] {
			mark = "⊟ "
		}
		depth := ""
		if on["wip"] {
			depth = "  ≡2"
		}
		b.WriteString("┌ ● " + mark + "f9e8d7c (10m) <jinwoo> WIP: 폼 검증" + depth + "\n")
		b.WriteString("└ ● " + mark + "c6b5a4d (1h) <jinwoo> save\n")
	}
	if on["breaking"] {
		row(true, "▲", "a1b2c3d", "2h", "jinwoo", "‼ feat!: 로그인 API v2", "████████ +210 −15", true)
	} else {
		row(true, "▲", "a1b2c3d", "2h", "jinwoo", "feat: 로그인 폼 추가", "████████ +210 −15", true)
	}
	if on["trailers"] {
		b.WriteString("          AI-Assisted-By: kiro-api\n")
	}
	if on["tags-rule"] {
		b.WriteString("──┤ ↑ 1 unpushed ├──────────────────────────\n")
	}
	row(false, "✕", "e4f5a6b", "1d", "kim", "fix: 빈 토큰 처리", "▎ +12 −3", false)
	if on["tags-rule"] {
		b.WriteString("──┤ v1.2.0 (3d) ├───────────────────────────\n")
	}
	row(false, "⊖", "9c8d7e6", "3d", "jinwoo", "chore: 의존성 갱신", "▏ +3 −1", false)

	if on["graph"] {
		b.WriteString("│ ● f0e1d2c (4d) <kim> feat: API v2 분기\n")
		b.WriteString("●─┘ b3a4c5d (5d) <jinwoo> Merge branch 'api-v2'\n")
	}
	if on["lanes"] {
		b.WriteString("jinwoo ●────────●──────●\n")
		b.WriteString("kim    ────●──●─────────\n")
	}
	if b.Len() == 0 {
		return "(선택된 레이어 없음 — 기본 한 줄 로그만 출력)"
	}
	return strings.TrimRight(b.String(), "\n")
}

// previewStatusVis renders the canned `gk status` sample for the selected
// layers: a feature branch with one staged and two unstaged files.
func previewStatusVis(selected []string) string {
	on := map[string]bool{}
	for _, k := range selected {
		on[k] = true
	}

	var b strings.Builder
	b.WriteString("█  BRANCH\n")
	branch := "   repo · feature/login ← main"
	if on["local"] {
		branch += " · 2 unstaged · 1 staged"
	}
	if on["since-push"] {
		branch += " · unpushed 2h (3c)"
	}
	b.WriteString(branch + "\n\n")

	b.WriteString("█  WORKING TREE\n")
	if on["gauge"] {
		b.WriteString("   [··▓▓│▒···]  (↑2 ↓1)  vs origin/feature/login\n")
	}
	if on["base"] {
		b.WriteString("   from main  [▓▓▓▓··│··]  → behind main: gk sync\n")
	}
	if on["bar"] {
		b.WriteString("   tree: [███████░░░░░░░░░░░░░] 1S 2?  (3 files)\n")
	}
	if on["progress"] {
		b.WriteString("   clean: [███░░░░░░░] 33%  commit 1 · add 2\n")
	}
	if on["types"] {
		b.WriteString("   types: .go×2 .md×1\n")
	}
	if on["tree"] {
		age1, age2 := "", ""
		if on["staleness"] {
			age1, age2 = "  · 5m", "  · 2h"
		}
		b.WriteString("   ├─ staged   app.go  +12" + age1 + "\n")
		b.WriteString("   ├─ mod      lib.go" + age2 + "\n")
		b.WriteString("   └─ new      readme.md\n")
	}
	if on["wip"] {
		b.WriteString("   wip: ×3 at HEAD (\"save\") · unwraps on gk commit\n")
	}
	if on["squash"] {
		b.WriteString("   squash debt: ◈ 3 (2 fixup · 1 wip) · folds via gk rebase --plan\n")
	}
	if on["ancestry"] {
		b.WriteString("   depth: feature/login → develop → main (2 hops · +18c vs develop)\n")
	}
	if on["collision"] {
		b.WriteString("   ⊠ 2 files also dirty in develop (app.go, lib.go)\n")
	}
	if on["conflict"] {
		b.WriteString("   conflict: merge.go (UU)  → gk resolve\n")
	}
	if on["churn"] {
		b.WriteString("   churn: app.go ×7 this week\n")
	}
	if on["risk"] {
		b.WriteString("   risk: ▲ medium — 1 file >200 lines changed\n")
	}
	if on["stash"] {
		b.WriteString("   stash: 2 entries · newest 3h\n")
	}
	if on["heatmap"] {
		b.WriteString("   heatmap: app.go ▓▓▒░ · lib.go ▒░··\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// previewXYStyles is the static body for the xy-style pick: the same three
// files rendered in each style, so the choice is made by eye.
func previewXYStyles() string {
	return strings.Join([]string{
		"같은 변경을 세 가지 표기로 보면:",
		"",
		"  labels   staged   app.go     glyphs   ● app.go     raw   A. app.go",
		"           mod      lib.go              ~ lib.go           .M lib.go",
		"           new      readme.md           + readme.md        ?? readme.md",
	}, "\n")
}
