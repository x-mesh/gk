package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestDetectWorktreeInit_EcosystemPrecedence(t *testing.T) {
	cases := []struct {
		name    string
		files   []string
		wantRun []string
	}{
		{"npm lockfile beats package.json", []string{"package.json", "package-lock.json"}, []string{"npm ci"}},
		{"pnpm beats npm", []string{"package.json", "package-lock.json", "pnpm-lock.yaml"}, []string{"pnpm install --frozen-lockfile"}},
		{"bare package.json", []string{"package.json"}, []string{"npm install"}},
		{"uv.lock suppresses requirements/pyproject", []string{"uv.lock", "requirements.txt", "pyproject.toml"}, []string{"uv sync"}},
		{"requirements without lock", []string{"requirements.txt"}, []string{"uv venv && uv pip install -r requirements.txt"}},
		{"go module", []string{"go.mod"}, []string{"go mod download"}},
		{"mixed node + python", []string{"package-lock.json", "uv.lock"}, []string{"npm ci", "uv sync"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got := detectWorktreeInit(dir)
			if !equalStrings(got.Run, tc.wantRun) {
				t.Errorf("Run: want %v, got %v", tc.wantRun, got.Run)
			}
		})
	}
}

func TestDetectWorktreeInit_NestedMonorepo(t *testing.T) {
	dir := t.TempDir()
	// No manifest at the root — only nested projects (the lib-mesh shape:
	// mesh-explorer-web/{frontend,backend}).
	mustWrite(t, filepath.Join(dir, "app", "frontend", "pnpm-lock.yaml"), "{}")
	mustWrite(t, filepath.Join(dir, "app", "frontend", "package.json"), "{}")
	mustWrite(t, filepath.Join(dir, "app", "backend", "uv.lock"), "x")
	mustWrite(t, filepath.Join(dir, "app", "backend", "pyproject.toml"), "x")
	// Noise that must be skipped, not descended into.
	mustWrite(t, filepath.Join(dir, "app", "frontend", "node_modules", "dep", "package.json"), "{}")

	got := detectWorktreeInit(dir)
	want := []string{
		"cd app/backend && uv sync",
		"cd app/frontend && pnpm install --frozen-lockfile",
	}
	if !equalStrings(got.Run, want) {
		t.Errorf("Run: want %v, got %v", want, got.Run)
	}
}

func TestDetectWorktreeInit_RootManifestSuppressesNestedScan(t *testing.T) {
	dir := t.TempDir()
	// A root manifest is authoritative (workspace assumption): the nested
	// package must NOT also be proposed.
	mustWrite(t, filepath.Join(dir, "package-lock.json"), "{}")
	mustWrite(t, filepath.Join(dir, "package.json"), "{}")
	mustWrite(t, filepath.Join(dir, "packages", "a", "package.json"), "{}")

	got := detectWorktreeInit(dir)
	if !equalStrings(got.Run, []string{"npm ci"}) {
		t.Errorf("Run: want [npm ci], got %v", got.Run)
	}
}

func TestDetectWorktreeInit_NestedRespectsMaxDepth(t *testing.T) {
	dir := t.TempDir()
	// 5 levels deep — beyond nestedScanMaxDepth (3), must be ignored.
	mustWrite(t, filepath.Join(dir, "a", "b", "c", "d", "go.mod"), "module x")
	got := detectWorktreeInit(dir)
	if len(got.Run) != 0 {
		t.Errorf("Run: want empty (too deep), got %v", got.Run)
	}
}

func TestDetectWorktreeInit_EnvLink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := detectWorktreeInit(dir)
	if !equalStrings(got.Link, []string{".env"}) {
		t.Errorf("Link: want [.env], got %v", got.Link)
	}
}

func TestValidateWorktreeInit_FlagsVenvAndNodeModules(t *testing.T) {
	init := &config.WorktreeInit{
		Link: []string{".env", ".venv"},
		Copy: []string{"node_modules"},
	}
	warns := validateWorktreeInit(init)
	if len(warns) != 2 {
		t.Fatalf("want 2 warnings, got %d: %v", len(warns), warns)
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, ".venv") || !strings.Contains(joined, "node_modules") {
		t.Errorf("warnings should mention .venv and node_modules: %v", warns)
	}
	// .env must NOT warn.
	if strings.Contains(joined, `"`+".env"+`"`) {
		t.Errorf(".env should not be flagged: %v", warns)
	}
}

