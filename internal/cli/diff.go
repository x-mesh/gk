package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/diff"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// diff 명령어 로컬 플래그
var (
	diffFlagStaged     bool
	diffFlagStat       bool
	diffFlagInteract   bool
	diffFlagNoPager    bool
	diffFlagNoWordDiff bool
	diffFlagContext    int
)

func init() {
	diffCmd := &cobra.Command{
		Use:   "diff [flags] [<ref>] [<ref>..<ref>] [-- <path>...]",
		Short: "향상된 diff 뷰어",
		Long:  "git diff 출력을 향상된 터미널 시각화로 제공한다. 컬러 코딩, 라인 번호, 워드 diff, 인터랙티브 파일 탐색을 지원한다.",
		RunE:  runDiff,
	}
	diffCmd.Flags().BoolVar(&diffFlagStaged, "staged", false, "staged 변경사항 표시")
	diffCmd.Flags().BoolVar(&diffFlagStat, "stat", false, "diff 통계 요약 표시")
	diffCmd.Flags().BoolVarP(&diffFlagInteract, "interactive", "i", false, "인터랙티브 파일 탐색 모드")
	diffCmd.Flags().BoolVar(&diffFlagNoPager, "no-pager", false, "페이저 비활성화")
	diffCmd.Flags().BoolVar(&diffFlagNoWordDiff, "no-word-diff", false, "단어 단위 하이라이트 비활성화")
	diffCmd.Flags().IntVarP(&diffFlagContext, "context", "U", 3, "컨텍스트 라인 수")
	// --json, --no-color는 rootCmd의 persistent 플래그 사용
	rootCmd.AddCommand(diffCmd)
}

// runDiff는 gk diff 명령어의 메인 실행 함수이다.
// 플래그 파싱 → git diff 실행 → 파싱 → 출력 분기를 수행한다.
func runDiff(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// ── 1. 플래그 검증 ──────────────────────────────────────────
	if diffFlagContext < 0 {
		return WithHint(
			fmt.Errorf("gk diff: 컨텍스트 라인 수는 0 이상이어야 합니다: %d", diffFlagContext),
			"예: gk diff -U3",
		)
	}

	useJSON := JSONOut()
	noColor := NoColorFlag()
	noPager := diffFlagNoPager

	// JSON 모드에서는 색상과 페이저를 비활성화한다.
	if useJSON {
		noColor = true
		noPager = true
	}

	// ── 2. git diff 실행 ────────────────────────────────────────
	runner := &git.ExecRunner{Dir: RepoFlag()}
	gitArgs := buildDiffArgs(args)

	stdout, stderr, err := runner.Run(ctx, gitArgs...)
	if err != nil {
		return classifyGitError(err, stderr, args)
	}

	// ── 3. 변경사항 없음 처리 ───────────────────────────────────
	raw := string(stdout)
	if strings.TrimSpace(raw) == "" {
		if useJSON {
			return diff.WriteJSON(cmd.OutOrStdout(), &diff.DiffResult{})
		}
		renderDiffNoChanges(cmd.ErrOrStderr(), ctx, runner, diffFlagStaged, args)
		return nil
	}

	// ── 4. diff 파싱 ────────────────────────────────────────────
	result, parseErr := diff.ParseUnifiedDiff(bytes.NewReader(stdout))
	if parseErr != nil {
		// 그레이스풀 디그레이드: 파싱 실패 시 원본 텍스트 그대로 출력
		fmt.Fprintln(cmd.ErrOrStderr(), "diff 파싱 실패, 원본 출력을 표시합니다")
		return writeDiffWithPager(cmd, []byte(raw), noPager)
	}

	// ── 5. 렌더 옵션 구성 ───────────────────────────────────────
	opts := diff.RenderOptions{
		NoColor:    noColor,
		NoWordDiff: diffFlagNoWordDiff,
		Context:    diffFlagContext,
	}

	// ── 6. 출력 분기 ────────────────────────────────────────────

	// 6a. JSON 출력
	if useJSON {
		return diff.WriteJSON(cmd.OutOrStdout(), result)
	}

	// 6b. 인터랙티브 모드
	if diffFlagInteract {
		if !ui.IsTerminal() {
			// non-TTY에서는 일반 출력으로 폴백
			fmt.Fprintln(cmd.ErrOrStderr(), "경고: TTY가 아닌 환경에서 인터랙티브 모드를 사용할 수 없습니다. 일반 출력으로 전환합니다.")
		} else {
			return runDiffInteractive(result, opts, noPager)
		}
	}

	// 6c. stat + diff 또는 diff만 출력
	var buf bytes.Buffer

	if diffFlagStat {
		if err := diff.RenderStat(&buf, result, noColor); err != nil {
			return fmt.Errorf("gk diff: stat 렌더링 실패: %w", err)
		}
		buf.WriteString("\n")
	}

	if err := diff.Render(&buf, result, opts); err != nil {
		return fmt.Errorf("gk diff: 렌더링 실패: %w", err)
	}

	// 6d. 페이저 연동
	return writeDiffWithPager(cmd, buf.Bytes(), noPager)
}

