package config_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/x-mesh/gk/internal/config"
)

func TestReadGK_EmptyOutsideRepo(t *testing.T) {
	// Change to a temp dir that is not a git repo.
	dir := t.TempDir()
	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	m, err := config.ReadGK()
	if err != nil {
		t.Fatalf("ReadGK() error: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map outside git repo, got %v", m)
	}
}

func TestReadGK_ParsesEntries(t *testing.T) {
	repoDir := t.TempDir()
	mustRunInDir(t, repoDir, "git", "init")
	mustRunInDir(t, repoDir, "git", "config", "user.email", "test@example.com")
	mustRunInDir(t, repoDir, "git", "config", "user.name", "Test")
	// Set gk.* config values.
	mustRunInDir(t, repoDir, "git", "config", "gk.base-branch", "main")
	mustRunInDir(t, repoDir, "git", "config", "gk.log.format", "%h %s")

	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}

	m, err := config.ReadGK()
	if err != nil {
		t.Fatalf("ReadGK() error: %v", err)
	}

	// gk.base-branch → base_branch (dash normalised to underscore)
	if got, ok := m["base_branch"]; !ok || got != "main" {
		t.Errorf("base_branch: want %q, got %v (ok=%v)", "main", got, ok)
	}

	// gk.log.format → nested map log.format
	logMap, ok := m["log"].(map[string]any)
	if !ok {
		t.Fatalf("expected m[\"log\"] to be map[string]any, got %T", m["log"])
	}
	if got := logMap["format"]; got != "%h %s" {
		t.Errorf("log.format: want %q, got %v", "%h %s", got)
	}
}

func TestParseGKLines_DashNormalisation(t *testing.T) {
	// Use a temporary repo to exercise ReadGK with a dash-separated key.
	repoDir := t.TempDir()
	mustRunInDir(t, repoDir, "git", "init")
	mustRunInDir(t, repoDir, "git", "config", "user.email", "test@example.com")
	mustRunInDir(t, repoDir, "git", "config", "user.name", "Test")
	mustRunInDir(t, repoDir, "git", "config", "gk.stale-days", "14")

	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(repoDir); err != nil {
		t.Fatal(err)
	}

	m, err := config.ReadGK()
	if err != nil {
		t.Fatalf("ReadGK() error: %v", err)
	}
	if got, ok := m["stale_days"]; !ok || got != "14" {
		t.Errorf("stale_days: want %q, got %v (ok=%v)", "14", got, ok)
	}
}

// runInDir is used by mustRunInDir (defined in load_test.go) but we need it
// here too as a standalone helper for test setup.
func runInDir(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}
