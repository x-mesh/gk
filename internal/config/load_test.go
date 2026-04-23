package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
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
	wantVis := []string{"gauge", "progress", "tree", "staleness"}
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

func TestAIDefaults(t *testing.T) {
	d := config.Defaults()
	if !d.AI.Enabled {
		t.Error("AI.Enabled: want true")
	}
	if d.AI.Lang != "en" {
		t.Errorf("AI.Lang: want %q, got %q", "en", d.AI.Lang)
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
