package cli

import (
	"encoding/json"
	"strings"
	"testing"

	ghapi "github.com/x-mesh/gk/internal/github"
)

func cmpCommit(oid, parent, message string) ghapi.CompareCommit {
	return ghapi.CompareCommit{OID: oid, Parent: parent, Message: message}
}

func TestComparisonPRs(t *testing.T) {
	// c4 is the head. s1 is a commit of the branch that #8 merged: it is not a
	// first-parent commit, and its "(#12)" names an issue.
	c := ghapi.TagComparison{Branch: "develop", Ahead: 5, Head: "c4", Commits: []ghapi.CompareCommit{
		cmpCommit("c1", "c0", "feat: a long subject that GitHub would cut in a headline, with the number at the end (#7)\n\nbody (#99)"),
		cmpCommit("s1", "c1", "fix: part of the branch (#12)"),
		cmpCommit("c2", "c1", "Merge pull request #8 from o/fix-b\n\nfix: b"),
		cmpCommit("c3", "c2", "docs: no PR here"),
		cmpCommit("c4", "c3", "fix: a again (#7)"),
	}}
	if got := comparisonPRs(c); got != 2 {
		t.Errorf("PRs = %d, want 2 (#7 one time, #8; #12 and #99 are not PRs of this branch)", got)
	}

	// The window does not reach the head's parent: the walk stops there.
	c.Commits = c.Commits[3:]
	if got := comparisonPRs(c); got != 1 {
		t.Errorf("PRs in a window of two = %d, want 1", got)
	}
	// A head outside the window finds nothing, and a parent loop ends.
	if got := comparisonPRs(ghapi.TagComparison{Head: "zz", Commits: c.Commits}); got != 0 {
		t.Errorf("PRs with an unknown head = %d, want 0", got)
	}
	loop := ghapi.TagComparison{Head: "a", Commits: []ghapi.CompareCommit{cmpCommit("a", "b", "x (#1)"), cmpCommit("b", "a", "y (#2)")}}
	if got := comparisonPRs(loop); got != 2 {
		t.Errorf("PRs in a parent loop = %d, want 2", got)
	}
}

func TestOrgUnreleasedJSON(t *testing.T) {
	one := []ghapi.CompareCommit{cmpCommit("h", "", "fix: a (#3)")}
	res := &ghapi.OwnerUnreleased{Total: 40, Scanned: 20, Notes: []string{"unreleased query: partial"}, Repos: []ghapi.RepoUnreleased{
		// The tag is on main, and develop is behind it by the release commit:
		// that is an ordinary release, with no note.
		{Repo: "o/on-develop", Tag: "v1.0.0", Branches: []ghapi.TagComparison{{Branch: "main", Default: true, Ahead: 0}, {Branch: "develop", Ahead: 1, Behind: 15, Head: "h", Commits: one}}},
		{Repo: "o/tie", Tag: "v2.0.0", Branches: []ghapi.TagComparison{{Branch: "main", Default: true, Ahead: 4, Head: "h", Commits: one}, {Branch: "develop", Ahead: 4, Head: "h", Commits: one}}},
		{Repo: "o/released", Tag: "v3.0.0", Branches: []ghapi.TagComparison{{Branch: "main", Default: true, Ahead: 0}, {Branch: "develop", Ahead: 0}}},
		{Repo: "o/off-branch", Tag: "v4.0.0", Branches: []ghapi.TagComparison{{Branch: "main", Default: true, Ahead: 1, Behind: 2, Head: "h", Commits: one}}},
		{Repo: "o/no-branch", Tag: "v5.0.0"},
		// The tag is on neither branch.
		{Repo: "o/off-both", Tag: "v6.0.0", Branches: []ghapi.TagComparison{{Branch: "main", Default: true, Ahead: 2, Behind: 9, Head: "h", Commits: one}, {Branch: "develop", Ahead: 3, Behind: 9, Head: "h", Commits: one}}},
		// No comparison with the default branch: develop alone cannot show
		// that the tag is a release.
		{Repo: "o/develop-only", Tag: "v7.0.0", Branches: []ghapi.TagComparison{{Branch: "develop", Ahead: 1, Behind: 1, Head: "h", Commits: one}}},
		// The tag is on develop and not on main, and main is the row.
		{Repo: "o/tag-on-develop", Tag: "v8.0.0", Branches: []ghapi.TagComparison{{Branch: "main", Default: true, Ahead: 5, Behind: 2, Head: "h", Commits: one}, {Branch: "develop", Ahead: 1, Head: "h", Commits: one}}},
	}}
	j := orgUnreleasedJSON(res)
	// 1 + 4 + 1 + 3 + 1 + 5 commits and one PR in each of the six rows. Three
	// rows have more commits than were read, so the PR sum is a lower bound.
	if j.Total != 40 || j.Scanned != 20 || j.Error != "unreleased query: partial" || j.Pending || j.Commits != 15 || j.PRs != 6 || !j.PRsPartial {
		t.Errorf("summary = %+v", j)
	}
	want := []dashRepoUnreleasedRow{
		{Repo: "o/on-develop", Since: "v1.0.0", Branch: "develop", Commits: 1, PRs: 1},
		{Repo: "o/tie", Since: "v2.0.0", Branch: "main", Commits: 4, PRs: 1, PRsPartial: true},
		{Repo: "o/off-branch", Since: "v4.0.0", Branch: "main", Commits: 1, PRs: 1, TagOffBranch: true},
		{Repo: "o/off-both", Since: "v6.0.0", Branch: "develop", Commits: 3, PRs: 1, PRsPartial: true, TagOffBranch: true},
		{Repo: "o/develop-only", Since: "v7.0.0", Branch: "develop", Commits: 1, PRs: 1, TagOffBranch: true},
		{Repo: "o/tag-on-develop", Since: "v8.0.0", Branch: "main", Commits: 5, PRs: 1, PRsPartial: true, TagOffBranch: true},
	}
	if len(j.Items) != len(want) {
		t.Fatalf("rows = %+v", j.Items)
	}
	for i, w := range want {
		if j.Items[i] != w {
			t.Errorf("row %d = %+v\nwant    %+v", i, j.Items[i], w)
		}
	}

	if empty := orgUnreleasedJSON(&ghapi.OwnerUnreleased{}); empty.Items == nil || empty.PRsPartial {
		t.Errorf("a read with no rows = %+v, want items that are not null", empty)
	}
	// A partial row before a whole one still makes the sum a lower bound.
	if mixed := orgUnreleasedJSON(&ghapi.OwnerUnreleased{Repos: res.Repos[:4]}); len(mixed.Items) != 3 || mixed.Items[2].PRsPartial || !mixed.PRsPartial {
		t.Errorf("a partial row before a whole one = %+v", mixed)
	}
	whole := orgUnreleasedJSON(&ghapi.OwnerUnreleased{Repos: res.Repos[:1]})
	if whole.Commits != 1 || whole.PRs != 1 || whole.PRsPartial {
		t.Errorf("a read with every commit = %+v, want no partial sum", whole)
	}
}

