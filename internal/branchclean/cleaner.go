package branchclean

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
)

// Cleaner는 브랜치 정리의 전체 흐름을 orchestrate한다.
type Cleaner struct {
	Runner   git.Runner
	Client   *git.Client
	Provider provider.Provider // nil이면 AI 비활성
	Stderr   io.Writer
	Stdout   io.Writer
}

// Run은 옵션에 따라 브랜치 정리를 실행한다.
func (c *Cleaner) Run(ctx context.Context, opts CleanOptions) (*CleanResult, error) {
	// --stale 유효성 검사
	if opts.Stale < 0 {
		return nil, fmt.Errorf("gk branch clean: invalid --stale value: must be > 0")
	}

	result := &CleanResult{
		Failed: make(map[string]error),
	}

	remote := opts.RemoteName
	if remote == "" {
		remote = "origin"
	}

	// --remote 처리: git remote prune
	if opts.Remote {
		if opts.DryRun {
			stdout, _, err := c.Runner.Run(ctx, "remote", "prune", remote, "--dry-run")
			if err != nil {
				return nil, fmt.Errorf("gk branch clean: remote prune: %w", err)
			}
			if c.Stderr != nil {
				fmt.Fprintf(c.Stderr, "%s", stdout)
			}
		} else {
			_, _, err := c.Runner.Run(ctx, "remote", "prune", remote)
			if err != nil {
				return nil, fmt.Errorf("gk branch clean: remote prune: %w", err)
			}
		}
		result.Pruned = true
	}

	// --remote만 단독 실행 시 로컬 정리 없이 반환
	if opts.Remote && !opts.Gone && !opts.All && opts.Stale == 0 && !opts.SquashMerged {
		// merged 기본 동작도 건너뛰려면 다른 플래그가 없어야 함
		// 하지만 기본 동작은 merged 수집이므로, --remote만 있으면 로컬 정리 skip
		return result, nil
	}

	// base branch 결정
	base := opts.BaseBranch
	if base == "" {
		b, err := c.Client.DefaultBranch(ctx, remote)
		if err != nil {
			return nil, fmt.Errorf("gk branch clean: could not determine base branch: %w", err)
		}
		base = b
	}

	// protected set 구성
	protected := make(map[string]bool)
	for _, p := range opts.Protected {
		protected[p] = true
	}
	protected[base] = true
	if cur, err := c.Client.CurrentBranch(ctx); err == nil {
		protected[cur] = true
	}

	// 브랜치 수집
	collector := &Collector{Runner: c.Runner, Client: c.Client}
	entries, err := collector.CollectAll(ctx, opts)
	if err != nil {
		return nil, err
	}

	// squash detection
	if opts.SquashMerged || opts.All {
		// 수집된 브랜치 외에 추가로 squash-merged 감지할 브랜치 목록 구성
		// for-each-ref로 전체 로컬 브랜치를 가져와서 squash 감지
		detector := &SquashDetector{Runner: c.Runner}
		allBranches := listBranchNames(ctx, c.Runner)
		squashed, ambiguous, warnings := detector.DetectSquashMerged(ctx, allBranches, base, protected)

		for _, w := range warnings {
			if c.Stderr != nil {
				fmt.Fprintf(c.Stderr, "warning: %s\n", w)
			}
		}

		// squash-merged 브랜치를 entries에 추가
		for _, name := range squashed {
			entries = append(entries, BranchEntry{
				Name:   name,
				Status: StatusSquashMerged,
			})
		}
		// ambiguous 브랜치도 추가
		for _, name := range ambiguous {
			entries = append(entries, BranchEntry{
				Name:   name,
				Status: StatusAmbiguous,
			})
		}
		entries = DeduplicateEntries(entries)
	}

	// protected 필터링
	entries = FilterProtected(entries, protected)

	// AI 분석
	var analyses map[string]provider.BranchAnalysis
	if c.Provider != nil && !opts.NoAI {
		if err := c.Provider.Available(ctx); err == nil {
			if analyzer, ok := c.Provider.(provider.BranchAnalyzer); ok {
				analyses, result.AIUsed, result.AIModel = c.runAIAnalysis(ctx, analyzer, entries, base, opts.Lang)
			}
		}
		// Available() 실패 시 경고 없이 rule-based fallback (provider가 없는 것과 동일)
	}

	// AI 실패 시 analyses는 nil → rule-based fallback (BuildCandidates가 처리)

	// BuildCandidates로 후보 생성
	candidates := BuildCandidates(entries, analyses, opts.Force)

	// dry-run 시 결과 반환
	if opts.DryRun {
		result.DryRun = candidates
		return result, nil
	}

	// 삭제 대상 결정: --yes 모드에서는 Selected=true인 브랜치 즉시 삭제
	var toDelete []string
	if opts.Yes {
		for _, c := range candidates {
			if c.Selected {
				toDelete = append(toDelete, c.Name)
			}
		}
	}

	// 삭제 실행
	deleteFlag := "-d"
	if opts.Force {
		deleteFlag = "-D"
	}
	for _, name := range toDelete {
		_, stderr, err := c.Runner.Run(ctx, "branch", deleteFlag, name)
		if err != nil {
			result.Failed[name] = fmt.Errorf("gk branch clean: delete %s: %s: %w", name, strings.TrimSpace(string(stderr)), err)
			if c.Stderr != nil {
				fmt.Fprintf(c.Stderr, "error: failed to delete %s: %s\n", name, strings.TrimSpace(string(stderr)))
			}
			continue
		}
		result.Deleted = append(result.Deleted, name)
	}

	return result, nil
}

