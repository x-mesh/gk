package resolve

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

// Resolver는 충돌 해결의 전체 흐름을 orchestrate한다.
type Resolver struct {
	Runner   git.Runner
	Client   *git.Client
	Provider provider.Provider // nil이면 AI 비활성
	Stderr   io.Writer
	Stdout   io.Writer
	// ReadFile은 파일 읽기 함수. 테스트에서 override 가능. nil이면 os.ReadFile.
	ReadFile func(path string) ([]byte, error)
	// WriteFile은 파일 쓰기 함수. 테스트에서 override 가능. nil이면 os.WriteFile.
	WriteFile func(path string, data []byte, perm os.FileMode) error
}

// readFile은 ReadFile 필드가 nil이면 os.ReadFile을 사용한다.
func (r *Resolver) readFile(path string) ([]byte, error) {
	if r.ReadFile != nil {
		return r.ReadFile(path)
	}
	return os.ReadFile(path)
}

// writeFile은 WriteFile 필드가 nil이면 os.WriteFile을 사용한다.
func (r *Resolver) writeFile(path string, data []byte, perm os.FileMode) error {
	if r.WriteFile != nil {
		return r.WriteFile(path, data, perm)
	}
	return os.WriteFile(path, data, perm)
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
		data, err := r.readFile(p)
		if err != nil {
			if r.Stderr != nil {
				fmt.Fprintf(r.Stderr, "warning: gk resolve: could not read %s: %v\n", p, err)
			}
			skipped = append(skipped, p)
			continue
		}
		cf, err := Parse(p, data)
		if err != nil {
			if r.Stderr != nil {
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

	result, err := resolver.ResolveConflicts(ctx, input)
	if err != nil {
		if r.Stderr != nil {
			fmt.Fprintf(r.Stderr, "warning: gk resolve: AI analysis failed for %s: %v\n", cf.Path, err)
		}
		return nil, nil
	}

	// ConflictResolutionOutput → []HunkResolution 변환
	resolutions := make([]HunkResolution, len(result.Resolutions))
	for i, res := range result.Resolutions {
		resolutions[i] = HunkResolution{
			Strategy:      Strategy(res.Strategy),
			ResolvedLines: res.Resolved,
			Rationale:     res.Rationale,
		}
	}
	return resolutions, nil
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
		orig, readErr := r.readFile(cf.Path)
		if readErr == nil {
			if backupErr := BackupOriginal(r.WriteFile, cf.Path, orig); backupErr != nil {
				if r.Stderr != nil {
					fmt.Fprintf(r.Stderr, "warning: gk resolve: backup %s.orig: %v\n", cf.Path, backupErr)
				}
			}
		}
	}

	if err := WriteResolved(r.WriteFile, cf.Path, resolved); err != nil {
		return fmt.Errorf("gk resolve: write %s: %w", cf.Path, err)
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
	// 1. 충돌 상태 확인
	if state.Kind == gitstate.StateNone {
		return nil, fmt.Errorf("gk resolve: no merge/rebase/cherry-pick conflict in progress")
	}

	opType := stateKindToOpType(state.Kind)

	// 2. 충돌 파일 수집
	conflicted, err := r.CollectConflictedFiles(ctx)
	if err != nil {
		return nil, err
	}
	if len(conflicted) == 0 {
		if r.Stderr != nil {
			fmt.Fprintf(r.Stderr, "no conflicted files found\n")
		}
		return &ResolveResult{}, nil
	}

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

	// 4. 파싱
	parsed, skipped, err := r.ParseConflictFiles(filesToProcess)
	if err != nil {
		return nil, err
	}

	result := &ResolveResult{
		Failed:  make(map[string]error),
		Skipped: skipped,
		Total:   len(filesToProcess),
	}

	// 5. AI 사용 가능 여부 판단
	aiAvailable := !opts.NoAI && r.Provider != nil
	if aiAvailable {
		if err := r.Provider.Available(ctx); err != nil {
			aiAvailable = false
		}
	}

	// 6. --strategy ai + AI 불가 → 에러
	if opts.Strategy == "ai" && !aiAvailable {
		return nil, fmt.Errorf("gk resolve: --strategy ai requires an available AI provider")
	}

	// 7. 파일별 처리
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
			result.Resolved = append(result.Resolved, cf.Path)
			continue
		}

		// AI 해결 시도
		if aiAvailable {
			aiRes, _ := r.ResolveWithAI(ctx, cf, opType, opts.Lang)
			if aiRes != nil && len(aiRes) == hunkCount {
				resolutions = aiRes
				result.AIUsed = true
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
