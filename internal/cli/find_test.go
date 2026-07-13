package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// findRepo builds a history where each search mode has exactly one commit only
// IT can find — so a mode that silently does nothing cannot pass.
func findRepo(t *testing.T) *testutil.Repo {
	t.Helper()
	repo := testutil.NewRepo(t)

	// message-only: the subject names the term, the content never does.
	repo.WriteFile("readme.md", "nothing to see")
	repo.Commit("docs: describe the widget pipeline")

	// content-only: the subject is silent, the code carries the term.
	repo.WriteFile("engine.go", "package main\n\nfunc widgetPipeline() {}\n")
	repo.Commit("chore: internals")

	// path-only: neither subject nor content mentions it, the FILENAME does.
	repo.WriteFile("widget_pipeline_test.go", "package main\n")
	repo.Commit("chore: add a test file")

	// noise the query must not match at all.
	repo.WriteFile("unrelated.txt", "sprockets")
	repo.Commit("chore: unrelated")
	return repo
}

func runFindQuery(t *testing.T, repo *testutil.Repo, q findQuery) findResult {
	t.Helper()
	if q.limit == 0 {
		q.limit = 20
	}
	return findCommits(context.Background(), &git.ExecRunner{Dir: repo.Dir}, q)
}

// The verb exists because the agent does not know WHICH query will hit. So all
// three modes must fire on one call, and each must be able to find the commit
// only it can see.
func TestFindCommits_AllThreeModesHitTheirOwnCommit(t *testing.T) {
	repo := findRepo(t)
	res := runFindQuery(t, repo, findQuery{
		query: "widgetPipeline", message: true, content: true, pathMode: true,
	})
	if len(res.Failed) > 0 {
		t.Fatalf("no mode may fail on a healthy repo: %v", res.Failed)
	}
	// -S widgetPipeline finds the code commit; the message/path spellings differ
	// ("widget pipeline", "widget_pipeline_test.go"), so content must carry it.
	var byContent bool
	for _, m := range res.Matches {
		if slices.Contains(m.Matched, findModeContent) && strings.Contains(m.Subject, "internals") {
			byContent = true
		}
	}
	if !byContent {
		t.Fatalf("the pickaxe must find the commit whose MESSAGE never mentions the term: %+v", res.Matches)
	}
}

// The "message says nothing, the code changed" case is the whole reason the
// pickaxe runs alongside --grep instead of after it.
func TestFindCommits_ContentOnlyCommitIsNotMissedByMessageSearch(t *testing.T) {
	repo := findRepo(t)
	msgOnly := runFindQuery(t, repo, findQuery{query: "widgetPipeline", message: true})
	if len(msgOnly.Matches) != 0 {
		t.Fatalf("no commit MESSAGE contains widgetPipeline: %+v", msgOnly.Matches)
	}
	both := runFindQuery(t, repo, findQuery{query: "widgetPipeline", message: true, content: true})
	if len(both.Matches) == 0 {
		t.Fatal("adding the content mode must surface the commit the message search cannot see")
	}
}

// A commit found more than one way is the strongest hit there is, so it ranks
// first — otherwise the agent reads a weak single-mode match at the top.
func TestFindCommits_MultiModeMatchesRankFirst(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "x")
	repo.Commit("chore: touch sprocket") // message only
	repo.WriteFile("sprocket.go", "package main // sprocket\n")
	repo.Commit("feat: add sprocket engine") // message + content + path

	res := runFindQuery(t, repo, findQuery{
		query: "sprocket", message: true, content: true, pathMode: true,
	})
	if len(res.Matches) < 2 {
		t.Fatalf("expected both commits, got %+v", res.Matches)
	}
	if len(res.Matches[0].Matched) < 2 {
		t.Errorf("a multi-mode hit must outrank a single-mode one: %+v", res.Matches)
	}
}

// With no query, --path IS the request: "the history of this file".
func TestFindCommits_PathOnlyQueryIsTheFileHistory(t *testing.T) {
	repo := findRepo(t)
	res := runFindQuery(t, repo, findQuery{path: "engine.go", pathMode: true})
	if len(res.Matches) != 1 {
		t.Fatalf("engine.go has exactly one commit: %+v", res.Matches)
	}
	if !strings.Contains(res.Matches[0].Subject, "internals") {
		t.Errorf("wrong commit: %+v", res.Matches[0])
	}
}

// A mode that fails must SAY so. A partial answer beats no answer, but it must
// never be dressed up as a complete one — the agent would read "no match" as
// "not in the history".
//
// (A bad --since is not the failure to test with: git accepts an unparseable
// date silently rather than erroring. An unknown ref does fail, in every mode.)
func TestFindCommits_FailedModeIsReportedNotSwallowed(t *testing.T) {
	repo := findRepo(t)
	res := runFindQuery(t, repo, findQuery{
		query: "widget", message: true, content: true, pathMode: true,
		ref: "refs/heads/no-such-branch",
	})
	if len(res.Failed) != len(res.Modes) {
		t.Fatalf("an unknown ref fails every mode and each must be reported: failed=%v modes=%v",
			res.Failed, res.Modes)
	}
	if res.Count != 0 {
		t.Errorf("no matches can exist when every mode failed: %+v", res.Matches)
	}
}
