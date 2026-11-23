package github

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func compareJSON(ahead, behind int, head string, commits ...map[string]any) map[string]any {
	nodes := make([]any, len(commits))
	for i, c := range commits {
		nodes[i] = c
	}
	return map[string]any{"aheadBy": ahead, "behindBy": behind, "headTarget": map[string]any{"oid": head}, "commits": map[string]any{"nodes": nodes}}
}

func commitJSON(oid, parent, message string) map[string]any {
	parents := []any{}
	if parent != "" {
		parents = append(parents, map[string]any{"oid": parent})
	}
	return map[string]any{"oid": oid, "message": message, "committedDate": "2026-11-17T01:00:00Z", "parents": map[string]any{"nodes": parents}}
}

func unreleasedRepoJSON(name, defaultBranch string, tag map[string]any) map[string]any {
	n := map[string]any{"nameWithOwner": name, "defaultBranchRef": nil, "tags": map[string]any{"nodes": []any{}}}
	if defaultBranch != "" {
		n["defaultBranchRef"] = map[string]any{"name": defaultBranch}
	}
	if tag != nil {
		n["tags"] = map[string]any{"nodes": []any{tag}}
	}
	return n
}

func unreleasedDataJSON(nodes ...map[string]any) map[string]any {
	list := make([]any, len(nodes))
	for i, n := range nodes {
		list[i] = n
	}
	return map[string]any{"repositoryOwner": map[string]any{"repositories": map[string]any{"totalCount": 40, "nodes": list}}}
}

func TestOwnerUnreleased(t *testing.T) {
	both := map[string]any{"name": "v1.2.0",
		"head": compareJSON(0, 0, "m1"),
		"dev":  compareJSON(2, 1, "d2", commitJSON("d1", "m1", "fix: a (#7)"), commitJSON("d2", "d1", "Merge pull request #8 from o/b\n\nfix: b")),
	}
	onlyDefault := map[string]any{"name": "v0.1.0", "head": compareJSON(3, 0, "h1", commitJSON("h1", "", "feat: c")), "dev": nil}
	// The default branch is develop: both comparisons are the same branch.
	developDefault := map[string]any{"name": "v2.0.0", "head": compareJSON(1, 0, "x1"), "dev": compareJSON(1, 0, "x1")}
	data := unreleasedDataJSON(
		unreleasedRepoJSON("o/both", "main", both),
		unreleasedRepoJSON("o/only-default", "main", onlyDefault),
		unreleasedRepoJSON("o/develop-default", "develop", developDefault),
		unreleasedRepoJSON("o/no-tag", "main", nil),
		// No default branch: a comparison with HEAD has no branch to name.
		unreleasedRepoJSON("o/empty", "", map[string]any{"name": "v0.0.1", "head": compareJSON(1, 0, "e1"), "dev": nil}),
	)
	srv, n, _, reqs := dashServer(t, nil, func(w http.ResponseWriter, _ dashReq) {
		writeJSON(t, w, map[string]any{"data": data, "errors": []any{
			map[string]any{"type": "NOT_FOUND", "message": "Could not resolve head ref 'develop'.", "path": []any{"repositoryOwner", "repositories", "nodes", 1, "tags", "nodes", 0, "dev"}},
			map[string]any{"type": "NOT_FOUND", "message": "Could not resolve head ref 'HEAD'.", "path": []any{"repositoryOwner", "repositories", "nodes", 4, "tags", "nodes", 0, "head"}},
			map[string]any{"type": "FORBIDDEN", "message": "Resource not accessible", "path": []any{"repositoryOwner", "repositories", "nodes", 3, "tags", "nodes", 0, "dev"}},
		}})
	})
	res, err := (&Client{APIBase: srv.URL, Token: "tok"}).OwnerUnreleased(context.Background(), "o")
	if err != nil {
		t.Fatalf("OwnerUnreleased: %v", err)
	}
	if n.Load() != 1 {
		t.Errorf("requests = %d, want 1", n.Load())
	}
	q := (*reqs)[0]
	for _, want := range []string{"repositoryOwner(login: $owner)", "first: 20", "PUSHED_AT", "isFork: false", "isArchived: false", "TAG_COMMIT_DATE",
		`compare(headRef: "HEAD")`, `compare(headRef: "develop")`, "aheadBy behindBy", "commits(last: 30)", "parents(first: 1)"} {
		if !strings.Contains(q.Query, want) {
			t.Errorf("query has no %q:\n%s", want, q.Query)
		}
	}
	if strings.Contains(q.Query, "messageHeadline") {
		t.Errorf("query reads messageHeadline, which GitHub truncates:\n%s", q.Query)
	}
	if q.Variables["owner"] != "o" {
		t.Errorf("variables = %v", q.Variables)
	}

	if res.Total != 40 || res.Scanned != 5 || len(res.Repos) != 4 {
		t.Fatalf("total %d scanned %d repos %d, want 40, 5, and 4 (one has no tag)", res.Total, res.Scanned, len(res.Repos))
	}
	// A missing develop is expected. Every other error is a note.
	if len(res.Notes) != 2 || !strings.Contains(res.Notes[0], "'HEAD'") || !strings.Contains(res.Notes[1], "Resource not accessible") {
		t.Errorf("notes = %v", res.Notes)
	}
	b := res.Repos[0]
	if b.Repo != "o/both" || b.Tag != "v1.2.0" || len(b.Branches) != 2 || b.Branches[0].Branch != "main" || b.Branches[1].Branch != "develop" {
		t.Fatalf("repo = %+v", b)
	}
	dev := b.Branches[1]
	if dev.Ahead != 2 || dev.Behind != 1 || dev.Head != "d2" || len(dev.Commits) != 2 || dev.Default || !b.Branches[0].Default {
		t.Fatalf("develop = %+v", dev)
	}
	if c := dev.Commits[1]; c.OID != "d2" || c.Parent != "d1" || !strings.HasPrefix(c.Message, "Merge pull request #8") || c.CommittedAt.IsZero() {
		t.Errorf("commit = %+v", c)
	}
	if o := res.Repos[1]; len(o.Branches) != 1 || o.Branches[0].Branch != "main" || o.Branches[0].Commits[0].Parent != "" {
		t.Errorf("only-default = %+v", o)
	}
	if d := res.Repos[2]; len(d.Branches) != 1 || d.Branches[0].Branch != "develop" || !d.Branches[0].Default {
		t.Errorf("develop-default = %+v, want one comparison", d)
	}
	if e := res.Repos[3]; e.Tag != "v0.0.1" || len(e.Branches) != 0 {
		t.Errorf("empty = %+v, want no comparison", e)
	}
}

