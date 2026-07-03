package resolve

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/ui"
)

// Resolver는 충돌 해결의 전체 흐름을 orchestrate한다.
type Resolver struct {
	Runner   git.Runner
	Client   *git.Client
	Provider provider.Provider // nil이면 AI 비활성
	Stderr   io.Writer
	Stdout   io.Writer
	// Root는 worktree 최상위 디렉토리. git이 주는 충돌 경로는 repo-root
	// 상대 경로이므로, 여기서 join해야 --repo로 밖에서 실행하거나 repo
	// 하위 디렉토리에서 실행해도 파일 IO가 맞는다. 빈 문자열이면 cwd 기준
	// (기존 동작).
	Root string
	// ReadFile은 파일 읽기 함수. 테스트에서 override 가능. nil이면 os.ReadFile.
	ReadFile func(path string) ([]byte, error)
	// WriteFile은 파일 쓰기 함수. 테스트에서 override 가능. nil이면 os.WriteFile.
	WriteFile func(path string, data []byte, perm os.FileMode) error

	// deferSkipped는 Run(batch)이 skipped 경로를 degenerate 경로
	// (delete/modify 등)로 곧바로 재처리할 예정임을 표시한다 —
	// ParseConflictFiles가 수동 해결 힌트를 찍지 않게 한다.
	deferSkipped bool
	// deferStage/pendingStage/pendingAccept: ResolveOptions.DeferStage가 켜진
	// 실행에서 git add를 미루고 경로를 모은다 (검증 게이트 뒤에 caller가
	// stage). pendingStage는 gk가 내용을 쓴 파일(롤백 = checkout -m으로 충돌
	// 복원 가능), pendingAccept는 사용자가 이미 정리해 둔 markerless 파일 —
	// 내용을 건드린 적이 없으므로 롤백 시 절대 덮어쓰면 안 된다.
	deferStage    bool
	pendingStage  []string
	pendingAccept []string
}

// readFile은 ReadFile 필드가 nil이면 os.ReadFile을 사용한다.
func (r *Resolver) readFile(path string) ([]byte, error) {
	if r.ReadFile != nil {
		return r.ReadFile(path)
	}
	return os.ReadFile(path)
}

// absPath는 repo-root 상대 경로를 Root 기준 절대 경로로 만든다.
func (r *Resolver) absPath(p string) string {
	if r.Root == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(r.Root, p)
}

// CollectConflictedFiles는 git status에서 unmerged 파일 경로를 수집한다.
func (r *Resolver) CollectConflictedFiles(ctx context.Context) ([]string, error) {
	stdout, _, err := r.Runner.Run(ctx, "status", "--porcelain=v2")
	if err != nil {
		return nil, fmt.Errorf("gk resolve: git status: %w", err)
	}

	var paths []string
	for _, line := range strings.Split(string(stdout), "\n") {
		if !strings.HasPrefix(line, "u ") {
			continue
		}
		fields := strings.SplitN(line, " ", 11)
		if len(fields) < 11 {
			continue
		}
		paths = append(paths, fields[10])
	}
	sort.Strings(paths)
	return paths, nil
}

// ParseConflictFiles는 파일 목록을 파싱하여 ConflictFile 슬라이스를 반환한다.
// 파싱 실패한 파일은 건너뛰고 경고를 stderr에 출력한다.
func (r *Resolver) ParseConflictFiles(paths []string) ([]ConflictFile, []string, error) {
	var files []ConflictFile
	var skipped []string

	for _, p := range paths {
		data, err := r.readFile(r.absPath(p))
		if err != nil {
			if r.Stderr != nil && !r.deferSkipped {
				if os.IsNotExist(err) {
					// Unmerged in the index but absent from the working tree —
					// usually a delete/modify conflict or a file the user removed
					// mid-conflict. gk can't parse what isn't there, so point the
					// user at the two git commands that clear the unmerged stage.
					fmt.Fprintf(r.Stderr, "warning: gk resolve: %s is unmerged in the index but missing from the working tree\n", p)
					fmt.Fprintf(r.Stderr, "  hint: to drop the file, run:  git rm -- %s\n", p)
					fmt.Fprintf(r.Stderr, "  hint: to keep a side, run:    git checkout --ours -- %s   (or --theirs), then git add -- %s\n", p, p)
				} else {
					fmt.Fprintf(r.Stderr, "warning: gk resolve: could not read %s: %v\n", p, err)
				}
			}
			skipped = append(skipped, p)
			continue
		}
		cf, err := Parse(p, data)
		if err != nil {
			if r.Stderr != nil && !r.deferSkipped {
				fmt.Fprintf(r.Stderr, "warning: gk resolve: parse %s: %v\n", p, err)
			}
			skipped = append(skipped, p)
			continue
		}
		files = append(files, cf)
	}
	return files, skipped, nil
}

