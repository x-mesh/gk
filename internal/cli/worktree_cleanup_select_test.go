package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestBranchUnstartedSince(t *testing.T) {
	const key = "reflog show --date=unix --format=%gd%x00%gs refs/heads/x"
	cases := []struct {
		name   string
		resp   git.FakeResponse
		wantOK bool
	}{
		{"created only", git.FakeResponse{Stdout: "x@{1700000000}\x00branch: Created from HEAD\n"}, true},
		{"moved after creation", git.FakeResponse{Stdout: "x@{1700000100}\x00commit: work\nx@{1700000000}\x00branch: Created from HEAD\n"}, false},
		{"one entry that is not the creation", git.FakeResponse{Stdout: "x@{1700000000}\x00commit: work\n"}, false},
		{"empty reflog", git.FakeResponse{Stdout: ""}, false},
		{"unreadable reflog", git.FakeResponse{Err: errors.New("no reflog")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &git.FakeRunner{Responses: map[string]git.FakeResponse{key: tc.resp}}
			created, ok := branchUnstartedSince(context.Background(), r, "x")
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && !created.Equal(time.Unix(1700000000, 0)) {
				t.Fatalf("created = %v, want the creation entry's time", created)
			}
		})
	}
}

// TestWorktreeCleanup_KeepsUnstartedBranch covers the workspace an agent just
// opened: its branch never moved, so the merge gate alone would remove it.
func TestWorktreeCleanup_KeepsUnstartedBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	root, buf := buildWorktreeCmd(repo.Dir, "add", "--no-init", "-b", filepath.Join(t.TempDir(), "fresh-wt"), "fresh")
	if err := root.Execute(); err != nil {
		t.Fatalf("add worktree: %v\nout: %s", err, buf.String())
	}
	addCleanupWorktree(t, repo.Dir, "worked")

	rep := runCleanupJSON(t, repo.Dir)
	if !cleanupSkippedFor(rep, "fresh", "unstarted") {
		t.Errorf("fresh branch not kept as unstarted: %+v", rep.Skipped)
	}
	if len(rep.Candidates) != 1 || rep.Candidates[0].Branch != "worked" {
		t.Errorf("want only the worked branch as candidate, got %+v", rep.Candidates)
	}
	// A branch created a moment ago is younger than any --stale age.
	if rep := runCleanupJSON(t, repo.Dir, "--stale", "1h"); !cleanupSkippedFor(rep, "fresh", "unstarted") {
		t.Errorf("--stale 1h removed a branch created just now: %+v", rep.Skipped)
	}
}

func cleanupSelectFixture() (worktreeCleanupJSON, []WorktreeEntry) {
	report := worktreeCleanupJSON{
		Candidates: []worktreeCleanupEntry{
			{Path: "/wt/done", Branch: "done", Action: "remove-worktree", Reasons: []string{"clean", "merged"}},
		},
		Skipped: []worktreeCleanupEntry{
			{Path: "/repo", Branch: "feature", Reasons: []string{"current"}},
			{Path: "/wt/open", Branch: "open", Reasons: []string{"clean", "unmerged"}},
			{Path: "/wt/busy", Branch: "busy", Reasons: []string{"locked-live"}},
			{Path: "/wt/main", Branch: "topic", Reasons: []string{"clean", "unmerged"}},
			{Path: "/wt/det", Reasons: []string{"detached"}},
		},
	}
	entries := []WorktreeEntry{
		{Path: "/wt/main", Branch: "topic"},
		{Path: "/repo", Branch: "feature"},
		{Path: "/wt/open", Branch: "open"},
		{Path: "/wt/busy", Branch: "busy", Locked: true},
		{Path: "/wt/done", Branch: "done"},
		{Path: "/wt/det", Detached: true, Head: "0123456789abcdef", Locked: true},
	}
	return report, entries
}

