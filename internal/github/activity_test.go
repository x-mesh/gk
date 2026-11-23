package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func twoDays() []DayRange {
	kst := time.FixedZone("KST", 9*3600)
	d1 := time.Date(2026, 11, 22, 0, 0, 0, 0, kst)
	d2 := d1.AddDate(0, 0, 1)
	return []DayRange{{Start: d1, End: d2.Add(-time.Second)}, {Start: d2, End: d2.AddDate(0, 0, 1).Add(-time.Second)}}
}

func TestPRActivity(t *testing.T) {
	srv, n, _, reqs := dashServer(t, nil, okData(t, map[string]any{
		"o0": map[string]any{"issueCount": 4}, "m0": map[string]any{"issueCount": 3},
		"o1": map[string]any{"issueCount": 0}, "m1": map[string]any{"issueCount": 7},
	}))
	opened, merged, err := (&Client{APIBase: srv.URL, Token: "tok"}).PRActivity(context.Background(), "org:o", twoDays())
	if err != nil {
		t.Fatalf("PRActivity: %v", err)
	}
	if fmt.Sprint(opened, merged) != "[4 0] [3 7]" {
		t.Errorf("opened %v merged %v, want [4 0] [3 7]", opened, merged)
	}
	if n.Load() != 1 {
		t.Errorf("requests = %d, want 1", n.Load())
	}
	q := (*reqs)[0]
	for _, want := range []string{"o0: search(type: ISSUE, query: $o0, first: 1) { issueCount }", "m1: search(type: ISSUE, query: $m1"} {
		if !strings.Contains(q.Query, want) {
			t.Errorf("query has no %q:\n%s", want, q.Query)
		}
	}
	// A day is a range in the caller's time zone, not a UTC date.
	wantVars := map[string]string{
		"o0": "org:o is:pr created:2026-11-22T00:00:00+09:00..2026-11-22T23:59:59+09:00",
		"m1": "org:o is:pr merged:2026-11-23T00:00:00+09:00..2026-11-23T23:59:59+09:00",
	}
	for k, want := range wantVars {
		if q.Variables[k] != want {
			t.Errorf("variable %s = %v\nwant         %s", k, q.Variables[k], want)
		}
	}
	if len(q.Variables) != 4 {
		t.Errorf("variables = %v, want 4", q.Variables)
	}

	t.Run("no day sends nothing", func(t *testing.T) {
		srv, n, _, _ := dashServer(t, nil, nil)
		if o, m, err := (&Client{APIBase: srv.URL, Token: "tok"}).PRActivity(context.Background(), "org:o", nil); err != nil || o != nil || m != nil || n.Load() != 0 {
			t.Errorf("no day: %v %v %v, %d requests", o, m, err, n.Load())
		}
	})
	t.Run("no token sends nothing", func(t *testing.T) {
		srv, n, _, _ := dashServer(t, nil, nil)
		if _, _, err := (&Client{APIBase: srv.URL}).PRActivity(context.Background(), "org:o", twoDays()); !errors.Is(err, ErrNoToken) || n.Load() != 0 {
			t.Errorf("err = %v, %d requests", err, n.Load())
		}
	})
	t.Run("a search that failed is an error, not a zero", func(t *testing.T) {
		srv, _, _, _ := dashServer(t, nil, func(w http.ResponseWriter, _ dashReq) {
			writeJSON(t, w, map[string]any{"data": map[string]any{"o0": map[string]any{"issueCount": 4}, "m0": nil},
				"errors": []any{map[string]any{"message": "search timed out"}}})
		})
		_, _, err := (&Client{APIBase: srv.URL, Token: "tok"}).PRActivity(context.Background(), "org:o", twoDays())
		if err == nil || !strings.Contains(err.Error(), "search timed out") {
			t.Errorf("err = %v, want the GraphQL message", err)
		}
	})
}

func commitRepoJSON(pushedAt string, counts ...int) map[string]any {
	target := map[string]any{}
	for i, c := range counts {
		target[fmt.Sprintf("d%d", i)] = map[string]any{"totalCount": c}
	}
	return map[string]any{"pushedAt": pushedAt, "defaultBranchRef": map[string]any{"target": target}}
}

