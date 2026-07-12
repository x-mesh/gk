package cli

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// TestWatchCommandRegistered: `gk watch` (alias `gk w`) resolves, and carries
// the full fleet flag set so the shared resolve helpers find their flags.
func TestWatchCommandRegistered(t *testing.T) {
	for _, name := range []string{"watch", "w"} {
		cmd, _, err := rootCmd.Find([]string{name})
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		if cmd.Name() != "watch" {
			t.Errorf("%q resolved to %q, want watch", name, cmd.Name())
		}
	}

	cmd, _, _ := rootCmd.Find([]string{"watch"})
	for _, flag := range []string{"interval", "repos", "scan", "all", "depth", "feed-stats", "events"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("watch is missing fleet flag --%s", flag)
		}
	}
}

// TestResolveFleetRepos_AutoScan: a bare run from a directory that is NOT a
// git repo scans it one level down — the zero-flag `cd ~/work && gk watch`
// entry. Inside a repo the bare run stays single-repo as before.
func TestResolveFleetRepos_AutoScan(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"one", "two"} {
		if out, err := exec.Command("git", "init", "-q", filepath.Join(parent, name)).CombinedOutput(); err != nil {
			t.Fatalf("git init %s: %v: %s", name, err, out)
		}
	}
	prev := flagRepo
	defer func() { flagRepo = prev }()

	cmd := &cobra.Command{}
	addFleetFlags(cmd)

	flagRepo = parent
	ids, multi, err := resolveFleetRepos(context.Background(), cmd)
	if err != nil {
		t.Fatalf("auto-scan: %v", err)
	}
	if !multi || len(ids) != 2 {
		t.Errorf("non-repo cwd should auto-scan depth 1: multi=%v ids=%d", multi, len(ids))
	}

	// Inside one of the repos: bare run stays single-repo.
	flagRepo = filepath.Join(parent, "one")
	_, multi, err = resolveFleetRepos(context.Background(), cmd)
	if err != nil {
		t.Fatalf("in-repo: %v", err)
	}
	if multi {
		t.Error("bare run inside a repo must stay single-repo")
	}
}
