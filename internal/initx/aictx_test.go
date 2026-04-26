package initx

import (
	"testing"

	"pgregory.net/rapid"
)

// --- Unit Tests ---

func TestGenerateAIContext_DefaultNoFiles(t *testing.T) {
	result := &AnalysisResult{}
	files := GenerateAIContext(result, AIContextOptions{})

	// IncludeKiro=false → 파일 없음 (CLAUDE.md/AGENTS.md는 AI가 자동 생성하므로 제외)
	if len(files) != 0 {
		t.Fatalf("expected 0 files without IncludeKiro, got %d", len(files))
	}
}

func TestGenerateAIContext_WithKiro(t *testing.T) {
	result := &AnalysisResult{}
	files := GenerateAIContext(result, AIContextOptions{IncludeKiro: true})

	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d", len(files))
	}

	wantPaths := []string{
		".kiro/steering/product.md",
		".kiro/steering/tech.md",
		".kiro/steering/structure.md",
	}
	for i, want := range wantPaths {
		if files[i].Path != want {
			t.Errorf("files[%d].Path = %q, want %q", i, files[i].Path, want)
		}
		if files[i].Content == "" {
			t.Errorf("file %q has empty content", files[i].Path)
		}
	}
}

func TestGenerateAIContext_IgnoresAnalysisResult(t *testing.T) {
	r1 := &AnalysisResult{}
	r2 := &AnalysisResult{
		Languages:    []Language{{Name: "go", MarkerFile: "go.mod"}},
		BuildSystems: []BuildSystem{{Name: "make"}},
		BaseBranch:   "develop",
	}

	f1 := GenerateAIContext(r1, AIContextOptions{IncludeKiro: true})
	f2 := GenerateAIContext(r2, AIContextOptions{IncludeKiro: true})

	if len(f1) != len(f2) {
		t.Fatalf("file count differs: %d vs %d", len(f1), len(f2))
	}
	for i := range f1 {
		if f1[i].Path != f2[i].Path || f1[i].Content != f2[i].Content {
			t.Errorf("files[%d] differ for different AnalysisResult", i)
		}
	}
}

// --- Property Tests ---

// genAnalysisResult는 임의의 AnalysisResult를 생성하는 rapid generator이다.
func genAnalysisResult(rt *rapid.T) *AnalysisResult {
	numLangs := rapid.IntRange(0, 7).Draw(rt, "numLangs")
	var langs []Language
	if numLangs > 0 {
		langNames := rapid.SliceOfNDistinct(
			rapid.SampledFrom(allLangKeys),
			numLangs, numLangs,
			rapid.ID[string],
		).Draw(rt, "langs")
		for _, name := range langNames {
			langs = append(langs, Language{Name: name})
		}
	}

	numBuild := rapid.IntRange(0, 3).Draw(rt, "numBuild")
	var builds []BuildSystem
	if numBuild > 0 {
		buildNames := rapid.SliceOfNDistinct(
			rapid.SampledFrom([]string{"make", "docker", "docker-compose", "taskfile", "just"}),
			numBuild, numBuild,
			rapid.ID[string],
		).Draw(rt, "builds")
		for _, name := range buildNames {
			builds = append(builds, BuildSystem{Name: name})
		}
	}

	return &AnalysisResult{
		Languages:    langs,
		BuildSystems: builds,
		BaseBranch:   rapid.SampledFrom([]string{"main", "master", "develop"}).Draw(rt, "baseBranch"),
	}
}

// Feature: gk-init, Property 7: AI context 생성 완전성
// Validates: Requirements 10.1
func TestProperty7_AIContextGenerationCompleteness(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		result := genAnalysisResult(rt)
		includeKiro := rapid.Bool().Draw(rt, "includeKiro")
		opts := AIContextOptions{IncludeKiro: includeKiro}

		files := GenerateAIContext(result, opts)

		// IncludeKiro=true → 3개 kiro steering 파일
		if includeKiro {
			if len(files) != 3 {
				rt.Fatalf("with IncludeKiro=true, expected 3 files, got %d", len(files))
			}
			pathSet := make(map[string]bool)
			for _, f := range files {
				pathSet[f.Path] = true
			}
			for _, p := range []string{
				".kiro/steering/product.md",
				".kiro/steering/tech.md",
				".kiro/steering/structure.md",
			} {
				if !pathSet[p] {
					rt.Fatalf("kiro file %q missing when IncludeKiro=true", p)
				}
			}
		}

		// IncludeKiro=false → 0개 파일
		if !includeKiro && len(files) != 0 {
			rt.Fatalf("with IncludeKiro=false, expected 0 files, got %d", len(files))
		}

		// 모든 파일의 Path와 Content가 비어있지 않음
		for _, f := range files {
			if f.Path == "" {
				rt.Fatal("file has empty Path")
			}
			if f.Content == "" {
				rt.Fatalf("file %q has empty Content", f.Path)
			}
		}
	})
}
