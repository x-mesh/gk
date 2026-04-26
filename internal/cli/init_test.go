package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectProjectType(t *testing.T) {
	cases := []struct {
		manifest string
		want     string
	}{
		{"go.mod", "go"},
		{"package.json", "node"},
		{"pyproject.toml", "python"},
		{"Cargo.toml", "rust"},
		{"pom.xml", "java"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.manifest), []byte{}, 0o644); err != nil {
				t.Fatal(err)
			}
			if got := detectProjectType(dir); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("unknown", func(t *testing.T) {
		if got := detectProjectType(t.TempDir()); got != "unknown" {
			t.Errorf("got %q, want %q", got, "unknown")
		}
	})
}

func TestInitCmd_OnlyFlagValidation(t *testing.T) {
	tests := []struct {
		name    string
		only    string
		wantErr string
	}{
		{"valid gitignore", "gitignore", ""},
		{"valid config", "config", ""},
		{"valid ai", "ai", ""},
		{"invalid value", "invalid", `invalid --only value "invalid"`},
		{"empty is ok", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// .git 디렉토리 생성하여 git init 건너뛰기
			os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
			// go.mod 생성하여 언어 감지
			os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644)

			cmd, _, _ := rootCmd.Find([]string{"init"})
			buf := &bytes.Buffer{}
			cmd.SetOut(buf)
			cmd.SetErr(buf)

			// 플래그 설정
			_ = cmd.Flags().Set("only", tc.only)
			_ = cmd.Root().PersistentFlags().Set("dry-run", "true")
			_ = cmd.Root().PersistentFlags().Set("repo", dir)
			defer func() {
				_ = cmd.Flags().Set("only", "")
				_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
				_ = cmd.Root().PersistentFlags().Set("repo", "")
			}()

			err := runInit(cmd, nil)

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
			}
		})
	}
}

func TestInitCmd_DryRunOutputsHeaders(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644)

	cmd, _, _ := rootCmd.Find([]string{"init"})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	_ = cmd.Flags().Set("only", "")
	_ = cmd.Root().PersistentFlags().Set("dry-run", "true")
	_ = cmd.Root().PersistentFlags().Set("repo", dir)
	defer func() {
		_ = cmd.Flags().Set("only", "")
		_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
		_ = cmd.Root().PersistentFlags().Set("repo", "")
	}()

	if err := runInit(cmd, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	out := buf.String()
	// dry-run은 --- <path> --- 헤더를 출력해야 함
	if !strings.Contains(out, "--- ") {
		t.Error("dry-run output should contain file path headers")
	}
	if !strings.Contains(out, ".gitignore") {
		t.Error("dry-run output should contain .gitignore")
	}
	if !strings.Contains(out, ".gk.yaml") {
		t.Error("dry-run output should contain .gk.yaml")
	}
}

func TestInitCmd_OnlyGitignore(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644)

	cmd, _, _ := rootCmd.Find([]string{"init"})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	_ = cmd.Flags().Set("only", "gitignore")
	_ = cmd.Flags().Set("force", "false")
	_ = cmd.Root().PersistentFlags().Set("dry-run", "true")
	_ = cmd.Root().PersistentFlags().Set("repo", dir)
	defer func() {
		_ = cmd.Flags().Set("only", "")
		_ = cmd.Flags().Set("force", "false")
		_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
		_ = cmd.Root().PersistentFlags().Set("repo", "")
	}()

	if err := runInit(cmd, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, ".gitignore") {
		t.Error("--only gitignore should output .gitignore")
	}
	if strings.Contains(out, ".gk.yaml") {
		t.Error("--only gitignore should NOT output .gk.yaml")
	}
	if strings.Contains(out, "CLAUDE.md") {
		t.Error("--only gitignore should NOT output CLAUDE.md")
	}
}

