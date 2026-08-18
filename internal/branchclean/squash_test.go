package branchclean

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"pgregory.net/rapid"
)

// Feature: ai-branch-clean, Property 1: git cherry 파싱 정확성
// **Validates: Requirements 1.1, 1.2, 1.3**
func TestProperty1_ParseCherryOutputAccuracy(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(0, 20).Draw(rt, "lineCount")

		var lines []string
		var plusCount, minusCount int

		for i := 0; i < n; i++ {
			prefix := rapid.SampledFrom([]string{"+", "-"}).Draw(rt, fmt.Sprintf("prefix_%d", i))
			hash := rapid.StringMatching(`[0-9a-f]{40}`).Draw(rt, fmt.Sprintf("hash_%d", i))
			lines = append(lines, prefix+" "+hash)
			if prefix == "+" {
				plusCount++
			} else {
				minusCount++
			}
		}

		output := strings.Join(lines, "\n")
		allApplied, mixed, err := ParseCherryOutput(output)

		if err != nil {
			rt.Fatalf("unexpected error: %v", err)
		}

		// 빈 출력
		if n == 0 {
			if allApplied || mixed {
				rt.Fatalf("empty output: expected allApplied=false, mixed=false, got allApplied=%v, mixed=%v", allApplied, mixed)
			}
			return
		}

		// 모든 라인이 `-`: allApplied=true, mixed=false
		if minusCount == n && plusCount == 0 {
			if !allApplied || mixed {
				rt.Fatalf("all minus: expected allApplied=true, mixed=false, got allApplied=%v, mixed=%v", allApplied, mixed)
			}
			return
		}

		// `+`와 `-` 혼합: allApplied=false, mixed=true
		if plusCount > 0 && minusCount > 0 {
			if allApplied || !mixed {
				rt.Fatalf("mixed: expected allApplied=false, mixed=true, got allApplied=%v, mixed=%v", allApplied, mixed)
			}
			return
		}

		// 모든 라인이 `+`: allApplied=false, mixed=false
		if plusCount == n && minusCount == 0 {
			if allApplied || mixed {
				rt.Fatalf("all plus: expected allApplied=false, mixed=false, got allApplied=%v, mixed=%v", allApplied, mixed)
			}
			return
		}
	})
}

// --- Unit tests for ParseCherryOutput ---

func TestParseCherryOutput_Empty(t *testing.T) {
	allApplied, mixed, err := ParseCherryOutput("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allApplied || mixed {
		t.Fatalf("expected false/false, got %v/%v", allApplied, mixed)
	}
}

func TestParseCherryOutput_AllMinus(t *testing.T) {
	output := "- abc123\n- def456\n- ghi789"
	allApplied, mixed, err := ParseCherryOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allApplied || mixed {
		t.Fatalf("expected true/false, got %v/%v", allApplied, mixed)
	}
}

func TestParseCherryOutput_AllPlus(t *testing.T) {
	output := "+ abc123\n+ def456"
	allApplied, mixed, err := ParseCherryOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allApplied || mixed {
		t.Fatalf("expected false/false, got %v/%v", allApplied, mixed)
	}
}

func TestParseCherryOutput_Mixed(t *testing.T) {
	output := "+ abc123\n- def456\n+ ghi789"
	allApplied, mixed, err := ParseCherryOutput(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allApplied || !mixed {
		t.Fatalf("expected false/true, got %v/%v", allApplied, mixed)
	}
}

func TestParseCherryOutput_InvalidFormat(t *testing.T) {
	output := "invalid line"
	_, _, err := ParseCherryOutput(output)
	if err == nil {
		t.Fatal("expected error for invalid format")
	}
}

// --- Unit tests for DetectSquashMerged ---

func TestDetectSquashMerged_SkipsProtected(t *testing.T) {
	runner := &git.FakeRunner{}
	d := &SquashDetector{Runner: runner}

	protected := map[string]bool{"main": true, "develop": true}
	squashed, ambig, warnings := d.DetectSquashMerged(
		context.Background(),
		[]string{"main", "feat/a"},
		"main",
		protected,
	)

	// main은 protected이므로 건너뛰어야 함
	// feat/a는 FakeRunner의 DefaultResp (빈 출력)이므로 어디에도 포함되지 않음
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(squashed) != 0 {
		t.Fatalf("unexpected squashed: %v", squashed)
	}
	if len(ambig) != 0 {
		t.Fatalf("unexpected ambiguous: %v", ambig)
	}
}

func TestDetectSquashMerged_ClassifiesCorrectly(t *testing.T) {
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"cherry main feat/done":    {Stdout: "- abc123\n- def456\n"},
			"cherry main feat/partial": {Stdout: "+ abc123\n- def456\n"},
			"cherry main feat/new":     {Stdout: "+ abc123\n+ def456\n"},
		},
	}
	d := &SquashDetector{Runner: runner}

	squashed, ambig, warnings := d.DetectSquashMerged(
		context.Background(),
		[]string{"feat/done", "feat/partial", "feat/new"},
		"main",
		nil,
	)

	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(squashed) != 1 || squashed[0] != "feat/done" {
		t.Fatalf("expected squashed=[feat/done], got %v", squashed)
	}
	if len(ambig) != 1 || ambig[0] != "feat/partial" {
		t.Fatalf("expected ambiguous=[feat/partial], got %v", ambig)
	}
}

