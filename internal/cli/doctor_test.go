package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// ---------------------------------------------------------------------------
// Unit — parseGitVersion
// ---------------------------------------------------------------------------

func TestParseGitVersion(t *testing.T) {
	cases := []struct {
		in              string
		wantMaj, wantMn int
	}{
		{"git version 2.54.0\n", 2, 54},
		{"git version 2.38.1 (Apple Git-143.1)\n", 2, 38},
		{"garbage\n", 0, 0},
		{"", 0, 0},
		{"git version 3.0.0.dev", 3, 0},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			maj, mn := parseGitVersion(c.in)
			if maj != c.wantMaj || mn != c.wantMn {
				t.Errorf("parseGitVersion(%q) = %d.%d, want %d.%d", c.in, maj, mn, c.wantMaj, c.wantMn)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Unit — countStatus, statusMarker
// ---------------------------------------------------------------------------

func TestCountStatus(t *testing.T) {
	checks := []doctorCheck{
		{Status: statusPass}, {Status: statusPass},
		{Status: statusWarn},
		{Status: statusFail},
	}
	if got := countStatus(checks, statusPass); got != 2 {
		t.Errorf("pass count = %d, want 2", got)
	}
	if got := countStatus(checks, statusFail); got != 1 {
		t.Errorf("fail count = %d, want 1", got)
	}
}

func TestStatusMarker(t *testing.T) {
	if !strings.Contains(statusMarker(statusPass), "PASS") {
		t.Error("PASS marker should contain PASS")
	}
	if !strings.Contains(statusMarker(statusWarn), "WARN") {
		t.Error("WARN marker should contain WARN")
	}
	if !strings.Contains(statusMarker(statusFail), "FAIL") {
		t.Error("FAIL marker should contain FAIL")
	}
}

// ---------------------------------------------------------------------------
// Unit — writeDoctorTable
// ---------------------------------------------------------------------------

func TestWriteDoctorTable(t *testing.T) {
	checks := []doctorCheck{
		{Name: "git version", Status: statusPass, Detail: "2.54.0"},
		{Name: "fzf", Status: statusWarn, Detail: "not installed", Fix: "brew install fzf"},
		{Name: "hooks: pre-push", Status: statusFail, Detail: "missing", Fix: "gk hooks install"},
	}
	buf := &bytes.Buffer{}
	writeDoctorTable(buf, checks)
	out := buf.String()

	for _, want := range []string{
		"git version", "2.54.0",
		"fzf", "not installed", "brew install fzf",
		"hooks: pre-push", "missing", "gk hooks install",
		"1 PASS", "1 WARN", "1 FAIL",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in table, got:\n%s", want, out)
		}
	}
}

// ---------------------------------------------------------------------------
// Unit — checkHook with a real filesystem
// ---------------------------------------------------------------------------

func TestCheckHook_Missing(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	runner := &git.ExecRunner{Dir: repo.Dir}
	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })

	c := checkHook(context.Background(), runner, "commit-msg", "gk lint-commit")
	if c.Status != statusWarn {
		t.Errorf("status = %s, want WARN", c.Status)
	}
	if !strings.Contains(c.Detail, "not installed") {
		t.Errorf("detail = %q, want 'not installed'", c.Detail)
	}
}

func TestCheckHook_Installed(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	hookPath := filepath.Join(repo.Dir, ".git", "hooks", "commit-msg")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexec gk lint-commit --file \"$1\"\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })

	c := checkHook(context.Background(), runner, "commit-msg", "gk lint-commit")
	if c.Status != statusPass {
		t.Errorf("status = %s, want PASS (detail: %s)", c.Status, c.Detail)
	}
	if !strings.Contains(c.Detail, "invokes gk") {
		t.Errorf("detail = %q, want 'invokes gk'", c.Detail)
	}
}

func TestCheckHook_NotExecutable(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	hookPath := filepath.Join(repo.Dir, ".git", "hooks", "commit-msg")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	runner := &git.ExecRunner{Dir: repo.Dir}
	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })

	c := checkHook(context.Background(), runner, "commit-msg", "gk lint-commit")
	if c.Status != statusFail {
		t.Errorf("status = %s, want FAIL", c.Status)
	}
	if !strings.Contains(c.Fix, "chmod +x") {
		t.Errorf("fix should suggest chmod +x, got %q", c.Fix)
	}
}

// ---------------------------------------------------------------------------
// Unit — checkEditor responds to $EDITOR
// ---------------------------------------------------------------------------

func TestCheckEditor_WithEditorSet(t *testing.T) {
	t.Setenv("GIT_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "sh") // guaranteed on POSIX systems

	c := checkEditor()
	if c.Status != statusPass {
		t.Errorf("expected PASS when $EDITOR=sh, got %s (%s)", c.Status, c.Detail)
	}
}

func TestCheckEditor_Unset(t *testing.T) {
	t.Setenv("GIT_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	c := checkEditor()
	if c.Status != statusWarn {
		t.Errorf("expected WARN when $EDITOR unset, got %s", c.Status)
	}
}

// ---------------------------------------------------------------------------
// Integration — full doctor command
// ---------------------------------------------------------------------------

func buildDoctorCmd(repoDir string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "path to git repo")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "verbose output")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "dry run")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "json output")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "disable color")

	doc := &cobra.Command{
		Use:          "doctor",
		RunE:         runDoctor,
		SilenceUsage: true,
	}
	doc.Flags().Bool("json", false, "emit JSON")
	testRoot.AddCommand(doc)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)

	allArgs := append([]string{"--repo", repoDir, "doctor"}, extraArgs...)
	testRoot.SetArgs(allArgs)
	return testRoot, buf
}

func TestDoctorCmd_Runs(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	root, buf := buildDoctorCmd(repo.Dir)
	_ = root.Execute() // ignore exit — may PASS or 1 FAIL depending on host

	out := buf.String()
	for _, want := range []string{"git version", "pager", "fzf", "editor", "config", "hooks:"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestDoctorCmd_JSON(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "hi\n")
	repo.Commit("init")

	root, buf := buildDoctorCmd(repo.Dir, "--json")
	_ = root.Execute()

	var checks []doctorCheck
	if err := json.Unmarshal(buf.Bytes(), &checks); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, buf.String())
	}
	if len(checks) < 5 {
		t.Errorf("expected >= 5 checks, got %d", len(checks))
	}
	foundGit := false
	for _, c := range checks {
		if c.Name == "git version" {
			foundGit = true
		}
	}
	if !foundGit {
		t.Error("expected 'git version' row in JSON output")
	}
}
