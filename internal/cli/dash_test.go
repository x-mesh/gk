package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// dashCmdRepo is a repository whose origin is github.com/o/r, with a local
// branch fix/a and origin/main at main.
func dashCmdRepo(t *testing.T) (*testutil.Repo, string) {
	t.Helper()
	r := testutil.NewRepo(t)
	r.AddRemote("origin", "https://github.com/o/r.git")
	r.CreateBranch("fix/a")
	r.WriteFile("a.txt", "a\n")
	head := r.Commit("a")
	r.Checkout("main")
	r.RunGit("update-ref", "refs/remotes/origin/main", "main")
	return r, strings.TrimSpace(r.RunGit("rev-parse", head))
}

func dashGraphQLPR(head, oid string) map[string]any {
	return map[string]any{
		"number": 7, "title": "feat: a dashboard", "url": "https://github.com/o/r/pull/7", "isDraft": false, "state": "OPEN",
		"headRefName": head, "headRefOid": oid, "baseRefName": "main", "baseRefOid": "", "isCrossRepository": false,
		"reviewDecision": "APPROVED", "updatedAt": "2026-11-17T01:00:00Z",
		"repository": map[string]any{"nameWithOwner": "o/r"}, "author": map[string]any{"login": "me"},
		"closingIssuesReferences": map[string]any{"nodes": []any{}},
		"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{
			"state": "SUCCESS", "contexts": map[string]any{"totalCount": 1, "nodes": []any{
				map[string]any{"__typename": "CheckRun", "name": "test", "status": "COMPLETED", "conclusion": "SUCCESS"},
			}}}}}}},
	}
}

type dashFake struct {
	graphql atomic.Int32
	mu      sync.Mutex
	queries []string
	limited bool
}

func (f *dashFake) server(t *testing.T, oid string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/users/") {
			writeDashJSON(t, w, map[string]any{"login": "o", "type": "Organization"})
			return
		}
		f.graphql.Add(1)
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &req)
		f.mu.Lock()
		f.queries = append(f.queries, req.Query)
		f.mu.Unlock()
		if f.limited {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if strings.Contains(req.Query, "mine: search") {
			pr := dashGraphQLPR("fix/a", oid)
			data := map[string]any{
				"mine":   map[string]any{"issueCount": 1, "nodes": []any{pr}},
				"review": map[string]any{"issueCount": 0, "nodes": []any{}},
			}
			if strings.Contains(req.Query, "repository(") {
				data["repository"] = map[string]any{
					"open":   map[string]any{"totalCount": 1, "nodes": []any{pr}},
					"merged": map[string]any{"totalCount": 0, "nodes": []any{}},
				}
			}
			writeDashJSON(t, w, map[string]any{"data": data})
			return
		}
		data := map[string]any{
			"assigned":  map[string]any{"issueCount": 1, "nodes": []any{map[string]any{"number": 70, "title": "track the dashboard", "url": "u", "updatedAt": "2026-11-17T01:00:00Z", "repository": map[string]any{"nameWithOwner": "o/r"}, "assignees": map[string]any{"totalCount": 1}, "closedByPullRequestsReferences": map[string]any{"nodes": []any{map[string]any{"number": 7, "state": "OPEN", "merged": false}}}}}},
			"mentioned": map[string]any{"issueCount": 0, "nodes": []any{}},
			"authored":  map[string]any{"issueCount": 0, "nodes": []any{}},
		}
		if strings.Contains(req.Query, "repository(") {
			data["repository"] = map[string]any{"issues": map[string]any{"totalCount": 4}, "unassigned": map[string]any{"totalCount": 0, "nodes": []any{}}}
		}
		writeDashJSON(t, w, map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeDashJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode: %v", err)
	}
}

// runDashCmd runs gk dash in dir against srv, with a token unless noToken.
func runDashCmd(t *testing.T, dir string, srv *httptest.Server, token string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("GH_TOKEN", token)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	prevBase, prevRepo := dashAPIBase, flagRepo
	dashAPIBase, flagRepo = srv.URL, ""
	t.Cleanup(func() { dashAPIBase, flagRepo = prevBase, prevRepo })
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	resetForecastMemo()

	cmd := newDashCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), err
}

func TestDashCmdTable(t *testing.T) {
	withAgentMode(t, false)
	r, oid := dashCmdRepo(t)
	f := &dashFake{}
	out, err := runDashCmd(t, r.Dir, f.server(t, oid), "tok")
	if err != nil {
		t.Fatalf("gk dash: %v\n%s", err, out)
	}
	for _, want := range []string{"gk dash · ", "o/r", "PRs", "mine 1", "#7", "feat: a dashboard", "fix/a", "ready to merge", "Issues", "#70", "#7 open", "PR #7 in review", "3 more assigned to others", "Forecast", "main · 1 PR(s)", "no conflicts", "branch"} {
		if !strings.Contains(out, want) {
			t.Errorf("output has no %q:\n%s", want, out)
		}
	}
	if got := f.graphql.Load(); got != 2 {
		t.Errorf("graphql requests = %d, want 2", got)
	}
	if c, ok := readGitHubCounts(context.Background(), &git.ExecRunner{Dir: r.Dir}, "o/r"); !ok || c.OpenPRs != 1 {
		t.Errorf("counts cache = %+v, %v; want open_prs 1", c, ok)
	}
}