func TestOwnerUnreleasedFailures(t *testing.T) {
	t.Run("no token sends nothing", func(t *testing.T) {
		srv, n, _, _ := dashServer(t, nil, nil)
		if _, err := (&Client{APIBase: srv.URL}).OwnerUnreleased(context.Background(), "o"); !errors.Is(err, ErrNoToken) {
			t.Errorf("err = %v, want ErrNoToken", err)
		}
		if n.Load() != 0 {
			t.Errorf("requests = %d, want 0", n.Load())
		}
	})
	t.Run("an owner GitHub does not know", func(t *testing.T) {
		srv, _, _, _ := dashServer(t, nil, okData(t, map[string]any{"repositoryOwner": nil}))
		_, err := (&Client{APIBase: srv.URL, Token: "tok"}).OwnerUnreleased(context.Background(), "nobody")
		if err == nil || !strings.Contains(err.Error(), `owner "nobody" not found`) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a timeout is a QueryTimeoutError", func(t *testing.T) {
		srv, _, _, _ := dashServer(t, nil, func(w http.ResponseWriter, _ dashReq) { w.WriteHeader(http.StatusBadGateway) })
		_, err := (&Client{APIBase: srv.URL, Token: "tok"}).OwnerUnreleased(context.Background(), "o")
		var qt *QueryTimeoutError
		if !errors.As(err, &qt) || qt.Query != "unreleased" {
			t.Errorf("err = %v, want a QueryTimeoutError of the unreleased query", err)
		}
	})
	t.Run("a rate limit is a RateLimitError", func(t *testing.T) {
		srv, _, _, _ := dashServer(t, nil, func(w http.ResponseWriter, _ dashReq) {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusForbidden)
		})
		_, err := (&Client{APIBase: srv.URL, Token: "tok"}).OwnerUnreleased(context.Background(), "o")
		var rl *RateLimitError
		if !errors.As(err, &rl) {
			t.Errorf("err = %v, want a RateLimitError", err)
		}
	})
}
