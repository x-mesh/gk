package initx

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"pgregory.net/rapid"
)

// --- Unit Tests ---

func TestGenerateConfig_Default(t *testing.T) {
	result := &AnalysisResult{}
	got := GenerateConfig(result)

	if !strings.Contains(got, "base_branch: main") {
		t.Error("expected base_branch: main")
	}
	if !strings.Contains(got, "commit-lint") {
		t.Error("expected default preflight step commit-lint")
	}
	if !strings.Contains(got, "branch-check") {
		t.Error("expected default preflight step branch-check")
	}
	if !strings.Contains(got, "# policies:") {
		t.Error("expected commented-out policies block")
	}
}

func TestGenerateConfig_WithAnalysis(t *testing.T) {
	result := &AnalysisResult{
		BaseBranch: "develop",
		Protected:  []string{"main", "develop", "release"},
		BranchPats: []string{"^(feat|fix)/[a-z0-9._-]+"},
		CommitInfo: CommitAnalysis{
			Types: []string{"feat", "fix", "docs"},
		},
		Preflight: []PreflightStep{
			{Name: "commit-lint", Command: "commit-lint"},
			{Name: "lint", Command: "golangci-lint run"},
		},
	}
	got := GenerateConfig(result)

	if !strings.Contains(got, "base_branch: develop") {
		t.Error("expected base_branch: develop")
	}
	if !strings.Contains(got, "release") {
		t.Error("expected release in protected branches")
	}
	if !strings.Contains(got, "golangci-lint run") {
		t.Error("expected golangci-lint run in preflight steps")
	}
}

func TestGenerateConfig_NoPersonalFields(t *testing.T) {
	result := &AnalysisResult{}
	got := GenerateConfig(result)

	for _, field := range []string{"provider:", "lang:", "ui:", "log:", "clone:", "nvidia:", "allow_remote:"} {
		if strings.Contains(got, field) {
			t.Errorf("personal config field %q should not appear in generated config", field)
		}
	}
}

func TestGenerateConfig_HasPoliciesComment(t *testing.T) {
	result := &AnalysisResult{}
	got := GenerateConfig(result)

	if !strings.Contains(got, "# policies:") {
		t.Error("expected commented-out policies block")
	}
	if !strings.Contains(got, "#   secret_patterns:") {
		t.Error("expected secret_patterns in policies comment")
	}
}

func TestMergeConfig_AddsNewFields(t *testing.T) {
	existing := []byte("base_branch: main\n")
	generated := []byte("base_branch: develop\nbranch:\n  protected:\n    - main\n")

	merged, added, err := MergeConfig(existing, generated)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	if err := yaml.Unmarshal(merged, &m); err != nil {
		t.Fatal(err)
	}

	// base_branch는 기존 값 유지
	if m["base_branch"] != "main" {
		t.Errorf("base_branch should remain 'main', got %v", m["base_branch"])
	}

	// branch는 새로 추가
	if _, ok := m["branch"]; !ok {
		t.Error("branch should be added")
	}

	if len(added) == 0 {
		t.Error("expected at least one added field")
	}
}

func TestMergeConfig_PreservesExistingValues(t *testing.T) {
	existing := []byte("base_branch: develop\ncommit:\n  types: [feat, fix]\n  scope_required: true\n")
	generated := []byte("base_branch: main\ncommit:\n  types: [feat, fix, chore]\n  scope_required: false\n  max_subject_length: 72\n")

	merged, _, err := MergeConfig(existing, generated)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	if err := yaml.Unmarshal(merged, &m); err != nil {
		t.Fatal(err)
	}

	// base_branch 기존 값 유지
	if m["base_branch"] != "develop" {
		t.Errorf("base_branch should remain 'develop', got %v", m["base_branch"])
	}

	// commit.scope_required 기존 값 유지
	commitMap := m["commit"].(map[string]any)
	if commitMap["scope_required"] != true {
		t.Errorf("scope_required should remain true, got %v", commitMap["scope_required"])
	}

	// commit.max_subject_length는 새로 추가
	if _, ok := commitMap["max_subject_length"]; !ok {
		t.Error("max_subject_length should be added")
	}
}