// 멀티커밋 squash merge는 cherry에 전부 `+`로 보인다(합쳐진 커밋의 patch-id가
// 원본 어느 커밋과도 불일치) — merge-tree 콘텐츠 검사가 잡아야 하고, 트리가
// 다르거나 충돌하는 브랜치는 잡으면 안 된다.
func TestDetectSquashMerged_ContentCheckCatchesWhatCherryCannot(t *testing.T) {
	baseTree := strings.Repeat("a", 40)
	otherTree := strings.Repeat("b", 40)
	conflictTree := strings.Repeat("c", 40)
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse main^{tree}":                      {Stdout: baseTree + "\n"},
			"cherry main feat/squashed":                  {Stdout: "+ 111\n+ 222\n"},
			"merge-tree --write-tree main feat/squashed": {Stdout: baseTree + "\n"},
			"cherry main feat/active":                    {Stdout: "+ 111\n"},
			"merge-tree --write-tree main feat/active":   {Stdout: otherTree + "\n"},
			"cherry main feat/conflict":                  {Stdout: "+ 111\n"},
			"merge-tree --write-tree main feat/conflict": {Stdout: conflictTree + "\n\nf.txt\n", ExitCode: 1},
			"cherry main feat/mixed":                     {Stdout: "+ 111\n- 222\n"},
			"merge-tree --write-tree main feat/mixed":    {Stdout: otherTree + "\n"},
		},
	}
	d := &SquashDetector{Runner: runner}

	squashed, ambig, warnings := d.DetectSquashMerged(
		context.Background(),
		[]string{"feat/squashed", "feat/active", "feat/conflict", "feat/mixed"},
		"main",
		nil,
	)

	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(squashed) != 1 || squashed[0] != "feat/squashed" {
		t.Fatalf("expected squashed=[feat/squashed], got %v", squashed)
	}
	// mixed cherry에 콘텐츠도 다르면 종전대로 ambiguous로 남는다.
	if len(ambig) != 1 || ambig[0] != "feat/mixed" {
		t.Fatalf("expected ambiguous=[feat/mixed], got %v", ambig)
	}
}

// cherry가 이미 확정(all `-`)한 브랜치는 merge-tree를 부르지 않는다 —
// 콘텐츠 검사는 미확정 브랜치에만 붙는 fork다.
func TestDetectSquashMerged_CherryFastPathSkipsMergeTree(t *testing.T) {
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse main^{tree}": {Stdout: strings.Repeat("a", 40) + "\n"},
			"cherry main feat/done": {Stdout: "- 111\n"},
		},
	}
	d := &SquashDetector{Runner: runner}

	squashed, _, _ := d.DetectSquashMerged(context.Background(), []string{"feat/done"}, "main", nil)
	if len(squashed) != 1 || squashed[0] != "feat/done" {
		t.Fatalf("expected squashed=[feat/done], got %v", squashed)
	}
	for _, call := range runner.Calls {
		if len(call.Args) > 0 && call.Args[0] == "merge-tree" {
			t.Fatalf("merge-tree must not run for a cherry-confirmed branch: %v", call.Args)
		}
	}
}

// EffectivelyMergedSet: base^{tree}는 한 번만 해석하고, net-zero 브랜치만
// 집합에 남는다. 검사 실패는 조용히 미포함(under-claim).
func TestEffectivelyMergedSet(t *testing.T) {
	baseTree := strings.Repeat("a", 40)
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse main^{tree}":                {Stdout: baseTree + "\n"},
			"merge-tree --write-tree main feat/sq": {Stdout: baseTree + "\n"},
			"merge-tree --write-tree main feat/wip": {
				Stdout: strings.Repeat("b", 40) + "\n"},
			"merge-tree --write-tree main feat/err": {Err: fmt.Errorf("boom")},
		},
	}

	got := EffectivelyMergedSet(context.Background(), runner,
		"main", []string{"feat/sq", "feat/wip", "feat/err"})
	if len(got) != 1 || !got["feat/sq"] {
		t.Fatalf("set = %v, want only feat/sq", got)
	}

	revParses := 0
	for _, call := range runner.Calls {
		if len(call.Args) > 0 && call.Args[0] == "rev-parse" {
			revParses++
		}
	}
	if revParses != 1 {
		t.Errorf("rev-parse ran %d times, want 1 (base tree resolved once)", revParses)
	}
}

// base^{tree} 해석 실패는 경고 + cherry-only 강등이지, 전체 실패가 아니다.
func TestDetectSquashMerged_BaseTreeFailureDegradesToCherry(t *testing.T) {
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse main^{tree}": {Err: fmt.Errorf("bad object")},
			"cherry main feat/done": {Stdout: "- 111\n"},
			"cherry main feat/new":  {Stdout: "+ 111\n"},
		},
	}
	d := &SquashDetector{Runner: runner}

	squashed, ambig, warnings := d.DetectSquashMerged(
		context.Background(), []string{"feat/done", "feat/new"}, "main", nil)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "content check disabled") {
		t.Fatalf("expected one content-check-disabled warning, got %v", warnings)
	}
	if len(squashed) != 1 || squashed[0] != "feat/done" {
		t.Fatalf("expected squashed=[feat/done], got %v", squashed)
	}
	if len(ambig) != 0 {
		t.Fatalf("unexpected ambiguous: %v", ambig)
	}
}

func TestDetectSquashMerged_GitCherryFailure(t *testing.T) {
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"cherry main feat/broken": {Err: fmt.Errorf("git cherry failed")},
			"cherry main feat/ok":     {Stdout: "- abc123\n"},
		},
	}
	d := &SquashDetector{Runner: runner}

	squashed, ambig, warnings := d.DetectSquashMerged(
		context.Background(),
		[]string{"feat/broken", "feat/ok"},
		"main",
		nil,
	)

	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %v", warnings)
	}
	if len(squashed) != 1 || squashed[0] != "feat/ok" {
		t.Fatalf("expected squashed=[feat/ok], got %v", squashed)
	}
	if len(ambig) != 0 {
		t.Fatalf("unexpected ambiguous: %v", ambig)
	}
}
