package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type dashReq struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func isPRQuery(q string) bool { return strings.Contains(q, "mine: search") }

func dashPRNodeJSON(n int, head string) map[string]any {
	return map[string]any{
		"number": n, "title": "pr " + strconv.Itoa(n), "url": "https://github.com/o/r/pull/" + strconv.Itoa(n),
		"isDraft": false, "state": "OPEN", "headRefName": head, "headRefOid": "aaaa", "baseRefName": "develop",
		"baseRefOid": "bbbb", "isCrossRepository": false, "reviewDecision": "", "updatedAt": "2026-11-17T01:00:00Z",
		"repository": map[string]any{"nameWithOwner": "o/r"}, "author": map[string]any{"login": "me"},
		"closingIssuesReferences": map[string]any{"nodes": []any{map[string]any{"number": 68}}},
		"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{
			"state": "SUCCESS",
			"contexts": map[string]any{"totalCount": 2, "nodes": []any{
				map[string]any{"__typename": "CheckRun", "name": "lint", "status": "COMPLETED", "conclusion": "SUCCESS", "detailsUrl": "https://ci/lint"},
				map[string]any{"__typename": "StatusContext", "context": "reviewer", "state": "SUCCESS", "description": "통과 · 차단 0건", "targetUrl": "https://rv"},
			}},
		}}}}},
	}
}

func dashIssueNodeJSON(n int) map[string]any {
	return map[string]any{
		"number": n, "title": "issue " + strconv.Itoa(n), "url": "https://github.com/o/r/issues/" + strconv.Itoa(n),
		"updatedAt": "2026-11-17T01:00:00Z", "repository": map[string]any{"nameWithOwner": "o/r"}, "author": nil,
		"assignees":                      map[string]any{"totalCount": 0},
		"closedByPullRequestsReferences": map[string]any{"nodes": []any{map[string]any{"number": 76, "state": "MERGED", "merged": true}}},
	}
}

func prData(withRepo bool) map[string]any {
	d := map[string]any{
		"mine":   map[string]any{"issueCount": 21, "nodes": []any{dashPRNodeJSON(78, "fix/a"), map[string]any{}}},
		"review": map[string]any{"issueCount": 0, "nodes": []any{}},
	}
	if withRepo {
		d["repository"] = map[string]any{
			"open":   map[string]any{"totalCount": 1, "nodes": []any{dashPRNodeJSON(78, "fix/a")}},
			"merged": map[string]any{"totalCount": 30, "nodes": []any{dashPRNodeJSON(75, "fix/b")}},
		}
	}
	return d
}

func issueData(withRepo bool) map[string]any {
	d := map[string]any{
		"assigned":  map[string]any{"issueCount": 1, "nodes": []any{dashIssueNodeJSON(70)}},
		"mentioned": map[string]any{"issueCount": 0, "nodes": []any{}},
		"authored":  map[string]any{"issueCount": 0, "nodes": []any{}},
	}
	if withRepo {
		d["repository"] = map[string]any{
			"issues":     map[string]any{"totalCount": 15},
			"unassigned": map[string]any{"totalCount": 3, "nodes": []any{dashIssueNodeJSON(80)}},
		}
	}
	return d
}

