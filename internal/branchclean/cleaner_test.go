package branchclean

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
	"pgregory.net/rapid"
)

// ---------------------------------------------------------------------------
// Test helpers: fake providers for branchclean tests
// ---------------------------------------------------------------------------

// fakeBranchProvider는 BranchAnalyzer를 구현하는 테스트용 fake provider이다.
// Available()와 AnalyzeBranches() 모두 에러를 반환할 수 있다.
type fakeBranchProvider struct {
	availableErr    error
	analyzeErr      error
	analyzeResult   provider.BranchAnalysisResult
	analyzeCalled   bool
}

func (f *fakeBranchProvider) Name() string                { return "fake-branch" }
func (f *fakeBranchProvider) Locality() provider.Locality { return provider.LocalityLocal }
func (f *fakeBranchProvider) Available(_ context.Context) error {
	return f.availableErr
}
func (f *fakeBranchProvider) Classify(_ context.Context, _ provider.ClassifyInput) (provider.ClassifyResult, error) {
	return provider.ClassifyResult{}, nil
}
func (f *fakeBranchProvider) Compose(_ context.Context, _ provider.ComposeInput) (provider.ComposeResult, error) {
	return provider.ComposeResult{}, nil
}
func (f *fakeBranchProvider) AnalyzeBranches(_ context.Context, _ provider.BranchAnalysisInput) (provider.BranchAnalysisResult, error) {
	f.analyzeCalled = true
	if f.analyzeErr != nil {
		return provider.BranchAnalysisResult{}, f.analyzeErr
	}
	return f.analyzeResult, nil
}

var _ provider.Provider = (*fakeBranchProvider)(nil)
var _ provider.BranchAnalyzer = (*fakeBranchProvider)(nil)

// ---------------------------------------------------------------------------
// Generators
// ---------------------------------------------------------------------------

func branchStatusGen() *rapid.Generator[BranchStatus] {
	return rapid.SampledFrom([]BranchStatus{
		StatusMerged, StatusGone, StatusStale,
		StatusSquashMerged, StatusAmbiguous, StatusActive,
	})
}

func aiCategoryGen() *rapid.Generator[string] {
	return rapid.SampledFrom([]string{
		"completed", "experiment", "in_progress", "preserve",
	})
}

func branchEntryGen() *rapid.Generator[BranchEntry] {
	return rapid.Custom[BranchEntry](func(t *rapid.T) BranchEntry {
		return BranchEntry{
			Name:           branchNameGen().Draw(t, "name"),
			Status:         branchStatusGen().Draw(t, "status"),
			LastCommitDate: time.Now().AddDate(0, 0, -rapid.IntRange(0, 365).Draw(t, "daysAgo")),
		}
	})
}

// ---------------------------------------------------------------------------
// Property 3: BuildCandidates 기본 선택 규칙
// ---------------------------------------------------------------------------

