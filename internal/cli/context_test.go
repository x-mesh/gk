package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

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

func TestParseContextIncludes(t *testing.T) {
	mk := func(vals ...string) *cobra.Command {
		cmd := &cobra.Command{}
		cmd.Flags().StringSlice("include", nil, "")
		if len(vals) > 0 {
			_ = cmd.Flags().Set("include", strings.Join(vals, ","))
		}
		return cmd
	}

	if got, err := parseContextIncludes(mk()); err != nil || len(got) != 0 {
		t.Errorf("no flag: got %v, %v", got, err)
	}
	if got, err := parseContextIncludes(mk("diff", "log")); err != nil || !got["diff"] || !got["log"] || got["precheck"] {
		t.Errorf("diff,log: got %v, %v", got, err)
	}
	if got, err := parseContextIncludes(mk("all")); err != nil || len(got) != len(contextIncludeValues) || !got["release"] || !got["conflict"] {
		t.Errorf("all: got %v, %v", got, err)
	}
	if _, err := parseContextIncludes(mk("digest")); err == nil || !strings.Contains(err.Error(), "unknown --include") {
		t.Errorf("typo must be a usage error, got %v", err)
	}
}

func TestParseContextConflictStatus(t *testing.T) {
	raw := strings.Join([]string{
		"u UU N... 100644 100644 100644 100644 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb cccccccccccccccccccccccccccccccccccccccc path with space.go",
		"1 .M N... 100644 100644 100644 dddddddddddddddddddddddddddddddddddddddd dddddddddddddddddddddddddddddddddddddddd clean.go",
	}, "\x00") + "\x00"

	got := parseContextConflictStatus([]byte(raw))
	if len(got) != 1 {
		t.Fatalf("conflicts = %+v, want 1", got)
	}
	if got[0].Path != "path with space.go" || got[0].XY != "UU" {
		t.Fatalf("file = %+v", got[0])
	}
	if len(got[0].Stages) != 3 || got[0].Stages[0].Role != "base" || !got[0].Stages[2].Present {
		t.Fatalf("stages = %+v", got[0].Stages)
	}
}

func TestIntegration_ContextIncludes(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.go", "package a\n\nfunc A() int { return 1 }\n")
	repo.Commit("feat: seed")
	repo.WriteFile("a.go", "package a\n\nfunc A() int { return 2 }\n") // unstaged change
	repo.WriteFile("new.txt", "one\ntwo\n")                            // untracked

	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	out, err := collectContext(context.Background(), runner, &cfg)
	if err != nil {
		t.Fatalf("collectContext: %v", err)
	}
	includes := map[string]bool{"diff": true, "log": true, "precheck": true, "conflict": true}
	collectContextIncludes(context.Background(), runner, &cfg, includes, &out)

	if out.Diff == nil || out.Diff.Stat.Files != 2 {
		t.Fatalf("diff section = %+v, want tracked + untracked = 2 files", out.Diff)
	}
	if out.Diff.Files[0].Path != "a.go" {
		t.Errorf("diff file = %+v", out.Diff.Files[0])
	}
	ut := out.Diff.Files[1]
	if ut.Path != "new.txt" || ut.Status != "untracked" || ut.Added != 2 {
		t.Errorf("untracked entry = %+v, want new.txt untracked +2", ut)
	}
	if len(out.Log) != 2 || out.Log[0].Subject != "feat: seed" {
		t.Errorf("log section = %+v", out.Log)
	}
	if out.Log[0].SHA == "" || out.Log[0].Date == "" {
		t.Errorf("log entry incomplete: %+v", out.Log[0])
	}
	if out.Conflict == nil || out.Conflict.Count != 0 || len(out.Conflict.Files) != 0 {
		t.Errorf("conflict section = %+v, want empty section", out.Conflict)
	}
	// No remote/upstream in this repo: precheck must degrade to a note,
	// never fail the call.
	if out.Precheck != nil {
		t.Errorf("precheck should be absent without upstream, got %+v", out.Precheck)
	}
	found := false
	for _, n := range out.Notes {
		if strings.Contains(n, "precheck skipped") {
			found = true
		}
	}
	if !found {
		t.Errorf("notes = %v, want a precheck-skipped note", out.Notes)
	}
}

