package cli

import (
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