func commitData(total int, nodes ...map[string]any) map[string]any {
	list := make([]any, len(nodes))
	for i, n := range nodes {
		list[i] = n
	}
	return map[string]any{"repositoryOwner": map[string]any{"repositories": map[string]any{"totalCount": total, "nodes": list}}}
}

func TestOwnerCommitActivity(t *testing.T) {
	empty := map[string]any{"pushedAt": "2026-11-22T03:00:00Z", "defaultBranchRef": nil}
	// The window starts at 2026-11-22T00:00+09:00, which is 2026-11-21T15:00Z.
	srv, n, _, reqs := dashServer(t, nil, okData(t, commitData(3,
		commitRepoJSON("2026-11-23T01:00:00Z", 2, 5), empty, commitRepoJSON("2026-11-21T14:59:59Z", 1, 0))))
	res, err := (&Client{APIBase: srv.URL, Token: "tok"}).OwnerCommitActivity(context.Background(), "o", twoDays())
	if err != nil {
		t.Fatalf("OwnerCommitActivity: %v", err)
	}
	if fmt.Sprint(res.Days) != "[3 5]" || res.Partial {
		t.Errorf("days %v partial %v, want [3 5] and every repository read", res.Days, res.Partial)
	}
	if n.Load() != 1 {
		t.Errorf("requests = %d, want 1", n.Load())
	}
	q := (*reqs)[0]
	for _, want := range []string{"repositoryOwner(login: $owner)", "first: 30", "PUSHED_AT", "isFork: false", "isArchived: false",
		"d0: history(since: $s0, until: $u0) { totalCount }", "d1: history(since: $s1, until: $u1)", "$s1: GitTimestamp!"} {
		if !strings.Contains(q.Query, want) {
			t.Errorf("query has no %q:\n%s", want, q.Query)
		}
	}
	if q.Variables["owner"] != "o" || q.Variables["s0"] != "2026-11-22T00:00:00+09:00" || q.Variables["u1"] != "2026-11-23T23:59:59+09:00" {
		t.Errorf("variables = %v", q.Variables)
	}

	for _, c := range []struct {
		name     string
		total    int
		lastPush string
		partial  bool
	}{
		{"more repositories, the last one read was pushed in the window", 9, "2026-11-21T15:00:00Z", true},
		{"more repositories, the last one read was pushed before the window", 9, "2026-11-21T14:59:59Z", false},
		{"every repository read", 2, "2026-11-23T00:00:00Z", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, _, _, _ := dashServer(t, nil, okData(t, commitData(c.total, commitRepoJSON("2026-11-23T01:00:00Z", 1, 1), commitRepoJSON(c.lastPush, 0, 0))))
			res, err := (&Client{APIBase: srv.URL, Token: "tok"}).OwnerCommitActivity(context.Background(), "o", twoDays())
			if err != nil || res.Partial != c.partial {
				t.Errorf("partial = %v, err %v; want %v", res != nil && res.Partial, err, c.partial)
			}
		})
	}

	t.Run("an owner GitHub does not know", func(t *testing.T) {
		srv, _, _, _ := dashServer(t, nil, okData(t, map[string]any{"repositoryOwner": nil}))
		_, err := (&Client{APIBase: srv.URL, Token: "tok"}).OwnerCommitActivity(context.Background(), "nobody", twoDays())
		if err == nil || !strings.Contains(err.Error(), `owner "nobody" not found`) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("a partial answer is an error, not a low count", func(t *testing.T) {
		srv, _, _, _ := dashServer(t, nil, func(w http.ResponseWriter, _ dashReq) {
			writeJSON(t, w, map[string]any{"data": commitData(1, commitRepoJSON("2026-11-23T01:00:00Z", 1, 1)), "errors": []any{map[string]any{"message": "timeout on a repository"}}})
		})
		if _, err := (&Client{APIBase: srv.URL, Token: "tok"}).OwnerCommitActivity(context.Background(), "o", twoDays()); err == nil || !strings.Contains(err.Error(), "timeout on a repository") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("no token sends nothing", func(t *testing.T) {
		srv, n, _, _ := dashServer(t, nil, nil)
		if _, err := (&Client{APIBase: srv.URL}).OwnerCommitActivity(context.Background(), "o", twoDays()); !errors.Is(err, ErrNoToken) || n.Load() != 0 {
			t.Errorf("err = %v, %d requests", err, n.Load())
		}
	})
}
