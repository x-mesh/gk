package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/testutil"
)

// ---------------------------------------------------------------------------
// Unit — renderHook / isGkManaged
// ---------------------------------------------------------------------------

func TestRenderHook_CarriesMarker(t *testing.T) {
	body := renderHook(hookSpec{Name: "commit-msg", Script: `exec gk lint-commit --file "$1"`, Description: "x"})
	if !strings.Contains(body, hooksManagedMarker) {
		t.Error("rendered body missing marker")
	}
	if !strings.HasPrefix(body, "#!/bin/sh\n") {
		t.Error("rendered body missing shebang")
	}
	if !strings.Contains(body, `exec gk lint-commit --file "$1"`) {
		t.Error("rendered body missing script")
	}
}

func TestIsGkManaged(t *testing.T) {
	yes := []byte("#!/bin/sh\n" + hooksManagedMarker + "\nexec gk ...\n")
	no := []byte("#!/bin/sh\nexec prettier --check\n")
	if !isGkManaged(yes) {
		t.Error("expected true for marked body")
	}
	if isGkManaged(no) {
		t.Error("expected false for unmarked body")
	}
}

// ---------------------------------------------------------------------------
// Unit — writeHook state machine
// ---------------------------------------------------------------------------

func TestWriteHook_FreshInstall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "commit-msg")

	action, err := writeHook(path, knownHooks()[0], false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if action != "installed" {
		t.Errorf("action = %q, want installed", action)
	}
	body, _ := os.ReadFile(path)
	if !isGkManaged(body) {
		t.Error("installed file missing marker")
	}
	info, _ := os.Stat(path)
	if info.Mode()&0o111 == 0 {
		t.Error("installed hook is not executable")
	}
}

func TestWriteHook_UpdateExistingManaged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "commit-msg")
	// Pre-populate with a gk-managed body.
	if _, err := writeHook(path, knownHooks()[0], false); err != nil {
		t.Fatal(err)
	}

	action, err := writeHook(path, knownHooks()[0], false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if action != "updated" {
		t.Errorf("action = %q, want updated", action)
	}
}

func TestWriteHook_RefusesForeignWithoutForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "commit-msg")
	custom := []byte("#!/bin/sh\nexec prettier --check\n")
	if err := os.WriteFile(path, custom, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := writeHook(path, knownHooks()[0], false)
	if err == nil {
		t.Fatal("expected refusal, got nil")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force, got: %v", err)
	}
	// Verify the original file was NOT modified.
	got, _ := os.ReadFile(path)
	if string(got) != string(custom) {
		t.Error("foreign hook was modified without --force")
	}
}

func TestWriteHook_ForceBackupsForeign(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "commit-msg")
	custom := []byte("#!/bin/sh\nexec prettier --check\n")
	if err := os.WriteFile(path, custom, 0o755); err != nil {
		t.Fatal(err)
	}

	action, err := writeHook(path, knownHooks()[0], true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.HasPrefix(action, "replaced") {
		t.Errorf("action = %q, want 'replaced ...'", action)
	}

	// Exactly one .bak.* sibling should exist.
	entries, _ := os.ReadDir(dir)
	var baks []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "commit-msg.bak.") {
			baks = append(baks, e.Name())
		}
	}
	if len(baks) != 1 {
		t.Fatalf("expected 1 backup, got %v", baks)
	}
	bakBody, _ := os.ReadFile(filepath.Join(dir, baks[0]))
	if string(bakBody) != string(custom) {
		t.Error("backup content differs from original")
	}
	// New hook is gk-managed.
	newBody, _ := os.ReadFile(path)
	if !isGkManaged(newBody) {
		t.Error("new hook missing marker after force-replace")
	}
}

// ---------------------------------------------------------------------------
// Unit — removeHook
// ---------------------------------------------------------------------------