func TestIntegration_ContextConflictIncludeMerge(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("f.txt", "base\n")
	repo.Commit("feat: base")
	repo.CreateBranch("side")
	repo.WriteFile("f.txt", "side\n")
	repo.Commit("feat: side")
	repo.Checkout("main")
	repo.WriteFile("f.txt", "main\n")
	repo.Commit("feat: main")
	if _, err := repo.TryGit("merge", "side"); err == nil {
		t.Fatal("merge should conflict")
	}

	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	out, err := collectContext(context.Background(), runner, &cfg)
	if err != nil {
		t.Fatalf("collectContext: %v", err)
	}
	collectContextIncludes(context.Background(), runner, &cfg, map[string]bool{"conflict": true}, &out)

	if out.Conflict == nil {
		t.Fatalf("conflict section missing, notes=%v", out.Notes)
	}
	if out.Conflict.Operation != "merge" || out.Conflict.Count != 1 {
		t.Fatalf("conflict summary = %+v", out.Conflict)
	}
	file := out.Conflict.Files[0]
	if file.Path != "f.txt" || file.XY != "UU" || file.Kind != "both modified" {
		t.Fatalf("conflict file = %+v", file)
	}
	if file.Hunks != 1 || !file.Markers || !file.WorktreeExists {
		t.Fatalf("conflict anatomy = %+v", file)
	}
	if len(file.Stages) != 3 || file.Stages[0].Role != "base" || file.Stages[1].Role != "ours" || file.Stages[2].Role != "theirs" {
		t.Fatalf("stages = %+v", file.Stages)
	}
}

func TestIntegration_ContextConflictIncludeStashApply(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("f.txt", "base\n")
	repo.Commit("feat: base")
	repo.WriteFile("f.txt", "stashed\n")
	repo.RunGit("stash", "push", "-m", "wip")
	repo.WriteFile("f.txt", "current\n")
	repo.Commit("feat: current")
	if _, err := repo.TryGit("stash", "pop"); err == nil {
		t.Fatal("stash pop should conflict")
	}

	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	out, err := collectContext(context.Background(), runner, &cfg)
	if err != nil {
		t.Fatalf("collectContext: %v", err)
	}
	collectContextIncludes(context.Background(), runner, &cfg, map[string]bool{"conflict": true}, &out)

	if out.Conflict == nil {
		t.Fatalf("conflict section missing, notes=%v", out.Notes)
	}
	if out.Conflict.Operation != "stash-apply-conflict" {
		t.Fatalf("operation = %q, want stash-apply-conflict", out.Conflict.Operation)
	}
	gotActions := strings.Join(out.NextActions, ",")
	if strings.Contains(gotActions, "continue") {
		t.Fatalf("stash conflict should not suggest continue: %v", out.NextActions)
	}
	if !strings.Contains(gotActions, "resolve --ai") {
		t.Fatalf("next_actions = %v, want resolve", out.NextActions)
	}
}

func TestIntegration_ContextRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "seed\n")
	repo.Commit("feat: seed")
	repo.RunGit("tag", "v1.0.0")
	repo.WriteFile("b.txt", "b\n")
	repo.Commit("feat: first since tag")
	repo.WriteFile("c.txt", "c\n")
	repo.Commit("fix: second since tag")

	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	out, err := collectContext(context.Background(), runner, &cfg)
	if err != nil {
		t.Fatalf("collectContext: %v", err)
	}
	collectContextIncludes(context.Background(), runner, &cfg, map[string]bool{"release": true}, &out)

	if out.Release == nil {
		t.Fatalf("release section missing, notes=%v", out.Notes)
	}
	if out.Release.SinceTag != "v1.0.0" {
		t.Errorf("since_tag = %q, want v1.0.0", out.Release.SinceTag)
	}
	if out.Release.CommitCount != 2 {
		t.Errorf("commit_count = %d, want 2", out.Release.CommitCount)
	}
	if len(out.Release.Commits) != 2 {
		t.Fatalf("commits = %+v, want 2 entries", out.Release.Commits)
	}
	// log order is newest-first.
	if out.Release.Commits[0].Subject != "fix: second since tag" {
		t.Errorf("newest commit = %+v, want 'fix: second since tag'", out.Release.Commits[0])
	}
	if out.Release.Commits[0].SHA == "" || out.Release.Commits[0].Date == "" {
		t.Errorf("commit entry incomplete: %+v", out.Release.Commits[0])
	}
}

