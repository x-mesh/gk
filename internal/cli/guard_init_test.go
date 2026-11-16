package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// --force replaces .gk.yaml with the scaffold. The file is the shared project
// config, so its old content goes to a backup first.
func TestGuardInit_ForceBacksUpExistingConfig(t *testing.T) {
	repo := testutil.NewRepo(t)
	cfg := filepath.Join(repo.Dir, ".gk.yaml")
	for name, prior := range map[string]string{
		"valid":   "ai:\n  commit:\n    trailer: true\n",
		"invalid": "{{not yaml",
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(cfg, []byte(prior), 0o644); err != nil {
				t.Fatal(err)
			}
			before, _ := filepath.Glob(cfg + ".bak.*")
			out, err := runGuardInitCmd(t, repo.Dir, repo.Dir, "--force")
			if err != nil {
				t.Fatalf("guard init --force: %v", err)
			}
			after, _ := filepath.Glob(cfg + ".bak.*")
			if len(after) != len(before)+1 {
				t.Fatalf("backups = %v, want one more than %v", after, before)
			}
			bak := after[len(after)-1]
			if got, err := os.ReadFile(bak); err != nil || string(got) != prior {
				t.Fatalf("backup %s = %q, %v; want %q", bak, got, err, prior)
			}
			if !strings.Contains(out, bak) {
				t.Errorf("output %q does not name the backup %s", out, bak)
			}
			if got, err := os.ReadFile(cfg); err != nil || string(got) != gkYAMLTemplate {
				t.Errorf(".gk.yaml = %q, %v; want the scaffold", got, err)
			}
			for _, b := range after {
				_ = os.Remove(b)
			}
		})
	}
}

// A backup that already has the name is kept, and the command fails.
func TestGuardInit_ForceKeepsAnExistingBackup(t *testing.T) {
	repo := testutil.NewRepo(t)
	cfg := filepath.Join(repo.Dir, ".gk.yaml")
	if err := os.WriteFile(cfg, []byte("ai: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var taken []string
	now := time.Now().Unix()
	for sec := now; sec <= now+2; sec++ {
		p := fmt.Sprintf("%s.bak.%d", cfg, sec)
		if err := os.WriteFile(p, []byte("first backup"), 0o644); err != nil {
			t.Fatal(err)
		}
		taken = append(taken, p)
	}
	if _, err := runGuardInitCmd(t, repo.Dir, repo.Dir, "--force"); err == nil {
		t.Fatal("want an error when the backup name is taken")
	}
	for _, p := range taken {
		if got, _ := os.ReadFile(p); string(got) != "first backup" {
			t.Errorf("%s = %q, want it unchanged", p, got)
		}
	}
	if got, _ := os.ReadFile(cfg); string(got) != "ai: {}\n" {
		t.Errorf(".gk.yaml = %q, want it unchanged", got)
	}
}

// --force replaces .gk.yaml with a renamed file instead of truncating it in
// place, so a failed write cannot leave the config half written. A hard link
// to the old file shows the difference: it keeps the old content.
func TestGuardInit_ForceReplacesConfigAtomically(t *testing.T) {
	repo := testutil.NewRepo(t)
	cfg := filepath.Join(repo.Dir, ".gk.yaml")
	const prior = "ai: {}\n"
	if err := os.WriteFile(cfg, []byte(prior), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "old.yaml")
	if err := os.Link(cfg, link); err != nil {
		t.Skipf("hard link: %v", err)
	}
	if _, err := runGuardInitCmd(t, repo.Dir, repo.Dir, "--force"); err != nil {
		t.Fatalf("guard init --force: %v", err)
	}
	if got, _ := os.ReadFile(link); string(got) != prior {
		t.Errorf("old file = %q, want %q: .gk.yaml was rewritten in place", got, prior)
	}
	if got, _ := os.ReadFile(cfg); string(got) != gkYAMLTemplate {
		t.Errorf(".gk.yaml = %q, want the scaffold", got)
	}
	if fi, err := os.Stat(cfg); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf(".gk.yaml mode = %v, %v; want 0600 kept", fi.Mode().Perm(), err)
	}
	if tmp, _ := filepath.Glob(filepath.Join(repo.Dir, "..gk.yaml.tmp-*")); len(tmp) != 0 {
		t.Errorf("temp files left: %v", tmp)
	}
}

// A symlinked .gk.yaml keeps the link: --force writes the scaffold to the
// target, as it did before the write became a rename.
func TestGuardInit_ForceFollowsSymlinkedConfig(t *testing.T) {
	repo := testutil.NewRepo(t)
	target := filepath.Join(t.TempDir(), "shared.yaml")
	if err := os.WriteFile(target, []byte("ai: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(repo.Dir, ".gk.yaml")
	if err := os.Symlink(target, cfg); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := runGuardInitCmd(t, repo.Dir, repo.Dir, "--force"); err != nil {
		t.Fatalf("guard init --force: %v", err)
	}
	if fi, err := os.Lstat(cfg); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf(".gk.yaml is no longer a symlink: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != gkYAMLTemplate {
		t.Errorf("target = %q, want the scaffold", got)
	}
}