func TestInitCmd_OnlyConfig(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644)

	cmd, _, _ := rootCmd.Find([]string{"init"})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	_ = cmd.Flags().Set("only", "config")
	_ = cmd.Root().PersistentFlags().Set("dry-run", "true")
	_ = cmd.Root().PersistentFlags().Set("repo", dir)
	defer func() {
		_ = cmd.Flags().Set("only", "")
		_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
		_ = cmd.Root().PersistentFlags().Set("repo", "")
	}()

	if err := runInit(cmd, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, ".gk.yaml") {
		t.Error("--only config should output .gk.yaml")
	}
	if strings.Contains(out, ".gitignore") {
		t.Error("--only config should NOT output .gitignore")
	}
}

func TestInitCmd_OnlyAI(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644)

	cmd, _, _ := rootCmd.Find([]string{"init"})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	// --only ai --kiro → kiro steering 파일만 생성
	_ = cmd.Flags().Set("only", "ai")
	_ = cmd.Flags().Set("kiro", "true")
	_ = cmd.Root().PersistentFlags().Set("dry-run", "true")
	_ = cmd.Root().PersistentFlags().Set("repo", dir)
	defer func() {
		_ = cmd.Flags().Set("only", "")
		_ = cmd.Flags().Set("kiro", "false")
		_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
		_ = cmd.Root().PersistentFlags().Set("repo", "")
	}()

	if err := runInit(cmd, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "product.md") {
		t.Error("--only ai --kiro should output kiro steering files")
	}
	if strings.Contains(out, ".gk.yaml") {
		t.Error("--only ai should NOT output .gk.yaml")
	}
}

func TestInitCmd_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644)

	// 기존 파일 생성
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("old-content\n"), 0o644)

	cmd, _, _ := rootCmd.Find([]string{"init"})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	_ = cmd.Flags().Set("only", "gitignore")
	_ = cmd.Flags().Set("force", "true")
	_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
	_ = cmd.Root().PersistentFlags().Set("repo", dir)
	defer func() {
		_ = cmd.Flags().Set("only", "")
		_ = cmd.Flags().Set("force", "false")
		_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
		_ = cmd.Root().PersistentFlags().Set("repo", "")
	}()

	if err := runInit(cmd, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if strings.Contains(string(data), "old-content") {
		t.Error("--force should overwrite existing .gitignore")
	}
	if !strings.Contains(string(data), "# Security") {
		t.Error("overwritten .gitignore should contain generated content")
	}
}

func TestInitCmd_AutoGitInit(t *testing.T) {
	dir := t.TempDir()
	// .git 없음 — git init이 자동 실행되어야 함

	cmd, _, _ := rootCmd.Find([]string{"init"})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	_ = cmd.Flags().Set("only", "gitignore")
	_ = cmd.Flags().Set("force", "false")
	_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
	_ = cmd.Root().PersistentFlags().Set("repo", dir)
	defer func() {
		_ = cmd.Flags().Set("only", "")
		_ = cmd.Flags().Set("force", "false")
		_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
		_ = cmd.Root().PersistentFlags().Set("repo", "")
	}()

	if err := runInit(cmd, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	// .git 디렉토리가 생성되었는지 확인
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		t.Error(".git directory should be created by auto git init")
	}

	out := buf.String()
	if !strings.Contains(out, "initialized git repository") {
		t.Error("should print 'initialized git repository' message")
	}
}

func TestInitCmd_DeprecatedAIAlias(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644)

	cmd, _, _ := rootCmd.Find([]string{"init", "ai"})
	buf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(errBuf)

	// 부모 initCmd에 필요한 플래그 설정
	parent := cmd.Parent()
	_ = parent.Flags().Set("only", "")
	_ = parent.Flags().Set("force", "false")
	_ = parent.Flags().Set("kiro", "false")
	_ = parent.Root().PersistentFlags().Set("dry-run", "true")
	_ = parent.Root().PersistentFlags().Set("repo", dir)
	defer func() {
		_ = parent.Flags().Set("only", "")
		_ = parent.Root().PersistentFlags().Set("dry-run", "false")
		_ = parent.Root().PersistentFlags().Set("repo", "")
	}()

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("deprecated ai: %v", err)
	}

	// stderr에 deprecated 경고가 출력되어야 함
	if !strings.Contains(errBuf.String(), "deprecated") {
		t.Errorf("stderr should contain deprecated warning, got: %q", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), `gk init --only ai`) {
		t.Errorf("stderr should mention replacement command, got: %q", errBuf.String())
	}
}

