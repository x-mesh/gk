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

func TestFindCommits_QueryWithPathUsesPathAsScopeNotMatch(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("internal/needle.go", "package internal\n\nconst needle = true\n")
	repo.Commit("feat: add scoped needle")
	repo.WriteFile("internal/unrelated.go", "package internal\n")
	repo.Commit("chore: unrelated newer change")
	repo.WriteFile("outside/needle.go", "package outside\n\nconst needle = true\n")
	repo.Commit("feat: add needle outside scope")

	message, content, pathMode, err := resolveFindModes("needle", "internal", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if pathMode {
		t.Fatal("query + --path must use the path only as a scope filter")
	}
	res := runFindQuery(t, repo, findQuery{
		query: "needle", path: "internal", message: message, content: content, pathMode: pathMode,
	})
	for _, match := range res.Matches {
		if strings.Contains(match.Subject, "unrelated") || strings.Contains(match.Subject, "outside scope") || slices.Contains(match.Matched, findModePath) {
			t.Fatalf("scoped search returned a false path match: %+v", res.Matches)
		}
	}
	if len(res.Matches) != 1 || !strings.Contains(res.Matches[0].Subject, "needle") {
		t.Fatalf("scoped search did not return the real match: %+v", res.Matches)
	}
}

func TestResolveFindModesRejectsNoSearches(t *testing.T) {
	if _, _, _, err := resolveFindModes("needle", "", true, true, true); err == nil {
		t.Fatal("disabling every mode must fail instead of reporting zero matches")
	}
	if _, _, _, err := resolveFindModes("", "file.go", false, false, true); err == nil {
		t.Fatal("path-only search with --no-path must fail")
	}
}

func TestValidateFindSinceRejectsGitNowFallback(t *testing.T) {
	repo := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	for _, valid := range []string{"0w", "2w", "2026-06-01", "last monday", "1 second ago", "now"} {
		if err := validateFindSince(context.Background(), runner, valid); err != nil {
			t.Errorf("valid --since %q rejected: %v", valid, err)
		}
	}
	if err := validateFindSince(context.Background(), runner, "definitely-not-a-date"); err == nil {
		t.Fatal("nonsense --since must not silently become now")
	}
}

func TestFindCommits_PathOnlyFollowsRename(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("old.go", "package renamed\n")
	repo.Commit("feat: add original file")
	repo.RunGit("mv", "old.go", "new.go")
	repo.Commit("refactor: rename file")

	res := runFindQuery(t, repo, findQuery{path: "new.go", pathMode: true, follow: true})
	if len(res.Matches) != 2 {
		t.Fatalf("path-only history must cross the rename: %+v", res.Matches)
	}
}

// A mode that fails must SAY so. A partial answer beats no answer, but it must
// never be dressed up as a complete one — the agent would read "no match" as
// "not in the history".
//
// (Invalid --since input is rejected before fan-out. An unknown ref fails in
// every mode and exercises the partial-result contract here.)
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