// ResolveWithAI는 AI provider를 통해 충돌 해결 제안을 가져온다.
// 실패 시 nil을 반환한다 (graceful fallback).
func (r *Resolver) ResolveWithAI(
	ctx context.Context,
	cf ConflictFile,
	opType string,
	lang string,
) ([]HunkResolution, error) {
	resolver, ok := r.Provider.(provider.ConflictResolver)
	if !ok {
		return nil, nil
	}

	// ConflictFile → ConflictResolutionInput 변환
	var hunks []provider.ConflictHunkInput
	hunkIdx := 0
	for i, seg := range cf.Segments {
		if seg.Hunk == nil {
			continue
		}
		h := provider.ConflictHunkInput{
			Index:  hunkIdx,
			Ours:   seg.Hunk.Ours,
			Theirs: seg.Hunk.Theirs,
			Base:   seg.Hunk.Base,
		}

		// context_before: 이전 Context segment의 마지막 3줄
		if i > 0 && cf.Segments[i-1].Hunk == nil {
			ctx := cf.Segments[i-1].Context
			start := len(ctx) - 3
			if start < 0 {
				start = 0
			}
			h.ContextBefore = ctx[start:]
		}

		// context_after: 다음 Context segment의 처음 3줄
		if i+1 < len(cf.Segments) && cf.Segments[i+1].Hunk == nil {
			ctx := cf.Segments[i+1].Context
			end := 3
			if end > len(ctx) {
				end = len(ctx)
			}
			h.ContextAfter = ctx[:end]
		}

		hunks = append(hunks, h)
		hunkIdx++
	}

	input := provider.ConflictResolutionInput{
		FilePath:      cf.Path,
		Hunks:         hunks,
		OperationType: opType,
		Lang:          lang,
	}

	stopSpinner := ui.StartBubbleSpinner(fmt.Sprintf("ai resolve — analyzing %d hunk(s) in %s", len(hunks), cf.Path))
	result, err := resolver.ResolveConflicts(ctx, input)
	stopSpinner()
	if err != nil {
		if r.Stderr != nil {
			fmt.Fprintf(r.Stderr, "warning: gk resolve: AI analysis failed for %s: %v\n", cf.Path, err)
		}
		return nil, err
	}

	resolutions, err := hunkResolutionsFromAI(result.Resolutions, len(hunks))
	if err != nil {
		if r.Stderr != nil {
			fmt.Fprintf(r.Stderr, "warning: gk resolve: invalid AI resolution for %s: %v\n", cf.Path, err)
		}
		return nil, err
	}
	return resolutions, nil
}

// hunkResolutionsFromAI validates provider output before it is allowed to
// rewrite files. The output must be exactly one selected resolution per input
// hunk, keyed by the input hunk index.
func hunkResolutionsFromAI(outputs []provider.ConflictResolutionOutput, hunkCount int) ([]HunkResolution, error) {
	if len(outputs) != hunkCount {
		return nil, fmt.Errorf("got %d resolution(s), want %d", len(outputs), hunkCount)
	}
	resolutions := make([]HunkResolution, hunkCount)
	seen := make([]bool, hunkCount)
	for _, out := range outputs {
		if out.Index < 0 || out.Index >= hunkCount {
			return nil, fmt.Errorf("resolution index %d out of range 0..%d", out.Index, hunkCount-1)
		}
		if seen[out.Index] {
			return nil, fmt.Errorf("duplicate resolution index %d", out.Index)
		}
		strategy := Strategy(out.Strategy)
		switch strategy {
		case StrategyOurs, StrategyTheirs, StrategyMerged:
		default:
			return nil, fmt.Errorf("invalid strategy %q at index %d", out.Strategy, out.Index)
		}
		for _, line := range out.Resolved {
			if looksLikeConflictMarker(line) {
				return nil, fmt.Errorf("conflict marker left in AI resolution at index %d", out.Index)
			}
		}
		seen[out.Index] = true
		resolutions[out.Index] = HunkResolution{
			Strategy:      strategy,
			ResolvedLines: out.Resolved,
			Rationale:     out.Rationale,
		}
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("missing resolution index %d", i)
		}
	}
	return resolutions, nil
}