func TestInitCmd_DeprecatedConfigAlias(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cfg.yaml")

	cmd, _, _ := rootCmd.Find([]string{"init", "config"})
	buf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(errBuf)

	_ = cmd.Flags().Set("out", target)
	_ = cmd.Flags().Set("force", "false")

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("deprecated config: %v", err)
	}

	// stderr에 deprecated 경고가 출력되어야 함
	if !strings.Contains(errBuf.String(), "deprecated") {
		t.Errorf("stderr should contain deprecated warning, got: %q", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), `gk config init`) {
		t.Errorf("stderr should mention replacement command, got: %q", errBuf.String())
	}

	// 파일이 생성되었는지 확인
	if !strings.Contains(buf.String(), "created:") {
		t.Errorf("stdout should contain 'created:', got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// Integration tests: 전체 gk init end-to-end (Task 14.3)
// ---------------------------------------------------------------------------

func TestInitIntegration_FullInitEmptyDir(t *testing.T) {
	// temp dir에 go.mod만 생성 (.git 없음)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644)

	cmd, _, _ := rootCmd.Find([]string{"init"})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	_ = cmd.Flags().Set("only", "")
	_ = cmd.Flags().Set("force", "false")
	_ = cmd.Flags().Set("kiro", "false")
	_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
	_ = cmd.Root().PersistentFlags().Set("repo", dir)
	defer func() {
		_ = cmd.Flags().Set("only", "")
		_ = cmd.Flags().Set("force", "false")
		_ = cmd.Flags().Set("kiro", "false")
		_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
		_ = cmd.Root().PersistentFlags().Set("repo", "")
	}()

	if err := runInit(cmd, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	out := buf.String()

	// .git 디렉토리가 자동 생성되었는지 확인 (Req 18.1)
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		t.Error(".git directory should be created by auto git init")
	}
	if !strings.Contains(out, "initialized git repository") {
		t.Error("should print 'initialized git repository' message")
	}

	// .gitignore 생성 확인
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); os.IsNotExist(err) {
		t.Error(".gitignore should be created")
	}

	// .gk.yaml 생성 확인
	if _, err := os.Stat(filepath.Join(dir, ".gk.yaml")); os.IsNotExist(err) {
		t.Error(".gk.yaml should be created")
	}

	// .gitignore 내용에 Go 패턴 포함 확인
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains(string(data), "# Language: Go") {
		t.Error(".gitignore should contain Go language section")
	}
	if !strings.Contains(string(data), "# Security") {
		t.Error(".gitignore should contain Security section")
	}
}

func TestInitIntegration_DryRunNoFiles(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644)

	cmd, _, _ := rootCmd.Find([]string{"init"})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	_ = cmd.Flags().Set("only", "")
	_ = cmd.Flags().Set("force", "false")
	_ = cmd.Flags().Set("kiro", "false")
	_ = cmd.Root().PersistentFlags().Set("dry-run", "true")
	_ = cmd.Root().PersistentFlags().Set("repo", dir)
	defer func() {
		_ = cmd.Flags().Set("only", "")
		_ = cmd.Flags().Set("force", "false")
		_ = cmd.Flags().Set("kiro", "false")
		_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
		_ = cmd.Root().PersistentFlags().Set("repo", "")
	}()

	if err := runInit(cmd, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	out := buf.String()

	// dry-run 출력에 모든 파일 헤더가 포함되어야 함
	if !strings.Contains(out, ".gitignore") {
		t.Error("dry-run output should contain .gitignore header")
	}
	if !strings.Contains(out, ".gk.yaml") {
		t.Error("dry-run output should contain .gk.yaml header")
	}

	// 실제 파일이 생성되지 않았는지 확인 (Req 13.1)
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); !os.IsNotExist(err) {
		t.Error(".gitignore should NOT be created in dry-run mode")
	}
	if _, err := os.Stat(filepath.Join(dir, ".gk.yaml")); !os.IsNotExist(err) {
		t.Error(".gk.yaml should NOT be created in dry-run mode")
	}
}

