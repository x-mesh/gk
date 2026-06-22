package config_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/x-mesh/gk/internal/config"
)

func TestDefaults(t *testing.T) {
	d := config.Defaults()
	if d.Remote != "origin" {
		t.Errorf("Remote: want %q, got %q", "origin", d.Remote)
	}
	if d.Log.Limit != 20 {
		t.Errorf("Log.Limit: want 20, got %d", d.Log.Limit)
	}
	if d.Log.Graph != false {
		t.Error("Log.Graph: want false")
	}
	if d.UI.Color != "auto" {
		t.Errorf("UI.Color: want %q, got %q", "auto", d.UI.Color)
	}
	if d.Branch.StaleDays != 30 {
		t.Errorf("Branch.StaleDays: want 30, got %d", d.Branch.StaleDays)
	}
	if len(d.Branch.Protected) == 0 {
		t.Error("Branch.Protected: want non-empty slice")
	}
	wantVis := []string{"gauge", "bar", "progress", "base", "tree", "staleness", "local", "since-push"}
	if len(d.Status.Vis) != len(wantVis) {
		t.Fatalf("Status.Vis len: want %d, got %d (%v)", len(wantVis), len(d.Status.Vis), d.Status.Vis)
	}
	for i, v := range wantVis {
		if d.Status.Vis[i] != v {
			t.Errorf("Status.Vis[%d]: want %q, got %q", i, v, d.Status.Vis[i])
		}
	}
	if d.Status.AutoFetch {
		t.Error("Status.AutoFetch: want false by default (fetch is opt-in via --fetch/-f)")
	}
	wantLogVis := []string{"cc", "safety", "tags-rule"}
	if len(d.Log.Vis) != len(wantLogVis) {
		t.Fatalf("Log.Vis len: want %d, got %d (%v)", len(wantLogVis), len(d.Log.Vis), d.Log.Vis)
	}
	for i, v := range wantLogVis {
		if d.Log.Vis[i] != v {
			t.Errorf("Log.Vis[%d]: want %q, got %q", i, v, d.Log.Vis[i])
		}
	}
}

func TestLoadNilFlags_NoConfigFile(t *testing.T) {
	// Point XDG_CONFIG_HOME to a nonexistent directory so no global config is loaded.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	// Ensure no GK_* env vars interfere.
	t.Setenv("GK_BASE_BRANCH", "")
	t.Setenv("GK_REMOTE", "")

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load(nil) error: %v", err)
	}
	if cfg.Remote != "origin" {
		t.Errorf("Remote: want %q, got %q", "origin", cfg.Remote)
	}
	if cfg.Log.Limit != 20 {
		t.Errorf("Log.Limit: want 20, got %d", cfg.Log.Limit)
	}
}

