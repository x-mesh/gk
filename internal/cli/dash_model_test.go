package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	ghapi "github.com/x-mesh/gk/internal/github"
)

var (
	ciOK      = ghapi.DashCheck{Kind: "check_run", Name: "test", State: "COMPLETED", Conclusion: "SUCCESS", URL: "https://ci/test"}
	ciFail    = ghapi.DashCheck{Kind: "check_run", Name: "lint", State: "COMPLETED", Conclusion: "FAILURE", URL: "https://ci/lint"}
	ciRunning = ghapi.DashCheck{Kind: "check_run", Name: "e2e", State: "IN_PROGRESS"}
	rvPass    = ghapi.DashCheck{Kind: "status", Name: "reviewer", State: "SUCCESS", Description: "통과 · 차단 0건", URL: "https://rv"}
	rvBlock   = ghapi.DashCheck{Kind: "status", Name: "reviewer", State: "FAILURE", Description: "차단 1건", URL: "https://rv"}
)

func TestReviewVerdict(t *testing.T) {
	for _, tc := range []struct {
		name string
		pr   ghapi.DashPR
		ctx  string
		want dashReview
	}{
		{"decision wins", ghapi.DashPR{ReviewDecision: "APPROVED", URL: "u", Checks: []ghapi.DashCheck{rvBlock}}, "reviewer", dashReview{Source: "decision", State: "approved", URL: "u"}},
		{"context when no decision", ghapi.DashPR{Checks: []ghapi.DashCheck{ciOK, rvPass}}, "reviewer", dashReview{Source: "context", State: "success", Description: "통과 · 차단 0건", URL: "https://rv"}},
		{"context configured but absent", ghapi.DashPR{Checks: []ghapi.DashCheck{ciOK}}, "reviewer", dashReview{Source: "none"}},
		{"context not configured", ghapi.DashPR{Checks: []ghapi.DashCheck{rvPass}}, "", dashReview{Source: "none"}},
		{"a check run of that name is not a status", ghapi.DashPR{Checks: []ghapi.DashCheck{{Kind: "check_run", Name: "reviewer", State: "COMPLETED", Conclusion: "SUCCESS"}}}, "reviewer", dashReview{Source: "none"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reviewVerdict(tc.pr, tc.ctx); got != tc.want {
				t.Errorf("verdict = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCISummary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pr     ghapi.DashPR
		want   string
		failed int
	}{
		{"all success", ghapi.DashPR{Checks: []ghapi.DashCheck{ciOK, rvPass}}, dashCISuccess, 0},
		{"failure wins", ghapi.DashPR{Checks: []ghapi.DashCheck{ciRunning, ciFail, ciOK}}, dashCIFailure, 1},
		{"unknown state is not success", ghapi.DashPR{Checks: []ghapi.DashCheck{ciOK, {Kind: "check_run", Name: "x", State: "COMPLETED", Conclusion: "STALE"}}}, "stale", 0},
		{"unknown beats pending", ghapi.DashPR{Checks: []ghapi.DashCheck{ciRunning, {Kind: "status", Name: "x", State: "WEIRD"}}}, "weird", 0},
		{"pending", ghapi.DashPR{Checks: []ghapi.DashCheck{ciOK, ciRunning}}, dashCIPending, 0},
		{"no checks", ghapi.DashPR{}, dashCINone, 0},
		{"the review context is not CI", ghapi.DashPR{Checks: []ghapi.DashCheck{rvBlock}}, dashCINone, 0},
		{"a partial page defers to the rollup", ghapi.DashPR{Checks: []ghapi.DashCheck{ciOK}, ChecksTotal: 25, Rollup: "FAILURE"}, dashCIFailure, 0},
		{"a partial page with a passing rollup", ghapi.DashPR{Checks: []ghapi.DashCheck{ciOK}, ChecksTotal: 25, Rollup: "SUCCESS"}, dashCISuccess, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ciSummary(tc.pr, "reviewer")
			if got.State != tc.want || len(got.Failed) != tc.failed {
				t.Errorf("ci = %+v, want %s with %d failed", got, tc.want, tc.failed)
			}
		})
	}
}

func TestPRAction(t *testing.T) {
	pr := ghapi.DashPR{Number: 7, URL: "https://pr/7", BaseRef: "develop", State: "OPEN"}
	branch := []dashLocalBranch{{Name: "fix/a", Worktree: "/wt/a", Tree: dashTreeClean}}
	current := []dashLocalBranch{{Name: "fix/a", Worktree: "/repo", Current: true}}
	pass := dashReview{Source: dashReviewContext, State: "success"}
	none := dashReview{Source: dashReviewNone}
	green := dashCI{State: dashCISuccess}
	merged := pr
	merged.State = "MERGED"
	draft := pr
	draft.Draft = true
	for _, tc := range []struct {
		name    string
		f       dashPRFacts
		rule    int
		text    string
		command string
		url     string
	}{
		{"1 merged with a local branch", dashPRFacts{pr: merged, branches: branch}, 1, "merged; local branch remains (in /wt/a)", "gk branch clean", ""},
		{"merged without a local branch is not cleanup", dashPRFacts{pr: merged, review: pass, ci: green}, 10, "", "", ""},
		{"2 base conflict with a local branch", dashPRFacts{pr: pr, branches: current, baseConflict: []string{"CHANGELOG.md"}, ci: green}, 2, "conflicts with develop: CHANGELOG.md", "gk sync --fetch", ""},
		{"2 base conflict without a local branch", dashPRFacts{pr: pr, baseConflict: []string{"a.go"}}, 2, "conflicts with develop: a.go", "gk pr checkout 7", ""},
		{"3 CI failed", dashPRFacts{pr: pr, ci: dashCI{State: dashCIFailure, Failed: []dashCheckRef{{Name: "lint", URL: "https://ci/lint"}}}, review: pass}, 3, "CI failed: lint", "", "https://ci/lint"},
		{"4 review blocks", dashPRFacts{pr: pr, ci: green, review: dashReview{Source: dashReviewContext, State: "failure", URL: "https://rv"}}, 4, "review: failure", "", "https://rv"},
		{"4 changes requested", dashPRFacts{pr: pr, ci: green, review: dashReview{Source: dashReviewDecision, State: "changes_requested"}}, 4, "review: changes_requested", "", "https://pr/7"},
		{"5 conflicts with another PR", dashPRFacts{pr: pr, ci: green, review: pass, baseClean: true, pairWith: 8, pairFiles: []string{"CHANGELOG.md"}}, 5, "conflicts with #8: CHANGELOG.md", "", ""},
		{"6 draft", dashPRFacts{pr: draft, ci: green, review: pass, baseClean: true}, 6, "draft", "", ""},
		{"7 CI running", dashPRFacts{pr: pr, ci: dashCI{State: dashCIPending}, review: pass, baseClean: true}, 7, "CI running", "", ""},
		{"8 ready", dashPRFacts{pr: pr, ci: green, review: pass, baseClean: true}, 8, "ready to merge", "", ""},
		{"not ready while the base is unknown", dashPRFacts{pr: pr, ci: green, review: pass}, 10, "", "", ""},
		{"not ready while CI is unknown", dashPRFacts{pr: pr, ci: dashCI{State: "stale"}, review: pass, baseClean: true}, 10, "", "", ""},
		{"not ready while CI is none", dashPRFacts{pr: pr, ci: dashCI{State: dashCINone}, review: pass, baseClean: true}, 10, "", "", ""},
		{"not ready with an unknown verdict", dashPRFacts{pr: pr, ci: green, review: dashReview{Source: dashReviewContext, State: "odd"}, baseClean: true}, 10, "", "", ""},
		{"9 no verdict", dashPRFacts{pr: pr, ci: green, review: none, baseClean: true}, 9, "CI passed, no review verdict", "", ""},
		{"10 nothing", dashPRFacts{pr: pr, review: none, ci: dashCI{State: dashCINone}}, 10, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := prAction(tc.f)
			if a.Rule != tc.rule || a.Text != tc.text || a.Command != tc.command || a.URL != tc.url {
				t.Errorf("action = %+v, want rule %d %q %q %q", a, tc.rule, tc.text, tc.command, tc.url)
			}
		})
	}
}

func TestIssueAction(t *testing.T) {
	is := ghapi.DashIssue{Number: 3, URL: "https://is/3"}
	open := is
	open.LinkedPRs = []ghapi.DashLinkedPR{{Number: 5, State: "MERGED", Merged: true}, {Number: 6, State: "OPEN"}}
	merged := is
	merged.LinkedPRs = []ghapi.DashLinkedPR{{Number: 5, State: "MERGED", Merged: true}}
	closed := is
	closed.LinkedPRs = []ghapi.DashLinkedPR{{Number: 9, State: "CLOSED"}}
	for _, tc := range []struct {
		name     string
		relation string
		is       ghapi.DashIssue
		text     string
	}{
		{"open PR first", dashRelAssigned, open, "PR #6 in review"},
		{"merged PR", dashRelAuthored, merged, "PR #5 merged; close the issue?"},
		{"mention", dashRelMentioned, is, "reply needed"},
		{"unassigned", dashRelUnassigned, is, "needs triage"},
		{"assigned without PR", dashRelAssigned, is, "no work branch"},
		{"a closed PR is no work", dashRelAssigned, closed, "no work branch"},
		{"authored without PR", dashRelAuthored, is, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := issueAction(tc.relation, tc.is).Text; got != tc.text {
				t.Errorf("text = %q, want %q", got, tc.text)
			}
		})
	}
}

func dashPR(n int, head, oid string, updated time.Time, checks ...ghapi.DashCheck) ghapi.DashPR {
	return ghapi.DashPR{Number: n, Repo: "o/r", Title: "pr", URL: "https://pr", State: "OPEN", HeadRef: head, HeadOID: oid, BaseRef: "develop", UpdatedAt: updated, Checks: checks}
}

func TestBuildDashSnapshot(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	ready := dashPR(76, "fix/a", "a1", now.Add(-3*time.Hour), ciOK, rvPass)
	paired := dashPR(77, "fix/b", "b1", now.Add(-2*time.Hour), ciOK, rvPass)
	failing := dashPR(78, "fix/c", "c1", now.Add(-time.Hour), ciFail, rvPass)
	mergedPR := dashPR(75, "fix/old", "o1", now.Add(-48*time.Hour), ciOK)
	mergedPR.State = "MERGED"
	reviewPR := dashPR(12, "feat/x", "x1", now)
	res := &ghapi.DashResult{
		HasRepo:    true,
		MyPRs:      ghapi.DashPRList{Total: 25, Items: []ghapi.DashPR{ready, paired, failing}},
		ReviewPRs:  ghapi.DashPRList{Total: 2, Items: []ghapi.DashPR{reviewPR, failing}},
		RepoOpen:   ghapi.DashPRList{Total: 4, Items: []ghapi.DashPR{ready, paired, failing, reviewPR}},
		RepoMerged: ghapi.DashPRList{Total: 1, Items: []ghapi.DashPR{mergedPR}},
		Assigned:   ghapi.DashIssueList{Total: 1, Items: []ghapi.DashIssue{{Number: 70, Repo: "o/r", UpdatedAt: now}}},
		Mentioned:  ghapi.DashIssueList{Total: 2, Items: []ghapi.DashIssue{{Number: 70, Repo: "o/r"}, {Number: 74, Repo: "o/r", UpdatedAt: now}}},
		Unassigned: ghapi.DashIssueList{Total: 1040, Items: []ghapi.DashIssue{{Number: 80, Repo: "o/r", UpdatedAt: now}}},
		RepoIssues: 1077,
		Notes:      []string{"pr query: partial"},
	}
	local := &dashLocal{Remote: "origin", Branches: []dashLocalBranch{
		{Name: "fix/a", Tip: "a1", Upstream: "origin/fix/a", Worktree: "/repo", Current: true, Tree: dashTreeClean},
		{Name: "fix/old", Tip: "o1", Upstream: "origin/fix/old"},
	}, Behind: []dashBaseBehind{{Base: "develop", Behind: 5}, {Base: "main", Behind: 0}}}
	forecast := []dashForecastGroup{{
		Base: "develop", Total: 4, Considered: 4, Clean: []int{76, 77, 78, 12},
		Pairs: []dashPairConflict{{A: 77, B: 78, Files: []string{"CHANGELOG.md"}}},
	}}
	snap := buildDashSnapshot(dashInputs{Scope: "repo:o/r", Now: now, Res: res, Local: local, Forecast: forecast, ReviewContext: "reviewer"})

	var got []string
	for _, r := range snap.PRs.Items {
		got = append(got, r.Relation+"#"+strconv.Itoa(r.Number)+":"+strconv.Itoa(r.Action.Rule))
	}
	// failing (3) and paired (5) first, newest first; then the review request;
	// then ready; cleanup last. #78 is shown once, as mine.
	want := "mine#78:3 mine#77:5 review#12:4 mine#76:8 cleanup#75:1"
	if strings.Join(got, " ") != want {
		t.Errorf("PR rows = %s\nwant      %s", strings.Join(got, " "), want)
	}
	if b := snap.PRs.Items[3].Branches; len(b) != 1 || b[0].Name != "fix/a" || !b[0].Current {
		t.Errorf("#76 branches = %+v", b)
	}
	if c := snap.PRs.Counts; len(c) != 3 || c[0] != (dashCount{Relation: "mine", Total: 25, Shown: 3}) || !c[0].truncated() || c[2].Total != 1 {
		t.Errorf("PR counts = %+v", c)
	}

	var issues []string
	for _, r := range snap.Issues.Items {
		issues = append(issues, r.Relation+"#"+strconv.Itoa(r.Number))
	}
	if strings.Join(issues, " ") != "assigned#70 mentioned#74 unassigned#80" {
		t.Errorf("issue rows = %v", issues)
	}
	if snap.Issues.Others == nil || *snap.Issues.Others != 36 {
		t.Errorf("others = %v, want 1077 - 1040 - 1 = 36", snap.Issues.Others)
	}
	if len(snap.BaseBehind) != 1 || snap.BaseBehind[0] != (dashBehindJSON{Base: "develop", Remote: "origin", Behind: 5}) {
		t.Errorf("base behind = %+v", snap.BaseBehind)
	}
	if len(snap.Notes) != 1 || len(snap.Forecast) != 1 || len(snap.Forecast[0].Pairs) != 1 {
		t.Errorf("notes %v forecast %+v", snap.Notes, snap.Forecast)
	}

	withAgentMode(t, true)
	var buf bytes.Buffer
	if err := emitAgentResult(&buf, snap); err != nil {
		t.Fatal(err)
	}
	var env struct {
		State  string         `json:"state"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	for _, k := range []string{"scope", "fetched_at", "prs", "issues", "forecast", "base_behind", "notes"} {
		if _, ok := env.Result[k]; !ok {
			t.Errorf("result has no %q: %s", k, buf.String())
		}
	}
	for _, k := range []string{`"others_assigned"`, `"updated_at"`} {
		if !strings.Contains(buf.String(), k) {
			t.Errorf("JSON has no %s", k)
		}
	}
	if env.State != "ok" {
		t.Errorf("state = %q, want ok", env.State)
	}
}

func TestBuildDashSnapshotPartialAndOrg(t *testing.T) {
	t.Run("PR query failed", func(t *testing.T) {
		res := &ghapi.DashResult{HasRepo: true, PRErr: errors.New("github pr query timed out (502 Bad Gateway)"),
			Assigned: ghapi.DashIssueList{Total: 1, Items: []ghapi.DashIssue{{Number: 1, Repo: "o/r"}}}, RepoIssues: 3, Unassigned: ghapi.DashIssueList{Total: 5}}
		snap := buildDashSnapshot(dashInputs{Res: res, Local: &dashLocal{}})
		if !strings.Contains(snap.PRs.Error, "timed out") || len(snap.PRs.Items) != 0 || len(snap.Issues.Items) != 1 {
			t.Errorf("snapshot = %+v", snap)
		}
		if snap.Issues.Others == nil || *snap.Issues.Others != 0 {
			t.Errorf("others = %v, want 0 for a negative difference", snap.Issues.Others)
		}
	})
	t.Run("a PR the forecast did not consider is not ready", func(t *testing.T) {
		in := dashPR(79, "fix/z", "z1", time.Time{}, ciOK, rvPass)
		out := dashPR(80, "fix/y", "y1", time.Time{}, ciOK, rvPass)
		res := &ghapi.DashResult{HasRepo: true,
			MyPRs:    ghapi.DashPRList{Total: 2, Items: []ghapi.DashPR{in, out}},
			RepoOpen: ghapi.DashPRList{Total: 2, Items: []ghapi.DashPR{in, out}}}
		fc := []dashForecastGroup{{Base: "develop", Total: 2, Considered: 1, Clean: []int{79}}}
		snap := buildDashSnapshot(dashInputs{Res: res, Local: &dashLocal{}, Forecast: fc, ReviewContext: "reviewer"})
		rules := map[int]int{}
		for _, r := range snap.PRs.Items {
			rules[r.Number] = r.Action.Rule
		}
		if rules[79] != 8 || rules[80] == 8 {
			t.Errorf("rules = %v, want #79 ready and #80 not", rules)
		}
	})
	t.Run("org scope", func(t *testing.T) {
		res := &ghapi.DashResult{MyPRs: ghapi.DashPRList{Total: 1, Items: []ghapi.DashPR{dashPR(1, "a", "a1", time.Time{}, ciOK)}}}
		snap := buildDashSnapshot(dashInputs{Res: res})
		if snap.Issues.Others != nil || len(snap.PRs.Counts) != 2 || len(snap.PRs.Items[0].Branches) != 0 {
			t.Errorf("snapshot = %+v", snap)
		}
		if a := snap.PRs.Items[0].Action; a.Rule == 8 || a.Rule == 9 {
			t.Errorf("org row says ready without a forecast: %+v", a)
		}
	})
}

func TestDashQuery(t *testing.T) {
	q := dashQuery("repo:o/r")
	if q.Owner != "o" || q.Repo != "r" || q.Unassigned != "repo:o/r is:issue is:open no:assignee" {
		t.Errorf("repo query = %+v", q)
	}
	for name, s := range map[string]string{"mine": q.MyPRs, "review": q.ReviewPRs, "assigned": q.Assigned, "mentioned": q.Mentioned, "authored": q.Authored} {
		if !strings.HasPrefix(s, "repo:o/r ") || !strings.Contains(s, "is:open") {
			t.Errorf("%s = %q", name, s)
		}
	}
	for want, s := range map[string]string{"author:@me": q.MyPRs, "review-requested:@me": q.ReviewPRs, "assignee:@me": q.Assigned, "mentions:@me": q.Mentioned} {
		if !strings.Contains(s, want) {
			t.Errorf("%q has no %s", s, want)
		}
	}
	if !strings.Contains(q.MyPRs, "is:pr") || !strings.Contains(q.Authored, "is:issue") || !strings.Contains(q.Authored, "author:@me") {
		t.Errorf("types: %q / %q", q.MyPRs, q.Authored)
	}
	org := dashQuery("org:o")
	if org.Owner != "" || org.Unassigned != "" || !strings.Contains(org.MyPRs, "archived:false") {
		t.Errorf("org query = %+v", org)
	}
}
