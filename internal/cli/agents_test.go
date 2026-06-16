package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallAgentsBlock_Lifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")

	state, err := installAgentsBlock(path)
	if err != nil || state != "created" {
		t.Fatalf("first install: state=%q err=%v", state, err)
	}
	state, err = installAgentsBlock(path)
	if err != nil || state != "unchanged" {
		t.Fatalf("idempotent install: state=%q err=%v", state, err)
	}

	// User content outside the block must survive a refresh; a stale block
	// (old marker version) must be replaced in place.
	cur := fmt.Sprintf("begin v%d", agentsContractVersion)
	b, _ := os.ReadFile(path)
	content := "# My project\n\nUser notes stay.\n\n" + strings.Replace(string(b), cur, "begin v0", 1)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err = installAgentsBlock(path)
	if err != nil || state != "updated" {
		t.Fatalf("refresh: state=%q err=%v", state, err)
	}
	after, _ := os.ReadFile(path)
	s := string(after)
	if !strings.Contains(s, "# My project") || !strings.Contains(s, "User notes stay.") {
		t.Errorf("user content lost:\n%s", s)
	}
	if !strings.Contains(s, cur) || strings.Contains(s, "begin v0") {
		t.Errorf("stale block not replaced:\n%s", s)
	}
	if strings.Count(s, "gk:agents:begin") != 1 {
		t.Errorf("duplicate blocks:\n%s", s)
	}
}

func TestInstallAgentsBlock_CreatesParentDir(t *testing.T) {
	// --global may target ~/.codex, which often doesn't exist yet; install
	// must create the parent chain rather than failing.
	path := filepath.Join(t.TempDir(), "nested", "deep", "CLAUDE.md")
	state, err := installAgentsBlock(path)
	if err != nil || state != "created" {
		t.Fatalf("nested install: state=%q err=%v", state, err)
	}
	if _, serr := os.Stat(path); serr != nil {
		t.Errorf("file not created under missing parents: %v", serr)
	}
}

func TestAgentsGlobalFiles_EnvOverrideAndDefault(t *testing.T) {
	// Explicit overrides win.
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/cc")
	t.Setenv("CODEX_HOME", "/tmp/cx")
	files, err := agentsGlobalFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 global files, got %d", len(files))
	}
	if files[0].path != "/tmp/cc/CLAUDE.md" || files[1].path != "/tmp/cx/AGENTS.md" {
		t.Errorf("override paths = %q, %q", files[0].path, files[1].path)
	}
	for _, f := range files {
		if f.scope != "global" {
			t.Errorf("scope = %q, want global", f.scope)
		}
	}

	// Empty env → ~/.claude and ~/.codex defaults.
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	home, _ := os.UserHomeDir()
	files, err = agentsGlobalFiles()
	if err != nil {
		t.Fatal(err)
	}
	if files[0].path != filepath.Join(home, ".claude", "CLAUDE.md") {
		t.Errorf("claude default = %q", files[0].path)
	}
	if files[1].path != filepath.Join(home, ".codex", "AGENTS.md") {
		t.Errorf("codex default = %q", files[1].path)
	}
}

func TestCheckAgentsFile_States(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer

	// Missing file → absent.
	if s := checkAgentsFile(&buf, filepath.Join(dir, "nope.md"), "nope.md"); s != agentsAbsent {
		t.Errorf("missing file → %v, want absent", s)
	}

	// Freshly installed → ok.
	okPath := filepath.Join(dir, "OK.md")
	if _, err := installAgentsBlock(okPath); err != nil {
		t.Fatal(err)
	}
	if s := checkAgentsFile(&buf, okPath, "OK.md"); s != agentsOK {
		t.Errorf("installed → %v, want ok", s)
	}

	// Older marker version → stale (drift).
	stalePath := filepath.Join(dir, "STALE.md")
	if _, err := installAgentsBlock(stalePath); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(stalePath)
	cur := fmt.Sprintf("begin v%d", agentsContractVersion)
	if err := os.WriteFile(stalePath, []byte(strings.Replace(string(b), cur, "begin v1", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := checkAgentsFile(&buf, stalePath, "STALE.md"); s != agentsStale {
		t.Errorf("downgraded block → %v, want stale", s)
	}

	// File exists but no gk block → absent.
	noBlk := filepath.Join(dir, "NOTES.md")
	if err := os.WriteFile(noBlk, []byte("# just my notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := checkAgentsFile(&buf, noBlk, "NOTES.md"); s != agentsAbsent {
		t.Errorf("no block → %v, want absent", s)
	}
}