// Feature: ai-branch-clean, Property 3: BuildCandidates 기본 선택 규칙
// **Validates: Requirements 3.5, 8.3, 8.4, 9.4, 11.2**
func TestProperty3_BuildCandidatesSelectionRules(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(0, 30).Draw(rt, "entryCount")

		// 고유 이름의 entries 생성
		usedNames := make(map[string]bool)
		var entries []BranchEntry
		for i := 0; i < n; i++ {
			e := branchEntryGen().Draw(rt, fmt.Sprintf("entry_%d", i))
			if usedNames[e.Name] {
				continue
			}
			usedNames[e.Name] = true
			entries = append(entries, e)
		}

		// AI analyses 매핑 생성 (일부 브랜치에만 AI 결과 존재)
		analyses := make(map[string]provider.BranchAnalysis)
		for _, e := range entries {
			hasAI := rapid.Bool().Draw(rt, fmt.Sprintf("hasAI_%s", e.Name))
			if hasAI {
				cat := aiCategoryGen().Draw(rt, fmt.Sprintf("cat_%s", e.Name))
				analyses[e.Name] = provider.BranchAnalysis{
					Name:       e.Name,
					Category:   cat,
					Summary:    "test summary",
					SafeDelete: cat == "completed" || cat == "experiment",
				}
			}
		}

		force := rapid.Bool().Draw(rt, "force")

		candidates := BuildCandidates(entries, analyses, force)

		if len(candidates) != len(entries) {
			rt.Fatalf("candidate count %d != entry count %d", len(candidates), len(entries))
		}

		for _, c := range candidates {
			ai, hasAI := analyses[c.Name]

			// Rule 1: merged/gone/squash-merged → Selected=true
			if c.Status == StatusMerged || c.Status == StatusGone || c.Status == StatusSquashMerged {
				if !c.Selected {
					rt.Fatalf("branch %q (status=%s) should be Selected=true", c.Name, c.Status)
				}
				continue
			}

			if hasAI {
				switch ai.Category {
				case "completed", "experiment":
					// Rule 2: AI completed/experiment → Selected=true
					if !c.Selected {
						rt.Fatalf("branch %q (AI=%s) should be Selected=true", c.Name, ai.Category)
					}
				case "in_progress", "preserve":
					// Rule 3: AI in_progress/preserve → Selected=false (force → true)
					if force && !c.Selected {
						rt.Fatalf("branch %q (AI=%s, force=true) should be Selected=true", c.Name, ai.Category)
					}
					if !force && c.Selected {
						rt.Fatalf("branch %q (AI=%s, force=false) should be Selected=false", c.Name, ai.Category)
					}
				}
			} else {
				// Rule 4: AI 미사용 + stale → Selected=true
				if c.Status == StatusStale {
					if !c.Selected {
						rt.Fatalf("branch %q (no AI, stale) should be Selected=true", c.Name)
					}
				}
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Property 4: AI 실패 시 graceful fallback
// ---------------------------------------------------------------------------

// Feature: ai-branch-clean, Property 4: AI 실패 시 graceful fallback
// **Validates: Requirements 4.4, 13.4**
func TestProperty4_AIFailureGracefulFallback(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		now := time.Now()
		n := rapid.IntRange(0, 10).Draw(rt, "branchCount")

		// FakeRunner 응답 구성
		var branchLines []string
		var mergedNames []string
		usedNames := make(map[string]bool)

		for i := 0; i < n; i++ {
			name := branchNameGen().Draw(rt, fmt.Sprintf("name_%d", i))
			if usedNames[name] || name == "main" {
				continue
			}
			usedNames[name] = true
			branchLines = append(branchLines, fmt.Sprintf("%s\x00\x00%d\x00", name, now.Unix()))
			mergedNames = append(mergedNames, name)
		}

		runner := &git.FakeRunner{
			Responses: map[string]git.FakeResponse{
				"symbolic-ref --short HEAD": {Stdout: "main\n"},
				"symbolic-ref --short refs/remotes/origin/HEAD": {Stdout: "origin/main\n"},
				"branch --merged main --format=%(refname:short)": {
					Stdout: strings.Join(mergedNames, "\n") + "\n",
				},
				"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track) refs/heads": {
					Stdout: strings.Join(branchLines, "\n"),
				},
			},
		}

		// AI provider가 에러를 반환하도록 설정
		failMode := rapid.SampledFrom([]string{"available", "analyze"}).Draw(rt, "failMode")
		fp := &fakeBranchProvider{}
		if failMode == "available" {
			fp.availableErr = fmt.Errorf("provider not available")
		} else {
			fp.analyzeErr = fmt.Errorf("AI analysis failed")
		}

		stderr := &bytes.Buffer{}
		cleaner := &Cleaner{
			Runner:   runner,
			Client:   git.NewClient(runner),
			Provider: fp,
			Stderr:   stderr,
		}

		result, err := cleaner.Run(ctx(), CleanOptions{
			DryRun: true, // dry-run으로 삭제 방지
		})

		// 에러가 반환되지 않아야 함
		if err != nil {
			rt.Fatalf("expected no error, got: %v", err)
		}

		// AIUsed는 false여야 함
		if result.AIUsed {
			rt.Fatalf("expected AIUsed=false when AI fails")
		}
	})
}

// ---------------------------------------------------------------------------
// Property 9: Dry-run 무부작용
// ---------------------------------------------------------------------------

// Feature: ai-branch-clean, Property 9: Dry-run 무부작용
// **Validates: Requirements 10.1, 10.2, 10.3**
func TestProperty9_DryRunNoSideEffects(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		now := time.Now()
		n := rapid.IntRange(0, 10).Draw(rt, "branchCount")

		var branchLines []string
		var mergedNames []string
		usedNames := make(map[string]bool)

		for i := 0; i < n; i++ {
			name := branchNameGen().Draw(rt, fmt.Sprintf("name_%d", i))
			if usedNames[name] || name == "main" {
				continue
			}
			usedNames[name] = true
			branchLines = append(branchLines, fmt.Sprintf("%s\x00\x00%d\x00", name, now.Unix()))
			mergedNames = append(mergedNames, name)
		}

		runner := &git.FakeRunner{
			Responses: map[string]git.FakeResponse{
				"symbolic-ref --short HEAD": {Stdout: "main\n"},
				"symbolic-ref --short refs/remotes/origin/HEAD": {Stdout: "origin/main\n"},
				"branch --merged main --format=%(refname:short)": {
					Stdout: strings.Join(mergedNames, "\n") + "\n",
				},
				"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track) refs/heads": {
					Stdout: strings.Join(branchLines, "\n"),
				},
			},
		}

		cleaner := &Cleaner{
			Runner: runner,
			Client: git.NewClient(runner),
			Stderr: &bytes.Buffer{},
		}

		opts := CleanOptions{
			DryRun: true,
			Yes:    rapid.Bool().Draw(rt, "yes"),
			Force:  rapid.Bool().Draw(rt, "force"),
		}

		result, err := cleaner.Run(ctx(), opts)
		if err != nil {
			rt.Fatalf("unexpected error: %v", err)
		}

		// git branch -d 또는 -D 호출이 없어야 함
		for _, call := range runner.Calls {
			args := strings.Join(call.Args, " ")
			if strings.Contains(args, "branch -d") || strings.Contains(args, "branch -D") {
				rt.Fatalf("dry-run should not call git branch -d/-D, but found: %v", call.Args)
			}
		}

		// Deleted는 비어있어야 함
		if len(result.Deleted) > 0 {
			rt.Fatalf("dry-run should not delete branches, but Deleted=%v", result.Deleted)
		}
	})
}

