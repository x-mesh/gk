package cli

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// contentRepo has 30 commits on main and one more on a side branch. Of each
// three commits on main, one changes the value on the needleTerm line of
// hit.txt, one changes a line with needleTerm under dir/, and one changes a
// line without it, so the matches are spread through the history with gaps.
func contentRepo(t *testing.T) *testutil.Repo {
	t.Helper()
	repo := testutil.NewRepo(t)
	for i := range 30 {
		switch i % 3 {
		case 0:
			repo.WriteFile("hit.txt", fmt.Sprintf("needleTerm=%d\n", i))
		case 1:
			repo.WriteFile("plain.txt", fmt.Sprintf("line %d\n", i))
		case 2:
			repo.WriteFile("dir/other.txt", fmt.Sprintf("needleTerm in a sentence, %d\n", i))
		}
		repo.Commit(fmt.Sprintf("c%02d", i))
	}
	// One commit that changes the needleTerm line outside dir/ and a line
	// without it inside dir/: a search scoped to dir/ must not match it.
	repo.WriteFile("hit.txt", "needleTerm=both\n")
	repo.WriteFile("dir/readme.txt", "no term here\n")
	repo.Commit("both")
	repo.CreateBranch("side")
	repo.WriteFile("hit.txt", "needleTerm=side\n")
	repo.Commit("side")
	return repo
}

// setFindContentSplit sets how the content search splits the history.
func setFindContentSplit(t *testing.T, chunkMin, chunkMax, workers int) {
	t.Helper()
	prevMin, prevMax, prevWorkers := findContentChunkMin, findContentChunkMax, findContentWorkers
	findContentChunkMin, findContentChunkMax = chunkMin, chunkMax
	findContentWorkers = func() int { return workers }
	t.Cleanup(func() {
		findContentChunkMin, findContentChunkMax, findContentWorkers = prevMin, prevMax, prevWorkers
	})
}

func contentHashes(t *testing.T, repo *testutil.Repo, q findQuery) []string {
	t.Helper()
	if q.limit == 0 {
		q.limit = 20
	}
	matches, err := runFindContent(context.Background(), &git.ExecRunner{Dir: repo.Dir}, q)
	if err != nil {
		t.Fatal(err)
	}
	hashes := make([]string, 0, len(matches))
	for _, m := range matches {
		hashes = append(hashes, m.Hash)
	}
	return hashes
}

// The split is an optimization, so it must return what one walk returns: the
// same commits, in the same order, whatever the chunk size and the count of
// processes.
func TestFindContent_SplitMatchesOneWalk(t *testing.T) {
	repo := contentRepo(t)
	want := strings.Fields(repo.RunGit("log", "--all", "--format=%H", "-GneedleTerm"))
	if len(want) != 22 {
		t.Fatalf("the fixture must have 22 matching commits, got %d", len(want))
	}
	for _, split := range []struct{ chunkMin, chunkMax, workers int }{
		{1, 1, 1},     // one commit a process, one at a time
		{1, 2, 3},     // many small rounds
		{4, 4, 16},    // more workers than chunks in the last round
		{32, 512, 8},  // the whole history in the first round
		{7, 7, 2},     // a chunk boundary inside the history
		{1, 512, 100}, // a round larger than the history
	} {
		setFindContentSplit(t, split.chunkMin, split.chunkMax, split.workers)
		if got := contentHashes(t, repo, findQuery{query: "needleTerm"}); !slices.Equal(got, want) {
			t.Errorf("split %+v: %d commits, want the %d commits of one walk in the same order", split, len(got), len(want))
		}
	}
}

// The cap takes the newest commits, as -n does on one walk.
func TestFindContent_CapKeepsTheNewest(t *testing.T) {
	repo := contentRepo(t)
	want := strings.Fields(repo.RunGit("log", "--all", "-n", "5", "--format=%H", "-GneedleTerm"))
	for _, split := range []struct{ chunkMin, chunkMax, workers int }{{1, 1, 1}, {2, 4, 3}, {32, 512, 8}} {
		setFindContentSplit(t, split.chunkMin, split.chunkMax, split.workers)
		if got := contentHashes(t, repo, findQuery{query: "needleTerm", limit: 1}); !slices.Equal(got, want) {
			t.Errorf("split %+v: got %d commits %v, want the newest 5 %v", split, len(got), got, want)
		}
	}
}