func TestDashCmdJSON(t *testing.T) {
	withAgentMode(t, true)
	r, oid := dashCmdRepo(t)
	out, err := runDashCmd(t, r.Dir, (&dashFake{}).server(t, oid), "tok")
	if err != nil {
		t.Fatalf("gk dash: %v\n%s", err, out)
	}
	var env struct {
		State  string       `json:"state"`
		Result dashSnapshot `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if env.State != "ok" || env.Result.Scope != "repo:o/r" || len(env.Result.PRs.Items) != 1 {
		t.Fatalf("envelope = %+v", env)
	}
	row := env.Result.PRs.Items[0]
	if row.Action.Rule != 8 || len(row.Branches) != 1 || row.Branches[0].Name != "fix/a" || row.Review.Source != "decision" {
		t.Errorf("row = %+v", row)
	}
	if len(env.Result.Forecast) != 1 || env.Result.Forecast[0].Base != "main" {
		t.Errorf("forecast = %+v", env.Result.Forecast)
	}
}

func TestDashCmdWithoutTokenSendsNothing(t *testing.T) {
	withAgentMode(t, false)
	r, oid := dashCmdRepo(t)
	f := &dashFake{}
	_, err := runDashCmd(t, r.Dir, f.server(t, oid), "")
	if err == nil || !strings.Contains(err.Error(), "GH_TOKEN") || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("err = %v, want the token guidance", err)
	}
	if got := f.graphql.Load(); got != 0 {
		t.Errorf("graphql requests = %d, want 0", got)
	}
}

func TestDashCmdRateLimited(t *testing.T) {
	withAgentMode(t, false)
	r, oid := dashCmdRepo(t)
	f := &dashFake{limited: true}
	out, err := runDashCmd(t, r.Dir, f.server(t, oid), "tok")
	if err == nil || !strings.Contains(err.Error(), "rate limit") || !strings.Contains(err.Error(), "retry after") {
		t.Fatalf("err = %v, want a rate-limit error with the retry time", err)
	}
	if strings.Contains(out, "PRs") {
		t.Errorf("printed a table on a failed read:\n%s", out)
	}
}

func TestDashCmdOrgScope(t *testing.T) {
	withAgentMode(t, false)
	r, oid := dashCmdRepo(t)
	f := &dashFake{}
	out, err := runDashCmd(t, r.Dir, f.server(t, oid), "tok", "--org", "o")
	if err != nil {
		t.Fatalf("gk dash --org: %v\n%s", err, out)
	}
	for _, q := range f.queries {
		if strings.Contains(q, "repository(") || strings.Contains(q, "unassigned") {
			t.Errorf("org query has a repository block:\n%s", q)
		}
	}
	for _, unwanted := range []string{"Forecast", "branch", "cleanup", "unassigned", "assigned to others"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("org output has %q:\n%s", unwanted, out)
		}
	}
	if !strings.Contains(out, "#7") {
		t.Errorf("org output has no PR:\n%s", out)
	}
}

func TestDashCmdRejectsPositional(t *testing.T) {
	r, oid := dashCmdRepo(t)
	_, err := runDashCmd(t, r.Dir, (&dashFake{}).server(t, oid), "tok", "acme")
	if err == nil || !strings.Contains(err.Error(), "--org acme") {
		t.Fatalf("err = %v, want the --org hint", err)
	}
}

// --fetch fetches the remote before the forecast, and a failed fetch stops
// the command before any GitHub request. The origin is an ssh URL on
// github.com, so the scope resolves, and GIT_SSH_COMMAND serves it from a
// local bare repository.
func TestDashCmdFetch(t *testing.T) {
	withAgentMode(t, false)
	sshOrigin := func(t *testing.T, r *testutil.Repo, sshCommand string) {
		r.RunGit("remote", "set-url", "origin", "git@github.com:o/r.git")
		t.Setenv("GIT_SSH_COMMAND", sshCommand)
	}
	t.Run("fetches first", func(t *testing.T) {
		r, oid := dashCmdRepo(t)
		bare := filepath.Join(t.TempDir(), "r.git")
		r.RunGit("clone", "-q", "--bare", r.Dir, bare)
		other := filepath.Join(t.TempDir(), "other")
		r.RunGit("clone", "-q", bare, other)
		r.RunGit("-C", other, "-c", "user.name=o", "-c", "user.email=o@o", "commit", "-q", "--allow-empty", "-m", "remote moves")
		r.RunGit("-C", other, "push", "-q", "origin", "main")
		script := filepath.Join(t.TempDir(), "ssh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nexec git upload-pack '"+bare+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		sshOrigin(t, r, script)
		want := strings.TrimSpace(r.RunGit("--git-dir", bare, "rev-parse", "main"))
		f := &dashFake{}
		if out, err := runDashCmd(t, r.Dir, f.server(t, oid), "tok", "--fetch"); err != nil {
			t.Fatalf("gk dash --fetch: %v\n%s", err, out)
		}
		if got := strings.TrimSpace(r.RunGit("rev-parse", "refs/remotes/origin/main")); got != want {
			t.Errorf("origin/main = %s, want the fetched %s", got, want)
		}
	})
	t.Run("a failed fetch stops", func(t *testing.T) {
		r, oid := dashCmdRepo(t)
		sshOrigin(t, r, "false")
		f := &dashFake{}
		_, err := runDashCmd(t, r.Dir, f.server(t, oid), "tok", "--fetch")
		if err == nil || !strings.Contains(err.Error(), "fetch origin") {
			t.Fatalf("err = %v, want a fetch error", err)
		}
		if got := f.graphql.Load(); got != 0 {
			t.Errorf("graphql requests = %d after a failed fetch, want 0", got)
		}
	})
}
