package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/diff"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// diff 명령어 로컬 플래그
var (
	diffFlagStaged      bool
	diffFlagStat        bool
	diffFlagInteract    bool
	diffFlagNoPager     bool
	diffFlagNoWordDiff  bool
	diffFlagContext     int
	diffFlagNoRefLabels bool
	diffFlagConflicts   bool
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
	diffCmd.Flags().BoolVar(&diffFlagNoRefLabels, "no-ref-labels", false, "파일 헤더 아래 ◀/▶ ref 라벨 비활성화")
	diffCmd.Flags().BoolVar(&diffFlagConflicts, "conflicts", false, "merge conflict 마커를 포함한 hunk만 표시 (working tree 비교에서만 유효)")
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

	// --conflicts는 두 가지 데이터 소스를 가진다:
	//   1) ref 인자가 있으면 → `git merge-tree`로 *예상* 충돌 시뮬레이션
	//      (working tree는 깨끗해도 OK — 머지 *전* 미리보기 용도)
	//   2) ref 인자가 없으면 → `git ls-files -u` + working tree 스캔
	//      (이미 머지를 시도해 marker가 박힌 상태에서 사용)
	// --staged와의 조합만 여전히 거부한다 — staged 모드는 index↔HEAD라
	// 충돌 도메인이 모호하고, 사용자가 원하는 working tree marker나
	// merge-tree 시뮬레이션과 모두 어긋난다.
	if diffFlagConflicts && diffFlagStaged {
		return WithHint(
			fmt.Errorf("gk diff --conflicts: --staged와 함께 쓸 수 없습니다"),
			"--staged를 빼면 working tree 또는 ref 비교(가상 머지)로 동작합니다",
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

	runner := &git.ExecRunner{Dir: RepoFlag()}

	// --conflicts 모드는 두 데이터 소스 중 하나를 쓴다 (위 검증부 참고).
	if diffFlagConflicts {
		conflictRefArgs := args
		if dashIdx := cmd.ArgsLenAtDash(); dashIdx >= 0 && dashIdx <= len(args) {
			conflictRefArgs = args[:dashIdx]
		}

		var (
			result   *diff.DiffResult
			err      error
			leftRef  string
			rightRef string
		)

		if hasExplicitDiffRef(conflictRefArgs) {
			// ref 인자 있음 → 가상 머지 시뮬레이션
			target := firstRef(conflictRefArgs)
			result, err = loadMergeTreeConflicts(ctx, runner, target)
			if err != nil {
				return fmt.Errorf("gk diff --conflicts: merge-tree 시뮬레이션 실패: %w", err)
			}
			leftRef, rightRef = "HEAD", target
		} else {
			// working tree marker 스캔
			result, err = loadConflictMarkerDiff(ctx, runner)
			if err != nil {
				return fmt.Errorf("gk diff --conflicts: unmerged 파일 스캔 실패: %w", err)
			}
			leftRef, rightRef = parseDiffRefs(false, nil)
		}

		opts := diff.RenderOptions{
			NoColor:    noColor,
			NoWordDiff: diffFlagNoWordDiff,
			Context:    diffFlagContext,
			ShowRefs:   !diffFlagNoRefLabels,
			LeftRef:    leftRef,
			RightRef:   rightRef,
		}

		if len(result.Files) == 0 {
			if useJSON {
				return diff.WriteJSON(cmd.OutOrStdout(), result)
			}
			noConflictTarget := ""
			if hasExplicitDiffRef(conflictRefArgs) {
				noConflictTarget = firstRef(conflictRefArgs)
			}
			renderNoConflicts(cmd.ErrOrStderr(), noConflictTarget)
			return nil
		}

		if useJSON {
			return diff.WriteJSON(cmd.OutOrStdout(), result)
		}
		if diffFlagInteract && ui.IsTerminal() {
			return runDiffInteractive(result, opts, noPager)
		}

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
		return writeDiffWithPager(cmd, buf.Bytes(), noPager)
	}

	// ── 2. git diff 실행 ────────────────────────────────────────
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

	// --conflicts 모드는 이미 위쪽 분기에서 처리됨 (combined-diff 우회).

	// ── 5. 렌더 옵션 구성 ───────────────────────────────────────
	// cobra는 `--` 자체를 args에서 소비하기 때문에 path 토큰이 ref와
	// 섞여 들어온다. ArgsLenAtDash()로 `--` 앞 토큰만 잘라 ref 라벨링에
	// 쓴다 (음수면 dash 없음 = 전체가 ref 후보).
	refArgs := args
	if dashIdx := cmd.ArgsLenAtDash(); dashIdx >= 0 && dashIdx <= len(args) {
		refArgs = args[:dashIdx]
	}
	leftRef, rightRef := parseDiffRefs(diffFlagStaged, refArgs)
	opts := diff.RenderOptions{
		NoColor:    noColor,
		NoWordDiff: diffFlagNoWordDiff,
		Context:    diffFlagContext,
		ShowRefs:   !diffFlagNoRefLabels,
		LeftRef:    leftRef,
		RightRef:   rightRef,
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

// parseDiffRefs는 사용자 인자와 --staged 플래그로부터 비교 양쪽 ref의
// 사람이 읽을 라벨을 추출한다. 반환값은 (left, right) — 각각 ◀/▶ 화살표가
// 가리키는 쪽이다. git diff 인자 의미론을 그대로 따른다:
//
//   - --staged              : HEAD ↔ index           → ("HEAD", "index")
//   - (no args)             : index ↔ working tree   → ("index", "working tree")
//   - <ref>                 : <ref> ↔ working tree   → ("<ref>", "working tree")
//   - <a>..<b>              : a ↔ b                  → ("<a>", "<b>")
//   - <a>...<b>             : merge-base(a,b) ↔ b    → ("<a>...", "<b>")
//   - <a> <b>               : a ↔ b                  → ("<a>", "<b>")
//
// `--` 이후 경로 인자는 무시한다. 플래그(`-foo`)도 ref로 간주하지 않는다.
func parseDiffRefs(staged bool, userArgs []string) (left, right string) {
	if staged {
		return "HEAD", "index"
	}

	var refs []string
	for _, a := range userArgs {
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		refs = append(refs, a)
	}

	switch len(refs) {
	case 0:
		return "index", "working tree"
	case 1:
		r := refs[0]
		if i := strings.Index(r, "..."); i >= 0 {
			return r[:i] + "...", r[i+3:]
		}
		if i := strings.Index(r, ".."); i >= 0 {
			return r[:i], r[i+2:]
		}
		return r, "working tree"
	default:
		return refs[0], refs[1]
	}
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

// firstRef는 hasExplicitDiffRef와 같은 traversal로 첫 번째 비-flag
// 토큰을 반환한다. ref가 없으면 빈 문자열 (호출자가 이미 has-ref 검사를
// 마친 후 호출하기 때문에 실용적으로는 항상 채워져 있다).
func firstRef(args []string) string {
	for _, a := range args {
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		// "A..B"/"A...B" 형태는 target이 뒤쪽이라고 보는 게 자연스럽다
		// (사용자가 머지하려는 대상). 단일 토큰도 동일 함수로 처리.
		if i := strings.Index(a, "..."); i >= 0 {
			return a[i+3:]
		}
		if i := strings.Index(a, ".."); i >= 0 {
			return a[i+2:]
		}
		return a
	}
	return ""
}

// loadMergeTreeConflicts는 HEAD와 target을 가상으로 머지한 결과에서
// conflict marker가 포함된 파일을 가져와 DiffResult로 반환한다.
//
// 동작:
//  1. `git merge-tree --write-tree HEAD <target>`로 결과 tree OID와 충돌
//     stage 라인을 받는다 (clean=exit 0, conflict=exit 1, 둘 다 stdout 사용).
//  2. stage 라인(`<mode> <sha> <stage>\t<path>`)에서 path를 dedup해 모은다.
//  3. 각 path에 대해 `git show <tree-OID>:<path>`로 conflict marker가 박힌
//     파일 내용을 받는다.
//  4. 그 내용을 working-tree 경로와 같은 가짜 hunk 구조로 변환한다.
//
// `gk precheck`와 동일한 백엔드(merge-tree)를 쓰므로 사용자가 본 precheck
// 결과와 일관된다.
func loadMergeTreeConflicts(ctx context.Context, runner git.Runner, target string) (*diff.DiffResult, error) {
	stdout, stderr, err := runner.Run(ctx, "merge-tree", "--write-tree", "HEAD", target)
	if err != nil {
		// exit code 1은 "충돌 있음"의 정상 신호다. 출력은 정상적으로
		// stdout에 들어있다. 그 외 코드는 진짜 에러.
		var exitErr *git.ExitError
		if !errors.As(err, &exitErr) || exitErr.Code > 1 {
			msg := strings.TrimSpace(string(stderr))
			if msg == "" {
				msg = err.Error()
			}
			return nil, fmt.Errorf("git merge-tree: %s", msg)
		}
	}

	lines := strings.Split(string(stdout), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		// bad ref나 dangling reference면 git이 stderr에만 메시지를 쓰고
		// stdout은 비어있다 (exit 1로 떨어져 위 분기가 못 잡음).
		if msg := strings.TrimSpace(string(stderr)); msg != "" {
			return nil, fmt.Errorf("git merge-tree: %s", msg)
		}
		return &diff.DiffResult{}, nil
	}
	treeOID := strings.TrimSpace(lines[0])

	// 첫 줄 다음부터 빈 줄 직전까지가 conflict stage 영역.
	seen := make(map[string]bool)
	var paths []string
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			break
		}
		tabIdx := strings.IndexByte(line, '\t')
		if tabIdx < 0 {
			continue
		}
		p := strings.TrimSpace(line[tabIdx+1:])
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}

	result := &diff.DiffResult{}
	for _, p := range paths {
		showOut, _, showErr := runner.Run(ctx, "show", treeOID+":"+p)
		if showErr != nil {
			// 파일이 한쪽에서만 삭제된 경우 등 — skip
			continue
		}
		fileLines := strings.Split(string(showOut), "\n")
		if n := len(fileLines); n > 0 && fileLines[n-1] == "" {
			fileLines = fileLines[:n-1]
		}

		hunks := diff.BuildConflictHunks(fileLines, 3)
		if len(hunks) == 0 {
			continue
		}

		result.Files = append(result.Files, diff.DiffFile{
			NewPath:      p,
			OldPath:      p,
			Status:       diff.StatusModified,
			Hunks:        hunks,
			AddedLines:   countLineKind(hunks, diff.LineAdded),
			DeletedLines: countLineKind(hunks, diff.LineDeleted),
		})
	}

	return result, nil
}

// countLineKind는 hunk들에서 특정 kind 라인의 총 수를 센다. DiffFile의
// AddedLines/DeletedLines 통계 채우기 용도.
func countLineKind(hunks []diff.Hunk, kind diff.LineKind) int {
	n := 0
	for _, h := range hunks {
		for _, l := range h.Lines {
			if l.Kind == kind {
				n++
			}
		}
	}
	return n
}

// loadConflictMarkerDiff는 unmerged 파일을 직접 읽어 conflict marker가
// 포함된 hunk로 구성된 DiffResult를 반환한다. 머지 충돌 중 `git diff`는
// combined diff(`@@@`) 형식을 출력하기 때문에 일반 unified-diff 파서로는
// marker 라인을 안정적으로 감지할 수 없다 — 그래서 `git ls-files -u`로
// unmerged path 목록을 얻고, 각 파일의 working tree 내용을 한 hunk로
// 변환한다. marker가 없는 파일은 건너뛴다 (예: 이미 해결된 파일).
//
// 결과 hunk는 컨텍스트 라인만 포함하며 word-diff와 fold는 의미가 없다 —
// 사용자가 marker 주변을 직접 편집하거나 `gk resolve`로 넘어가게 하는 게
// 목적이다.
func loadConflictMarkerDiff(ctx context.Context, runner git.Runner) (*diff.DiffResult, error) {
	// git ls-files -u 출력 형식: "<mode> <sha> <stage>\t<path>". 같은 path가
	// stage 1/2/3로 최대 3번 반복되므로 dedup이 필요하다. 탭이 path 자체에
	// 들어가는 경우는 없으므로 첫 탭 이후를 path로 잡는다.
	out, _, err := runner.Run(ctx, "ls-files", "-u")
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		tabIdx := strings.IndexByte(line, '\t')
		if tabIdx < 0 {
			continue
		}
		p := strings.TrimSpace(line[tabIdx+1:])
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}

	repoDir := RepoFlag()
	if repoDir == "" {
		repoDir = "."
	}

	result := &diff.DiffResult{}
	for _, p := range paths {
		full := p
		if !filepath.IsAbs(p) {
			full = filepath.Join(repoDir, p)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		// 파일 끝의 빈 라인 1개는 split 부산물 — 잘라낸다.
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = lines[:n-1]
		}

		hunks := diff.BuildConflictHunks(lines, 3)
		if len(hunks) == 0 {
			continue
		}

		result.Files = append(result.Files, diff.DiffFile{
			NewPath:      p,
			OldPath:      p,
			Status:       diff.StatusModified,
			Hunks:        hunks,
			AddedLines:   countLineKind(hunks, diff.LineAdded),
			DeletedLines: countLineKind(hunks, diff.LineDeleted),
		})
	}

	return result, nil
}

// renderNoConflicts는 `gk diff --conflicts`가 marker를 하나도 찾지
// 못했을 때 stderr로 출력한다. "변경사항 없음" 배너와 동일한 톤(굵음
// + 흐림 hint)을 써서 시각적으로 같은 계열의 안내임을 알린다. target이
// 비어있지 않으면 ref-비교 시뮬레이션 컨텍스트로 메시지를 다르게 한다.
func renderNoConflicts(w io.Writer, target string) {
	bold := color.New(color.Bold).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()

	if target != "" {
		fmt.Fprintf(w, "예상 충돌 없음  %s\n", faint("("+target+"을 깨끗하게 머지할 수 있음)"))
		fmt.Fprintf(w, "  %s %s\n", faint("머지 실행:"), bold("gk merge "+target))
		fmt.Fprintf(w, "  %s %s\n", faint("변경 보기:"), bold("gk diff "+target)+faint("  (--conflicts 없이)"))
		return
	}

	fmt.Fprintf(w, "충돌 마커 없음  %s\n", faint("(working tree에 <<<<<<< / ======= / >>>>>>> 라인 없음)"))
	fmt.Fprintf(w, "  %s %s\n", faint("상태 확인:"), bold("gk status"))
	fmt.Fprintf(w, "  %s %s\n", faint("머지 중단:"), bold("gk merge --abort")+faint("  (머지 진행 중이라면)"))
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