// runAIAnalysis는 BranchAnalyzer를 통해 AI 분석을 실행한다.
// 실패 시 경고를 출력하고 nil map을 반환한다 (graceful fallback).
func (c *Cleaner) runAIAnalysis(
	ctx context.Context,
	analyzer provider.BranchAnalyzer,
	entries []BranchEntry,
	base, lang string,
) (map[string]provider.BranchAnalysis, bool, string) {
	if len(entries) == 0 {
		return nil, false, ""
	}

	input := provider.BranchAnalysisInput{
		BaseBranch: base,
		Lang:       lang,
	}
	for _, e := range entries {
		input.Branches = append(input.Branches, provider.BranchInfo{
			Name:           e.Name,
			LastCommitMsg:  e.LastCommitMsg,
			DiffStat:       e.DiffStat,
			LastCommitDate: e.LastCommitDate,
			Status:         string(e.Status),
		})
	}

	aiResult, err := analyzer.AnalyzeBranches(ctx, input)
	if err != nil {
		if c.Stderr != nil {
			fmt.Fprintf(c.Stderr, "warning: AI analysis failed, falling back to rule-based: %v\n", err)
		}
		return nil, false, ""
	}

	analyses := make(map[string]provider.BranchAnalysis, len(aiResult.Analyses))
	for _, a := range aiResult.Analyses {
		analyses[a.Name] = a
	}
	return analyses, true, aiResult.Model
}

// listBranchNames는 로컬 브랜치 이름 목록을 반환한다.
func listBranchNames(ctx context.Context, r git.Runner) []string {
	stdout, _, err := r.Run(ctx, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(strings.TrimRight(string(stdout), "\n"), "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// BuildCandidates는 수집된 브랜치와 AI 분석 결과를 결합하여
// CleanCandidate 목록을 생성한다. 순수 함수.
//
// Selected 필드 규칙:
//   - merged/gone/squash-merged → Selected=true
//   - AI completed/experiment → Selected=true
//   - AI in_progress/preserve → Selected=false (force=true → Selected=true)
//   - AI 미사용 + stale → Selected=true
func BuildCandidates(
	entries []BranchEntry,
	analyses map[string]provider.BranchAnalysis,
	force bool,
) []CleanCandidate {
	candidates := make([]CleanCandidate, 0, len(entries))

	for _, e := range entries {
		c := CleanCandidate{BranchEntry: e}

		// AI 분석 결과 매핑
		if a, ok := analyses[e.Name]; ok {
			c.AICategory = a.Category
			c.AISummary = a.Summary
			c.SafeDelete = a.SafeDelete
		}

		// Selected 결정
		c.Selected = determineSelected(e.Status, c.AICategory, force)
		candidates = append(candidates, c)
	}

	return candidates
}

// determineSelected는 브랜치 상태와 AI 카테고리에 따라 기본 선택 여부를 결정한다.
func determineSelected(status BranchStatus, aiCategory string, force bool) bool {
	// merged/gone/squash-merged → 항상 선택
	switch status {
	case StatusMerged, StatusGone, StatusSquashMerged:
		return true
	}

	// AI 카테고리가 있는 경우
	switch aiCategory {
	case "completed", "experiment":
		return true
	case "in_progress", "preserve":
		return force
	}

	// AI 미사용 (aiCategory == "") + stale → 선택
	if aiCategory == "" && status == StatusStale {
		return true
	}

	return false
}

// FilterProtected는 protected set에 포함된 브랜치를 제거한다. 순수 함수.
func FilterProtected(entries []BranchEntry, protected map[string]bool) []BranchEntry {
	var result []BranchEntry
	for _, e := range entries {
		if !protected[e.Name] {
			result = append(result, e)
		}
	}
	return result
}

// DeduplicateEntries는 이름 기준으로 중복을 제거한다. 순수 함수.
// 동일 이름이 여러 번 나타나면 첫 번째만 유지한다.
func DeduplicateEntries(entries []BranchEntry) []BranchEntry {
	seen := make(map[string]bool)
	var result []BranchEntry
	for _, e := range entries {
		if seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		result = append(result, e)
	}
	return result
}