// TestCleanupSelectRows pins who the checkbox list offers: everything git can
// remove, in worktree order, without the current, main, or live-locked ones.
func TestCleanupSelectRows(t *testing.T) {
	report, entries := cleanupSelectFixture()
	rows := cleanupSelectRows(report, entries, "/wt/main")
	var got []string
	for _, r := range rows {
		got = append(got, r.entry.Path)
	}
	if strings.Join(got, ",") != "/wt/open,/wt/done,/wt/det" {
		t.Fatalf("rows = %v, want /wt/open,/wt/done,/wt/det", got)
	}
	if rows[0].candidate || !rows[1].candidate || rows[2].candidate {
		t.Errorf("only /wt/done is a cleanup candidate: %+v", rows)
	}
	if !rows[2].detached || rows[2].head != "0123456789abcdef" {
		t.Errorf("detached row lost its HEAD: %+v", rows[2])
	}
	if !rows[2].entry.Locked {
		t.Error("a locked detached worktree must carry its lock: cleanup returns before its lock check")
	}
}

func TestMainWorktreeOf(t *testing.T) {
	if got := mainWorktreeOf([]WorktreeEntry{{Path: "/repo"}, {Path: "/wt/a"}}); got != "/repo" {
		t.Errorf("main = %q, want the first entry /repo", got)
	}
	if got := mainWorktreeOf([]WorktreeEntry{{Path: "/repo.git", Bare: true}, {Path: "/wt/a"}}); got != "" {
		t.Errorf("a bare main must not let the first linked worktree stand in for it, got %q", got)
	}
}

// TestWorktreeCleanup_PrunableWithoutParentKeepsGates covers a worktree on a
// volume that is not mounted: git marks it prunable, but its directory may
// still exist, so an unmerged branch must stay behind the merge gate.
func TestWorktreeCleanup_PrunableWithoutParentKeepsGates(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	repo.CreateBranch("vol")
	repo.WriteFile("vol.txt", "x\n")
	repo.Commit("unmerged on the volume")
	repo.Checkout("main")
	volume := filepath.Join(t.TempDir(), "volume")
	root, buf := buildWorktreeCmd(repo.Dir, "add", "--no-init", filepath.Join(volume, "vol-wt"), "vol")
	if err := root.Execute(); err != nil {
		t.Fatalf("add worktree: %v\n%s", err, buf.String())
	}
	if err := os.RemoveAll(volume); err != nil {
		t.Fatal(err)
	}
	rep := runCleanupJSON(t, repo.Dir)
	if len(rep.Candidates) != 0 || !cleanupSkippedFor(rep, "vol", "unmerged") {
		t.Fatalf("want the unmerged entry kept as unmerged, got candidates=%+v skipped=%+v", rep.Candidates, rep.Skipped)
	}
}

func TestCleanupSelectItems(t *testing.T) {
	report, entries := cleanupSelectFixture()
	rows := cleanupSelectRows(report, entries, "/wt/main")
	items, preselect := cleanupSelectItems(rows, map[string]int64{"/wt/done": 2048}, cleanupBranchWidth(rows))
	if len(preselect) != 1 || !preselect["/wt/done"] {
		t.Errorf("preselect = %v, want only the candidate /wt/done", preselect)
	}
	want := map[string][]string{
		"/wt/open": {"open", "-", "unmerged"},
		"/wt/done": {"done", "2.0KiB", "merged"},
		"/wt/det":  {"(detached)", "-", "detached"},
	}
	for _, it := range items {
		for _, part := range want[it.Key] {
			if !strings.Contains(it.Display, part) {
				t.Errorf("item %s = %q, missing %q", it.Key, it.Display, part)
			}
		}
	}
}