// --ref, --path, --author and --since narrow the split search as they narrow
// one walk.
func TestFindContent_NarrowingMatchesOneWalk(t *testing.T) {
	repo := contentRepo(t)
	setFindContentSplit(t, 2, 4, 3)
	cases := []struct {
		name string
		q    findQuery
		args []string
	}{
		{"ref", findQuery{query: "needleTerm", ref: "main"}, []string{"main", "-GneedleTerm"}},
		{"path", findQuery{query: "needleTerm", path: "hit.txt"}, []string{"--all", "-GneedleTerm", "--", "hit.txt"}},
		{"path dir", findQuery{query: "needleTerm", path: "dir"}, []string{"--all", "-GneedleTerm", "--", "dir"}},
		{"author miss", findQuery{query: "needleTerm", author: "nobody-by-this-name"}, []string{"--all", "--author=nobody-by-this-name", "-GneedleTerm"}},
		{"since tomorrow", findQuery{query: "needleTerm", since: "tomorrow"}, []string{"--all", "--since=tomorrow", "-GneedleTerm"}},
	}
	for _, c := range cases {
		want := strings.Fields(repo.RunGit(append([]string{"log", "--format=%H"}, c.args...)...))
		if got := contentHashes(t, repo, c.q); !slices.Equal(got, want) {
			t.Errorf("%s: %d commits, want %d", c.name, len(got), len(want))
		}
	}
	if main := contentHashes(t, repo, findQuery{query: "needleTerm", ref: "main"}); len(main) != 21 {
		t.Errorf("--ref main: %d commits, want the 21 of main without the side branch", len(main))
	}
}

func TestFindContent_UnknownRefIsAnError(t *testing.T) {
	repo := contentRepo(t)
	_, err := runFindContent(context.Background(), &git.ExecRunner{Dir: repo.Dir}, findQuery{query: "needleTerm", ref: "refs/heads/no-such-branch", limit: 20})
	if err == nil || !strings.Contains(err.Error(), "no-such-branch") {
		t.Fatalf("err = %v, want the git error that names the ref", err)
	}
}

// The comparisons above pass when both sides are whole, so the narrowing is
// also checked by its count. The commits of a test repository carry the dates
// that testutil gives them, so --since is checked only to narrow, with a date
// past any of them. git reads "tomorrow" as now, and a year it does not
// expect, such as 2999-01-01, as the current year, so an ISO time is used.
func TestFindContent_SinceAndAuthorNarrow(t *testing.T) {
	repo := contentRepo(t)
	setFindContentSplit(t, 2, 4, 3)
	all := len(contentHashes(t, repo, findQuery{query: "needleTerm"}))
	const future = "2099-12-31T00:00:00Z"
	if got := contentHashes(t, repo, findQuery{query: "needleTerm", since: future}); len(got) >= all {
		t.Errorf("--since %s: %d commits of %d, want fewer", future, len(got), all)
	}
	if got := contentHashes(t, repo, findQuery{query: "needleTerm", author: "nobody-by-this-name"}); len(got) != 0 {
		t.Errorf("--author of nobody: %d commits, want none", len(got))
	}
}

// The listing reads no file content, so it succeeds where a diff fails. A
// diff that fails must fail the search and not shorten it.
func TestFindContent_DiffFailureIsAnError(t *testing.T) {
	repo := contentRepo(t)
	setFindContentSplit(t, 2, 4, 3)
	// A text conversion that fails breaks the diff of hit.txt only.
	repo.WriteFile(".gitattributes", "hit.txt diff=broken\n")
	repo.Commit("attributes")
	repo.RunGit("config", "diff.broken.textconv", "false")
	if _, err := repo.TryGit("log", "--all", "--format=%H", "-GneedleTerm"); err == nil {
		t.Skip("this git does not fail the walk on a failed text conversion")
	}
	_, err := runFindContent(context.Background(), &git.ExecRunner{Dir: repo.Dir}, findQuery{query: "needleTerm", limit: 20})
	if err == nil {
		t.Fatal("a failed diff must fail the search")
	}
}