// ---------------------------------------------------------------------------
// Unit tests: BuildCandidates
// ---------------------------------------------------------------------------

func TestBuildCandidates_MergedAlwaysSelected(t *testing.T) {
	entries := []BranchEntry{
		{Name: "feat/done", Status: StatusMerged},
		{Name: "feat/gone", Status: StatusGone},
		{Name: "feat/squash", Status: StatusSquashMerged},
	}
	candidates := BuildCandidates(entries, nil, false)
	for _, c := range candidates {
		if !c.Selected {
			t.Fatalf("branch %s (status=%s) should be selected", c.Name, c.Status)
		}
	}
}

func TestBuildCandidates_AIInProgressNotSelected(t *testing.T) {
	entries := []BranchEntry{
		{Name: "feat/wip", Status: StatusStale},
	}
	analyses := map[string]provider.BranchAnalysis{
		"feat/wip": {Name: "feat/wip", Category: "in_progress", Summary: "work in progress"},
	}
	candidates := BuildCandidates(entries, analyses, false)
	if candidates[0].Selected {
		t.Fatal("in_progress branch should not be selected without force")
	}

	// force=true → selected
	candidates = BuildCandidates(entries, analyses, true)
	if !candidates[0].Selected {
		t.Fatal("in_progress branch should be selected with force=true")
	}
}

func TestBuildCandidates_StaleNoAISelected(t *testing.T) {
	entries := []BranchEntry{
		{Name: "feat/old", Status: StatusStale},
	}
	candidates := BuildCandidates(entries, nil, false)
	if !candidates[0].Selected {
		t.Fatal("stale branch without AI should be selected")
	}
}

