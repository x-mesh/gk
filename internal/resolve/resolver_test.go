package resolve

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"pgregory.net/rapid"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// fakeResolveProvider는 ConflictResolver를 구현하는 테스트용 fake provider이다.
type fakeResolveProvider struct {
	availableErr error
	resolveErr   error
	resolveRes   provider.ConflictResolutionResult
	resolveCalls int
}

func (f *fakeResolveProvider) Name() string                { return "fake-resolve" }
func (f *fakeResolveProvider) Locality() provider.Locality { return provider.LocalityLocal }
func (f *fakeResolveProvider) Available(_ context.Context) error {
	return f.availableErr
}
func (f *fakeResolveProvider) Classify(_ context.Context, _ provider.ClassifyInput) (provider.ClassifyResult, error) {
	return provider.ClassifyResult{}, nil
}
func (f *fakeResolveProvider) Compose(_ context.Context, _ provider.ComposeInput) (provider.ComposeResult, error) {
	return provider.ComposeResult{}, nil
}
func (f *fakeResolveProvider) ResolveConflicts(_ context.Context, _ provider.ConflictResolutionInput) (provider.ConflictResolutionResult, error) {
	f.resolveCalls++
	if f.resolveErr != nil {
		return provider.ConflictResolutionResult{}, f.resolveErr
	}
	return f.resolveRes, nil
}

var _ provider.Provider = (*fakeResolveProvider)(nil)
var _ provider.ConflictResolver = (*fakeResolveProvider)(nil)

// buildConflictContent는 ConflictFile에 대한 conflict marker 텍스트를 생성한다.
func buildConflictContent(cf ConflictFile) []byte {
	return Print(cf)
}

// buildPorcelainV2 builds `git status --porcelain=v2` output for unmerged files.
func buildPorcelainV2(paths []string) string {
	var lines []string
	for _, p := range paths {
		// u <xy> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
		lines = append(lines, fmt.Sprintf("u UU N... 100644 100644 100644 100644 abc1234 def5678 ghi9012 %s", p))
	}
	return strings.Join(lines, "\n") + "\n"
}

// makeConflictFile creates a simple ConflictFile with one hunk for testing.
func makeConflictFile(path string) ConflictFile {
	return ConflictFile{
		Path: path,
		Segments: []Segment{
			{Context: []string{"package main", ""}},
			{Hunk: &ConflictHunk{
				Ours:        []string{"// ours change"},
				Theirs:      []string{"// theirs change"},
				OursLabel:   "HEAD",
				TheirsLabel: "feature",
			}},
			{Context: []string{"", "func main() {}"}},
		},
	}
}

// ---------------------------------------------------------------------------
// Feature: ai-resolve, Property 5: Dry-run 무부작용
// Validates: Requirements 9.1, 9.2
// ---------------------------------------------------------------------------