func TestMergeConfig_EmptyExisting(t *testing.T) {
	existing := []byte("{}\n")
	generated := []byte("base_branch: main\nbranch:\n  protected:\n    - main\n")

	merged, added, err := MergeConfig(existing, generated)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]any
	if err := yaml.Unmarshal(merged, &m); err != nil {
		t.Fatal(err)
	}

	if m["base_branch"] != "main" {
		t.Errorf("expected base_branch: main, got %v", m["base_branch"])
	}
	if len(added) < 2 {
		t.Errorf("expected at least 2 added fields, got %d", len(added))
	}
}

// --- Property Tests ---

// drawAnalysisResult는 rapid로 임의의 AnalysisResult를 생성한다.
func drawAnalysisResult(rt *rapid.T) *AnalysisResult {
	numLangs := rapid.IntRange(0, 7).Draw(rt, "numLangs")
	var langs []Language
	if numLangs > 0 {
		selected := rapid.SliceOfNDistinct(
			rapid.SampledFrom(allLangKeys),
			numLangs, numLangs,
			rapid.ID[string],
		).Draw(rt, "langs")
		for _, l := range selected {
			langs = append(langs, Language{Name: l, MarkerFile: l + ".marker"})
		}
	}

	baseBranch := rapid.SampledFrom([]string{"main", "develop", "master", ""}).Draw(rt, "baseBranch")

	numProtected := rapid.IntRange(0, 3).Draw(rt, "numProtected")
	var protected []string
	if numProtected > 0 {
		protected = rapid.SliceOfNDistinct(
			rapid.SampledFrom([]string{"main", "master", "develop", "release"}),
			numProtected, numProtected,
			rapid.ID[string],
		).Draw(rt, "protected")
	}

	numPats := rapid.IntRange(0, 2).Draw(rt, "numPats")
	var pats []string
	for i := 0; i < numPats; i++ {
		prefix := rapid.SampledFrom([]string{"feat", "fix", "chore", "release"}).Draw(rt, "prefix")
		pats = append(pats, "^"+prefix+"/[a-z0-9._-]+")
	}

	numTypes := rapid.IntRange(0, 11).Draw(rt, "numTypes")
	var types []string
	if numTypes > 0 {
		types = rapid.SliceOfNDistinct(
			rapid.SampledFrom(DefaultCommitTypes),
			numTypes, numTypes,
			rapid.ID[string],
		).Draw(rt, "types")
	}

	numSteps := rapid.IntRange(0, 5).Draw(rt, "numSteps")
	var steps []PreflightStep
	stepPool := []PreflightStep{
		{Name: "commit-lint", Command: "commit-lint"},
		{Name: "branch-check", Command: "branch-check"},
		{Name: "no-conflict", Command: "no-conflict"},
		{Name: "lint", Command: "golangci-lint run"},
		{Name: "test", Command: "go test ./..."},
	}
	if numSteps > 0 {
		count := numSteps
		if count > len(stepPool) {
			count = len(stepPool)
		}
		indices := rapid.SliceOfNDistinct(
			rapid.IntRange(0, len(stepPool)-1),
			count, count,
			rapid.ID[int],
		).Draw(rt, "stepIndices")
		for _, idx := range indices {
			steps = append(steps, stepPool[idx])
		}
	}

	return &AnalysisResult{
		Languages:  langs,
		BaseBranch: baseBranch,
		Protected:  protected,
		BranchPats: pats,
		CommitInfo: CommitAnalysis{Types: types},
		Preflight:  steps,
	}
}

// globalConfigKeys는 .gk.yaml에 절대 포함되면 안 되는 Global_Config 최상위 키이다.
var globalConfigTopKeys = []string{"ui", "log", "clone"}