// dashServer answers the PR and issue queries with pr and issue. Each is
// called with the decoded request and writes the response.
func dashServer(t *testing.T, pr, issue func(w http.ResponseWriter, req dashReq)) (*httptest.Server, *atomic.Int32, *sync.Mutex, *[]dashReq) {
	t.Helper()
	var n atomic.Int32
	var mu sync.Mutex
	var reqs []dashReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/graphql" {
			t.Errorf("request = %s %s, want POST /graphql", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req dashReq
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		reqs = append(reqs, req)
		mu.Unlock()
		if isPRQuery(req.Query) {
			pr(w, req)
		} else {
			issue(w, req)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &n, &mu, &reqs
}

func okData(t *testing.T, data map[string]any) func(http.ResponseWriter, dashReq) {
	return func(w http.ResponseWriter, _ dashReq) { writeJSON(t, w, map[string]any{"data": data}) }
}

func fullQuery() DashQuery {
	return DashQuery{MyPRs: "repo:o/r is:pr is:open author:@me", ReviewPRs: "rv", Assigned: "as", Mentioned: "mn", Authored: "au", Owner: "o", Repo: "r"}
}

func TestDashboardSendsTwoQueriesAtOnce(t *testing.T) {
	var arrived sync.WaitGroup
	arrived.Add(2)
	both := make(chan struct{})
	go func() { arrived.Wait(); close(both) }()
	wait := func(next func(http.ResponseWriter, dashReq)) func(http.ResponseWriter, dashReq) {
		return func(w http.ResponseWriter, req dashReq) {
			arrived.Done()
			select {
			case <-both:
			case <-time.After(5 * time.Second):
				t.Error("the two queries were not in flight at the same time")
			}
			next(w, req)
		}
	}
	srv, n, _, reqs := dashServer(t, wait(okData(t, prData(true))), wait(okData(t, issueData(true))))
	c := &Client{APIBase: srv.URL, Token: "tok"}
	res, err := c.Dashboard(context.Background(), fullQuery())
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}
	if got := n.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	for _, r := range *reqs {
		if strings.Contains(r.Query, "mutation") {
			t.Errorf("query contains a mutation:\n%s", r.Query)
		}
		if strings.Contains(r.Query, "first: 50") || !strings.Contains(r.Query, "first: 20") {
			t.Errorf("query page size is not 20:\n%s", r.Query)
		}
		for _, f := range []string{"labels", "assignees(first"} {
			if strings.Contains(r.Query, f) {
				t.Errorf("query requests %s:\n%s", f, r.Query)
			}
		}
		if !isPRQuery(r.Query) {
			if strings.Contains(r.Query, "no:assignee") || r.Variables["unassigned"] != nil ||
				!strings.Contains(r.Query, "unassigned: issues(states: OPEN, first: 20, filterBy: {assignee: null}") {
				t.Errorf("unassigned issues are not read from the issues connection:\n%s", r.Query)
			}
		}
	}

	if !res.HasRepo || res.PRErr != nil || res.IssueErr != nil || len(res.Notes) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if res.MyPRs.Total != 21 || len(res.MyPRs.Items) != 1 {
		t.Fatalf("MyPRs total %d items %d, want 21 and 1 (a non-PR node is dropped)", res.MyPRs.Total, len(res.MyPRs.Items))
	}
	p := res.MyPRs.Items[0]
	if p.Number != 78 || p.HeadRef != "fix/a" || p.HeadOID != "aaaa" || p.BaseRef != "develop" || p.Author != "me" || p.Repo != "o/r" || p.Rollup != "SUCCESS" || p.ChecksTotal != 2 {
		t.Fatalf("pr = %+v", p)
	}
	if len(p.ClosingIssues) != 1 || p.ClosingIssues[0] != 68 {
		t.Errorf("closing issues = %v", p.ClosingIssues)
	}
	wantChecks := []DashCheck{
		{Kind: "check_run", Name: "lint", State: "COMPLETED", Conclusion: "SUCCESS", URL: "https://ci/lint"},
		{Kind: "status", Name: "reviewer", State: "SUCCESS", Description: "통과 · 차단 0건", URL: "https://rv"},
	}
	if len(p.Checks) != 2 || p.Checks[0] != wantChecks[0] || p.Checks[1] != wantChecks[1] {
		t.Errorf("checks = %+v", p.Checks)
	}
	if res.RepoMerged.Total != 30 || len(res.RepoMerged.Items) != 1 || res.RepoOpen.Total != 1 {
		t.Errorf("repo blocks = %+v / %+v", res.RepoOpen, res.RepoMerged)
	}
	if res.RepoIssues != 15 || res.Unassigned.Total != 3 || res.Assigned.Total != 1 {
		t.Errorf("issue totals = repo %d unassigned %d assigned %d", res.RepoIssues, res.Unassigned.Total, res.Assigned.Total)
	}
	is := res.Assigned.Items[0]
	if is.Number != 70 || is.Author != "" || len(is.LinkedPRs) != 1 || !is.LinkedPRs[0].Merged || is.LinkedPRs[0].Number != 76 {
		t.Errorf("issue = %+v", is)
	}
}

func TestDashboardWithoutRepoOmitsRepoBlocks(t *testing.T) {
	srv, _, _, reqs := dashServer(t, okData(t, prData(false)), okData(t, issueData(false)))
	c := &Client{APIBase: srv.URL, Token: "tok"}
	q := fullQuery()
	q.Owner, q.Repo = "", ""
	res, err := c.Dashboard(context.Background(), q)
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}
	for _, r := range *reqs {
		if strings.Contains(r.Query, "repository(") || strings.Contains(r.Query, "unassigned") {
			t.Errorf("query without a repo has a repo block:\n%s", r.Query)
		}
		if _, ok := r.Variables["owner"]; ok {
			t.Errorf("variables carry owner: %v", r.Variables)
		}
	}
	if res.HasRepo || res.RepoIssues != 0 || len(res.RepoOpen.Items) != 0 {
		t.Errorf("result = %+v", res)
	}
}