func TestRenderDashOrgUnreleased(t *testing.T) {
	setNoColor(t, true, func() {})
	snap := dashTableSnapshot("org:x-mesh", "x-mesh/gk")
	if out := renderSnapshot(t, snap, 200); strings.Contains(out, "Unreleased") {
		t.Errorf("a snapshot without the block prints it:\n%s", out)
	}

	snap.UnreleasedRepos = &dashOrgUnreleasedJSON{Pending: true}
	if out := renderSnapshot(t, snap, 200); !strings.Contains(out, "\nUnreleased  loading…\n") {
		t.Errorf("a pending block does not say loading:\n%s", out)
	}

	snap.UnreleasedRepos = &dashOrgUnreleasedJSON{Total: 40, Scanned: 20, Commits: 1483, PRs: 18, PRsPartial: true, Items: []dashRepoUnreleasedRow{
		{Repo: "x-mesh/gk", Since: "v0.164.0", Branch: "develop", Commits: 12, PRs: 6},
		{Repo: "x-mesh/ai-mesh", Since: "v0.2.3\x1b[2J", Branch: "main", Commits: 471, PRs: 12, PRsPartial: true, TagOffBranch: true},
	}}
	out := renderSnapshot(t, snap, 200)
	for _, want := range []string{"Unreleased  2 repos · 1,483 commits · 18+ PRs · newest 20 of 40 repos",
		"repo     range              commits  PRs  note", "gk       v0.164.0..develop  12       6\n", "v0.2.3?[2J..main", "12+  tag is not on main"} {
		if !strings.Contains(out, want) {
			t.Errorf("output has no %q:\n%s", want, out)
		}
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("output carries a control character: %q", out)
	}
	if snap.UnreleasedRepos.Items[1].Since != "v0.2.3\x1b[2J" {
		t.Errorf("rendering changed the snapshot")
	}

	// Without a tag off its branch there is no note column.
	snap.UnreleasedRepos = &dashOrgUnreleasedJSON{Total: 5, Scanned: 5, Commits: 12, PRs: 1, Items: snap.UnreleasedRepos.Items[:1]}
	out = renderSnapshot(t, snap, 200)
	if strings.Contains(out, "note") || !strings.Contains(out, "Unreleased  1 repo · 12 commits · 1 PR · all 5 repos\n") {
		t.Errorf("output without notes:\n%s", out)
	}

	// A failed refresh keeps the rows and says what failed.
	snap.UnreleasedRepos.Error = "github unreleased query timed out (502 Bad Gateway)"
	out = renderSnapshot(t, snap, 200)
	if !strings.Contains(out, "could not read: github unreleased query timed out") || !strings.Contains(out, "v0.164.0..develop") {
		t.Errorf("a failed read with rows:\n%s", out)
	}

	snap.UnreleasedRepos = &dashOrgUnreleasedJSON{Total: 5, Scanned: 5, Items: []dashRepoUnreleasedRow{}}
	if out = renderSnapshot(t, snap, 200); !strings.Contains(out, "Unreleased  nothing after the latest tag · all 5 repos\n  none\n") {
		t.Errorf("an org with nothing unreleased:\n%s", out)
	}
	snap.UnreleasedRepos.Error = "boom"
	if out = renderSnapshot(t, snap, 200); strings.Contains(out, "none") || strings.Contains(out, "nothing after") || !strings.Contains(out, "\nUnreleased\n  could not read: boom\n") {
		t.Errorf("a failed first read says none:\n%s", out)
	}
}

