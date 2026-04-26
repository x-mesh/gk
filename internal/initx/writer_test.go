package initx

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// --- Unit tests for ExecutePlan ---

func TestExecutePlan_DryRun_PrintsHeadersAndContent(t *testing.T) {
	plan := &InitPlan{
		Gitignore: &FilePlan{Path: ".gitignore", Content: "node_modules/\n", Action: ActionCreate},
		Config:    &FilePlan{Path: ".gk.yaml", Content: "base_branch: main\n", Action: ActionCreate},
		AIFiles: []FilePlan{
			{Path: "CLAUDE.md", Content: "# Claude\n", Action: ActionCreate},
		},
	}

	var buf bytes.Buffer
	if err := ExecutePlan(plan, &buf, true); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	for _, want := range []string{
		"--- .gitignore ---",
		"node_modules/",
		"--- .gk.yaml ---",
		"base_branch: main",
		"--- CLAUDE.md ---",
		"# Claude",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q", want)
		}
	}
}

func TestExecutePlan_Skip_PrintsSkipped(t *testing.T) {
	plan := &InitPlan{
		Gitignore: &FilePlan{Path: ".gitignore", Content: "x", Action: ActionSkip},
	}

	var buf bytes.Buffer
	if err := ExecutePlan(plan, &buf, false); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(buf.String(), "skipped: .gitignore") {
		t.Errorf("expected 'skipped: .gitignore', got %q", buf.String())
	}
}

func TestExecutePlan_Write_CreatesFiles(t *testing.T) {
	dir := t.TempDir()

	plan := &InitPlan{
		Gitignore: &FilePlan{
			Path:    filepath.Join(dir, ".gitignore"),
			Content: "bin/\n",
			Action:  ActionCreate,
		},
		Config: &FilePlan{
			Path:    filepath.Join(dir, "sub", ".gk.yaml"),
			Content: "base_branch: main\n",
			Action:  ActionMerge,
		},
	}

	var buf bytes.Buffer
	if err := ExecutePlan(plan, &buf, false); err != nil {
		t.Fatal(err)
	}

	// 파일 생성 확인
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "bin/\n" {
		t.Errorf("unexpected .gitignore content: %q", data)
	}

	data, err = os.ReadFile(filepath.Join(dir, "sub", ".gk.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "base_branch: main\n" {
		t.Errorf("unexpected .gk.yaml content: %q", data)
	}

	// 출력 메시지 확인
	out := buf.String()
	if !strings.Contains(out, "created:") {
		t.Errorf("expected 'created:' in output, got %q", out)
	}
	if !strings.Contains(out, "updated:") {
		t.Errorf("expected 'updated:' for merge action, got %q", out)
	}
}

func TestExecutePlan_NilFields_Skipped(t *testing.T) {
	plan := &InitPlan{
		Gitignore: nil,
		Config:    nil,
		AIFiles:   nil,
	}

	var buf bytes.Buffer
	if err := ExecutePlan(plan, &buf, false); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output for empty plan, got %q", buf.String())
	}
}

func TestExecutePlan_WriteError_ReturnsWrappedError(t *testing.T) {
	// 존재하지 않는 읽기 전용 경로에 쓰기 시도
	plan := &InitPlan{
		Gitignore: &FilePlan{
			Path:    "/dev/null/impossible/path/.gitignore",
			Content: "x",
			Action:  ActionCreate,
		},
	}

	var buf bytes.Buffer
	err := ExecutePlan(plan, &buf, false)
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
	if !strings.Contains(err.Error(), "gk init: write") {
		t.Errorf("error should contain 'gk init: write', got %q", err.Error())
	}
}


// --- Property-Based Tests ---