func TestDashboardWithoutTokenSendsNothing(t *testing.T) {
	srv, n, _, _ := dashServer(t, okData(t, prData(true)), okData(t, issueData(true)))
	c := &Client{APIBase: srv.URL}
	if _, err := c.Dashboard(context.Background(), fullQuery()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	if got := n.Load(); got != 0 {
		t.Errorf("requests = %d, want 0", got)
	}
}

func TestDashboardGraphQLErrors(t *testing.T) {
	t.Run("data null is an error", func(t *testing.T) {
		srv, _, _, _ := dashServer(t,
			func(w http.ResponseWriter, _ dashReq) {
				writeJSON(t, w, map[string]any{"data": nil, "errors": []any{map[string]any{"message": "bad field"}}})
			},
			okData(t, issueData(true)))
		res, err := (&Client{APIBase: srv.URL, Token: "tok"}).Dashboard(context.Background(), fullQuery())
		if err != nil {
			t.Fatalf("Dashboard: %v", err)
		}
		if res.PRErr == nil || !strings.Contains(res.PRErr.Error(), "bad field") {
			t.Errorf("PRErr = %v, want the GraphQL message", res.PRErr)
		}
		if len(res.MyPRs.Items) != 0 || res.Assigned.Total != 1 {
			t.Errorf("result = %+v", res)
		}
	})
	t.Run("errors with data are notes", func(t *testing.T) {
		srv, _, _, _ := dashServer(t,
			func(w http.ResponseWriter, _ dashReq) {
				writeJSON(t, w, map[string]any{"data": prData(true), "errors": []any{map[string]any{"message": "Resource not accessible by integration"}}})
			},
			okData(t, issueData(true)))
		res, err := (&Client{APIBase: srv.URL, Token: "tok"}).Dashboard(context.Background(), fullQuery())
		if err != nil || res.PRErr != nil {
			t.Fatalf("Dashboard: %v / %v", err, res.PRErr)
		}
		if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "pr query: Resource not accessible") {
			t.Errorf("notes = %v", res.Notes)
		}
		if len(res.MyPRs.Items) != 1 {
			t.Errorf("partial data dropped: %+v", res.MyPRs)
		}
	})
}

