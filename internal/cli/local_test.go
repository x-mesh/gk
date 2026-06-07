package cli

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestLocalChangeBadge(t *testing.T) {
	prevNoColor := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prevNoColor })

	cases := []struct {
		name    string
		entries []git.StatusEntry
		want    string
	}{
		{name: "clean tree → empty", entries: nil, want: ""},
		{
			name: "modified only",
			entries: []git.StatusEntry{
				{Kind: git.KindOrdinary, XY: ".M", Path: "a"},
			},
			want: "· 1 unstaged",
		},
		{
			name: "all layers",
			entries: []git.StatusEntry{
				{Kind: git.KindOrdinary, XY: ".M", Path: "a"},  // unstaged
				{Kind: git.KindUntracked, XY: "??", Path: "b"}, // unstaged
				{Kind: git.KindOrdinary, XY: "M.", Path: "c"},  // staged
				{Kind: git.KindUnmerged, XY: "UU", Path: "d"},  // conflict
			},
			want: "· 2 unstaged · 1 staged · 1 conflicts",
		},
		{
			name: "submodules excluded",
			entries: []git.StatusEntry{
				{Kind: git.KindSubmodule, XY: ".M", Path: "sub"},
			},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := localChangeBadge(&git.Status{Entries: tc.entries})
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestUnpushedCommits_NoUpstreamButRemotes verifies the --remotes fallback for
// gk local: a branch with no upstream but a remote present lists its
// local-only commits.
func TestUnpushedCommits_NoUpstreamButRemotes(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", bareDir).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v\n%s", err, out)
	}
	r.RunGit("remote", "add", "origin", bareDir)
	r.RunGit("push", "-q", "origin", "main")

	r.CreateBranch("feature")
	r.WriteFile("x.txt", "x\n")
	r.Commit("local only 1")
	r.WriteFile("y.txt", "y\n")
	r.Commit("local only 2")

	runner := &git.ExecRunner{Dir: r.Dir}
	commits, ok := unpushedCommits(context.Background(), runner, 10)
	if !ok {
		t.Fatal("remote present → expected ok=true")
	}
	if len(commits) != 2 {
		t.Fatalf("want 2 unpushed commits, got %d (%+v)", len(commits), commits)
	}
	if !strings.Contains(commits[0].Subject, "local only 2") {
		t.Errorf("newest-first expected; got first=%q", commits[0].Subject)
	}
}

// TestUnpushedCommits_NoRemote: a repo with no remotes at all cannot determine
// push state — ok must be false so gk local prints "no remote to compare".
func TestUnpushedCommits_NoRemote(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	r := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}
	if _, ok := unpushedCommits(context.Background(), runner, 10); ok {
		t.Error("no remote → expected ok=false")
	}
}