func TestRemoveHook_Missing(t *testing.T) {
	dir := t.TempDir()
	status, err := removeHook(filepath.Join(dir, "commit-msg"))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != "not-installed" {
		t.Errorf("status = %q, want not-installed", status)
	}
}

func TestRemoveHook_GkManaged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "commit-msg")
	if _, err := writeHook(path, knownHooks()[0], false); err != nil {
		t.Fatal(err)
	}
	status, err := removeHook(path)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if status != "removed" {
		t.Errorf("status = %q, want removed", status)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file still exists after remove")
	}
}

func TestRemoveHook_RefusesForeign(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "commit-msg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncustom\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := removeHook(path)
	if err == nil {
		t.Fatal("expected refusal")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Error("foreign hook should not have been removed")
	}
}

// ---------------------------------------------------------------------------
// Integration — hooks install/uninstall via cobra
// ---------------------------------------------------------------------------

func buildHooksCmd(repoDir string, subcommand string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "path to git repo")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "verbose output")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "dry run")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "json output")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "disable color")

	hooks := &cobra.Command{Use: "hooks", SilenceUsage: true}
	install := &cobra.Command{
		Use:          "install",
		RunE:         runHooksInstall,
		SilenceUsage: true,
	}
	install.Flags().Bool("commit-msg", false, "")
	install.Flags().Bool("pre-push", false, "")
	install.Flags().Bool("all", false, "")
	install.Flags().Bool("force", false, "")
	hooks.AddCommand(install)

	uninstall := &cobra.Command{
		Use:          "uninstall",
		RunE:         runHooksUninstall,
		SilenceUsage: true,
	}
	uninstall.Flags().Bool("commit-msg", false, "")
	uninstall.Flags().Bool("pre-push", false, "")
	uninstall.Flags().Bool("all", false, "")
	hooks.AddCommand(uninstall)

	testRoot.AddCommand(hooks)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)

	allArgs := append([]string{"--repo", repoDir, "hooks", subcommand}, extraArgs...)
	testRoot.SetArgs(allArgs)
	return testRoot, buf
}

func TestHooksCmd_InstallAllThenUninstallAll(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	root, buf := buildHooksCmd(repo.Dir, "install", "--all")
	if err := root.Execute(); err != nil {
		t.Fatalf("install: %v\n%s", err, buf.String())
	}
	// Both hooks should now exist and be gk-managed.
	for _, name := range []string{"commit-msg", "pre-push"} {
		p := filepath.Join(repo.Dir, ".git", "hooks", name)
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("hook %s missing: %v", name, err)
		}
		if !isGkManaged(body) {
			t.Errorf("hook %s missing marker", name)
		}
	}

	root2, buf2 := buildHooksCmd(repo.Dir, "uninstall", "--all")
	if err := root2.Execute(); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, buf2.String())
	}
	for _, name := range []string{"commit-msg", "pre-push"} {
		p := filepath.Join(repo.Dir, ".git", "hooks", name)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("hook %s still exists after uninstall", name)
		}
	}
}

func TestHooksCmd_InstallNothingSelected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	root, buf := buildHooksCmd(repo.Dir, "install")
	err := root.Execute()
	if err == nil {
		t.Fatalf("expected error when nothing selected\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "at least one") {
		t.Errorf("expected 'at least one' in err, got: %v", err)
	}
}

func TestHooksCmd_InstallSingle(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	root, buf := buildHooksCmd(repo.Dir, "install", "--commit-msg")
	if err := root.Execute(); err != nil {
		t.Fatalf("err: %v\n%s", err, buf.String())
	}
	// commit-msg should exist
	if _, err := os.Stat(filepath.Join(repo.Dir, ".git", "hooks", "commit-msg")); err != nil {
		t.Error("commit-msg not installed")
	}
	// pre-push should NOT exist
	if _, err := os.Stat(filepath.Join(repo.Dir, ".git", "hooks", "pre-push")); !os.IsNotExist(err) {
		t.Error("pre-push was installed but wasn't selected")
	}
}