func TestDashboardRateLimit(t *testing.T) {
	reset := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	limited200 := func(w http.ResponseWriter, _ dashReq) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		writeJSON(t, w, map[string]any{"data": nil, "errors": []any{map[string]any{"type": "RATE_LIMITED", "message": "API rate limit exceeded"}}})
	}
	limited403 := func(w http.ResponseWriter, _ dashReq) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusForbidden)
	}
	for name, pr := range map[string]func(http.ResponseWriter, dashReq){"200 with errors": limited200, "403": limited403} {
		t.Run(name, func(t *testing.T) {
			// The issue query succeeds: a limit on either side still wins.
			srv, _, _, _ := dashServer(t, pr, okData(t, issueData(true)))
			_, err := (&Client{APIBase: srv.URL, Token: "tok"}).Dashboard(context.Background(), fullQuery())
			var rl *RateLimitError
			if !errors.As(err, &rl) {
				t.Fatalf("err = %v, want *RateLimitError", err)
			}
			if name == "200 with errors" && !rl.Reset.Equal(reset) {
				t.Errorf("reset = %v, want %v", rl.Reset, reset)
			}
			if rl.Reset.Before(time.Now()) {
				t.Errorf("reset %v is in the past", rl.Reset)
			}
		})
	}
}

func TestDashboardTimeoutAndFailures(t *testing.T) {
	status := func(code int, body string) func(http.ResponseWriter, dashReq) {
		return func(w http.ResponseWriter, _ dashReq) {
			w.WriteHeader(code)
			_, _ = io.WriteString(w, body)
		}
	}
	t.Run("one side 502 keeps the other side", func(t *testing.T) {
		srv, n, _, _ := dashServer(t, okData(t, prData(true)), status(http.StatusBadGateway, "<html>502</html>"))
		res, err := (&Client{APIBase: srv.URL, Token: "tok"}).Dashboard(context.Background(), fullQuery())
		if err != nil {
			t.Fatalf("Dashboard: %v", err)
		}
		var to *QueryTimeoutError
		if !errors.As(res.IssueErr, &to) || to.Query != "issue" {
			t.Fatalf("IssueErr = %v, want an issue *QueryTimeoutError", res.IssueErr)
		}
		if len(res.MyPRs.Items) != 1 || res.Assigned.Total != 0 {
			t.Errorf("result = %+v", res)
		}
		if got := n.Load(); got != 2 {
			t.Errorf("requests = %d, want 2 (no retry with a smaller page)", got)
		}
	})
	t.Run("both sides fail", func(t *testing.T) {
		srv, _, _, _ := dashServer(t, status(http.StatusGatewayTimeout, ""), status(http.StatusBadGateway, ""))
		_, err := (&Client{APIBase: srv.URL, Token: "tok"}).Dashboard(context.Background(), fullQuery())
		var to *QueryTimeoutError
		if err == nil || !errors.As(err, &to) {
			t.Fatalf("err = %v, want a joined *QueryTimeoutError", err)
		}
		if !strings.Contains(err.Error(), "pr query") || !strings.Contains(err.Error(), "issue query") {
			t.Errorf("err = %q, want both queries named", err)
		}
	})
	t.Run("other status names status and body", func(t *testing.T) {
		long := strings.Repeat("x", 5000)
		srv, _, _, _ := dashServer(t, status(http.StatusInternalServerError, long), okData(t, issueData(true)))
		res, err := (&Client{APIBase: srv.URL, Token: "tok"}).Dashboard(context.Background(), fullQuery())
		if err != nil {
			t.Fatalf("Dashboard: %v", err)
		}
		msg := res.PRErr.Error()
		if !strings.Contains(msg, "500") || len(msg) > 4200 {
			t.Errorf("PRErr = %d bytes, %.80q", len(msg), msg)
		}
		var to *QueryTimeoutError
		if errors.As(res.PRErr, &to) {
			t.Errorf("500 reported as a timeout")
		}
	})
	t.Run("caller deadline is a timeout", func(t *testing.T) {
		block := make(chan struct{})
		hang := func(w http.ResponseWriter, _ dashReq) { <-block }
		srv, _, _, _ := dashServer(t, hang, hang)
		// Registered after dashServer, so it runs before srv.Close, which
		// waits for these handlers.
		t.Cleanup(func() { close(block) })
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := (&Client{APIBase: srv.URL, Token: "tok"}).Dashboard(ctx, fullQuery())
		var to *QueryTimeoutError
		if !errors.As(err, &to) {
			t.Fatalf("err = %v, want *QueryTimeoutError", err)
		}
	})
}