// A round is one chunk for each worker, so the diffs of a round run at once.
func TestNextFindChunks_OneChunkPerWorker(t *testing.T) {
	revs := bufio.NewScanner(strings.NewReader("r1\nr2\nr3\nr4\nr5\nr6\nr7\nr8\nr9\nr10\n"))
	first := nextFindChunks(revs, 3, 2)
	if want := [][]string{{"r1", "r2"}, {"r3", "r4"}, {"r5", "r6"}}; !slices.EqualFunc(first, want, slices.Equal) {
		t.Fatalf("first round = %v, want %v", first, want)
	}
	second := nextFindChunks(revs, 3, 3)
	if want := [][]string{{"r7", "r8", "r9"}, {"r10"}}; !slices.EqualFunc(second, want, slices.Equal) {
		t.Fatalf("second round = %v, want %v", second, want)
	}
	if rest := nextFindChunks(revs, 3, 3); len(rest) != 0 {
		t.Errorf("after the listing ends: %v, want no chunk", rest)
	}
}

// countFindDiffs counts the `git log` processes that fn starts.
func countFindDiffs(t *testing.T, fn func()) int {
	t.Helper()
	var mu sync.Mutex
	count := 0
	prev := git.ExecHook
	git.ExecHook = func(args []string, _ time.Duration, _ error) {
		if slices.Contains(args, "log") && slices.Contains(args, "--no-walk=unsorted") {
			mu.Lock()
			count++
			mu.Unlock()
		}
	}
	t.Cleanup(func() { git.ExecHook = prev })
	fn()
	git.ExecHook = prev
	return count
}

// A query that most commits match fills the cap from the newest commits, and
// the rest of the history is never diffed.
func TestFindContent_StopsWhenTheCapIsFull(t *testing.T) {
	repo := testutil.NewRepo(t)
	for i := range 40 {
		repo.WriteFile("a.txt", fmt.Sprintf("needleTerm=%d\n", i))
		repo.Commit(fmt.Sprintf("c%02d", i))
	}
	setFindContentSplit(t, 1, 1, 1)
	var got []string
	diffs := countFindDiffs(t, func() {
		got = contentHashes(t, repo, findQuery{query: "needleTerm", limit: 1})
	})
	if len(got) != 5 {
		t.Fatalf("got %d commits, want the cap of 5", len(got))
	}
	if diffs != 5 {
		t.Errorf("%d diff processes for 40 commits, want 5: the search must stop when the cap is full", diffs)
	}
}

func TestFindContent_NoMatchIsEmptyNotAnError(t *testing.T) {
	repo := contentRepo(t)
	setFindContentSplit(t, 2, 4, 3)
	if got := contentHashes(t, repo, findQuery{query: "termThatIsNowhere"}); len(got) != 0 {
		t.Errorf("got %d commits, want none", len(got))
	}
}

// A --limit too large to multiply by five has no cap, instead of a negative
// one that would cut the matches at a negative index.
func TestFindModeCap(t *testing.T) {
	for limit, want := range map[int]int{1: 5, 20: 100, 0: math.MaxInt, -1: math.MaxInt, math.MaxInt/5 + 1: math.MaxInt, math.MaxInt: math.MaxInt} {
		if got := findModeCap(limit); got != want {
			t.Errorf("findModeCap(%d) = %d, want %d", limit, got, want)
		}
	}
	repo := contentRepo(t)
	got, err := runFindContent(context.Background(), &git.ExecRunner{Dir: repo.Dir}, findQuery{query: "needleTerm", limit: math.MaxInt})
	if err != nil || len(got) == 0 {
		t.Errorf("--limit %d: %d matches, err %v", math.MaxInt, len(got), err)
	}
}