func looksLikeConflictMarker(line string) bool {
	s := strings.TrimSpace(line)
	return strings.HasPrefix(s, "<<<<<<<") ||
		strings.HasPrefix(s, "|||||||") ||
		strings.HasPrefix(s, "=======") ||
		strings.HasPrefix(s, ">>>>>>>")
}

// ApplyFileResolution은 하나의 파일에 해결을 적용한다.
// backup=true이면 .orig 파일을 생성한다.
func (r *Resolver) ApplyFileResolution(
	ctx context.Context,
	cf ConflictFile,
	resolutions []HunkResolution,
	backup bool,
	dryRun bool,
) error {
	resolved, err := ApplyResolutions(cf, resolutions)
	if err != nil {
		return fmt.Errorf("gk resolve: apply %s: %w", cf.Path, err)
	}

	if dryRun {
		if r.Stdout != nil {
			fmt.Fprintf(r.Stdout, "--- a/%s\n+++ b/%s\n%s\n", cf.Path, cf.Path, string(resolved))
		}
		return nil
	}

	if backup {
		orig, readErr := r.readFile(r.absPath(cf.Path))
		if readErr == nil {
			if backupErr := BackupOriginal(r.WriteFile, r.absPath(cf.Path), orig); backupErr != nil {
				if r.Stderr != nil {
					fmt.Fprintf(r.Stderr, "warning: gk resolve: backup %s.orig: %v\n", cf.Path, backupErr)
				}
			}
		}
	}

	if err := WriteResolved(r.WriteFile, r.absPath(cf.Path), resolved); err != nil {
		return fmt.Errorf("gk resolve: write %s: %w", cf.Path, err)
	}

	if r.deferStage {
		r.pendingStage = append(r.pendingStage, cf.Path)
		return nil
	}
	if err := GitAdd(ctx, r.Runner, cf.Path); err != nil {
		return fmt.Errorf("gk resolve: git add %s: %w", cf.Path, err)
	}

	return nil
}

// stateKindToOpType은 gitstate.StateKind를 operation type 문자열로 변환한다.
func stateKindToOpType(kind gitstate.StateKind) string {
	switch kind {
	case gitstate.StateMerge:
		return "merge"
	case gitstate.StateRebaseMerge, gitstate.StateRebaseApply:
		return "rebase"
	case gitstate.StateCherryPick:
		return "cherry-pick"
	default:
		return "merge"
	}
}