// A limit on the issue query wins too, and Retry-After sets the reset.
func TestDashboardRateLimitOnTheIssueQuery(t *testing.T) {
	for name, issue := range map[string]func(http.ResponseWriter, dashReq){
		"200 with errors": func(w http.ResponseWriter, _ dashReq) {
			w.Header().Set("Retry-After", "120")
			writeJSON(t, w, map[string]any{"data": nil, "errors": []any{map[string]any{"message": "rate limited"}}})
		},
		"403": func(w http.ResponseWriter, _ dashReq) {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusForbidden)
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv, _, _, _ := dashServer(t, okData(t, prData(true)), issue)
			before := time.Now()
			_, err := (&Client{APIBase: srv.URL, Token: "tok"}).Dashboard(context.Background(), fullQuery())
			var rl *RateLimitError
			if !errors.As(err, &rl) {
				t.Fatalf("err = %v, want *RateLimitError", err)
			}
			// 120 s, not the one-minute fallback.
			if d := rl.Reset.Sub(before); d < 115*time.Second || d > 125*time.Second {
				t.Errorf("reset in %v, want about 120s", d)
			}
		})
	}
}

// review_context is read by name, so it is found past the first page of checks.
func TestDashboardReadsTheReviewContextByName(t *testing.T) {
	withStatus := func(t *testing.T) func(http.ResponseWriter, dashReq) {
		return func(w http.ResponseWriter, _ dashReq) {
			d := prData(true)
			node := d["mine"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
			commit := node["commits"].(map[string]any)["nodes"].([]any)[0].(map[string]any)["commit"].(map[string]any)
			commit["status"] = map[string]any{"context": map[string]any{"state": "FAILURE", "description": "차단 1건", "targetUrl": "https://rv/83"}}
			writeJSON(t, w, map[string]any{"data": d})
		}
	}
	srv, _, _, reqs := dashServer(t, withStatus(t), okData(t, issueData(true)))
	q := fullQuery()
	q.ReviewContext = "reviewer"
	res, err := (&Client{APIBase: srv.URL, Token: "tok"}).Dashboard(context.Background(), q)
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}
	got := res.MyPRs.Items[0].ReviewStatus
	if got == nil || got.State != "FAILURE" || got.Description != "차단 1건" || got.URL != "https://rv/83" {
		t.Errorf("review status = %+v", got)
	}
	for _, r := range *reqs {
		if isPRQuery(r.Query) && (!strings.Contains(r.Query, "context(name: $rc)") || r.Variables["rc"] != "reviewer") {
			t.Errorf("PR query does not read the context by name: %v\n%s", r.Variables, r.Query)
		}
	}

	srv2, _, _, reqs2 := dashServer(t, okData(t, prData(true)), okData(t, issueData(true)))
	res2, err := (&Client{APIBase: srv2.URL, Token: "tok"}).Dashboard(context.Background(), fullQuery())
	if err != nil {
		t.Fatalf("Dashboard: %v", err)
	}
	if res2.MyPRs.Items[0].ReviewStatus != nil {
		t.Errorf("review status without a context = %+v", res2.MyPRs.Items[0].ReviewStatus)
	}
	for _, r := range *reqs2 {
		if strings.Contains(r.Query, "$rc") {
			t.Errorf("query without a review context declares $rc:\n%s", r.Query)
		}
	}
}