// A repo with no tags: the release section must degrade to a note, never
// report the whole history as "unreleased" or fail the call.
func TestIntegration_ContextReleaseNoTags(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "seed\n")
	repo.Commit("feat: seed")

	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: repo.Dir}
	cfg := config.Defaults()
	out, err := collectContext(context.Background(), runner, &cfg)
	if err != nil {
		t.Fatalf("collectContext: %v", err)
	}
	collectContextIncludes(context.Background(), runner, &cfg, map[string]bool{"release": true}, &out)

	if out.Release != nil {
		t.Errorf("release should be absent without tags, got %+v", out.Release)
	}
	found := false
	for _, n := range out.Notes {
		if strings.Contains(n, "release skipped") {
			found = true
		}
	}
	if !found {
		t.Errorf("notes = %v, want a release-skipped note", out.Notes)
	}
}

func TestIntegration_ContextRemotes(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	origin := testutil.NewRepo(t)
	origin.WriteFile("seed.txt", "seed\n")
	origin.Commit("seed: initial")

	local := testutil.NewRepo(t)
	local.AddRemote("origin", origin.Dir)
	local.RunGit("fetch", "origin")
	local.RunGit("reset", "--hard", "origin/main")
	// Asymmetric pushurl on origin — the deployer-bot footgun shape.
	local.RunGit("remote", "set-url", "--add", "--push", "origin", origin.Dir)
	local.RunGit("remote", "set-url", "--add", "--push", "origin", "/elsewhere/mirror.git")

	// Second remote, one commit ahead, fetched.
	mirror := testutil.NewRepo(t)
	mirror.AddRemote("seedsrc", origin.Dir)
	mirror.RunGit("fetch", "seedsrc")
	mirror.RunGit("reset", "--hard", "seedsrc/main")
	mirror.WriteFile("m.txt", "m\n")
	mirror.Commit("feat: mirror work")
	local.AddRemote("tape42", mirror.Dir)
	local.RunGit("fetch", "tape42")

	prev := flagRepo
	flagRepo = local.Dir
	t.Cleanup(func() { flagRepo = prev })

	runner := &git.ExecRunner{Dir: local.Dir}
	remotes, err := collectContextRemotes(context.Background(), runner, "main", false)
	if err != nil {
		t.Fatalf("collectContextRemotes: %v", err)
	}
	if len(remotes) != 2 {
		t.Fatalf("remotes = %+v, want origin + tape42", remotes)
	}
	byName := map[string]contextRemoteJSON{}
	for _, r := range remotes {
		byName[r.Name] = r
	}
	o := byName["origin"]
	if !o.Fetched || o.Ahead != 0 || o.Behind != 0 {
		t.Errorf("origin = %+v, want fetched in sync", o)
	}
	if len(o.PushURLs) != 1 || o.PushURLs[0] != "/elsewhere/mirror.git" {
		t.Errorf("origin push_urls = %v, want the asymmetric extra only", o.PushURLs)
	}
	tp := byName["tape42"]
	if !tp.Fetched || tp.Behind != 1 || tp.Ahead != 0 {
		t.Errorf("tape42 = %+v, want behind=1 (mirror-side commit visible)", tp)
	}
}

// TestIntegration_ContextDiffUnbornHEAD covers the freshly-initialized repo:
// no commit yet, so HEAD does not resolve. The diff section must still
// honor its "untracked included" contract (staged files report against the
// empty tree) instead of degrading to a note.
func TestIntegration_ContextDiffUnbornHEAD(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	dir := t.TempDir()
	runner := &git.ExecRunner{Dir: dir}
	ctx := context.Background()
	if _, _, err := runner.Run(ctx, "init", "-q"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	writeContextTestFile(t, dir, "staged.go", "package a\n\nfunc S() {}\n")
	writeContextTestFile(t, dir, "loose.txt", "one\ntwo\nthree\n")
	if _, _, err := runner.Run(ctx, "add", "staged.go"); err != nil {
		t.Fatalf("git add: %v", err)
	}

	dg, err := collectContextDiff(ctx, runner)
	if err != nil {
		t.Fatalf("collectContextDiff on unborn HEAD: %v", err)
	}
	if dg.Stat.Files != 2 {
		t.Fatalf("digest = %+v, want staged + untracked = 2 files", dg)
	}
	byPath := map[string]diffDigestFileJSON{}
	for _, f := range dg.Files {
		byPath[f.Path] = f
	}
	if s, ok := byPath["staged.go"]; !ok || s.Added == 0 {
		t.Errorf("staged file missing or empty against the empty tree: %+v", byPath)
	}
	if u, ok := byPath["loose.txt"]; !ok || u.Status != "untracked" || u.Added != 3 {
		t.Errorf("untracked entry = %+v, want loose.txt untracked +3", byPath["loose.txt"])
	}
}

func writeContextTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