func dashOrgRepoNode(name string) map[string]any {
	commit := func(oid, parent, msg string) map[string]any {
		return map[string]any{"oid": oid, "message": msg, "committedDate": "2026-11-17T01:00:00Z", "parents": map[string]any{"nodes": []any{map[string]any{"oid": parent}}}}
	}
	return map[string]any{
		"nameWithOwner": name, "defaultBranchRef": map[string]any{"name": "main"},
		"tags": map[string]any{"nodes": []any{map[string]any{"name": "v1.0.0",
			"head": map[string]any{"aheadBy": 0, "behindBy": 0, "headTarget": map[string]any{"oid": "m0"}, "commits": map[string]any{"nodes": []any{}}},
			"dev": map[string]any{"aheadBy": 2, "behindBy": 0, "headTarget": map[string]any{"oid": "d2"}, "commits": map[string]any{"nodes": []any{
				commit("d1", "m0", "fix: a (#7)"), commit("d2", "d1", "Merge pull request #8 from o/b\n\nfix: b"),
			}}},
		}}},
	}
}

func TestDashCmdOrgUnreleased(t *testing.T) {
	r, oid := dashCmdRepo(t)

	t.Run("table", func(t *testing.T) {
		withAgentMode(t, false)
		f := &dashFake{orgRepos: []any{dashOrgRepoNode("o/r")}}
		out, err := runDashCmd(t, r.Dir, f.server(t, oid), "tok", "--org", "o")
		if err != nil {
			t.Fatalf("gk dash --org: %v\n%s", err, out)
		}
		for _, want := range []string{"Unreleased  1 repo · 2 commits · 2 PRs · newest 1 of 40 repos", "v1.0.0..develop  2        2",
			"Activity  7d · PRs opened 28 ▂▃▄▅▆▇█ · merged 21 ·▂▃▄▆▇█ · commits 21 ███████"} {
			if !strings.Contains(out, want) {
				t.Errorf("output has no %q:\n%s", want, out)
			}
		}
		// The PR query, the issue query, the PR activity, and the unreleased
		// summary. The commit activity is one search for each day.
		if got, searches := f.graphql.Load(), f.searches.Load(); got != 4 || searches != dashActivityDays {
			t.Errorf("graphql requests = %d, searches = %d; want 4 and %d", got, searches, dashActivityDays)
		}
	})

	t.Run("JSON", func(t *testing.T) {
		withAgentMode(t, true)
		f := &dashFake{orgRepos: []any{dashOrgRepoNode("o/r")}}
		out, err := runDashCmd(t, r.Dir, f.server(t, oid), "tok", "--org", "o")
		if err != nil {
			t.Fatalf("gk dash --org: %v\n%s", err, out)
		}
		var env struct {
			Result struct {
				Unreleased      []dashUnreleasedJSON   `json:"unreleased"`
				UnreleasedRepos *dashOrgUnreleasedJSON `json:"unreleased_repos"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("decode: %v\n%s", err, out)
		}
		u := env.Result.UnreleasedRepos
		if u == nil || len(u.Items) != 1 || u.Items[0] != (dashRepoUnreleasedRow{Repo: "o/r", Since: "v1.0.0", Branch: "develop", Commits: 2, PRs: 2}) || u.Pending || u.Commits != 2 || u.PRs != 2 {
			t.Errorf("unreleased_repos = %+v", u)
		}
		if len(env.Result.Unreleased) != 0 {
			t.Errorf("an org scope has the repository block: %+v", env.Result.Unreleased)
		}
	})

	t.Run("a failed unreleased query does not fail the snapshot", func(t *testing.T) {
		withAgentMode(t, false)
		f := &dashFake{orgFails: true}
		out, err := runDashCmd(t, r.Dir, f.server(t, oid), "tok", "--org", "o")
		if err != nil {
			t.Fatalf("gk dash --org: %v\n%s", err, out)
		}
		if !strings.Contains(out, "#7") || !strings.Contains(out, "\nUnreleased\n  could not read: github unreleased query timed out") || strings.Contains(out, "nothing after") {
			t.Errorf("output:\n%s", out)
		}
	})

	t.Run("a repository scope does not send the unreleased query", func(t *testing.T) {
		withAgentMode(t, false)
		f := &dashFake{}
		if out, err := runDashCmd(t, r.Dir, f.server(t, oid), "tok"); err != nil {
			t.Fatalf("gk dash: %v\n%s", err, out)
		}
		for _, q := range f.queries {
			if strings.Contains(q, "repositoryOwner(") {
				t.Errorf("a repository scope sent the unreleased query")
			}
		}
	})
}
