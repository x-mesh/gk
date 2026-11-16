package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/testutil"
)

func runGuardInitCmd(t *testing.T, repoRoot, workDir string, args ...string) (string, error) {
	t.Helper()
	prev := os.Getenv("GIT_CEILING_DIRECTORIES")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(repoRoot))
	t.Cleanup(func() { _ = os.Setenv("GIT_CEILING_DIRECTORIES", prev) })

	prevRepo := flagRepo
	flagRepo = ""
	t.Cleanup(func() { flagRepo = prevRepo })

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	cmd := newGuardInitCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(cmd.OutOrStdout())
	cmd.SetContext(context.Background())
	cmd.SetArgs(args)
	err = cmd.Execute()
	out := cmd.OutOrStdout().(*bytes.Buffer).String()
	return out, err
}

func newGuardInitCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "init", RunE: runGuardInit}
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().String("out", "", "")
	return cmd
}

func TestGuardInit_WritesRepoRootFromSubdirectory(t *testing.T) {
	repo := testutil.NewRepo(t)
	sub := filepath.Join(repo.Dir, "pkg", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := runGuardInitCmd(t, repo.Dir, sub); err != nil {
		t.Fatalf("guard init: %v", err)
	}
	cfg := filepath.Join(repo.Dir, ".gk.yaml")
	if _, err := os.Stat(cfg); err != nil {
		t.Fatalf("expected %s: %v", cfg, err)
	}
	if _, err := os.Stat(filepath.Join(sub, ".gk.yaml")); err == nil {
		t.Fatal("expected no .gk.yaml in subdirectory")
	}
}

func TestGuardInit_ForceMergesPoliciesPreservesOtherKeys(t *testing.T) {
	repo := testutil.NewRepo(t)
	cfg := filepath.Join(repo.Dir, ".gk.yaml")
	const prior = "ai:\n  commit:\n    trailer: true\npolicies:\n  secret_patterns:\n    enabled: true\n"
	if err := os.WriteFile(cfg, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGuardInitCmd(t, repo.Dir, repo.Dir, "--force"); err != nil {
		t.Fatalf("guard init --force: %v", err)
	}
	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "trailer: true") {
		t.Fatalf("ai.commit.trailer missing after --force:\n%s", text)
	}
	if strings.Contains(text, "secret_patterns:\n    enabled: true") {
		t.Fatalf("policies block should be replaced by scaffold:\n%s", text)
	}
	if !strings.Contains(text, "policies:") {
		t.Fatalf("policies key missing:\n%s", text)
	}
}

func TestGuardInit_ForceBacksUpWhenYAMLInvalid(t *testing.T) {
	repo := testutil.NewRepo(t)
	cfg := filepath.Join(repo.Dir, ".gk.yaml")
	const broken = "{{not yaml"
	if err := os.WriteFile(cfg, []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runGuardInitCmd(t, repo.Dir, repo.Dir, "--force")
	if err != nil {
		t.Fatalf("guard init --force: %v", err)
	}
	if !strings.Contains(out, ".gk.yaml.bak") {
		t.Fatalf("expected backup notice, got %q", out)
	}
	bak := cfg + ".bak"
	prev, err := os.ReadFile(bak)
	if err != nil {
		t.Fatal(err)
	}
	if string(prev) != broken {
		t.Fatalf("backup content mismatch")
	}
	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "policies:") {
		t.Fatalf("expected full scaffold after backup path:\n%s", got)
	}
}
