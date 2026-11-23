package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
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
	if j.Total != 40 || j.Scanned != 20 || j.Error != "unreleased query: partial" || j.Pending {
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

	if empty := orgUnreleasedJSON(&ghapi.OwnerUnreleased{}); empty.Items == nil {
		t.Errorf("items is null in JSON for a read with no rows")
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

	snap.UnreleasedRepos = &dashOrgUnreleasedJSON{Total: 40, Scanned: 20, Items: []dashRepoUnreleasedRow{
		{Repo: "x-mesh/gk", Since: "v0.164.0", Branch: "develop", Commits: 12, PRs: 6},
		{Repo: "x-mesh/ai-mesh", Since: "v0.2.3\x1b[2J", Branch: "main", Commits: 471, PRs: 12, PRsPartial: true, TagOffBranch: true},
	}}
	out := renderSnapshot(t, snap, 200)
	for _, want := range []string{"Unreleased  2 repo(s) with commits after the latest tag · newest 20 of 40 repos",
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
	snap.UnreleasedRepos = &dashOrgUnreleasedJSON{Total: 5, Scanned: 5, Items: snap.UnreleasedRepos.Items[:1]}
	out = renderSnapshot(t, snap, 200)
	if strings.Contains(out, "note") || !strings.Contains(out, "1 repo(s) with commits after the latest tag · 5 repos\n") {
		t.Errorf("output without notes:\n%s", out)
	}

	// A failed refresh keeps the rows and says what failed.
	snap.UnreleasedRepos.Error = "github unreleased query timed out (502 Bad Gateway)"
	out = renderSnapshot(t, snap, 200)
	if !strings.Contains(out, "could not read: github unreleased query timed out") || !strings.Contains(out, "v0.164.0..develop") {
		t.Errorf("a failed read with rows:\n%s", out)
	}

	snap.UnreleasedRepos = &dashOrgUnreleasedJSON{Total: 5, Scanned: 5, Items: []dashRepoUnreleasedRow{}}
	if out = renderSnapshot(t, snap, 200); !strings.Contains(out, "0 repo(s) with commits after the latest tag · 5 repos\n  none\n") {
		t.Errorf("an org with nothing unreleased:\n%s", out)
	}
	snap.UnreleasedRepos.Error = "boom"
	if out = renderSnapshot(t, snap, 200); strings.Contains(out, "none") || !strings.Contains(out, "could not read: boom") {
		t.Errorf("a failed first read says none:\n%s", out)
	}
}

func TestDashTUIOrgUnreleasedFillsLater(t *testing.T) {
	m, clock := newTestDashTUI(t, false)
	m.width = 120
	reads := 0
	m.fetchOrg = func(context.Context) (*dashOrgUnreleasedJSON, error) { reads++; return nil, nil }
	if m.Init(); !m.orgInFlight || !m.inFlight {
		t.Fatalf("Init did not start both reads: main %v org %v", m.inFlight, m.orgInFlight)
	}
	if m.startOrg(true) != nil {
		t.Errorf("a second org read started while one is in flight")
	}

	// The main read ends first: the block is pending, the rest is shown.
	m.Update(okResult(m))
	if m.snap == nil || m.snap.UnreleasedRepos == nil || !m.snap.UnreleasedRepos.Pending {
		t.Fatalf("snapshot before the org read = %+v", m.snap)
	}
	if !strings.Contains(m.status(), "refreshing") {
		t.Errorf("status while the org read runs = %q", m.status())
	}
	first := &dashOrgUnreleasedJSON{Total: 3, Scanned: 3, Items: []dashRepoUnreleasedRow{{Repo: "o/r", Since: "v1.0.0", Branch: "main", Commits: 2, PRs: 1}}}
	m.Update(dashOrgResultMsg{res: first})
	if m.orgInFlight || m.snap.UnreleasedRepos != first || !strings.Contains(m.vp.View(), "v1.0.0..main") {
		t.Fatalf("org result not shown: in flight %v, block %+v", m.orgInFlight, m.snap.UnreleasedRepos)
	}
	if len(m.sections) != 3 {
		t.Errorf("sections = %v, want PRs, Issues, and Unreleased", m.sections)
	}

	// A later main read keeps the rows, and an auto-refresh does not repeat
	// the org read until it is old.
	m.Update(key("r"))
	if !m.orgInFlight {
		t.Fatalf("r did not start the org read")
	}
	m.Update(okResult(m))
	if m.snap.UnreleasedRepos != first {
		t.Errorf("a main read dropped the org rows: %+v", m.snap.UnreleasedRepos)
	}
	m.Update(dashOrgResultMsg{res: first})
	if m.startOrg(false) != nil {
		t.Errorf("an auto-refresh repeats a fresh org read")
	}
	clock.advance(dashOrgUnreleasedMaxAge)
	if m.startOrg(false) == nil {
		t.Errorf("an auto-refresh does not repeat an old org read")
	}
	m.orgInFlight = false

	// A failure keeps the rows; a rate limit also stops the next reads.
	m.Update(dashOrgResultMsg{err: errors.New("boom")})
	if u := m.snap.UnreleasedRepos; u.Error != "boom" || len(u.Items) != 1 || first.Error != "" {
		t.Errorf("after a failure: %+v (first result changed: %q)", u, first.Error)
	}
	reset := clock.now().Add(2 * time.Minute)
	m.Update(dashOrgResultMsg{err: &ghapi.RateLimitError{Status: "403", Reset: reset}})
	if !m.limited() || m.startOrg(true) != nil {
		t.Errorf("a rate-limited org read does not stop the reads: limited %v", m.limited())
	}
	if reads != 0 {
		t.Errorf("the test ran %d fetch commands", reads)
	}
}

func TestDashTUIRepoScopeHasNoOrgRead(t *testing.T) {
	m, _ := newTestDashTUI(t, false)
	m.Init()
	if m.orgInFlight || m.startOrg(true) != nil {
		t.Errorf("a repository scope starts an org read")
	}
	m.Update(okResult(m))
	if m.snap.UnreleasedRepos != nil {
		t.Errorf("a repository scope has an org block: %+v", m.snap.UnreleasedRepos)
	}
	if strings.Contains(m.status(), "refreshing") {
		t.Errorf("status = %q", m.status())
	}
}

// The screen sends the unreleased query one time for each org read: the main
// read leaves it out.
func TestDashScreenSplitsTheOrgRead(t *testing.T) {
	r, oid := dashCmdRepo(t)
	count := func(f *dashFake) (total, unreleased int) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, q := range f.queries {
			if strings.Contains(q, "repositoryOwner(") {
				unreleased++
			}
		}
		return len(f.queries), unreleased
	}
	screen := func(f *dashFake, prefix string) *dashTUI {
		resetForecastMemo()
		env := dashEnv{client: &ghapi.Client{Token: "tok", APIBase: f.server(t, oid).URL}, runner: &git.ExecRunner{Dir: r.Dir}, prefix: prefix, remote: "origin"}
		m := newDashScreen(context.Background(), env, time.Minute, false)
		t.Cleanup(m.cancel)
		return m
	}

	f := &dashFake{orgRepos: []any{dashOrgRepoNode("o/r")}}
	m := screen(f, "org:o")
	snap, err := m.fetch(context.Background())
	if err != nil {
		t.Fatalf("main read: %v", err)
	}
	if total, unreleased := count(f); total != 2 || unreleased != 0 || snap.UnreleasedRepos != nil {
		t.Errorf("main read sent %d queries, %d unreleased; block %+v", total, unreleased, snap.UnreleasedRepos)
	}
	if m.fetchOrg == nil {
		t.Fatalf("an org screen has no org read")
	}
	org, err := m.fetchOrg(context.Background())
	if err != nil || len(org.Items) != 1 {
		t.Fatalf("org read: %v, %+v", err, org)
	}
	if total, unreleased := count(f); total != 3 || unreleased != 1 {
		t.Errorf("after the org read: %d queries, %d unreleased", total, unreleased)
	}

	if m := screen(&dashFake{}, "repo:o/r"); m.fetchOrg != nil {
		t.Errorf("a repository screen has an org read")
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
		for _, want := range []string{"Unreleased  1 repo(s) with commits after the latest tag · newest 1 of 40 repos", "v1.0.0..develop  2        2"} {
			if !strings.Contains(out, want) {
				t.Errorf("output has no %q:\n%s", want, out)
			}
		}
		// The PR query, the issue query, and the unreleased query.
		if got := f.graphql.Load(); got != 3 {
			t.Errorf("graphql requests = %d, want 3", got)
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
		if u == nil || len(u.Items) != 1 || u.Items[0] != (dashRepoUnreleasedRow{Repo: "o/r", Since: "v1.0.0", Branch: "develop", Commits: 2, PRs: 2}) || u.Pending {
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
		if !strings.Contains(out, "#7") || !strings.Contains(out, "could not read: github unreleased query timed out") {
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