// globalAISubKeys는 ai 하위에 포함되면 안 되는 Global_Config 키이다.
var globalAISubKeys = []string{"provider", "lang", "nvidia"}

// projectConfigTopKeys는 .gk.yaml에 반드시 포함되어야 하는 최상위 키이다.
var projectConfigTopKeys = []string{"base_branch", "branch", "commit", "preflight", "ai"}

// Feature: gk-init, Property 5: Config 생성 — 필수 필드 포함 및 개인 설정 배제
// Validates: Requirements 8.1, 8.2
func TestProperty5_ConfigRequiredFieldsAndNoPersonal(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		result := drawAnalysisResult(rt)
		generated := GenerateConfig(result)

		// YAML 파싱 (주석 제거 후)
		var m map[string]any
		if err := yaml.Unmarshal([]byte(generated), &m); err != nil {
			rt.Fatalf("failed to parse generated config: %v", err)
		}

		// 검증 1: Project_Config 필수 최상위 키 존재
		for _, key := range projectConfigTopKeys {
			if _, ok := m[key]; !ok {
				rt.Fatalf("required top-level key %q missing from generated config", key)
			}
		}

		// 검증 2: branch 하위 필드 존재
		branchMap, ok := toStringMap(m["branch"])
		if !ok {
			rt.Fatal("branch should be a map")
		}
		for _, key := range []string{"protected", "patterns"} {
			if _, ok := branchMap[key]; !ok {
				rt.Fatalf("branch.%s missing from generated config", key)
			}
		}

		// 검증 3: commit 하위 필드 존재
		commitMap, ok := toStringMap(m["commit"])
		if !ok {
			rt.Fatal("commit should be a map")
		}
		for _, key := range []string{"types", "scope_required", "max_subject_length"} {
			if _, ok := commitMap[key]; !ok {
				rt.Fatalf("commit.%s missing from generated config", key)
			}
		}

		// 검증 4: ai.commit 하위 필드 존재
		aiMap, ok := toStringMap(m["ai"])
		if !ok {
			rt.Fatal("ai should be a map")
		}
		aiCommitMap, ok := toStringMap(aiMap["commit"])
		if !ok {
			rt.Fatal("ai.commit should be a map")
		}
		for _, key := range []string{"deny_paths", "trailer", "audit"} {
			if _, ok := aiCommitMap[key]; !ok {
				rt.Fatalf("ai.commit.%s missing from generated config", key)
			}
		}

		// 검증 5: Global_Config 최상위 키 부재
		for _, key := range globalConfigTopKeys {
			if _, ok := m[key]; ok {
				rt.Fatalf("global config top-level key %q should not appear in generated config", key)
			}
		}

		// 검증 6: ai 하위에 개인 설정 키 부재
		for _, key := range globalAISubKeys {
			if _, ok := aiMap[key]; ok {
				rt.Fatalf("global config ai.%s should not appear in generated config", key)
			}
		}

		// 검증 7: ai.commit에 allow_remote 부재
		if _, ok := aiCommitMap["allow_remote"]; ok {
			rt.Fatal("ai.commit.allow_remote should not appear in generated config")
		}
	})
}

// Feature: gk-init, Property 6: Config 병합 — 기존 값 보존
// Validates: Requirements 9.1, 9.2, 9.3
func TestProperty6_ConfigMergePreservesExisting(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// 임의의 기존 YAML 생성
		existingMap := drawRandomYAMLMap(rt, "existing")
		existingBytes, err := yaml.Marshal(existingMap)
		if err != nil {
			rt.Fatalf("marshal existing: %v", err)
		}

		// 임의의 AnalysisResult로 generated config 생성
		result := drawAnalysisResult(rt)
		generatedStr := GenerateConfig(result)

		// 주석 제거 후 generated YAML 추출
		var genMap map[string]any
		if err := yaml.Unmarshal([]byte(generatedStr), &genMap); err != nil {
			rt.Fatalf("parse generated: %v", err)
		}
		generatedBytes, err := yaml.Marshal(genMap)
		if err != nil {
			rt.Fatalf("marshal generated: %v", err)
		}

		// 병합 실행
		mergedBytes, _, err := MergeConfig(existingBytes, generatedBytes)
		if err != nil {
			rt.Fatalf("MergeConfig error: %v", err)
		}

		var mergedMap map[string]any
		if err := yaml.Unmarshal(mergedBytes, &mergedMap); err != nil {
			rt.Fatalf("parse merged: %v", err)
		}

		// 검증: 기존 필드의 모든 값이 변경되지 않음
		assertValuesPreserved(rt, existingMap, mergedMap, "")
	})
}