// TestCleanupSelectLoss covers the two things removing a worktree can lose:
// uncommitted changes and commits that only a detached HEAD holds.
func TestCleanupSelectLoss(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	ctx := context.Background()

	onRef := filepath.Join(t.TempDir(), "on-ref")
	repo.RunGit("worktree", "add", "--detach", onRef)
	offRef := filepath.Join(t.TempDir(), "off-ref")
	repo.RunGit("worktree", "add", "--detach", offRef)
	off := testutil.Attach(t, offRef)
	off.RunGit("commit", "--allow-empty", "-m", "only here")
	dirty := addCleanupWorktree(t, repo.Dir, "dirty-wt")
	if err := os.WriteFile(filepath.Join(dirty, "note.md"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	head := func(p string) string { return testutil.Attach(t, p).RunGit("rev-parse", "HEAD") }
	cases := []struct {
		name string
		row  cleanupSelectRow
		want string
	}{
		{"detached on a branch", cleanupSelectRow{entry: worktreeCleanupEntry{Path: onRef, Reasons: []string{"detached"}}, detached: true, head: head(onRef)}, ""},
		{"detached commit on no ref", cleanupSelectRow{entry: worktreeCleanupEntry{Path: offRef, Reasons: []string{"detached"}}, detached: true, head: head(offRef)}, "is on no branch or tag"},
		{"uncommitted file", cleanupSelectRow{entry: worktreeCleanupEntry{Path: dirty, Branch: "dirty-wt", Reasons: []string{"protected"}}}, "uncommitted changes"},
		{"clean candidate", cleanupSelectRow{entry: worktreeCleanupEntry{Path: onRef, Branch: "x", Action: "remove-worktree"}, candidate: true}, ""},
		{"candidate changed after the verdict", cleanupSelectRow{entry: worktreeCleanupEntry{Path: dirty, Branch: "dirty-wt", Action: "remove-worktree"}, candidate: true}, "made after the list opened"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.row
			got := cleanupSelectLoss(ctx, runner, &r)
			if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
				t.Fatalf("loss = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "uncommitted") && r.entry.Dirty == nil {
				t.Fatal("a dirty row must carry Dirty so the removal forces it")
			}
		})
	}
}

// TestWorktreeCleanup_RemovesSubmoduleWorktree covers a merged worktree that
// checked out a submodule: git refuses a plain remove of it, so cleanup -y
// failed on every such candidate.
func TestWorktreeCleanup_RemovesSubmoduleWorktree(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	sub := testutil.NewRepo(t)
	repo := testutil.NewRepo(t)
	repo.RunGit("-c", "protocol.file.allow=always", "submodule", "add", sub.Dir, "vendor/sub")
	repo.Commit("add submodule")
	wtPath := filepath.Join(t.TempDir(), "sub-wt")
	add, abuf := buildWorktreeCmd(repo.Dir, "add", "--no-init", "-b", wtPath, "feat/sub-wt")
	if err := add.Execute(); err != nil {
		t.Fatalf("add worktree: %v\n%s", err, abuf.String())
	}
	testutil.Attach(t, wtPath).RunGit("-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive")
	startCleanupBranch(t, wtPath)

	rep := runCleanupJSON(t, repo.Dir, "-y")
	if len(rep.Removed) != 1 || len(rep.Failed) != 0 {
		t.Fatalf("want the submodule worktree removed, got removed=%+v failed=%+v", rep.Removed, rep.Failed)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
}

// TestWorktreeCleanup_KeepsSubmoduleWithHiddenChanges covers the forced
// removal of a submodule worktree: `submodule.<name>.ignore` hides changes
// inside the submodule from a plain git status, so the worktree looked clean
// and `submodule deinit --force` deleted the unsaved file.
func TestWorktreeCleanup_KeepsSubmoduleWithHiddenChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	sub := testutil.NewRepo(t)
	repo := testutil.NewRepo(t)
	repo.RunGit("-c", "protocol.file.allow=always", "submodule", "add", sub.Dir, "vendor/sub")
	repo.Commit("add submodule")
	repo.RunGit("config", "submodule.vendor/sub.ignore", "all")
	wtPath := filepath.Join(t.TempDir(), "sub-wt")
	add, abuf := buildWorktreeCmd(repo.Dir, "add", "--no-init", "-b", wtPath, "feat/sub-wt")
	if err := add.Execute(); err != nil {
		t.Fatalf("add worktree: %v\n%s", err, abuf.String())
	}
	testutil.Attach(t, wtPath).RunGit("-c", "protocol.file.allow=always", "submodule", "update", "--init", "--recursive")
	startCleanupBranch(t, wtPath)
	unsaved := filepath.Join(wtPath, "vendor", "sub", "unsaved.txt")
	if err := os.WriteFile(unsaved, []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := runCleanupJSON(t, repo.Dir, "-y")
	if len(rep.Removed) != 0 || len(rep.Failed) != 1 || !strings.Contains(rep.Failed[0].Error, "uncommitted") {
		t.Fatalf("want the worktree kept with an uncommitted-changes error, got removed=%+v failed=%+v", rep.Removed, rep.Failed)
	}
	if _, err := os.Stat(unsaved); err != nil {
		t.Fatalf("the unsaved file inside the submodule is gone: %v", err)
	}
}

func TestDirDiskUsage(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a", "b", filepath.Join("nested", "c")} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, 10000), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	n, ok := dirDiskUsage(context.Background(), dir)
	if !ok || n < 30000 {
		t.Fatalf("usage = %d, ok = %v; want at least the 30000 bytes written", n, ok)
	}
	if _, ok := dirDiskUsage(context.Background(), filepath.Join(dir, "missing")); ok {
		t.Fatal("a missing directory must report ok = false")
	}
}

func TestWorktreeCleanup_InteractiveRejectsConflicts(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-i", "-y"}, "-i and -y cannot be combined"},
		{[]string{"-i", "--global"}, "-i works on one repository"},
		{[]string{"-i"}, "-i needs an interactive terminal"},
	}
	for _, tc := range cases {
		root, buf := buildWorktreeCmd(repo.Dir, "cleanup", tc.args...)
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("cleanup %v: err = %v, want %q\nout: %s", tc.args, err, tc.want, buf.String())
		}
	}
}