// buildDiffArgs는 사용자 인자와 플래그를 기반으로 git diff 인자를 조합한다.
func buildDiffArgs(userArgs []string) []string {
	args := []string{"diff"}

	if diffFlagStaged {
		args = append(args, "--cached")
	}

	// 컨텍스트 라인 수
	args = append(args, fmt.Sprintf("-U%d", diffFlagContext))

	// 사용자 인자 (ref, ref..ref, -- path...) 그대로 전달
	args = append(args, userArgs...)

	return args
}

// classifyGitError는 git diff 실행 에러를 분류하여 적절한 힌트와 함께 반환한다.
func classifyGitError(err error, stderr []byte, userArgs []string) error {
	msg := err.Error()
	stderrStr := string(stderr)

	// git 저장소가 아닌 디렉토리
	if strings.Contains(msg, "not a git repository") || strings.Contains(stderrStr, "not a git repository") {
		return WithHint(
			fmt.Errorf("gk diff: git 저장소가 아닙니다"),
			"git init 으로 저장소를 초기화하거나, 올바른 디렉토리로 이동하세요",
		)
	}

	// 유효하지 않은 ref
	if strings.Contains(stderrStr, "unknown revision") || strings.Contains(stderrStr, "bad revision") {
		ref := extractRef(userArgs)
		return WithHint(
			fmt.Errorf("gk diff: ref를 찾을 수 없습니다: %s", ref),
			"git branch -a 로 사용 가능한 브랜치를 확인하세요",
		)
	}

	// 기타 git 프로세스 실패: stderr 포함
	var exitErr *git.ExitError
	if errors.As(err, &exitErr) && stderrStr != "" {
		return WithHint(
			fmt.Errorf("gk diff: git 프로세스 실패: %s", strings.TrimSpace(stderrStr)),
			"git diff 명령어를 직접 실행하여 원인을 확인하세요",
		)
	}

	return fmt.Errorf("gk diff: %w", err)
}