func TestLoadLocalYAML(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	t.Setenv("GK_BASE_BRANCH", "")
	t.Setenv("GK_REMOTE", "")

	// Create a temporary git repo with a .gk.yaml.
	repoDir := t.TempDir()
	mustRunInDir(t, repoDir, "git", "init")
	mustRunInDir(t, repoDir, "git", "config", "user.email", "test@example.com")
	mustRunInDir(t, repoDir, "git", "config", "user.name", "Test")

	yamlContent := "remote: upstream\nlog:\n  limit: 50\n"
	if err := os.WriteFile(filepath.Join(repoDir, ".gk.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Change working directory to the temp repo so git rev-parse finds it.
	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Remote != "upstream" {
		t.Errorf("Remote: want %q, got %q", "upstream", cfg.Remote)
	}
	if cfg.Log.Limit != 50 {
		t.Errorf("Log.Limit: want 50, got %d", cfg.Log.Limit)
	}
}

// TestLoadAILangFollowsOutputLang: ai.lang unset → follows output.lang, so
// Easy Mode / output.lang=ko yields Korean AI answers. Regression: ai.lang
// had a fixed "en" default (struct + scaffolded config), overriding output.lang.
// TestLoadWorktreeInit verifies the nested pointer struct WorktreeConfig.Init
// is unmarshalled from .gk.yaml — the `gk wt init` policy source.
func TestLoadWorktreeInit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	t.Setenv("GK_BASE_BRANCH", "")
	t.Setenv("GK_REMOTE", "")

	repoDir := t.TempDir()
	mustRunInDir(t, repoDir, "git", "init")
	mustRunInDir(t, repoDir, "git", "config", "user.email", "test@example.com")
	mustRunInDir(t, repoDir, "git", "config", "user.name", "Test")

	yamlContent := "worktree:\n  init:\n    link:\n      - .env\n    run:\n      - npm ci\n      - uv sync\n"
	if err := os.WriteFile(filepath.Join(repoDir, ".gk.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Worktree.Init == nil {
		t.Fatal("Worktree.Init: want non-nil")
	}
	if len(cfg.Worktree.Init.Link) != 1 || cfg.Worktree.Init.Link[0] != ".env" {
		t.Errorf("Worktree.Init.Link: want [.env], got %v", cfg.Worktree.Init.Link)
	}
	wantRun := []string{"npm ci", "uv sync"}
	if len(cfg.Worktree.Init.Run) != len(wantRun) {
		t.Fatalf("Worktree.Init.Run: want %v, got %v", wantRun, cfg.Worktree.Init.Run)
	}
	for i, v := range wantRun {
		if cfg.Worktree.Init.Run[i] != v {
			t.Errorf("Worktree.Init.Run[%d]: want %q, got %q", i, v, cfg.Worktree.Init.Run[i])
		}
	}
}

func TestLoadAILangFollowsOutputLang(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	t.Setenv("GK_BASE_BRANCH", "")
	t.Setenv("GK_REMOTE", "")

	repoDir := t.TempDir()
	mustRunInDir(t, repoDir, "git", "init")
	mustRunInDir(t, repoDir, "git", "config", "user.email", "test@example.com")
	mustRunInDir(t, repoDir, "git", "config", "user.name", "Test")
	// output.lang=ko, no ai.lang.
	if err := os.WriteFile(filepath.Join(repoDir, ".gk.yaml"), []byte("output:\n  lang: ko\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.AI.Lang != "ko" {
		t.Errorf("ai.lang should follow output.lang: got %q, want %q", cfg.AI.Lang, "ko")
	}
}

// TestLoadAILangExplicitWins: an explicit ai.lang overrides output.lang.
func TestLoadAILangExplicitWins(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	t.Setenv("GK_BASE_BRANCH", "")
	t.Setenv("GK_REMOTE", "")

	repoDir := t.TempDir()
	mustRunInDir(t, repoDir, "git", "init")
	mustRunInDir(t, repoDir, "git", "config", "user.email", "test@example.com")
	mustRunInDir(t, repoDir, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, ".gk.yaml"), []byte("output:\n  lang: ko\nai:\n  lang: en\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.AI.Lang != "en" {
		t.Errorf("explicit ai.lang should win over output.lang: got %q, want %q", cfg.AI.Lang, "en")
	}
}

// TestLoadRepoFlagFindsLocalYAML verifies that the --repo flag points
// config discovery at that repo's .gk.yaml even when the cwd is elsewhere.
// Regression: discovery used the cwd's `git rev-parse` and ignored --repo,
// so `gk --repo /other <cmd>` silently dropped /other's repo-local config.
func TestLoadRepoFlagFindsLocalYAML(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	t.Setenv("GK_BASE_BRANCH", "")
	t.Setenv("GK_REMOTE", "")

	// Target repo carries the .gk.yaml.
	repoDir := t.TempDir()
	mustRunInDir(t, repoDir, "git", "init")
	mustRunInDir(t, repoDir, "git", "config", "user.email", "test@example.com")
	mustRunInDir(t, repoDir, "git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoDir, ".gk.yaml"), []byte("remote: from-repo-flag\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// cwd is a different dir with no .gk.yaml — only --repo can reach it.
	otherDir := t.TempDir()
	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(otherDir); err != nil {
		t.Fatal(err)
	}

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("repo", "", "")
	if err := fs.Set("repo", repoDir); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(fs)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Remote != "from-repo-flag" {
		t.Errorf("--repo should load %s/.gk.yaml: Remote = %q, want %q", repoDir, cfg.Remote, "from-repo-flag")
	}
}

func TestLoadEnvVar(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	t.Setenv("GK_BASE_BRANCH", "develop")

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.BaseBranch != "develop" {
		t.Errorf("BaseBranch: want %q, got %q", "develop", cfg.BaseBranch)
	}
}

// TestLoadLandPromoteEnv guards the env binding parity gap: land.promote got a
// config/git path but, unlike ship.* and output.*, no SetDefault/BindEnv — so
// GK_LAND_PROMOTE silently did nothing in CI/scripts. Both the natural-name and
// (post-fix) bound env must populate cfg.Land.Promote.
func TestLoadLandPromoteEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	t.Setenv("GK_BASE_BRANCH", "")
	t.Setenv("GK_REMOTE", "")
	t.Setenv("GK_LAND_PROMOTE", "main")

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Land.Promote != "main" {
		t.Errorf("Land.Promote: want %q from GK_LAND_PROMOTE, got %q", "main", cfg.Land.Promote)
	}
}

func TestLoadFlagPriority(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	t.Setenv("GK_BASE_BRANCH", "env-branch")

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("base_branch", "", "base branch")
	if err := fs.Set("base_branch", "flag-branch"); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(fs)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.BaseBranch != "flag-branch" {
		t.Errorf("BaseBranch: want %q (flag wins over env), got %q", "flag-branch", cfg.BaseBranch)
	}
}

// TestLoadFlagNameCollidingWithSection guards the v0.58.0 regression where
// `gk resolve --ai` (a bool flag named "ai") was bound over the `ai:`
// config section, making Load fail with "'ai' expected a map or struct,
// got bool". A flag whose name matches a top-level section must be
// ignored for config binding, leaving the section intact.
func TestLoadFlagNameCollidingWithSection(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	t.Setenv("GK_BASE_BRANCH", "")
	t.Setenv("GK_REMOTE", "")

	// Mirror resolve's flag set: a bool --ai that collides with `ai:`.
	fs := pflag.NewFlagSet("resolve", pflag.ContinueOnError)
	fs.Bool("ai", false, "shortcut for --strategy ai")
	fs.Bool("no-ai", false, "disable AI analysis")
	if err := fs.Set("ai", "true"); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(fs)
	if err != nil {
		t.Fatalf("Load with --ai flag must not clobber the ai section: %v", err)
	}
	// The ai section survives with its defaults rather than collapsing to
	// the flag's bool value.
	if !cfg.AI.Enabled {
		t.Errorf("ai.enabled should keep its default (true), got %v", cfg.AI.Enabled)
	}
}

func TestAIDefaults(t *testing.T) {
	d := config.Defaults()
	if !d.AI.Enabled {
		t.Error("AI.Enabled: want true")
	}
	// AI.Lang defaults to empty: Load() then follows output.lang (see
	// TestLoadAILangFollowsOutputLang). fallbackLang() turns a still-empty
	// value into "en" at the call site.
	if d.AI.Lang != "" {
		t.Errorf("AI.Lang: want %q (follows output.lang), got %q", "", d.AI.Lang)
	}
	if d.AI.Provider != "" {
		t.Errorf("AI.Provider default: want empty (auto-detect), got %q", d.AI.Provider)
	}
	if d.AI.Commit.Mode != "interactive" {
		t.Errorf("AI.Commit.Mode: want %q, got %q", "interactive", d.AI.Commit.Mode)
	}
	if d.AI.Commit.MaxGroups != 10 {
		t.Errorf("AI.Commit.MaxGroups: want 10, got %d", d.AI.Commit.MaxGroups)
	}
	if d.AI.Commit.Trailer {
		t.Error("AI.Commit.Trailer: want false (opt-in only)")
	}
	if d.AI.Commit.Audit {
		t.Error("AI.Commit.Audit: want false (opt-in only)")
	}
	if len(d.AI.Commit.DenyPaths) == 0 {
		t.Error("AI.Commit.DenyPaths: want non-empty default (secret-bearing paths)")
	}
	for _, want := range []string{".env", "*.pem", "id_rsa*"} {
		if !containsString(d.AI.Commit.DenyPaths, want) {
			t.Errorf("AI.Commit.DenyPaths missing %q", want)
		}
	}
}

func TestLoadAIFromLocalYAML(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nonexistent"))

	repoDir := t.TempDir()
	mustRunInDir(t, repoDir, "git", "init")
	mustRunInDir(t, repoDir, "git", "config", "user.email", "test@example.com")
	mustRunInDir(t, repoDir, "git", "config", "user.name", "Test")

	yamlContent := "ai:\n  provider: qwen\n  lang: ko\n  commit:\n    mode: force\n    trailer: true\n"
	if err := os.WriteFile(filepath.Join(repoDir, ".gk.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AI.Provider != "qwen" {
		t.Errorf("AI.Provider: want %q, got %q", "qwen", cfg.AI.Provider)
	}
	if cfg.AI.Lang != "ko" {
		t.Errorf("AI.Lang: want %q, got %q", "ko", cfg.AI.Lang)
	}
	if cfg.AI.Commit.Mode != "force" {
		t.Errorf("AI.Commit.Mode: want %q, got %q", "force", cfg.AI.Commit.Mode)
	}
	if !cfg.AI.Commit.Trailer {
		t.Error("AI.Commit.Trailer: want true (set in yaml)")
	}
	// Untouched fields keep their defaults.
	if cfg.AI.Commit.MaxGroups != 10 {
		t.Errorf("AI.Commit.MaxGroups: default should persist, got %d", cfg.AI.Commit.MaxGroups)
	}
}

func TestLoadAIEnvOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	t.Setenv("GK_AI_PROVIDER", "gemini")
	t.Setenv("GK_AI_COMMIT_TRAILER", "true")

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AI.Provider != "gemini" {
		t.Errorf("AI.Provider from env: want %q, got %q", "gemini", cfg.AI.Provider)
	}
	if !cfg.AI.Commit.Trailer {
		t.Error("AI.Commit.Trailer: env GK_AI_COMMIT_TRAILER=true should flip to true")
	}
}

func TestLoadAIFlagBeatsEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	t.Setenv("GK_AI_PROVIDER", "qwen")

	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("ai.provider", "", "ai provider")
	if err := fs.Set("ai.provider", "gemini"); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(fs)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AI.Provider != "gemini" {
		t.Errorf("AI.Provider: want %q (flag wins), got %q", "gemini", cfg.AI.Provider)
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// mustRunInDir executes a command in the given directory and fails the test on error.
func mustRunInDir(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("command %s %v in %s failed: %v\n%s", name, args, dir, err, out)
	}
}

func TestNvidiaDefaults(t *testing.T) {
	d := config.Defaults()
	if d.AI.Nvidia.Timeout != "60s" {
		t.Errorf("AI.Nvidia.Timeout: want %q, got %q", "60s", d.AI.Nvidia.Timeout)
	}
	if d.AI.Nvidia.Model != "" {
		t.Errorf("AI.Nvidia.Model: want empty (use provider default), got %q", d.AI.Nvidia.Model)
	}
	if d.AI.Nvidia.Endpoint != "" {
		t.Errorf("AI.Nvidia.Endpoint: want empty (use provider default), got %q", d.AI.Nvidia.Endpoint)
	}
}

func TestLoadNvidiaFromLocalYAML(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "nonexistent"))
	t.Setenv("GK_BASE_BRANCH", "")
	t.Setenv("GK_REMOTE", "")

	repoDir := t.TempDir()
	mustRunInDir(t, repoDir, "git", "init")
	mustRunInDir(t, repoDir, "git", "config", "user.email", "test@example.com")
	mustRunInDir(t, repoDir, "git", "config", "user.name", "Test")

	yamlContent := `ai:
  nvidia:
    model: "nvidia/llama-3.1-nemotron-70b-instruct"
    endpoint: "https://custom.nvidia.api/v1/chat/completions"
    timeout: "120s"
`
	if err := os.WriteFile(filepath.Join(repoDir, ".gk.yaml"), []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AI.Nvidia.Model != "nvidia/llama-3.1-nemotron-70b-instruct" {
		t.Errorf("AI.Nvidia.Model: want %q, got %q",
			"nvidia/llama-3.1-nemotron-70b-instruct", cfg.AI.Nvidia.Model)
	}
	if cfg.AI.Nvidia.Endpoint != "https://custom.nvidia.api/v1/chat/completions" {
		t.Errorf("AI.Nvidia.Endpoint: want custom endpoint, got %q", cfg.AI.Nvidia.Endpoint)
	}
	if cfg.AI.Nvidia.Timeout != "120s" {
		t.Errorf("AI.Nvidia.Timeout: want %q, got %q", "120s", cfg.AI.Nvidia.Timeout)
	}
}

// TestLoad_BrokenGlobalConfigDegrades: a parse error in the global config —
// the classic case is a duplicate top-level key like two `pull:` sections —
// must not abort Load (whose nil result panicked every `cfg, _ :=` call
// site). Expect: defaults survive, no error, and a one-time warning that
// names the file and the yaml diagnosis.
func TestLoad_BrokenGlobalConfigDegrades(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "gk"), 0o755); err != nil {
		t.Fatal(err)
	}
	broken := "pull:\n  strategy: rebase\npull:\n  with_base: true\n"
	if err := os.WriteFile(filepath.Join(dir, "gk", "config.yaml"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}

	var warnBuf bytes.Buffer
	restore := config.SetConfigWarnWriter(&warnBuf)
	defer restore()

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load must degrade, not fail: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load returned nil Config")
	}
	if cfg.Remote != "origin" {
		t.Errorf("defaults should survive a broken layer, Remote = %q", cfg.Remote)
	}

	warn := warnBuf.String()
	if !strings.Contains(warn, "config error") || !strings.Contains(warn, "config.yaml") {
		t.Errorf("warning should name the broken file, got: %q", warn)
	}
	if !strings.Contains(warn, "already defined") {
		t.Errorf("warning should pass through yaml's duplicate-key diagnosis, got: %q", warn)
	}

	// Dedupe: a second Load in the same process must not warn again.
	warnBuf.Reset()
	if _, err := config.Load(nil); err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if warnBuf.Len() != 0 {
		t.Errorf("warning must print once per process, got again: %q", warnBuf.String())
	}
}

