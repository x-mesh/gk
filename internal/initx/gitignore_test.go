package initx

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// allLangKeys는 langIgnorePatterns에 정의된 모든 언어 키이다.
var allLangKeys = []string{"go", "node", "python", "rust", "java", "ruby", "php"}

// --- Unit Tests ---

func TestGenerateGitignore_NoLanguages(t *testing.T) {
	result := &AnalysisResult{}
	got := GenerateGitignore(result)

	if !strings.Contains(got, "# Security") {
		t.Error("expected # Security header")
	}
	if !strings.Contains(got, "# IDE/Editor") {
		t.Error("expected # IDE/Editor header")
	}
	for _, p := range SecurityPatterns {
		if !strings.Contains(got, p) {
			t.Errorf("missing security pattern %q", p)
		}
	}
	for _, p := range IDEPatterns {
		if !strings.Contains(got, p) {
			t.Errorf("missing IDE pattern %q", p)
		}
	}
}

func TestGenerateGitignore_SingleLanguage(t *testing.T) {
	result := &AnalysisResult{
		Languages: []Language{{Name: "go", MarkerFile: "go.mod"}},
	}
	got := GenerateGitignore(result)

	if !strings.Contains(got, "# Language: Go") {
		t.Error("expected # Language: Go header")
	}
	for _, p := range langIgnorePatterns["go"] {
		if !strings.Contains(got, p) {
			t.Errorf("missing go pattern %q", p)
		}
	}
}

func TestGenerateGitignore_MultipleLanguages(t *testing.T) {
	result := &AnalysisResult{
		Languages: []Language{
			{Name: "go", MarkerFile: "go.mod"},
			{Name: "node", MarkerFile: "package.json"},
		},
	}
	got := GenerateGitignore(result)

	if !strings.Contains(got, "# Language: Go") {
		t.Error("expected # Language: Go header")
	}
	if !strings.Contains(got, "# Language: Node.js") {
		t.Error("expected # Language: Node.js header")
	}
}