// genFilePlan은 임의의 FilePlan을 생성하는 rapid generator이다.
func genFilePlan(rt *rapid.T, label string) FilePlan {
	// 안전한 상대 경로 생성 (디렉토리 구분자 포함 가능)
	segments := rapid.IntRange(1, 3).Draw(rt, label+"-depth")
	parts := make([]string, segments)
	for i := 0; i < segments; i++ {
		parts[i] = rapid.StringMatching(`[a-z][a-z0-9_-]{1,8}`).Draw(rt, fmt.Sprintf("%s-seg%d", label, i))
	}
	filename := rapid.StringMatching(`[a-z][a-z0-9_.-]{1,10}`).Draw(rt, label+"-file")
	parts = append(parts, filename)
	path := filepath.Join(parts...)

	content := rapid.StringMatching(`[a-zA-Z0-9 \n]{0,200}`).Draw(rt, label+"-content")
	action := rapid.SampledFrom([]FileAction{ActionCreate, ActionMerge, ActionOverwrite, ActionSkip}).Draw(rt, label+"-action")

	return FilePlan{Path: path, Content: content, Action: action}
}

// genInitPlan은 임의의 InitPlan을 생성하는 rapid generator이다.
func genInitPlan(rt *rapid.T) *InitPlan {
	plan := &InitPlan{}

	if rapid.Bool().Draw(rt, "hasGitignore") {
		fp := genFilePlan(rt, "gitignore")
		plan.Gitignore = &fp
	}
	if rapid.Bool().Draw(rt, "hasConfig") {
		fp := genFilePlan(rt, "config")
		plan.Config = &fp
	}

	aiCount := rapid.IntRange(0, 5).Draw(rt, "aiCount")
	for i := 0; i < aiCount; i++ {
		plan.AIFiles = append(plan.AIFiles, genFilePlan(rt, fmt.Sprintf("ai-%d", i)))
	}

	return plan
}

// Feature: gk-init, Property 8: Dry-run 무부작용
// Validates: Requirements 13.1, 13.2, 13.3
func TestProperty8_DryRunNoSideEffects(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		plan := genInitPlan(rt)

		// temp dir 생성 — dry-run 후 이 디렉토리에 파일이 없어야 함
		dir := t.TempDir()

		// plan의 모든 경로를 temp dir 기준으로 재설정
		rebasePlan(plan, dir)

		// dry-run 전 디렉토리 상태 스냅샷
		beforeEntries := listAllFiles(dir)

		var buf bytes.Buffer
		err := ExecutePlan(plan, &buf, true)
		if err != nil {
			rt.Fatalf("ExecutePlan(dryRun=true) returned error: %v", err)
		}

		// 검증 1: 파일 시스템에 변경이 없어야 함
		afterEntries := listAllFiles(dir)
		if len(beforeEntries) != len(afterEntries) {
			rt.Fatalf("file count changed: before=%d, after=%d", len(beforeEntries), len(afterEntries))
		}

		// 검증 2: 출력에 non-skip 파일의 헤더와 내용이 포함되어야 함
		output := buf.String()
		for _, fp := range collectPlans(plan) {
			if fp.Action == ActionSkip {
				continue
			}
			header := fmt.Sprintf("--- %s ---", fp.Path)
			if !strings.Contains(output, header) {
				rt.Fatalf("dry-run output missing header %q", header)
			}
			if fp.Content != "" && !strings.Contains(output, fp.Content) {
				rt.Fatalf("dry-run output missing content for %s", fp.Path)
			}
		}
	})
}

// rebasePlan은 plan의 모든 경로를 baseDir 기준으로 재설정한다.
func rebasePlan(plan *InitPlan, baseDir string) {
	if plan.Gitignore != nil {
		plan.Gitignore.Path = filepath.Join(baseDir, plan.Gitignore.Path)
	}
	if plan.Config != nil {
		plan.Config.Path = filepath.Join(baseDir, plan.Config.Path)
	}
	for i := range plan.AIFiles {
		plan.AIFiles[i].Path = filepath.Join(baseDir, plan.AIFiles[i].Path)
	}
}

// listAllFiles는 디렉토리 내 모든 파일을 재귀적으로 나열한다.
func listAllFiles(dir string) []string {
	var files []string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	return files
}