// extractRef는 사용자 인자에서 ref를 추출한다.
// -- 이전의 첫 번째 인자를 ref로 간주한다.
func extractRef(args []string) string {
	for _, a := range args {
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return "(unknown)"
}

// renderDiffNoChanges prints a context-aware "no changes" banner when
// `git diff …` produced empty output. The banner does three things the
// previous one-liner did not:
//
//  1. Names the comparison that was just performed (working tree ↔
//     index, index ↔ HEAD, working tree ↔ <ref>) so users with mixed
//     mental models see what gk actually compared.
//  2. Probes the *other* side cheaply — if the user ran `gk diff`
//     with all changes staged, the staged probe surfaces that fact
//     and points at `gk diff --staged` directly, instead of leaving
//     the user puzzled why their staged work is invisible.
//  3. Always shows two universal escape hatches (`gk diff HEAD`,
//     `gk diff <ref>`) so the user sees how to widen the comparison
//     without leaving the message.
//
// All output goes to stderr (the caller passes cmd.ErrOrStderr()) so
// pipelines that capture stdout aren't polluted by the hint.
func renderDiffNoChanges(w io.Writer, ctx context.Context, runner git.Runner, staged bool, userArgs []string) {
	bold := color.New(color.Bold).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()

	scope, label := diffComparisonLabel(staged, userArgs)
	fmt.Fprintf(w, "변경사항 없음  %s\n", faint("("+label+")"))

	// Smart hint: probe the side the user did NOT compare.
	switch scope {
	case "default":
		// User compared working tree ↔ index. Are there staged changes?
		if n := countStagedFiles(ctx, runner); n > 0 {
			fmt.Fprintf(w, "  %s staged 변경 %s — %s\n",
				faint("hint:"),
				bold(fmt.Sprintf("%d 파일", n)),
				bold("gk diff --staged"))
		}
	case "staged":
		// User compared index ↔ HEAD. Are there unstaged changes?
		if hasUnstagedChanges(ctx, runner) {
			fmt.Fprintf(w, "  %s unstaged 변경 있음 — %s\n",
				faint("hint:"),
				bold("gk diff"))
		}
	}

	// Universal alternates — surface even when the smart probe found
	// nothing, so first-time users learn the comparison vocabulary.
	fmt.Fprintf(w, "  %s %s\n",
		faint("또는:"),
		bold("gk diff HEAD")+"     "+faint("(staged + unstaged 합쳐서)"))
	fmt.Fprintf(w, "        %s\n",
		bold("gk diff <ref>")+"   "+faint("(다른 commit/branch와 비교)"))
}

// diffComparisonLabel returns (scopeKey, humanLabel) describing which
// pairing of trees gk just diffed. scopeKey drives the smart probe;
// humanLabel goes in the banner.
func diffComparisonLabel(staged bool, userArgs []string) (scope, label string) {
	switch {
	case staged:
		return "staged", "index ↔ HEAD · --staged"
	case hasExplicitDiffRef(userArgs):
		ref := extractRef(userArgs)
		return "ref", "working tree ↔ " + ref
	default:
		return "default", "working tree ↔ index · 기본"
	}
}

// hasExplicitDiffRef reports whether the user passed any positional
// argument that is not a flag and not the `--` path separator. Mirror
// of extractRef's traversal — kept separate for readability.
func hasExplicitDiffRef(args []string) bool {
	for _, a := range args {
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") {
			return true
		}
	}
	return false
}

// countStagedFiles returns the number of files with staged changes,
// or 0 on any error. Cheap probe (single git invocation).
func countStagedFiles(ctx context.Context, runner git.Runner) int {
	out, _, err := runner.Run(ctx, "diff", "--cached", "--name-only")
	if err != nil {
		return 0
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

// hasUnstagedChanges reports whether the working tree differs from the
// index. Uses `git diff --quiet` which exits 1 when changes exist —
// the cheapest possible probe (no diff content materialised).
func hasUnstagedChanges(ctx context.Context, runner git.Runner) bool {
	_, _, err := runner.Run(ctx, "diff", "--quiet")
	return err != nil
}

// writeDiffWithPager는 출력을 페이저를 통해 표시하거나, 페이저가 비활성화된 경우 stdout으로 직접 출력한다.
func writeDiffWithPager(cmd *cobra.Command, data []byte, noPager bool) error {
	if noPager || !ui.IsTerminal() {
		_, err := cmd.OutOrStdout().Write(data)
		return err
	}

	pg := ui.Detect()
	if pg.Disabled {
		_, err := cmd.OutOrStdout().Write(data)
		return err
	}

	w, wait, err := pg.Run()
	if err != nil {
		// 페이저 실행 실패 시 stdout으로 폴백
		_, writeErr := cmd.OutOrStdout().Write(data)
		return writeErr
	}

	_, writeErr := w.Write(data)
	closeErr := w.Close()
	waitErr := wait()

	// 쓰기 에러가 가장 중요
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	// 페이저 종료 에러는 사용자가 q로 종료한 경우 등이므로 무시
	_ = waitErr
	return nil
}