func TestRenderWorktreeInitYAML_QuotesCommands(t *testing.T) {
	init := &config.WorktreeInit{
		Link: []string{".env"},
		Run:  []string{"npm ci", "uv venv && uv pip install -r requirements.txt"},
	}
	out := renderWorktreeInitYAML(init)
	// The install command contains no ':' so stays bare; add one that does.
	if !strings.Contains(out, "worktree:") || !strings.Contains(out, "init:") {
		t.Errorf("missing header:\n%s", out)
	}
	if !strings.Contains(out, "- .env") {
		t.Errorf("missing link entry:\n%s", out)
	}
	if !strings.Contains(out, "- npm ci") {
		t.Errorf("missing run entry:\n%s", out)
	}
}

func TestYamlScalar(t *testing.T) {
	cases := map[string]string{
		"npm ci":          "npm ci",
		"a: b":            `"a: b"`,
		"echo #c":         `"echo #c"`,
		"plain-command":   "plain-command",
		"uv venv && uv s": "uv venv && uv s", // & not in significant set, stays bare
	}
	for in, want := range cases {
		if got := yamlScalar(in); got != want {
			t.Errorf("yamlScalar(%q): want %q, got %q", in, want, got)
		}
	}
}

func TestApplyWorktreeInit_LinkCopyIdempotent(t *testing.T) {
	main := t.TempDir()
	target := t.TempDir()
	// Source files in the main worktree.
	if err := os.WriteFile(filepath.Join(main, ".env"), []byte("SECRET=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "config.local.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	init := &config.WorktreeInit{
		Link: []string{".env"},
		Copy: []string{"config.local.json"},
	}

	var buf bytes.Buffer
	if err := applyWorktreeInit(context.Background(), &buf, init, main, nil, target, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// .env is a symlink pointing at main's .env.
	linkPath := filepath.Join(target, ".env")
	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if got != filepath.Join(main, ".env") {
		t.Errorf("symlink target: want %q, got %q", filepath.Join(main, ".env"), got)
	}
	// config.local.json is a real copy.
	copyPath := filepath.Join(target, "config.local.json")
	if fi, lerr := os.Lstat(copyPath); lerr != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("copy should be a regular file, got %v err=%v", fi.Mode(), lerr)
	}

	// Second run is idempotent: no error, link/copy reported as already ok.
	buf.Reset()
	if err := applyWorktreeInit(context.Background(), &buf, init, main, nil, target, false); err != nil {
		t.Fatalf("apply (2nd): %v", err)
	}
	if !strings.Contains(buf.String(), "already linked") {
		t.Errorf("2nd run should report already linked:\n%s", buf.String())
	}
}

func TestApplyWorktreeInit_RunFailureAborts(t *testing.T) {
	main := t.TempDir()
	target := t.TempDir()
	init := &config.WorktreeInit{
		Run: []string{"true", "exit 3", "echo should-not-run > marker"},
	}
	var buf bytes.Buffer
	err := applyWorktreeInit(context.Background(), &buf, init, main, nil, target, false)
	if err == nil {
		t.Fatal("want error from failing run step")
	}
	if fileExists(filepath.Join(target, "marker")) {
		t.Error("steps after a failure must not run")
	}
}

func TestApplyWorktreeInit_DryRunNoSideEffects(t *testing.T) {
	main := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(main, ".env"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	init := &config.WorktreeInit{
		Link: []string{".env"},
		Run:  []string{"echo hi > created"},
	}
	var buf bytes.Buffer
	if err := applyWorktreeInit(context.Background(), &buf, init, main, nil, target, true); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if fileExists(filepath.Join(target, ".env")) {
		t.Error("dry-run must not create the symlink")
	}
	if fileExists(filepath.Join(target, "created")) {
		t.Error("dry-run must not execute run commands")
	}
	if !strings.Contains(buf.String(), "dry-run") {
		t.Errorf("output should mark dry-run:\n%s", buf.String())
	}
}

func TestApplyWorktreeInit_SkipsLinkWhenTargetIsMain(t *testing.T) {
	main := t.TempDir()
	if err := os.WriteFile(filepath.Join(main, ".env"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	init := &config.WorktreeInit{Link: []string{".env"}}
	var buf bytes.Buffer
	if err := applyWorktreeInit(context.Background(), &buf, init, main, nil, main, false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(buf.String(), "target is the main worktree") {
		t.Errorf("should skip link/copy when target==main:\n%s", buf.String())
	}
}

func TestHasTopLevelWorktreeKey(t *testing.T) {
	if !hasTopLevelWorktreeKey("remote: origin\nworktree:\n  base: x\n") {
		t.Error("should detect top-level worktree key")
	}
	if hasTopLevelWorktreeKey("ai:\n  worktree: nope\n") {
		t.Error("indented worktree must not count as top-level")
	}
}

func TestSaveWorktreeInitBlock(t *testing.T) {
	main := t.TempDir()
	// No existing .gk.yaml → file is created with the block.
	init := &config.WorktreeInit{Run: []string{"npm ci"}}
	var buf bytes.Buffer
	saveWorktreeInitBlock(&buf, main, init)
	data, err := os.ReadFile(filepath.Join(main, ".gk.yaml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "worktree:") || !strings.Contains(string(data), "npm ci") {
		t.Errorf("written file missing block:\n%s", data)
	}

	// Existing worktree: block → refuse to append, advise manual merge.
	main2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(main2, ".gk.yaml"), []byte("worktree:\n  base: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	saveWorktreeInitBlock(&buf, main2, init)
	if !strings.Contains(buf.String(), "by hand") {
		t.Errorf("should advise manual merge when worktree: exists:\n%s", buf.String())
	}
}

// TestWorktreeInit_DryRunSaveSkipsWrite is the P2 regression: `--save
// --dry-run` must preview only, never touch .gk.yaml.
func TestWorktreeInit_DryRunSaveSkipsWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "none"))
	repo := testutil.NewRepo(t)
	repo.WriteFile("package-lock.json", "{}")
	repo.WriteFile(".gitignore", "node_modules\n")
	repo.Commit("init")

	root, buf := buildWorktreeCmd(repo.Dir, "init", repo.Dir, "--save", "--dry-run")
	if err := root.Execute(); err != nil {
		t.Fatalf("init: %v\n%s", err, buf.String())
	}
	if fileExists(filepath.Join(repo.Dir, ".gk.yaml")) {
		t.Errorf(".gk.yaml must not be written under --dry-run:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "skipped (dry-run)") {
		t.Errorf("expected a dry-run skip message:\n%s", buf.String())
	}
}

// TestWorktreeInit_AnchorsToTargetRepo is the P2 regression: a path
// argument pointing at a DIFFERENT repo must use that repo's policy, not
// the caller's cwd/--repo policy.
func TestWorktreeInit_AnchorsToTargetRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "none"))

	// Caller's repo (A) carries a policy that must NOT leak into the target.
	repoA := testutil.NewRepo(t)
	repoA.WriteFile(".gitignore", ".env\n")
	repoA.WriteFile(".gk.yaml", "worktree:\n  init:\n    run:\n      - touch RAN_A\n")
	repoA.Commit("a")

	// Target's repo (B) carries its own policy, committed so the linked
	// worktree checks it out.
	repoB := testutil.NewRepo(t)
	repoB.WriteFile(".gitignore", "node_modules\n")
	repoB.WriteFile(".gk.yaml", "worktree:\n  init:\n    run:\n      - touch RAN_B\n")
	repoB.Commit("b")
	wtB := filepath.Join(t.TempDir(), "wtB")
	repoB.RunGit("worktree", "add", wtB, "-b", "feat")

	// Caller is anchored at repo A, but the target is repo B's worktree.
	root, buf := buildWorktreeCmd(repoA.Dir, "init", wtB)
	if err := root.Execute(); err != nil {
		t.Fatalf("init: %v\n%s", err, buf.String())
	}
	if !fileExists(filepath.Join(wtB, "RAN_B")) {
		t.Errorf("target repo (B) policy should have run:\n%s", buf.String())
	}
	if fileExists(filepath.Join(wtB, "RAN_A")) {
		t.Errorf("caller repo (A) policy leaked into the target:\n%s", buf.String())
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
