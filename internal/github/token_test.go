package github

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTokenPrefersGHTokenEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "from-gh-token")
	t.Setenv("GITHUB_TOKEN", "from-github-token")
	if got := ResolveToken(); got != "from-gh-token" {
		t.Errorf("ResolveToken() = %q, want %q", got, "from-gh-token")
	}
}

func TestResolveTokenFallsBackToGitHubTokenEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "from-github-token")
	if got := ResolveToken(); got != "from-github-token" {
		t.Errorf("ResolveToken() = %q, want %q", got, "from-github-token")
	}
}

func TestResolveTokenReadsGHConfigFile(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	dir := t.TempDir()
	hosts := "github.com:\n    oauth_token: from-gh-config\n    user: someone\n"
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(hosts), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_CONFIG_DIR", dir)

	if got := ResolveToken(); got != "from-gh-config" {
		t.Errorf("ResolveToken() = %q, want %q", got, "from-gh-config")
	}
}

func TestResolveTokenEmptyWhenNothingConfigured(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())

	if got := ResolveToken(); got != "" {
		t.Errorf("ResolveToken() = %q, want empty", got)
	}
}
