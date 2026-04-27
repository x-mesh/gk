package initx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/commitlint"
	"github.com/x-mesh/gk/internal/git"
	"pgregory.net/rapid"
)

// Feature: gk-init, Property 1: Marker file 감지 정확성
// Validates: Requirements 1.1, 1.2, 2.1, 2.2
func TestProperty1_MarkerFileDetectionAccuracy(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		dir := t.TempDir()

		// 임의의 marker file 부분집합 생성
		selectedMarkers := rapid.SliceOfDistinct(
			rapid.SampledFrom(MarkerFiles),
			rapid.ID[string],
		).Draw(rt, "markers")

		for _, marker := range selectedMarkers {
			p := filepath.Join(dir, marker)
			if err := os.WriteFile(p, []byte(""), 0644); err != nil {
				rt.Fatal(err)
			}
		}

		// 임의의 빌드 시스템 파일 부분집합 생성
		selectedBuildFiles := rapid.SliceOfDistinct(
			rapid.SampledFrom(BuildSystemFileList),
			rapid.ID[string],
		).Draw(rt, "buildFiles")

		for _, bf := range selectedBuildFiles {
			p := filepath.Join(dir, bf)
			if err := os.WriteFile(p, []byte(""), 0644); err != nil {
				rt.Fatal(err)
			}
		}

		result, err := AnalyzeProject(dir, nil)
		if err != nil {
			rt.Fatal(err)
		}

		// 검증 1: 감지된 언어가 매핑 테이블과 일치하는지
		expectedLangs := make(map[string]bool)
		for _, marker := range selectedMarkers {
			expectedLangs[markerToLang[marker]] = true
		}

		gotLangs := make(map[string]bool)
		for _, lang := range result.Languages {
			gotLangs[lang.Name] = true
		}

		if len(expectedLangs) != len(gotLangs) {
			rt.Fatalf("language count mismatch: expected %d, got %d (expected=%v, got=%v)",
				len(expectedLangs), len(gotLangs), setKeys(expectedLangs), setKeys(gotLangs))
		}
		for lang := range expectedLangs {
			if !gotLangs[lang] {
				rt.Fatalf("expected language %q not found in result", lang)
			}
		}
		for lang := range gotLangs {
			if !expectedLangs[lang] {
				rt.Fatalf("unexpected language %q in result", lang)
			}
		}

		// 검증 2: 감지된 빌드 시스템이 매핑 테이블과 일치하는지
		expectedBS := make(map[string]bool)
		for _, bf := range selectedBuildFiles {
			expectedBS[buildSystemFiles[bf]] = true
		}

		gotBS := make(map[string]bool)
		for _, bs := range result.BuildSystems {
			gotBS[bs.Name] = true
		}

		if len(expectedBS) != len(gotBS) {
			rt.Fatalf("build system count mismatch: expected %d, got %d (expected=%v, got=%v)",
				len(expectedBS), len(gotBS), setKeys(expectedBS), setKeys(gotBS))
		}
		for bs := range expectedBS {
			if !gotBS[bs] {
				rt.Fatalf("expected build system %q not found in result", bs)
			}
		}
		for bs := range gotBS {
			if !expectedBS[bs] {
				rt.Fatalf("unexpected build system %q in result", bs)
			}
		}
	})
}

// setKeys는 map의 키를 정렬된 슬라이스로 반환한다 (디버깅용).
func setKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// conventionalTypes는 테스트에서 사용할 conventional commit type 목록이다.
var conventionalTypes = []string{"feat", "fix", "chore", "docs", "style", "refactor", "perf", "test", "build", "ci", "revert"}

// genConventionalMsg는 conventional commit 메시지를 생성하는 rapid generator이다.
func genConventionalMsg(rt *rapid.T, label string) string {
	typ := rapid.SampledFrom(conventionalTypes).Draw(rt, label+"-type")
	subject := rapid.StringMatching(`[a-z][a-z0-9 ]{2,20}`).Draw(rt, label+"-subject")
	return fmt.Sprintf("%s: %s", typ, subject)
}

// genFreeFormMsg는 conventional commit 형식이 아닌 메시지를 생성하는 rapid generator이다.
func genFreeFormMsg(rt *rapid.T, label string) string {
	// conventional 형식과 매칭되지 않도록 콜론 없는 메시지 생성
	return rapid.StringMatching(`[A-Z][a-z]{3,15} [a-z]{3,10} [a-z]{3,10}`).Draw(rt, label+"-freeform")
}

