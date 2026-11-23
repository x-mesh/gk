package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
	"github.com/x-mesh/gk/internal/testutil"
)

// unreleasedRepo has origin/main one squash commit (#9) after v1.0.0, and
// origin/develop with a merged PR (#7) and a squashed PR (#8) on top of it.
// origin/HEAD names main, as after a clone. A branch that no PR was merged into
// is checked out.
func unreleasedRepo(t *testing.T, tag bool) *testutil.Repo {
	t.Helper()
	r := testutil.NewRepo(t)
	squashPRCommit(r, "feat: released (#1)")
	if tag {
		r.RunGit("tag", "v1.0.0")
	}
	squashPRCommit(r, "fix: hotfix on main (#9)")
	r.RunGit("update-ref", "refs/remotes/origin/main", "main")
	r.SetRemoteHEAD("origin", "main")
	r.CreateBranch("develop")
	mergePRCommit(r, 7, "fix/a", "fix: a merged title")
	squashPRCommit(r, "perf: b (#8)")
	r.RunGit("update-ref", "refs/remotes/origin/develop", "develop")
	r.Checkout("main")
	r.CreateBranch("feat/elsewhere")
	return r
}

func unreleasedSummary(groups []dashUnreleasedGroup) string {
	parts := make([]string, len(groups))
	for i, g := range groups {
		parts[i] = fmt.Sprintf("%s since %s: %s", g.Base, g.Since, releasePRNumbers(g.PRs))
	}
	return strings.Join(parts, " | ")
}