func TestBuildCandidates_AIFieldsPopulated(t *testing.T) {
	entries := []BranchEntry{
		{Name: "feat/x", Status: StatusStale},
	}
	analyses := map[string]provider.BranchAnalysis{
		"feat/x": {Name: "feat/x", Category: "completed", Summary: "done", SafeDelete: true},
	}
	candidates := BuildCandidates(entries, analyses, false)
	c := candidates[0]
	if c.AICategory != "completed" {
		t.Fatalf("expected AICategory=completed, got %s", c.AICategory)
	}
	if c.AISummary != "done" {
		t.Fatalf("expected AISummary=done, got %s", c.AISummary)
	}
	if !c.SafeDelete {
		t.Fatal("expected SafeDelete=true")
	}
}

// ---------------------------------------------------------------------------
// Unit tests: Cleaner.Run
// ---------------------------------------------------------------------------

func TestCleanerRun_StaleNegativeError(t *testing.T) {
	runner := &git.FakeRunner{}
	cleaner := &Cleaner{
		Runner: runner,
		Client: git.NewClient(runner),
		Stderr: &bytes.Buffer{},
	}
	_, err := cleaner.Run(ctx(), CleanOptions{Stale: -1})
	if err == nil {
		t.Fatal("expected error for negative stale")
	}
	if !strings.Contains(err.Error(), "invalid --stale value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCleanerRun_RemoteOnly(t *testing.T) {
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"remote prune origin": {Stdout: ""},
		},
	}
	cleaner := &Cleaner{
		Runner: runner,
		Client: git.NewClient(runner),
		Stderr: &bytes.Buffer{},
	}
	result, err := cleaner.Run(ctx(), CleanOptions{Remote: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Pruned {
		t.Fatal("expected Pruned=true")
	}
}

func TestCleanerRun_DryRunReturnsCandiates(t *testing.T) {
	now := time.Now()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short HEAD":                        {Stdout: "main\n"},
			"symbolic-ref --short refs/remotes/origin/HEAD":    {Stdout: "origin/main\n"},
			"branch --merged main --format=%(refname:short)":   {Stdout: "feat/done\n"},
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track) refs/heads": {
				Stdout: fmt.Sprintf("feat/done\x00\x00%d\x00\nmain\x00origin/main\x00%d\x00\n", now.Unix(), now.Unix()),
			},
		},
	}
	cleaner := &Cleaner{
		Runner: runner,
		Client: git.NewClient(runner),
		Stderr: &bytes.Buffer{},
	}
	result, err := cleaner.Run(ctx(), CleanOptions{DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.DryRun) == 0 {
		t.Fatal("expected dry-run candidates")
	}
	if len(result.Deleted) > 0 {
		t.Fatal("dry-run should not delete")
	}
}

func TestCleanerRun_YesDeletesMerged(t *testing.T) {
	now := time.Now()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short HEAD":                        {Stdout: "develop\n"},
			"symbolic-ref --short refs/remotes/origin/HEAD":    {Stdout: "origin/main\n"},
			"branch --merged main --format=%(refname:short)":   {Stdout: "feat/done\n"},
			"branch -d feat/done":                              {Stdout: "Deleted branch feat/done\n"},
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track) refs/heads": {
				Stdout: fmt.Sprintf("feat/done\x00\x00%d\x00\ndevelop\x00\x00%d\x00\nmain\x00origin/main\x00%d\x00\n", now.Unix(), now.Unix(), now.Unix()),
			},
		},
	}
	cleaner := &Cleaner{
		Runner: runner,
		Client: git.NewClient(runner),
		Stderr: &bytes.Buffer{},
	}
	result, err := cleaner.Run(ctx(), CleanOptions{Yes: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Deleted) != 1 || result.Deleted[0] != "feat/done" {
		t.Fatalf("expected [feat/done] deleted, got %v", result.Deleted)
	}
}

