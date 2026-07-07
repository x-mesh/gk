package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hookSettingsPath returns a temp settings.json path the hook subcommands
// can be pointed at via --settings, isolating tests from ~/.claude.
func hookSettingsPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "settings.json")
}

func runHook(t *testing.T, settings string, sub ...string) string {
	t.Helper()
	args := append([]string{"snapshot", "hook"}, sub...)
	args = append(args, "--settings", settings)
	return runSnap(t, t.TempDir(), args...)
}

func readHookSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse settings: %v\nraw: %s", err, raw)
	}
	return m
}

// TestSnapshotHook_InstallCreatesSettings installs into a fresh path and
// verifies the Stop hook entry lands with the expected command.
func TestSnapshotHook_InstallCreatesSettings(t *testing.T) {
	path := hookSettingsPath(t)

	out := runHook(t, path, "install")
	if !strings.Contains(out, "installed") {
		t.Fatalf("expected install confirmation, got: %s", out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("settings not written: %v", err)
	}
	if !strings.Contains(string(raw), snapshotHookCommand) {
		t.Fatalf("settings missing hook command, got: %s", raw)
	}

	if out := runHook(t, path, "status"); !strings.Contains(out, "installed —") {
		t.Fatalf("status should report installed, got: %s", out)
	}
}

// TestSnapshotHook_InstallIsIdempotent runs install twice and checks the
// file carries exactly one gk hook entry.
func TestSnapshotHook_InstallIsIdempotent(t *testing.T) {
	path := hookSettingsPath(t)

	runHook(t, path, "install")
	out := runHook(t, path, "install")
	if !strings.Contains(out, "already installed") {
		t.Fatalf("second install should be a no-op, got: %s", out)
	}
	raw, _ := os.ReadFile(path)
	if got := strings.Count(string(raw), snapshotHookCommand); got != 1 {
		t.Fatalf("want exactly 1 hook entry, got %d in: %s", got, raw)
	}
}

// TestSnapshotHook_PreservesExistingSettings installs into a settings file
// that already has an unrelated Stop hook and top-level keys — both must
// survive install AND uninstall untouched.
func TestSnapshotHook_PreservesExistingSettings(t *testing.T) {
	path := hookSettingsPath(t)
	seed := `{
  "model": "opus",
  "hooks": {
    "Stop": [
      {"hooks": [{"type": "command", "command": "./my-stop.sh", "timeout": 30}]}
    ],
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "./guard.sh"}]}
    ]
  }
}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	runHook(t, path, "install")
	m := readHookSettings(t, path)
	if m["model"] != "opus" {
		t.Fatalf("top-level key lost: %#v", m)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{"./my-stop.sh", "./guard.sh", snapshotHookCommand} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("missing %q after install: %s", want, raw)
		}
	}
	// The user hook's timeout must not be mangled into float notation.
	if !strings.Contains(string(raw), `"timeout": 30`) {
		t.Fatalf("numeric timeout mangled: %s", raw)
	}

	out := runHook(t, path, "uninstall")
	if !strings.Contains(out, "uninstalled") {
		t.Fatalf("expected uninstall confirmation, got: %s", out)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), snapshotHookCommand) {
		t.Fatalf("gk hook survived uninstall: %s", raw)
	}
	for _, want := range []string{"./my-stop.sh", "./guard.sh"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("user hook %q lost on uninstall: %s", want, raw)
		}
	}
}

// TestSnapshotHook_UninstallWhenAbsent is a friendly no-op.
func TestSnapshotHook_UninstallWhenAbsent(t *testing.T) {
	path := hookSettingsPath(t)
	out := runHook(t, path, "uninstall")
	if !strings.Contains(out, "not installed") {
		t.Fatalf("expected not-installed notice, got: %s", out)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("uninstall must not create a settings file")
	}
}

// TestSnapshotHook_RefusesCorruptJSON never rewrites a file it can't parse.
func TestSnapshotHook_RefusesCorruptJSON(t *testing.T) {
	path := hookSettingsPath(t)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, buf := buildSnapshotCmd(t.TempDir(), "snapshot", "hook", "install", "--settings", path)
	if err := root.Execute(); err == nil {
		t.Fatalf("corrupt JSON must error, out: %s", buf.String())
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "{not json" {
		t.Fatalf("corrupt file must be left untouched, got: %s", raw)
	}
}