// Run은 옵션에 따라 충돌 해결을 실행한다.
func (r *Resolver) Run(ctx context.Context, state *gitstate.State, opts ResolveOptions) (*ResolveResult, error) {
	r.deferStage = opts.DeferStage && !opts.DryRun
	r.pendingStage = nil
	r.pendingAccept = nil
	defer func() { r.deferStage = false }()
	// 1. 충돌 파일 수집 — state.Kind 가드보다 먼저 한다. git stash
	// apply / git apply --3way / 일부 partial reset 경로는 in-progress
	// op 마커를 남기지 않으면서 index에 unmerged stage만 남기는데,
	// state.Kind만 보고 거부하면 이런 정상적으로 해결 가능한 케이스를
	// 사용자가 손으로 풀어야 하는 막다른 길로 보내게 된다.
	conflicted, err := r.CollectConflictedFiles(ctx)
	if err != nil {
		return nil, err
	}
	if len(conflicted) == 0 {
		if state.Kind == gitstate.StateNone {
			return nil, fmt.Errorf("gk resolve: no merge/rebase/cherry-pick conflict in progress and no unmerged paths")
		}
		if err := CheckStuck(state); err != nil {
			return nil, err
		}
		if r.Stderr != nil {
			fmt.Fprintf(r.Stderr, "no conflicted files found\n")
		}
		return &ResolveResult{}, nil
	}

	opType := stateKindToOpType(state.Kind)

	// 3. opts.Files 필터링
	filesToProcess := conflicted
	if len(opts.Files) > 0 {
		conflictedSet := make(map[string]bool, len(conflicted))
		for _, f := range conflicted {
			conflictedSet[f] = true
		}
		var filtered []string
		for _, f := range opts.Files {
			if conflictedSet[f] {
				filtered = append(filtered, f)
			} else if r.Stderr != nil {
				fmt.Fprintf(r.Stderr, "warning: gk resolve: %s is not in conflict state\n", f)
			}
		}
		filesToProcess = filtered
	}

	if len(filesToProcess) == 0 {
		return &ResolveResult{}, nil
	}

	// 4. 파싱 — strategy 모드에서는 skipped 경로를 아래 7.5에서 degenerate
	// 경로로 재처리하므로 수동 해결 힌트를 찍지 않는다.
	r.deferSkipped = opts.Strategy != ""
	parsed, skipped, err := r.ParseConflictFiles(filesToProcess)
	r.deferSkipped = false
	if err != nil {
		return nil, err
	}

	result := &ResolveResult{
		Failed:  make(map[string]error),
		Skipped: skipped,
		Total:   len(filesToProcess),
	}

	// 5. AI 사용 가능 여부 판단. Explicit mechanical strategies must stay
	// deterministic: ours/theirs never consult an AI provider.
	aiAvailable := !opts.NoAI && r.Provider != nil && opts.Strategy == "ai"
	if aiAvailable {
		if err := provider.ConflictResolverAvailable(ctx, r.Provider); err != nil {
			aiAvailable = false
		}
	}

	// 6. --strategy ai + AI 불가 → 에러
	if opts.Strategy == "ai" && !aiAvailable {
		return nil, fmt.Errorf("gk resolve: --strategy ai requires an available AI provider")
	}

	// 6.5 stage 정보 — marker 없는 unmerged 파일과 skipped 경로의 분기에
	// 필요하다 (ls-files -u 한 번).
	stages, serr := r.unmergedStages(ctx)
	if serr != nil {
		return nil, serr
	}

	// 7. 파일별 처리
	var degenerate []string
	for _, cf := range parsed {
		var resolutions []HunkResolution

		// hunk 수 계산
		hunkCount := 0
		for _, seg := range cf.Segments {
			if seg.Hunk != nil {
				hunkCount++
			}
		}
		if hunkCount == 0 {
			// marker가 없어도 index는 여전히 unmerged다. 양쪽 stage가 있는
			// UU는 사용자가 이미 내용을 정리한 파일 — 내용을 그대로 받아들여
			// stage만 해소한다. stage가 비대칭이면(delete/modify에서 git이
			// 살아남은 쪽을 worktree에 남긴 경우) 7.5의 degenerate 경로가
			// strategy에 따라 keep/delete를 결정한다. 예전에는 둘 다 손도
			// 안 대고 "해결됨"으로 집계해 continue가 막혔다.
			sp := stages[cf.Path]
			if sp.Ours && sp.Theirs {
				if !opts.DryRun {
					if r.deferStage {
						// 사용자의 수동 해결 — stage만 미룬다. pendingStage가
						// 아닌 이유: 롤백(checkout -m)이 이 내용을 마커로
						// 덮어쓰면 사용자의 작업이 파괴된다.
						r.pendingAccept = append(r.pendingAccept, cf.Path)
					} else if err := GitAdd(ctx, r.Runner, cf.Path); err != nil {
						return nil, fmt.Errorf("gk resolve: git add %s: %w", cf.Path, err)
					}
				}
				result.Resolved = append(result.Resolved, cf.Path)
			} else {
				degenerate = append(degenerate, cf.Path)
			}
			continue
		}

		// Mechanical pre-pass — deterministic tier before any AI. It is the
		// whole of strategy "safe" and shrinks the AI surface for strategy
		// "ai"; explicit ours/theirs keep their pure side-take promise and
		// never enter here.
		if opts.Strategy == StrategySafe || opts.Strategy == "ai" {
			if mres, ok := mechanicalFileResolutions(cf, opts.UnionFiles); ok {
				backup := !opts.NoBackup
				if err := r.ApplyFileResolution(ctx, cf, mres, backup, opts.DryRun); err != nil {
					return nil, err
				}
				result.Resolved = append(result.Resolved, cf.Path)
				result.Mechanical = append(result.Mechanical, cf.Path)
				continue
			}
			if opts.Strategy == StrategySafe {
				// Needs judgment — safe mode leaves it marked and unmerged.
				result.Remaining = append(result.Remaining, cf.Path)
				continue
			}
		}

		// AI 해결 시도
		if aiAvailable {
			aiRes, aiErr := r.ResolveWithAI(ctx, cf, opType, opts.Lang)
			if aiRes != nil {
				resolutions = aiRes
				result.AIUsed = true
			} else if aiErr != nil && opts.Strategy == "ai" {
				result.Failed[cf.Path] = aiErr
				continue
			}
		}

		// --strategy 모드: AI 결과가 없으면 strategy에 따라 생성
		if resolutions == nil && opts.Strategy != "" && opts.Strategy != "ai" {
			resolutions = r.buildStrategyResolutions(cf, opts.Strategy)
		}

		// AI strategy 모드: AI 결과를 그대로 사용 (이미 위에서 설정됨)
		if resolutions == nil && opts.Strategy == "ai" {
			// AI가 실패했으면 여기 도달하지 않음 (위에서 에러 반환)
			result.Failed[cf.Path] = fmt.Errorf("AI resolution unavailable")
			continue
		}

		// resolutions가 여전히 nil이면 (interactive 모드 필요) — strategy 없이 호출된 경우
		// 이 구현에서는 strategy가 있는 경우만 처리
		if resolutions == nil {
			result.Failed[cf.Path] = fmt.Errorf("no resolution strategy specified")
			continue
		}

		// 적용 — write/git-add 실패는 치명적이므로 즉시 중단한다.
		backup := !opts.NoBackup
		if err := r.ApplyFileResolution(ctx, cf, resolutions, backup, opts.DryRun); err != nil {
			return nil, err
		}
		result.Resolved = append(result.Resolved, cf.Path)
	}

	// 7.5 degenerate 경로 — delete/modify, markerless 비대칭 stage,
	// worktree에 없는 파일. marker 파서가 다루지 못한 것을 index stage
	// 기반으로 해결한다: ours/theirs는 기계적으로, ai는 양쪽 내용+삭제
	// 여부를 AI가 판단.
	pending := append(append([]string{}, result.Skipped...), degenerate...)
	if len(pending) > 0 && opts.Strategy != "" {
		var still []string
		for _, p := range pending {
			sp, ok := stages[p]
			if !ok {
				still = append(still, p)
				continue
			}
			done, derr := r.resolveDegenerate(ctx, p, sp, opts, opType)
			if derr != nil {
				result.Failed[p] = derr
				still = append(still, p)
				continue
			}
			if done {
				result.Resolved = append(result.Resolved, p)
				if opts.Strategy == "ai" {
					result.AIUsed = true
				}
			} else {
				still = append(still, p)
			}
		}
		result.Skipped = still
	} else if len(degenerate) > 0 {
		// strategy 없는 호출 — 해결하지 못한 경로로 보고만 한다.
		result.Skipped = append(result.Skipped, degenerate...)
	}

	// safe 모드에서 해결하지 못한 것은 전부 "판단이 필요해 남겨둔 것"이다 —
	// skipped(파싱 불가)와 구분하지 않고 Remaining으로 합쳐 보고한다.
	if opts.Strategy == StrategySafe && len(result.Skipped) > 0 {
		result.Remaining = append(result.Remaining, result.Skipped...)
		result.Skipped = nil
	}
	result.PendingStage = append([]string{}, r.pendingStage...)
	result.PendingAccept = append([]string{}, r.pendingAccept...)

	return result, nil
}

// buildStrategyResolutions는 strategy에 따라 모든 hunk에 대한 resolution을 생성한다.
func (r *Resolver) buildStrategyResolutions(cf ConflictFile, strategy Strategy) []HunkResolution {
	var resolutions []HunkResolution
	for _, seg := range cf.Segments {
		if seg.Hunk == nil {
			continue
		}
		var lines []string
		switch strategy {
		case StrategyOurs:
			lines = seg.Hunk.Ours
		case StrategyTheirs:
			lines = seg.Hunk.Theirs
		}
		resolutions = append(resolutions, HunkResolution{
			Strategy:      strategy,
			ResolvedLines: lines,
		})
	}
	return resolutions
}