func TestBranchCreatedAt(t *testing.T) {
	const key = "reflog show --date=unix --format=%gd%x00%gs refs/heads/x"
	cases := []struct {
		name   string
		out    string
		wantOK bool
	}{
		{"creation is the oldest entry", "x@{1700000300}\x00commit: more\nx@{1700000000}\x00branch: Created from main\n", true},
		{"expiry dropped the creation", "x@{1700000300}\x00commit: more\nx@{1700000100}\x00commit: work\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &git.FakeRunner{Responses: map[string]git.FakeResponse{key: {Stdout: tc.out}}}
			at, ok := branchCreatedAt(context.Background(), r, "x")
			if ok != tc.wantOK || (ok && !at.Equal(time.Unix(1700000000, 0))) {
				t.Fatalf("got (%v, %v), want ok=%v at the creation entry", at, ok, tc.wantOK)
			}
		})
	}
}

// TestCleanupSelectStateAndDetail pins what a row tells the user: the verdict
// with its numbers, uncommitted work, whether the branch is on the remote,
// and, in the detail box, when it was worked on.
func TestCleanupSelectStateAndDetail(t *testing.T) {
	now := time.Now()
	unmerged := cleanupSelectRow{
		entry: worktreeCleanupEntry{Path: "/wt/feat", Branch: "feat", Reasons: []string{"clean", "unmerged"}},
		info: cleanupRowInfo{commitAt: now.Add(-3 * time.Hour), subject: "fix: retry", createdAt: now.Add(-72 * time.Hour),
			target: "develop", ahead: 3, behind: 18, upstream: "origin/feat", unpushed: 2},
	}
	cases := []struct {
		name  string
		row   cleanupSelectRow
		state []string
	}{
		{"unmerged with unpushed commits", unmerged, []string{"unmerged ↑3", "↑2 unpushed"}},
		{"merged and pushed", cleanupSelectRow{candidate: true,
			entry: worktreeCleanupEntry{Path: "/wt/done", Branch: "done", Reasons: []string{"clean", "merged"}},
			info:  cleanupRowInfo{upstream: "origin/done"}}, []string{"merged", "pushed"}},
		{"unstarted", cleanupSelectRow{
			entry: worktreeCleanupEntry{Path: "/wt/new", Branch: "new", Reasons: []string{"clean", "unstarted"}},
			info:  cleanupRowInfo{createdAt: now.Add(-2 * time.Hour)}}, []string{"unstarted, created 2h ago", "not pushed"}},
		{"dirty with a gone upstream", cleanupSelectRow{
			entry: worktreeCleanupEntry{Path: "/wt/wip", Branch: "wip", Reasons: []string{"dirty"}, Dirty: &contextDirtyJSON{Unstaged: 2}},
			info:  cleanupRowInfo{upstream: "origin/wip", gone: true}}, []string{"dirty: 2 modified", "upstream gone"}},
		{"prunable", cleanupSelectRow{entry: worktreeCleanupEntry{Path: "/wt/gone", Branch: "gone", Reasons: []string{"prunable"}}, candidate: true},
			[]string{"prunable: directory gone"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanupSelectState(tc.row)
			for _, want := range tc.state {
				if !strings.Contains(got, want) {
					t.Errorf("state = %q, missing %q", got, want)
				}
			}
		})
	}

	detail := cleanupSelectDetail(unmerged)
	if lines := strings.Split(detail, "\n"); len(lines) != 3 {
		t.Fatalf("detail must be 3 lines so the box keeps its height, got %d:\n%s", len(lines), detail)
	}
	for _, want := range []string{"feat  /wt/feat", "last commit 3h ago \"fix: retry\"", "created 3d ago", "vs develop: ↑3 ↓18", "↑2 unpushed", "no uncommitted changes"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail missing %q:\n%s", want, detail)
		}
	}
}

