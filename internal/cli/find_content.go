package cli

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"runtime"
	"strings"
	"sync"

	"github.com/x-mesh/gk/internal/git"
)

// --- gk find: the content search, split over processes -------------------------
//
// `git log -G` makes the diff of every commit it walks, on one core: on the gk
// repository that one call was 611 ms of a 625 ms search. Listing the commits
// is cheap (16 ms), so rev-list lists them and several `git log --no-walk`
// processes diff them at once, as the history scan of gk guard does. In a
// trial that gave 16 processes equal shares of those commits, the diffs took
// 136 ms and returned the same commits in the same order.

var (
	// findContentChunkMin and findContentChunkMax bound the commits that one
	// git process diffs. A round is one chunk for each worker. The first
	// round is small, so a query that most commits match fills the cap from
	// the newest commits and the rest of the history is never read. Each
	// later round doubles, so a rare query on a long history does not pay
	// for a process start every few commits.
	findContentChunkMin = 32
	findContentChunkMax = 512
	findContentWorkers  = func() int { return runtime.GOMAXPROCS(0) }
)

// runFindContent returns the commits whose diff adds or removes a line that
// holds the query, newest first, at most q.limit*5 of them: the same commits
// as one `git log -n <cap> -G<query>` walk.
func runFindContent(ctx context.Context, runner *git.ExecRunner, q findQuery) ([]findMatch, error) {
	// The cap is applied here and not with -n: on a --no-walk list, -n
	// counts the commits read, not the commits that match.
	limit := q.limit * 5

	list := []string{"--no-optional-locks", "rev-list"}
	if q.ref != "" {
		list = append(list, q.ref)
	} else {
		list = append(list, "--all")
	}
	if q.since != "" {
		list = append(list, "--since="+normalizeSince(q.since))
	}
	if q.author != "" {
		list = append(list, "--author="+q.author)
	}
	// -U0 leaves the context lines out of the diffs that -G reads, and -s
	// prints none of them: on the gk repository that alone took the call
	// from 630 ms to 400 ms.
	diff := []string{"--no-optional-locks", "log", "--no-walk=unsorted", "--stdin", "--no-color", "--no-show-signature", findFormat, "-U0", "-s", findPickaxeArg(q.query)}
	if q.path != "" {
		list = append(list, "--", q.path)
		diff = append(diff, "--", q.path)
	}

	// rev-list streams, so a search that fills its cap from the newest
	// commits stops the listing and does not wait for the whole history.
	listCtx, stopList := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	listDone := make(chan struct{})
	go func() {
		defer close(listDone)
		// The error of the listing reaches the reader as the scanner error.
		_ = pw.CloseWithError(runner.Stream(listCtx, nil, pw, list...))
	}()
	// The listing ends before the search returns, so no git process
	// outlives the search. Closing the read end releases a listing that is
	// still writing.
	defer func() {
		stopList()
		_ = pr.Close()
		<-listDone
	}()

	workers := max(findContentWorkers(), 1)
	revs := bufio.NewScanner(pr)
	var matches []findMatch
	for per := findContentChunkMin; len(matches) < limit; per = min(per*2, findContentChunkMax) {
		chunks := nextFindChunks(revs, workers, per)
		if len(chunks) == 0 {
			break
		}
		found := make([][]findMatch, len(chunks))
		errs := make([]error, len(chunks))
		var wg sync.WaitGroup
		for i, chunk := range chunks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var out bytes.Buffer
				errs[i] = runner.Stream(ctx, strings.NewReader(strings.Join(chunk, "\n")+"\n"), &out, diff...)
				found[i] = parseFindLog(out.String())
			}()
		}
		wg.Wait()
		// The chunks are joined in the order of the listing, so the result
		// is newest first whichever process ends first.
		for i := range chunks {
			if errs[i] != nil {
				return nil, findGitErr(errs[i])
			}
			matches = append(matches, found[i]...)
		}
	}
	if len(matches) >= limit {
		// The listing was stopped on purpose, so its error is not read.
		return matches[:limit], nil
	}
	if err := revs.Err(); err != nil {
		return nil, findGitErr(err)
	}
	return matches, nil
}

// nextFindChunks reads the commits of one round from the listing: at most
// workers chunks of per commits. It returns fewer, or none, at the end of the
// listing.
func nextFindChunks(revs *bufio.Scanner, workers, per int) [][]string {
	var chunks [][]string
	for len(chunks) < workers {
		chunk := make([]string, 0, per)
		for len(chunk) < per && revs.Scan() {
			if rev := strings.TrimSpace(revs.Text()); rev != "" {
				chunk = append(chunk, rev)
			}
		}
		if len(chunk) == 0 {
			break
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}