func TestPropertyDryRunNoSideEffects(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// 1~3개의 충돌 파일 생성
		n := rapid.IntRange(1, 3).Draw(rt, "file_count")
		var paths []string
		fileContents := make(map[string][]byte)

		for i := 0; i < n; i++ {
			path := fmt.Sprintf("file%d.go", i)
			paths = append(paths, path)
			cf := makeConflictFile(path)
			fileContents[path] = buildConflictContent(cf)
		}

		// FakeRunner: git status --porcelain=v2 응답 + add 명령 기록
		runner := &git.FakeRunner{
			Responses: map[string]git.FakeResponse{
				"status --porcelain=v2": {Stdout: buildPorcelainV2(paths)},
			},
		}

		// spy writeFile: 호출 횟수 추적
		writeFileCalls := 0
		spyWriteFile := func(path string, data []byte, perm os.FileMode) error {
			writeFileCalls++
			return nil
		}

		stderr := &bytes.Buffer{}
		stdout := &bytes.Buffer{}
		resolver := &Resolver{
			Runner: runner,
			Client: git.NewClient(runner),
			Stderr: stderr,
			Stdout: stdout,
			ReadFile: func(path string) ([]byte, error) {
				if data, ok := fileContents[path]; ok {
					return data, nil
				}
				return nil, fmt.Errorf("file not found: %s", path)
			},
			WriteFile: spyWriteFile,
		}

		state := &gitstate.State{Kind: gitstate.StateMerge}
		opts := ResolveOptions{
			DryRun:   true,
			Strategy: StrategyOurs,
		}

		_, err := resolver.Run(context.Background(), state, opts)
		if err != nil {
			rt.Fatalf("unexpected error: %v", err)
		}

		// WriteFile이 호출되지 않아야 함
		if writeFileCalls > 0 {
			rt.Fatalf("dry-run should not call WriteFile, but called %d times", writeFileCalls)
		}

		// git add가 호출되지 않아야 함
		for _, call := range runner.Calls {
			if len(call.Args) >= 1 && call.Args[0] == "add" {
				rt.Fatalf("dry-run should not call git add, but found: %v", call.Args)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Feature: ai-resolve, Property 6: AI 실패 시 graceful fallback
// Validates: Requirements 10.4
// ---------------------------------------------------------------------------

func TestPropertyAIFailureGracefulFallback(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// 1~3개의 충돌 파일 생성
		n := rapid.IntRange(1, 3).Draw(rt, "file_count")
		var paths []string
		fileContents := make(map[string][]byte)

		for i := 0; i < n; i++ {
			path := fmt.Sprintf("file%d.go", i)
			paths = append(paths, path)
			cf := makeConflictFile(path)
			fileContents[path] = buildConflictContent(cf)
		}

		// FakeRunner: status + add 응답
		responses := map[string]git.FakeResponse{
			"status --porcelain=v2": {Stdout: buildPorcelainV2(paths)},
		}
		for _, p := range paths {
			responses["add "+p] = git.FakeResponse{}
		}
		runner := &git.FakeRunner{Responses: responses}

		// AI provider가 에러를 반환하도록 설정
		fp := &fakeResolveProvider{
			resolveErr: fmt.Errorf("AI service unavailable"),
		}

		writtenFiles := make(map[string][]byte)
		stderr := &bytes.Buffer{}
		resolver := &Resolver{
			Runner:   runner,
			Client:   git.NewClient(runner),
			Provider: fp,
			Stderr:   stderr,
			ReadFile: func(path string) ([]byte, error) {
				if data, ok := fileContents[path]; ok {
					return data, nil
				}
				return nil, fmt.Errorf("file not found: %s", path)
			},
			WriteFile: func(path string, data []byte, perm os.FileMode) error {
				writtenFiles[path] = data
				return nil
			},
		}

		state := &gitstate.State{Kind: gitstate.StateMerge}
		opts := ResolveOptions{
			Strategy: StrategyOurs, // fallback strategy
		}

		result, err := resolver.Run(context.Background(), state, opts)

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
// Feature: ai-resolve, Property 7: 파일 필터링 정확성
// Validates: Requirements 11.1
// ---------------------------------------------------------------------------

func TestPropertyFileFilteringAccuracy(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// 2~5개의 충돌 파일 경로 생성
		totalCount := rapid.IntRange(2, 5).Draw(rt, "total_count")
		var allPaths []string
		fileContents := make(map[string][]byte)

		for i := 0; i < totalCount; i++ {
			path := fmt.Sprintf("src/file%d.go", i)
			allPaths = append(allPaths, path)
			cf := makeConflictFile(path)
			fileContents[path] = buildConflictContent(cf)
		}

		// 부분 집합 선택: 각 파일을 독립적으로 포함/제외 (최소 1개 보장)
		var subset []string
		for _, p := range allPaths {
			if rapid.Bool().Draw(rt, "include_"+p) {
				subset = append(subset, p)
			}
		}
		// 최소 1개 보장
		if len(subset) == 0 {
			subset = append(subset, allPaths[0])
		}

		// FakeRunner: status + add 응답
		responses := map[string]git.FakeResponse{
			"status --porcelain=v2": {Stdout: buildPorcelainV2(allPaths)},
		}
		for _, p := range allPaths {
			responses["add "+p] = git.FakeResponse{}
		}
		runner := &git.FakeRunner{Responses: responses}

		stderr := &bytes.Buffer{}
		resolver := &Resolver{
			Runner: runner,
			Client: git.NewClient(runner),
			Stderr: stderr,
			ReadFile: func(path string) ([]byte, error) {
				if data, ok := fileContents[path]; ok {
					return data, nil
				}
				return nil, fmt.Errorf("file not found: %s", path)
			},
			WriteFile: func(path string, data []byte, perm os.FileMode) error {
				return nil
			},
		}

		state := &gitstate.State{Kind: gitstate.StateMerge}
		opts := ResolveOptions{
			Strategy: StrategyOurs,
			Files:    subset,
		}

		result, err := resolver.Run(context.Background(), state, opts)
		if err != nil {
			rt.Fatalf("unexpected error: %v", err)
		}

		// 처리된 파일이 정확히 subset과 일치해야 함
		resolved := make(map[string]bool)
		for _, p := range result.Resolved {
			resolved[p] = true
		}

		expected := make(map[string]bool)
		for _, p := range subset {
			expected[p] = true
		}

		// resolved + failed = 처리 대상 전체
		for p := range result.Failed {
			resolved[p] = true
		}

		// resolved 파일은 모두 subset에 포함되어야 함
		for p := range resolved {
			if !expected[p] {
				rt.Fatalf("processed file %q is not in subset %v", p, subset)
			}
		}

		// subset의 모든 파일이 처리되어야 함 (resolved 또는 failed)
		for _, p := range subset {
			if !resolved[p] {
				rt.Fatalf("subset file %q was not processed", p)
			}
		}

		// subset에 없는 파일은 처리되지 않아야 함
		sort.Strings(allPaths)
		for _, p := range allPaths {
			if !expected[p] && resolved[p] {
				rt.Fatalf("file %q is not in subset but was processed", p)
			}
		}
	})
}
