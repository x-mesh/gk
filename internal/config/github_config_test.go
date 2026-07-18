package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitHubOwnerSettable is the regression guard for the load-bearing choice
// to declare GitHubConfig.Owner WITHOUT `omitempty`: `gk config set` only
// accepts keys that resolve to a leaf in the marshaled Defaults() schema, and
// an omitempty empty-string field would vanish from that schema, breaking the
// set path. If someone re-adds omitempty, ValidKey drops to false and this
// fails.
func TestGitHubOwnerSettable(t *testing.T) {
	if !ValidKey("github.owner") {
		t.Fatal("github.owner must be a settable config key (did GitHubConfig.Owner regain omitempty?)")
	}
	if ValidKey("github.nonexistent") {
		t.Error("github.nonexistent should not be a valid key")
	}
}

// TestGitHubOwnerSetValueWrites verifies the full set path writes the key.
func TestGitHubOwnerSetValueWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("base_branch: main\n"), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if _, err := SetValue(path, "github.owner", "acme"); err != nil {
		t.Fatalf("SetValue github.owner: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(data), "acme") {
		t.Errorf("config file should contain the written owner, got:\n%s", data)
	}
}
