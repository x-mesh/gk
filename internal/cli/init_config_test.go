package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitConfigRegistered(t *testing.T) {
	found, _, err := rootCmd.Find([]string{"init", "config"})
	if err != nil {
		t.Fatalf("rootCmd.Find(init config): %v", err)
	}
	if found.Use != "config" {
		t.Errorf("Use: want %q, got %q", "config", found.Use)
	}
}

func TestRunInitConfigWritesToCustomOut(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "mycfg.yaml")

	found, _, _ := rootCmd.Find([]string{"init", "config"})
	buf := &bytes.Buffer{}
	found.SetOut(buf)
	found.SetErr(buf)
	_ = found.Flags().Set("out", target)
	_ = found.Flags().Set("force", "false")

	if err := runInitConfig(found, nil); err != nil {
		t.Fatalf("runInitConfig: %v", err)
	}
	if !strings.Contains(buf.String(), "created:") {
		t.Errorf("stdout: %q", buf.String())
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "ai:") {
		t.Error("template missing ai: section")
	}

	// Second run without --force should report skipped, not error.
	buf.Reset()
	if err := runInitConfig(found, nil); err != nil {
		t.Errorf("second run: %v", err)
	}
	if !strings.Contains(buf.String(), "skipped:") {
		t.Errorf("stdout: %q", buf.String())
	}
}

func TestRunInitConfigForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cfg.yaml")
	_ = os.WriteFile(target, []byte("stub\n"), 0o644)

	found, _, _ := rootCmd.Find([]string{"init", "config"})
	buf := &bytes.Buffer{}
	found.SetOut(buf)
	_ = found.Flags().Set("out", target)
	_ = found.Flags().Set("force", "true")

	if err := runInitConfig(found, nil); err != nil {
		t.Fatalf("runInitConfig: %v", err)
	}
	data, _ := os.ReadFile(target)
	if strings.Contains(string(data), "stub") {
		t.Error("force=true must overwrite")
	}

	// Reset flag for later tests.
	_ = found.Flags().Set("force", "false")
}
