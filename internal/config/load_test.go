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
	if !d.Status.AutoFetch {
		t.Error("Status.AutoFetch: want true by default")
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

// mustRunInDir executes a command in the given directory and fails the test on error.
func mustRunInDir(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("command %s %v in %s failed: %v\n%s", name, args, dir, err, out)
	}
}