func TestInitIntegration_OnlyGitignoreForce(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644)

	// 기존 .gitignore 생성
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("old-pattern\n"), 0o644)

	cmd, _, _ := rootCmd.Find([]string{"init"})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	_ = cmd.Flags().Set("only", "gitignore")
	_ = cmd.Flags().Set("force", "true")
	_ = cmd.Flags().Set("kiro", "false")
	_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
	_ = cmd.Root().PersistentFlags().Set("repo", dir)
	defer func() {
		_ = cmd.Flags().Set("only", "")
		_ = cmd.Flags().Set("force", "false")
		_ = cmd.Flags().Set("kiro", "false")
		_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
		_ = cmd.Root().PersistentFlags().Set("repo", "")
	}()

	if err := runInit(cmd, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	// .gitignore가 덮어쓰기 되었는지 확인 (--force)
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	content := string(data)
	if strings.Contains(content, "old-pattern") {
		t.Error("--force should overwrite existing .gitignore, but old content remains")
	}
	if !strings.Contains(content, "# Security") {
		t.Error("overwritten .gitignore should contain generated content")
	}

	// --only gitignore이므로 다른 파일은 생성되지 않아야 함
	if _, err := os.Stat(filepath.Join(dir, ".gk.yaml")); !os.IsNotExist(err) {
		t.Error(".gk.yaml should NOT be created with --only gitignore")
	}
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Error("CLAUDE.md should NOT be created with --only gitignore")
	}
}

func TestInitIntegration_MergeExistingGitignore(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644)

	// 기존 .gitignore에 일부 패턴만 포함
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("# My patterns\nvendor/\nmy-custom-rule\n"), 0o644)

	cmd, _, _ := rootCmd.Find([]string{"init"})
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	_ = cmd.Flags().Set("only", "")
	_ = cmd.Flags().Set("force", "false")
	_ = cmd.Flags().Set("kiro", "false")
	_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
	_ = cmd.Root().PersistentFlags().Set("repo", dir)
	defer func() {
		_ = cmd.Flags().Set("only", "")
		_ = cmd.Flags().Set("force", "false")
		_ = cmd.Flags().Set("kiro", "false")
		_ = cmd.Root().PersistentFlags().Set("dry-run", "false")
		_ = cmd.Root().PersistentFlags().Set("repo", "")
	}()

	if err := runInit(cmd, nil); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	// .gitignore에 기존 패턴이 보존되어야 함 (Req 7.4)
	data, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	content := string(data)
	if !strings.Contains(content, "vendor/") {
		t.Error("merged .gitignore should preserve existing 'vendor/' pattern")
	}
	if !strings.Contains(content, "my-custom-rule") {
		t.Error("merged .gitignore should preserve existing 'my-custom-rule' pattern")
	}

	// 새 패턴도 추가되어야 함
	if !strings.Contains(content, "# Security") {
		t.Error("merged .gitignore should contain new Security section")
	}
	if !strings.Contains(content, ".env") {
		t.Error("merged .gitignore should contain .env pattern")
	}

	// .gk.yaml도 생성되어야 함 (--only 없으므로 전체 생성)
	if _, err := os.Stat(filepath.Join(dir, ".gk.yaml")); os.IsNotExist(err) {
		t.Error(".gk.yaml should be created")
	}
}
