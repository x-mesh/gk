package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestCountContextDirty(t *testing.T) {
	fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"status --porcelain": {Stdout: "M  staged.go\n M unstaged.go\nMM both.go\n?? new.txt\nUU conflict.go\nAA both-added.go\n"},
	}}
	d := countContextDirty(context.Background(), fake)
	if d.Staged != 2 || d.Unstaged != 2 || d.Untracked != 1 || d.Conflicts != 2 {
		t.Errorf("dirty = %+v, want staged=2 unstaged=2 untracked=1 conflicts=2", d)
	}
}

func TestContextNextActions(t *testing.T) {
	cases := []struct {
		name string
		c    contextJSON
		want string
	}{
		{"in-progress rebase wins", contextJSON{
			InProgress: &contextOpJSON{Kind: "rebase", Resume: "gk continue", Abort: "gk abort"},
			Dirty:      contextDirtyJSON{Conflicts: 2},
		}, "gk resolve --ai,gk continue,gk abort"},
		{"dirty then sync", contextJSON{
			Dirty: contextDirtyJSON{Unstaged: 1}, Behind: 2, Ahead: 1,
		}, "gk commit,gk pull,gk push"},
		{"base drift", contextJSON{
			Base: &contextBaseJSON{Name: "main", BehindRemote: 3},
		}, "gk pull --with-base"},
		{"clean and synced", contextJSON{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(contextNextActions(tc.c), ",")
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIntegration_CollectContext(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	upstream := testutil.NewRepo(t)
	upstream.WriteFile("seed.txt", "seed\n")
	upstream.Commit("seed: initial")

	downstream := testutil.NewRepo(t)
	downstream.AddRemote("origin", upstream.Dir)
	downstream.RunGit("fetch", "origin")
	downstream.SetRemoteHEAD("origin", "main")
	downstream.RunGit("reset", "--hard", "origin/main")
	downstream.RunGit("branch", "--set-upstream-to=origin/main", "main")
	downstream.WriteFile("local.txt", "x\n")
	downstream.Commit("feat: local work")
	downstream.WriteFile("wip.txt", "wip\n") // untracked

	prev := flagRepo
	flagRepo = downstream.Dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: downstream.Dir}
	cfg := config.Defaults()
	got, err := collectContext(context.Background(), runner, &cfg)
	if err != nil {
		t.Fatalf("collectContext: %v", err)
	}
	if got.Schema != 1 || got.Branch != "main" || got.Upstream != "origin/main" {
		t.Errorf("identity fields: %+v", got)
	}
	if got.Ahead != 1 || got.Behind != 0 {
		t.Errorf("ahead/behind = %d/%d, want 1/0", got.Ahead, got.Behind)
	}
	if got.Dirty.Untracked != 1 {
		t.Errorf("untracked = %d, want 1", got.Dirty.Untracked)
	}
	joined := strings.Join(got.NextActions, ",")
	if !strings.Contains(joined, "gk commit") || !strings.Contains(joined, "gk push") {
		t.Errorf("next_actions = %v", got.NextActions)
	}
}
