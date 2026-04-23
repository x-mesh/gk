package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
)

func TestWriteDefaultConfigFreshWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gk", "config.yaml")

	if err := config.WriteDefaultConfig(path, false); err != nil {
		t.Fatalf("WriteDefaultConfig: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "ai:") {
		t.Error("template should include ai: section")
	}
	// Template is ~180 lines of commented YAML.
	if len(data) < 1500 {
		t.Errorf("template suspiciously short (%d bytes)", len(data))
	}
}

func TestWriteDefaultConfigRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("custom: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := config.WriteDefaultConfig(path, false)
	if !errors.Is(err, config.ErrConfigExists) {
		t.Errorf("want ErrConfigExists, got %v", err)
	}
	// Confirm the file was NOT clobbered.
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "custom: true") {
		t.Error("existing file must not be overwritten")
	}
}

func TestWriteDefaultConfigForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	_ = os.WriteFile(path, []byte("stub\n"), 0o644)

	if err := config.WriteDefaultConfig(path, true); err != nil {
		t.Fatalf("force write: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "stub") {
		t.Error("force=true should have overwritten the stub")
	}
	if !strings.Contains(string(data), "ai:") {
		t.Error("force overwrite did not produce the template")
	}
}

func TestGlobalConfigPathHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-custom")
	got := config.GlobalConfigPath()
	want := "/tmp/xdg-custom/gk/config.yaml"
	if got != want {
		t.Errorf("XDG path: want %q, got %q", want, got)
	}
}

func TestGlobalConfigPathFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	got := config.GlobalConfigPath()
	if got == "" {
		t.Skip("no HOME in this env; skip")
	}
	if !strings.HasSuffix(got, ".config/gk/config.yaml") {
		t.Errorf("HOME fallback path: got %q", got)
	}
}

func TestEnsureGlobalConfigCreatesFirstTime(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("GK_NO_AUTO_CONFIG", "")

	created, path := config.EnsureGlobalConfig()
	if !created {
		t.Fatalf("first run should create; got created=%v path=%q", created, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("path should exist: %v", err)
	}

	// Second call is a no-op.
	created2, _ := config.EnsureGlobalConfig()
	if created2 {
		t.Error("second call must not re-create")
	}
}

func TestEnsureGlobalConfigRespectsOptOut(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("GK_NO_AUTO_CONFIG", "1")

	created, _ := config.EnsureGlobalConfig()
	if created {
		t.Error("GK_NO_AUTO_CONFIG=1 must skip creation")
	}
	if _, err := os.Stat(filepath.Join(tmp, "gk", "config.yaml")); !os.IsNotExist(err) {
		t.Errorf("file should NOT exist with opt-out: err=%v", err)
	}
}