// TestLoad_ShipConfig: the ship section (watch/verify hooks + explicit
// version files) must round-trip from yaml into the struct — it powers
// `gk ship`'s post-tag pipeline.
func TestLoad_ShipConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// The gk repo's own .gk.yaml now carries a real ship: section — leave
	// the repo so the repo-local layer cannot shadow the fixture.
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(dir, "gk"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := `ship:
  watch:
    - name: ci
      command: gh run watch --exit-status
  verify:
    - name: cdn
      command: curl -fsI https://example.com/checksums.txt
      continue_on_failure: true
  version_files:
    - VERSION
    - extension/package.json
  auto_confirm: true
  wait: false
`
	if err := os.WriteFile(filepath.Join(dir, "gk", "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Ship.Watch) != 1 || cfg.Ship.Watch[0].Name != "ci" || cfg.Ship.Watch[0].Command != "gh run watch --exit-status" {
		t.Errorf("Ship.Watch = %+v", cfg.Ship.Watch)
	}
	if len(cfg.Ship.Verify) != 1 || !cfg.Ship.Verify[0].ContinueOnFailure {
		t.Errorf("Ship.Verify = %+v", cfg.Ship.Verify)
	}
	if len(cfg.Ship.VersionFiles) != 2 || cfg.Ship.VersionFiles[1] != "extension/package.json" {
		t.Errorf("Ship.VersionFiles = %+v", cfg.Ship.VersionFiles)
	}
	if !cfg.Ship.AutoConfirm {
		t.Error("Ship.AutoConfirm = false, want true")
	}
	if cfg.Ship.Wait {
		t.Error("Ship.Wait = true, want false")
	}
}

// TestLoad_ShipWaitDefault: ship.wait must default to true — including when
// a ship: section exists but omits the key. A zero-value default here would
// silently skip every configured watch/verify pipeline.
func TestLoad_ShipWaitDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(dir, "gk"), 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := `ship:
  watch:
    - name: ci
      command: gh run watch --exit-status
`
	if err := os.WriteFile(filepath.Join(dir, "gk", "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Ship.Wait {
		t.Error("Ship.Wait = false, want default true")
	}
	if cfg.Ship.AutoConfirm {
		t.Error("Ship.AutoConfirm = true, want default false")
	}
}
