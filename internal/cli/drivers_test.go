package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/testutil"
)

// TestDriversBlockRoundTrip: install writes the fenced block, a second install
// is byte-identical (idempotent), foreign lines survive both install and
// uninstall, and uninstalling a gk-only file removes it entirely.
func TestDriversBlockRoundTrip(t *testing.T) {
	repo := testutil.NewRepo(t)
	attrs := filepath.Join(repo.Dir, ".git", "info", "attributes")

	// Pre-existing user content must survive everything gk does.
	if err := os.MkdirAll(filepath.Dir(attrs), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := "*.bin -diff\n# user note\n"
	if err := os.WriteFile(attrs, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}

	first := stripDriversBlock(foreign)
	if strings.TrimRight(first, "\n") != strings.TrimRight(foreign, "\n") {
		t.Fatalf("strip on a block-less file must be a no-op, got %q", first)
	}

	// Simulate install: kept + block (mirrors runDriversInstall's assembly).
	kept := strings.TrimRight(stripDriversBlock(foreign), "\n")
	installed := kept + "\n\n" + driversBlock() + "\n"

	if !strings.Contains(installed, "*.css diff=css") || !strings.Contains(installed, "*.py diff=python") {
		t.Error("installed block missing expected rules")
	}
	if !strings.Contains(installed, foreign[:10]) {
		t.Error("foreign content lost on install")
	}

	// Idempotency: strip+reassemble of the installed content is identical.
	again := strings.TrimRight(stripDriversBlock(installed), "\n") + "\n\n" + driversBlock() + "\n"
	if again != installed {
		t.Errorf("re-install not idempotent:\n%q\nvs\n%q", installed, again)
	}

	// Uninstall: only the fenced block goes; the foreign lines remain.
	removed := stripDriversBlock(installed)
	if strings.Contains(removed, "diff=python") || strings.Contains(removed, driversMarkerPrefix) {
		t.Error("uninstall left gk block content behind")
	}
	if !strings.Contains(removed, "*.bin -diff") || !strings.Contains(removed, "# user note") {
		t.Error("uninstall damaged foreign content")
	}
}

// TestDriversCommandsEndToEnd drives the run functions against a temp repo
// (install → status → uninstall). The global rootCmd is deliberately NOT
// executed — it carries package-level flag state across tests; instead the
// repo is targeted through the --repo global the handlers already honor.
func TestDriversCommandsEndToEnd(t *testing.T) {
	repo := testutil.NewRepo(t)
	prevRepo := flagRepo
	flagRepo = repo.Dir
	defer func() { flagRepo = prevRepo }()

	run := func(fn func(*cobra.Command, []string) error) string {
		t.Helper()
		out := new(strings.Builder)
		cmd := &cobra.Command{}
		cmd.SetOut(out)
		cmd.SetContext(context.Background())
		if err := fn(cmd, nil); err != nil {
			t.Fatalf("drivers handler: %v", err)
		}
		return out.String()
	}

	run(runDriversInstall)
	attrs := filepath.Join(repo.Dir, ".git", "info", "attributes")
	data, err := os.ReadFile(attrs)
	if err != nil {
		t.Fatalf("attributes not written: %v", err)
	}
	if !strings.Contains(string(data), "*.css diff=css") {
		t.Errorf("attributes missing css rule:\n%s", data)
	}

	if out := run(runDriversStatus); !strings.Contains(out, "installed") {
		t.Errorf("status after install = %q", out)
	}

	run(runDriversUninstall)
	if _, err := os.Stat(attrs); !os.IsNotExist(err) {
		t.Errorf("gk-only attributes file should be removed on uninstall (err=%v)", err)
	}
}

// TestConflictHunkSymbols: each conflict marker is attributed to the nearest
// definition above it, per-extension patterns apply, and marker-less files
// yield nothing.
func TestConflictHunkSymbols(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join([]string{
		"def alpha():",
		"    x = 1",
		"<<<<<<< HEAD",
		"    return 1",
		"=======",
		"    return 2",
		">>>>>>> theirs",
		"",
		"def beta():",
		"    pass",
		"",
		"class Gamma:",
		"<<<<<<< HEAD",
		"    a = 1",
		"=======",
		"    a = 2",
		">>>>>>> theirs",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "m.py"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	syms := conflictHunkSymbols(dir, "m.py")
	if len(syms) != 2 || syms[0] != "alpha" || syms[1] != "Gamma" {
		t.Errorf("symbols = %v, want [alpha Gamma]", syms)
	}

	if err := os.WriteFile(filepath.Join(dir, "clean.py"), []byte("def ok():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if syms := conflictHunkSymbols(dir, "clean.py"); len(syms) != 0 {
		t.Errorf("marker-less file must yield no symbols, got %v", syms)
	}
}