func TestSortCleanupRows(t *testing.T) {
	now := time.Now()
	rows := []cleanupSelectRow{
		{entry: worktreeCleanupEntry{Path: "/c", Branch: "c", Dirty: &contextDirtyJSON{Unstaged: 1}}, info: cleanupRowInfo{commitAt: now.Add(-1 * time.Hour)}},
		{entry: worktreeCleanupEntry{Path: "/a", Branch: "a"}, info: cleanupRowInfo{commitAt: now.Add(-48 * time.Hour)}},
		{entry: worktreeCleanupEntry{Path: "/b", Branch: "b"}, candidate: true},
	}
	sizes := map[string]int64{"/a": 10, "/b": 30, "/c": 20}
	want := map[string]string{
		"safe first":    "b,a,c",
		"oldest commit": "a,c,b",
		"largest":       "b,c,a",
		"name":          "a,b,c",
	}
	for mode, m := range cleanupSortModes {
		var got []string
		for _, r := range sortCleanupRows(rows, mode, sizes) {
			got = append(got, r.entry.Branch)
		}
		if strings.Join(got, ",") != want[m.name] {
			t.Errorf("%s: order = %v, want %s", m.name, got, want[m.name])
		}
	}
	if rows[0].entry.Branch != "c" {
		t.Error("sorting must not reorder the caller's rows")
	}
}

// TestLoadCleanupRowInfo reads the details from a real repository: the last
// commit, the creation from the reflog, and the distance to the base.
func TestLoadCleanupRowInfo(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	repo := testutil.NewRepo(t)
	wtPath := filepath.Join(t.TempDir(), "ahead-wt")
	root, buf := buildWorktreeCmd(repo.Dir, "add", "--no-init", "-b", wtPath, "ahead")
	if err := root.Execute(); err != nil {
		t.Fatalf("add worktree: %v\n%s", err, buf.String())
	}
	testutil.Attach(t, wtPath).RunGit("commit", "--allow-empty", "-m", "ahead work")

	rows := []cleanupSelectRow{{entry: worktreeCleanupEntry{Path: wtPath, Branch: "ahead", Reasons: []string{"clean", "unmerged"}}}}
	loadCleanupRowInfo(context.Background(), &git.ExecRunner{Dir: repo.Dir}, nil, rows)
	info := rows[0].info
	if info.subject != "ahead work" || info.commitAt.IsZero() || info.createdAt.IsZero() {
		t.Errorf("commit and creation not read: %+v", info)
	}
	if info.target != "main" || info.ahead != 1 || info.behind != 0 {
		t.Errorf("distance to main = %s ↑%d ↓%d, want main ↑1 ↓0", info.target, info.ahead, info.behind)
	}
}