func TestParseGitignore(t *testing.T) {
	content := "# comment\nnode_modules/\n\n*.log\n# another comment\ndist/\n"
	got := ParseGitignore(content)

	want := []string{"node_modules/", "*.log", "dist/"}
	if len(got) != len(want) {
		t.Fatalf("expected %d patterns, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pattern[%d]: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestParseGitignore_Empty(t *testing.T) {
	got := ParseGitignore("")
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestMergeGitignore_NoDuplicates(t *testing.T) {
	existing := "node_modules/\n*.log\n"
	generated := "# Language: Node.js\nnode_modules/\ndist/\n\n# Security\n.env\n"

	merged, added := MergeGitignore(existing, generated)

	// 기존 패턴 보존
	if !strings.Contains(merged, "node_modules/") {
		t.Error("existing pattern node_modules/ lost")
	}
	if !strings.Contains(merged, "*.log") {
		t.Error("existing pattern *.log lost")
	}

	// 중복 미추가: node_modules/는 added에 없어야 함
	for _, a := range added {
		if a == "node_modules/" {
			t.Error("node_modules/ should not be in added (already exists)")
		}
	}

	// 새 패턴 추가 확인
	if !strings.Contains(merged, "dist/") {
		t.Error("new pattern dist/ not added")
	}
	if !strings.Contains(merged, ".env") {
		t.Error("new pattern .env not added")
	}
}

func TestMergeGitignore_AllExist(t *testing.T) {
	existing := ".env\n.idea/\n"
	generated := "# Security\n.env\n\n# IDE/Editor\n.idea/\n"

	merged, added := MergeGitignore(existing, generated)

	if len(added) != 0 {
		t.Errorf("expected no additions, got %v", added)
	}
	if merged != existing {
		t.Errorf("merged should equal existing when nothing to add")
	}
}

func TestMergeGitignore_PreservesOrder(t *testing.T) {
	existing := "# My custom rules\nfoo/\nbar/\nbaz/\n"
	generated := "# Security\n.env\n"

	merged, _ := MergeGitignore(existing, generated)

	// 기존 내용이 앞에 그대로 있어야 함
	if !strings.HasPrefix(merged, existing) {
		t.Error("existing content should be preserved at the beginning")
	}
}

// --- Property Tests ---

// Feature: gk-init, Property 3: Gitignore 생성 완전성
// Validates: Requirements 6.1, 6.2, 6.3, 6.4
func TestProperty3_GitignoreGenerationCompleteness(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// 0~7개의 언어를 임의로 선택
		numLangs := rapid.IntRange(0, 7).Draw(rt, "numLangs")
		var selectedLangs []string
		if numLangs > 0 {
			selectedLangs = rapid.SliceOfNDistinct(
				rapid.SampledFrom(allLangKeys),
				numLangs, numLangs,
				rapid.ID[string],
			).Draw(rt, "langs")
		}

		// AnalysisResult 구성
		result := &AnalysisResult{}
		for _, lang := range selectedLangs {
			result.Languages = append(result.Languages, Language{Name: lang})
		}

		got := GenerateGitignore(result)

		// 검증 1: 각 언어의 표준 ignore 패턴이 모두 포함
		for _, lang := range selectedLangs {
			patterns := langIgnorePatterns[lang]
			for _, pat := range patterns {
				if !strings.Contains(got, pat) {
					rt.Fatalf("language %q pattern %q missing from output", lang, pat)
				}
			}
		}

		// 검증 2: 공통 보안 패턴 항상 포함
		for _, pat := range SecurityPatterns {
			if !strings.Contains(got, pat) {
				rt.Fatalf("security pattern %q missing from output", pat)
			}
		}

		// 검증 3: 공통 IDE/에디터 패턴 항상 포함
		for _, pat := range IDEPatterns {
			if !strings.Contains(got, pat) {
				rt.Fatalf("IDE pattern %q missing from output", pat)
			}
		}

		// 검증 4: 카테고리별 주석 헤더 포함
		for _, lang := range selectedLangs {
			display := langDisplayName[lang]
			header := "# Language: " + display
			if !strings.Contains(got, header) {
				rt.Fatalf("language header %q missing from output", header)
			}
		}
		if !strings.Contains(got, "# Security") {
			rt.Fatal("# Security header missing")
		}
		if !strings.Contains(got, "# IDE/Editor") {
			rt.Fatal("# IDE/Editor header missing")
		}
	})
}

// Feature: gk-init, Property 4: Gitignore 병합 보존성
// Validates: Requirements 7.1, 7.3, 7.4
func TestProperty4_GitignoreMergePreservation(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// 임의의 기존 gitignore 생성: 알려진 패턴 + 랜덤 패턴 혼합
		knownPatterns := collectAllKnownPatterns()
		numExisting := rapid.IntRange(0, 10).Draw(rt, "numExisting")
		numRandom := rapid.IntRange(0, 5).Draw(rt, "numRandom")

		var existingLines []string
		existingSet := make(map[string]bool)

		// 알려진 패턴에서 일부 선택
		if numExisting > 0 && len(knownPatterns) > 0 {
			count := numExisting
			if count > len(knownPatterns) {
				count = len(knownPatterns)
			}
			selected := rapid.SliceOfNDistinct(
				rapid.SampledFrom(knownPatterns),
				count, count,
				rapid.ID[string],
			).Draw(rt, "existingKnown")
			for _, p := range selected {
				existingLines = append(existingLines, p)
				existingSet[p] = true
			}
		}

		// 랜덤 패턴 추가
		for i := 0; i < numRandom; i++ {
			p := rapid.StringMatching(`[a-z][a-z0-9_-]{2,10}/`).Draw(rt, "randomPat")
			if !existingSet[p] {
				existingLines = append(existingLines, p)
				existingSet[p] = true
			}
		}

		existing := strings.Join(existingLines, "\n")
		if len(existingLines) > 0 {
			existing += "\n"
		}

		// 임의의 AnalysisResult로 generated gitignore 생성
		numLangs := rapid.IntRange(0, 7).Draw(rt, "numLangs")
		result := &AnalysisResult{}
		if numLangs > 0 {
			langs := rapid.SliceOfNDistinct(
				rapid.SampledFrom(allLangKeys),
				numLangs, numLangs,
				rapid.ID[string],
			).Draw(rt, "langs")
			for _, l := range langs {
				result.Languages = append(result.Languages, Language{Name: l})
			}
		}
		generated := GenerateGitignore(result)

		merged, added := MergeGitignore(existing, generated)

		// 검증 1: 기존 규칙이 모두 보존됨
		mergedPatterns := ParseGitignore(merged)
		mergedSet := make(map[string]bool)
		for _, p := range mergedPatterns {
			mergedSet[p] = true
		}
		for _, p := range existingLines {
			if !mergedSet[p] {
				rt.Fatalf("existing pattern %q lost after merge", p)
			}
		}

		// 검증 2: 기존 규칙의 순서 보존 (merged 내에서 기존 패턴의 상대 순서)
		if len(existingLines) > 1 {
			lastIdx := -1
			for _, p := range existingLines {
				found := false
				for i := lastIdx + 1; i < len(mergedPatterns); i++ {
					if mergedPatterns[i] == p {
						lastIdx = i
						found = true
						break
					}
				}
				if !found {
					rt.Fatalf("existing pattern %q order not preserved in merged output", p)
				}
			}
		}

		// 검증 3: 중복 미추가 — added에 기존 패턴이 없어야 함
		for _, a := range added {
			if existingSet[a] {
				rt.Fatalf("pattern %q already existed but was added again", a)
			}
		}

		// 검증 4: added에 있는 패턴은 merged에 존재해야 함
		for _, a := range added {
			if !mergedSet[a] {
				rt.Fatalf("added pattern %q not found in merged output", a)
			}
		}
	})
}

// collectAllKnownPatterns는 모든 알려진 gitignore 패턴을 수집한다.
func collectAllKnownPatterns() []string {
	seen := make(map[string]bool)
	var all []string
	for _, patterns := range langIgnorePatterns {
		for _, p := range patterns {
			if !seen[p] {
				seen[p] = true
				all = append(all, p)
			}
		}
	}
	for _, p := range SecurityPatterns {
		if !seen[p] {
			seen[p] = true
			all = append(all, p)
		}
	}
	for _, p := range IDEPatterns {
		if !seen[p] {
			seen[p] = true
			all = append(all, p)
		}
	}
	return all
}