func TestCleanerRun_DeleteFailureContinues(t *testing.T) {
	now := time.Now()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short HEAD":                        {Stdout: "develop\n"},
			"symbolic-ref --short refs/remotes/origin/HEAD":    {Stdout: "origin/main\n"},
			"branch --merged main --format=%(refname:short)":   {Stdout: "feat/a\nfeat/b\n"},
			"branch -d feat/a":                                 {Stderr: "error: not fully merged", ExitCode: 1},
			"branch -d feat/b":                                 {Stdout: "Deleted branch feat/b\n"},
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track) refs/heads": {
				Stdout: fmt.Sprintf("feat/a\x00\x00%d\x00\nfeat/b\x00\x00%d\x00\ndevelop\x00\x00%d\x00\nmain\x00origin/main\x00%d\x00\n",
					now.Unix(), now.Unix(), now.Unix(), now.Unix()),
			},
		},
	}
	cleaner := &Cleaner{
		Runner: runner,
		Client: git.NewClient(runner),
		Stderr: &bytes.Buffer{},
	}
	result, err := cleaner.Run(ctx(), CleanOptions{Yes: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Deleted) != 1 || result.Deleted[0] != "feat/b" {
		t.Fatalf("expected [feat/b] deleted, got %v", result.Deleted)
	}
	if _, ok := result.Failed["feat/a"]; !ok {
		t.Fatal("expected feat/a in Failed")
	}
}

func TestCleanerRun_ForceUsesCapitalD(t *testing.T) {
	now := time.Now()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short HEAD":                        {Stdout: "develop\n"},
			"symbolic-ref --short refs/remotes/origin/HEAD":    {Stdout: "origin/main\n"},
			"branch --merged main --format=%(refname:short)":   {Stdout: "feat/done\n"},
			"branch -D feat/done":                              {Stdout: "Deleted branch feat/done\n"},
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track) refs/heads": {
				Stdout: fmt.Sprintf("feat/done\x00\x00%d\x00\ndevelop\x00\x00%d\x00\nmain\x00origin/main\x00%d\x00\n", now.Unix(), now.Unix(), now.Unix()),
			},
		},
	}
	cleaner := &Cleaner{
		Runner: runner,
		Client: git.NewClient(runner),
		Stderr: &bytes.Buffer{},
	}
	result, err := cleaner.Run(ctx(), CleanOptions{Yes: true, Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Deleted) != 1 {
		t.Fatalf("expected 1 deleted, got %d", len(result.Deleted))
	}

	// -D가 호출되었는지 확인
	found := false
	for _, call := range runner.Calls {
		if len(call.Args) >= 2 && call.Args[0] == "branch" && call.Args[1] == "-D" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected git branch -D call")
	}
}

func TestCleanerRun_AIGracefulFallback(t *testing.T) {
	now := time.Now()
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short HEAD":                        {Stdout: "develop\n"},
			"symbolic-ref --short refs/remotes/origin/HEAD":    {Stdout: "origin/main\n"},
			"branch --merged main --format=%(refname:short)":   {Stdout: "feat/done\n"},
			"for-each-ref --format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track) refs/heads": {
				Stdout: fmt.Sprintf("feat/done\x00\x00%d\x00\ndevelop\x00\x00%d\x00\nmain\x00origin/main\x00%d\x00\n", now.Unix(), now.Unix(), now.Unix()),
			},
		},
	}

	fp := &fakeBranchProvider{analyzeErr: fmt.Errorf("AI timeout")}
	stderr := &bytes.Buffer{}
	cleaner := &Cleaner{
		Runner:   runner,
		Client:   git.NewClient(runner),
		Provider: fp,
		Stderr:   stderr,
	}

	result, err := cleaner.Run(ctx(), CleanOptions{DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.AIUsed {
		t.Fatal("expected AIUsed=false after AI failure")
	}
	if !strings.Contains(stderr.String(), "AI analysis failed") {
		t.Fatalf("expected warning in stderr, got: %s", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func ctx() context.Context {
	return context.Background()
}