// Feature: gk-init, Property 2: Conventional commit 비율 계산 및 type 정렬
// Validates: Requirements 3.1, 3.2
func TestProperty2_ConventionalCommitRatioAndTypeSorting(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// conventional + free-form 혼합 메시지 생성
		convCount := rapid.IntRange(0, 30).Draw(rt, "convCount")
		freeCount := rapid.IntRange(0, 30).Draw(rt, "freeCount")

		// 최소 1개의 메시지가 있어야 함
		if convCount+freeCount == 0 {
			convCount = 1
		}

		var messages []string
		for i := 0; i < convCount; i++ {
			messages = append(messages, genConventionalMsg(rt, fmt.Sprintf("conv-%d", i)))
		}
		for i := 0; i < freeCount; i++ {
			messages = append(messages, genFreeFormMsg(rt, fmt.Sprintf("free-%d", i)))
		}

		// FakeRunner 구성
		logOutput := strings.Join(messages, "\n") + "\n"
		fake := &git.FakeRunner{
			Responses: map[string]git.FakeResponse{
				"log --format=%s -200": {Stdout: logOutput},
				"branch -a":            {Stdout: "* main\n"},
			},
		}

		result := analyzeCommits(context.Background(), fake)

		// 검증 1: ConventionalRatio가 수동 계산과 일치
		var expectedValid int
		for _, msg := range messages {
			parsed := commitlint.Parse(msg)
			if parsed.HeaderValid {
				expectedValid++
			}
		}
		expectedRatio := float64(expectedValid) / float64(len(messages))

		if result.ConventionalRatio != expectedRatio {
			rt.Fatalf("ratio mismatch: expected %f, got %f (valid=%d, total=%d)",
				expectedRatio, result.ConventionalRatio, expectedValid, len(messages))
		}

		// 검증 2: 비율 >= 0.5일 때 Types가 빈도 내림차순 정렬
		if result.ConventionalRatio >= 0.5 {
			// type별 빈도 수동 계산
			typeCounts := make(map[string]int)
			for _, msg := range messages {
				parsed := commitlint.Parse(msg)
				if parsed.HeaderValid {
					typeCounts[parsed.Type]++
				}
			}

			// 결과의 type이 빈도 내림차순인지 검증
			for i := 1; i < len(result.Types); i++ {
				prevCount := typeCounts[result.Types[i-1]]
				currCount := typeCounts[result.Types[i]]
				if prevCount < currCount {
					rt.Fatalf("types not sorted by frequency: %q (count=%d) before %q (count=%d)",
						result.Types[i-1], prevCount, result.Types[i], currCount)
				}
			}

			// 결과에 모든 감지된 type이 포함되어 있는지 검증
			resultTypeSet := make(map[string]bool)
			for _, typ := range result.Types {
				resultTypeSet[typ] = true
			}
			for typ := range typeCounts {
				if !resultTypeSet[typ] {
					rt.Fatalf("type %q found in commits but missing from result.Types", typ)
				}
			}
		}
	})
}

// branchPrefixes는 테스트에서 사용할 branch prefix 목록이다.
var branchPrefixes = []string{"feat", "fix", "chore", "release", "hotfix", "bugfix", "docs", "refactor"}

// Feature: gk-init, Property 9: Branch 패턴 추출 매칭
// Validates: Requirements 4.1, 4.2
func TestProperty9_BranchPatternExtractionMatching(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// 공통 prefix를 가진 branch 이름 생성
		numPrefixes := rapid.IntRange(1, 4).Draw(rt, "numPrefixes")
		selectedPrefixes := rapid.SliceOfNDistinct(
			rapid.SampledFrom(branchPrefixes),
			numPrefixes, numPrefixes,
			rapid.ID[string],
		).Draw(rt, "prefixes")

		var branches []string
		// 각 prefix에 대해 1~5개의 branch 생성
		for _, prefix := range selectedPrefixes {
			count := rapid.IntRange(1, 5).Draw(rt, "count-"+prefix)
			for i := 0; i < count; i++ {
				suffix := rapid.StringMatching(`[a-z][a-z0-9._-]{2,15}`).Draw(rt, fmt.Sprintf("suffix-%s-%d", prefix, i))
				branches = append(branches, prefix+"/"+suffix)
			}
		}

		// 기본 branch도 추가 (패턴 없는 branch)
		branches = append(branches, "main")

		// ExtractBranchPatterns 호출
		patterns := ExtractBranchPatterns(branches)

		if len(patterns) == 0 {
			rt.Fatal("expected at least one pattern, got none")
		}

		// 검증: 추출된 정규식이 원본 prefix branch와 매칭되는지
		for _, pat := range patterns {
			re, err := regexp.Compile(pat)
			if err != nil {
				rt.Fatalf("invalid regex pattern %q: %v", pat, err)
			}

			// prefix가 있는 모든 branch가 패턴과 매칭되어야 함
			for _, b := range branches {
				if !strings.Contains(b, "/") {
					continue // main, master 등은 건너뜀
				}
				prefix := b[:strings.Index(b, "/")]
				// 이 prefix가 선택된 prefix 중 하나인지 확인
				found := false
				for _, sp := range selectedPrefixes {
					if prefix == sp {
						found = true
						break
					}
				}
				if !found {
					continue
				}

				if !re.MatchString(b) {
					rt.Fatalf("pattern %q does not match branch %q", pat, b)
				}
			}
		}
	})
}