func TestCollectDashUnreleased(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	merged := []ghapi.DashPR{{Number: 7, BaseRef: "develop"}, {Number: 8, BaseRef: "develop"}, {Number: 30, BaseRef: "gone"}, {Number: 9, BaseRef: "main"}}

	t.Run("after the latest tag, for each base", func(t *testing.T) {
		r := unreleasedRepo(t, true)
		groups, notes := collectDashUnreleased(context.Background(), &git.ExecRunner{Dir: r.Dir}, "origin", "", merged)
		// main is the release branch, from origin/HEAD. develop leaves out #9,
		// which main holds. "gone" has no remote branch.
		if got, want := unreleasedSummary(groups), "main since v1.0.0: 9 | develop since v1.0.0: 8 7"; got != want {
			t.Errorf("groups = %s\nwant     %s", got, want)
		}
		if len(notes) != 0 {
			t.Errorf("notes = %v", notes)
		}
		// Every commit of the range counts, with or without a PR: develop has
		// the merge commit of #7, the commit of its branch, and #8.
		if groups[0].Commits != 1 || groups[1].Commits != 3 {
			t.Errorf("commits = %d and %d, want 1 and 3", groups[0].Commits, groups[1].Commits)
		}
	})

	t.Run("a PR shows under the first branch that holds it", func(t *testing.T) {
		r := unreleasedRepo(t, true)
		r.Checkout("develop")
		r.CreateBranch("feat/stack")
		squashPRCommit(r, "feat: stacked (#10)")
		r.RunGit("update-ref", "refs/remotes/origin/feat/stack", "feat/stack")
		// #8 and #7 are not on the merged page, so GitHub does not name their
		// base. feat/stack holds them too.
		stacked := []ghapi.DashPR{{Number: 10, BaseRef: "feat/stack"}, {Number: 20, BaseRef: "develop"}, {Number: 21, BaseRef: "develop"}}
		groups, _ := collectDashUnreleased(context.Background(), &git.ExecRunner{Dir: r.Dir}, "origin", "", stacked)
		// develop has more merged PRs, so it is read first and keeps them.
		if got, want := unreleasedSummary(groups), "main since v1.0.0: 9 | develop since v1.0.0: 8 7 | feat/stack since v1.0.0: 10"; got != want {
			t.Errorf("groups = %s\nwant     %s", got, want)
		}
	})

	t.Run("the base on GitHub decides, not the scan order", func(t *testing.T) {
		r := unreleasedRepo(t, true)
		r.Checkout("develop")
		r.CreateBranch("feat/stack")
		squashPRCommit(r, "feat: stacked (#10)")
		squashPRCommit(r, "feat: stacked again (#11)")
		squashPRCommit(r, "feat: not on the merged page (#12)")
		r.RunGit("update-ref", "refs/remotes/origin/feat/stack", "feat/stack")
		// feat/stack has more merged PRs than develop, so it is read first.
		stacked := []ghapi.DashPR{{Number: 10, BaseRef: "feat/stack"}, {Number: 11, BaseRef: "feat/stack"}, {Number: 13, BaseRef: "feat/stack"},
			{Number: 7, BaseRef: "develop"}, {Number: 8, BaseRef: "develop"}}
		groups, _ := collectDashUnreleased(context.Background(), &git.ExecRunner{Dir: r.Dir}, "origin", "", stacked)
		if got, want := unreleasedSummary(groups), "main since v1.0.0: 9 | feat/stack since v1.0.0: 12 11 10 | develop since v1.0.0: 8 7"; got != want {
			t.Errorf("groups = %s\nwant     %s", got, want)
		}
	})

	t.Run("a merged PR whose commit is not fetched is named", func(t *testing.T) {
		r := unreleasedRepo(t, true)
		have := strings.TrimSpace(r.RunGit("rev-parse", "refs/remotes/origin/develop"))
		gone := strings.Repeat("1", 40)
		prs := []ghapi.DashPR{{Number: 8, BaseRef: "develop", MergeOID: have}, {Number: 31, BaseRef: "develop", MergeOID: gone},
			{Number: 32, BaseRef: "develop", MergeOID: "--output=x"}, {Number: 33, BaseRef: "develop"}, {Number: 40, BaseRef: "main", MergeOID: gone}}
		groups, notes := collectDashUnreleased(context.Background(), &git.ExecRunner{Dir: r.Dir}, "origin", "", prs)
		if got, want := unreleasedSummary(groups), "main since v1.0.0: 9 | develop since v1.0.0: 8 7"; got != want || len(notes) != 0 {
			t.Fatalf("groups = %s, notes %v", got, notes)
		}
		// #32 has no object name and #33 no merge commit: neither is known to
		// be missing.
		if got := fmt.Sprint(groups[0].NotFetched, groups[1].NotFetched); got != "[40] [31]" {
			t.Errorf("not fetched = %s, want [40] [31]", got)
		}
	})

	t.Run("no merged PR from GitHub still reads the release branch", func(t *testing.T) {
		r := unreleasedRepo(t, true)
		groups, _ := collectDashUnreleased(context.Background(), &git.ExecRunner{Dir: r.Dir}, "origin", "main", nil)
		if got, want := unreleasedSummary(groups), "main since v1.0.0: 9"; got != want {
			t.Errorf("groups = %s, want %s", got, want)
		}
	})

	t.Run("without a tag, a branch counts from the release branch", func(t *testing.T) {
		r := unreleasedRepo(t, false)
		groups, _ := collectDashUnreleased(context.Background(), &git.ExecRunner{Dir: r.Dir}, "origin", "main", merged)
		// main has no tag to count from, so it has no group.
		if got, want := unreleasedSummary(groups), "develop since origin/main: 8 7"; got != want {
			t.Errorf("groups = %s, want %s", got, want)
		}
	})

	t.Run("an unknown release branch is a note", func(t *testing.T) {
		r := unreleasedRepo(t, true)
		// DefaultBranch falls back to origin/develop, main, and master.
		r.RunGit("symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
		r.RunGit("update-ref", "refs/remotes/origin/trunk", "refs/remotes/origin/main")
		r.RunGit("update-ref", "-d", "refs/remotes/origin/main")
		r.RunGit("update-ref", "-d", "refs/remotes/origin/develop")
		_, notes := collectDashUnreleased(context.Background(), &git.ExecRunner{Dir: r.Dir}, "origin", "", nil)
		if len(notes) != 1 || !strings.Contains(notes[0], "release branch unknown") {
			t.Errorf("notes = %v, want one naming the unknown release branch", notes)
		}
	})

	t.Run("a release branch that is all released has an empty group", func(t *testing.T) {
		r := unreleasedRepo(t, true)
		r.RunGit("tag", "v1.1.0", "refs/remotes/origin/main")
		groups, _ := collectDashUnreleased(context.Background(), &git.ExecRunner{Dir: r.Dir}, "origin", "main", merged)
		if got, want := unreleasedSummary(groups), "main since v1.1.0:  | develop since v1.1.0: 8 7"; got != want {
			t.Errorf("groups = %q\nwant     %q", got, want)
		}
	})
}

func TestIsHexOID(t *testing.T) {
	for s, want := range map[string]bool{
		strings.Repeat("a", 40): true, strings.Repeat("0", 64): true,
		"": false, "abc123": false, strings.Repeat("A", 40): false, strings.Repeat("g", 40): false,
		"--output=" + strings.Repeat("a", 31): false,
	} {
		if got := isHexOID(s); got != want {
			t.Errorf("isHexOID(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestBuildDashSnapshotUnreleased(t *testing.T) {
	prs := make([]releasePR, releasePRListCap+3)
	for i := range prs {
		prs[i] = releasePR{Number: 200 - i, Title: "commit title", SHA: "abc1234", Date: "2026-11-17T08:00:00Z"}
	}
	res := &ghapi.DashResult{HasRepo: true, RepoMerged: ghapi.DashPRList{Items: []ghapi.DashPR{
		{Number: 200, BaseRef: "develop", URL: "https://github.com/o/r/pull/200", Author: "me", State: "MERGED"},
		{Number: 199, BaseRef: "other", URL: "https://github.com/o/r/pull/199", Author: "someone", State: "MERGED"},
	}}}
	in := dashInputs{Res: res, Local: &dashLocal{Remote: "origin"}, Unreleased: []dashUnreleasedGroup{{Base: "develop", Since: "v1.0.0", Commits: 40, PRs: prs, NotFetched: []int{201}}}}
	snap := buildDashSnapshot(in)
	if len(snap.Unreleased) != 1 {
		t.Fatalf("unreleased = %+v", snap.Unreleased)
	}
	u := snap.Unreleased[0]
	if u.Base != "develop" || u.Remote != "origin" || u.Since != "v1.0.0" || u.Total != len(prs) || len(u.Items) != releasePRListCap || len(u.NotFetched) != 1 || u.NotFetched[0] != 201 || u.Commits != 40 {
		t.Errorf("group = %s %s %s total %d items %d", u.Base, u.Remote, u.Since, u.Total, len(u.Items))
	}
	if r := u.Items[0]; r.Number != 200 || r.URL == "" || r.Author != "me" || r.Title != "commit title" || !r.MergedAt.Equal(time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("row = %+v", r)
	}
	// #199 on GitHub was merged into another base: it is not this commit's PR.
	if r := u.Items[1]; r.Number != 199 || r.URL != "" || r.Author != "" {
		t.Errorf("row of another base = %+v", r)
	}

	in.Local = nil
	if snap := buildDashSnapshot(in); len(snap.Unreleased) != 0 {
		t.Errorf("unreleased without the local repository = %+v", snap.Unreleased)
	}
}

func TestRenderDashUnreleased(t *testing.T) {
	setNoColor(t, true, func() {})
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	snap := dashTableSnapshot("repo:x-mesh/gk", "x-mesh/gk")
	snap.Unreleased = []dashUnreleasedJSON{
		{Base: "main", Remote: "origin", Since: "v1.0.0", Total: 0, Items: []dashUnreleasedRow{}},
		{Base: "develop", Remote: "origin", Since: "v1.0.0", Commits: 1204, Total: 3, NotFetched: []int{92, 91}, Items: []dashUnreleasedRow{
			{Number: 90, Title: "fix(branch): prune the configured remote", SHA: "d707eb9", MergedAt: now.Add(-2 * time.Hour)},
			{Number: 89, Title: "evil\x1b]0;pwned\x07title", SHA: "e502b8a", MergedAt: now.Add(-72 * time.Hour)},
		}},
	}
	out := renderSnapshot(t, snap, 200)
	for _, want := range []string{"Unreleased  develop · 1,204 commits · 3 merged PR(s) since v1.0.0", "#90  fix(branch): prune the configured remote", "evil?]0;pwned?title", "1 more · gk log v1.0.0..origin/develop",
		"#92 #91 merged on GitHub, not in the local repository → gk dash --fetch"} {
		if !strings.Contains(out, want) {
			t.Errorf("output has no %q:\n%s", want, out)
		}
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("output carries a control character: %q", out)
	}
	if strings.Contains(out, "main ·") {
		t.Errorf("an empty branch has a block next to one with PRs:\n%s", out)
	}
	if strings.Index(out, "Unreleased") < strings.Index(out, "Issues") {
		t.Errorf("Unreleased is not after Issues:\n%s", out)
	}

	snap.Unreleased = snap.Unreleased[:1]
	out = renderSnapshot(t, snap, 200)
	if !strings.Contains(out, "Unreleased  main · no merged PR since v1.0.0") {
		t.Errorf("an all-released repository has no line:\n%s", out)
	}

	// A branch with nothing in the range but a PR that is not fetched is not
	// shown as all released.
	snap.Unreleased[0].NotFetched = []int{95}
	out = renderSnapshot(t, snap, 200)
	if !strings.Contains(out, "main · no merged PR since v1.0.0") || !strings.Contains(out, "#95 merged on GitHub, not in the local repository") {
		t.Errorf("a PR that is not fetched has no line:\n%s", out)
	}

	snap.Unreleased = nil
	if out = renderSnapshot(t, snap, 200); strings.Contains(out, "Unreleased") {
		t.Errorf("a snapshot without the section prints it:\n%s", out)
	}
}

func TestDashTUIJumpsToUnreleased(t *testing.T) {
	m, _ := newTestDashTUI(t, false)
	snap := dashTableSnapshot("repo:x-mesh/gk", "x-mesh/gk")
	snap.Unreleased = []dashUnreleasedJSON{{Base: "develop", Remote: "origin", Since: "v1.0.0", Total: 1,
		Items: []dashUnreleasedRow{{Number: 90, Title: "fix: a", SHA: "d707eb9"}}}}
	m.snap = &snap
	m.width = 120
	m.render()
	if len(m.sections) != 3 {
		t.Errorf("sections = %v, want PRs, Issues, and Unreleased", m.sections)
	}
}

func TestDashCmdUnreleased(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	withAgentMode(t, false)
	r := unreleasedRepo(t, true)
	r.AddRemote("origin", "https://github.com/o/r.git")
	// GitHub merged #31 into develop after the last fetch: its commit is not
	// in the repository.
	late := dashGraphQLPR("fix/late", "")
	late["number"], late["state"], late["baseRefName"] = 31, "MERGED", "develop"
	late["mergeCommit"] = map[string]any{"oid": strings.Repeat("1", 40)}
	f := &dashFake{merged: []any{late}}
	out, err := runDashCmd(t, r.Dir, f.server(t, ""), "tok")
	if err != nil {
		t.Fatalf("gk dash: %v\n%s", err, out)
	}
	// develop has the merge commit of #7, the commit of its branch, and #8.
	for _, want := range []string{"Unreleased  main · 1 commit · 1 merged PR(s) since v1.0.0", "#9  fix: hotfix on main",
		"Unreleased  develop · 3 commits · 2 merged PR(s) since v1.0.0", "#31 merged on GitHub, not in the local repository"} {
		if !strings.Contains(out, want) {
			t.Errorf("output has no %q:\n%s", want, out)
		}
	}
}