// drawRandomYAMLMap은 rapid로 임의의 YAML-like map을 생성한다.
// config 병합 테스트에 적합한 구조를 생성한다.
func drawRandomYAMLMap(rt *rapid.T, label string) map[string]any {
	m := make(map[string]any)

	// 일부 Project_Config 키를 임의로 포함
	if rapid.Bool().Draw(rt, label+"_baseBranch") {
		m["base_branch"] = rapid.SampledFrom([]string{"main", "develop", "master"}).Draw(rt, label+"_bb")
	}

	if rapid.Bool().Draw(rt, label+"_branch") {
		branch := make(map[string]any)
		if rapid.Bool().Draw(rt, label+"_branchProtected") {
			branch["protected"] = []any{"main"}
		}
		if rapid.Bool().Draw(rt, label+"_branchPatterns") {
			branch["patterns"] = []any{"^feat/[a-z]+"}
		}
		if len(branch) > 0 {
			m["branch"] = branch
		}
	}

	if rapid.Bool().Draw(rt, label+"_commit") {
		commit := make(map[string]any)
		if rapid.Bool().Draw(rt, label+"_commitTypes") {
			commit["types"] = []any{"feat", "fix"}
		}
		if rapid.Bool().Draw(rt, label+"_scopeReq") {
			commit["scope_required"] = rapid.Bool().Draw(rt, label+"_scopeVal")
		}
		if rapid.Bool().Draw(rt, label+"_maxSubj") {
			commit["max_subject_length"] = rapid.IntRange(50, 120).Draw(rt, label+"_maxSubjVal")
		}
		if len(commit) > 0 {
			m["commit"] = commit
		}
	}

	if rapid.Bool().Draw(rt, label+"_ai") {
		ai := make(map[string]any)
		aiCommit := make(map[string]any)
		if rapid.Bool().Draw(rt, label+"_denyPaths") {
			aiCommit["deny_paths"] = []any{".env", "*.pem"}
		}
		if rapid.Bool().Draw(rt, label+"_trailer") {
			aiCommit["trailer"] = rapid.Bool().Draw(rt, label+"_trailerVal")
		}
		if len(aiCommit) > 0 {
			ai["commit"] = aiCommit
		}
		if len(ai) > 0 {
			m["ai"] = ai
		}
	}

	return m
}

// assertValuesPreserved는 original의 모든 키-값이 merged에 보존되었는지 재귀적으로 검증한다.
func assertValuesPreserved(rt *rapid.T, original, merged map[string]any, prefix string) {
	for key, origVal := range original {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}

		mergedVal, exists := merged[key]
		if !exists {
			rt.Fatalf("existing key %q was removed after merge", path)
		}

		origMap, origIsMap := toStringMap(origVal)
		mergedMap, mergedIsMap := toStringMap(mergedVal)

		if origIsMap && mergedIsMap {
			assertValuesPreserved(rt, origMap, mergedMap, path)
			continue
		}

		// 비-map 값 비교: YAML round-trip을 통해 정규화 후 비교
		origYAML, _ := yaml.Marshal(origVal)
		mergedYAML, _ := yaml.Marshal(mergedVal)
		if string(origYAML) != string(mergedYAML) {
			rt.Fatalf("value at %q changed: original=%s, merged=%s",
				path, strings.TrimSpace(string(origYAML)), strings.TrimSpace(string(mergedYAML)))
		}
	}
}
