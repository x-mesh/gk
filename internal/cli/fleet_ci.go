package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
)

// --- CI status of pushed worktree branches (`gk watch`) -----------------------
//
// The branch's CI is read for the tip of its remote-tracking ref, taken from
// that ref's reflog: the newest entry is the tip and when it moved. Push and
// fetch both count, so a commit pushed from this clone, from another clone, or
// merged on GitHub and fetched here all show CI. The reflog names the SHA,
// which the Actions API matches exactly. GitHub is never called from a
// gather: a tracker polls in the background and gathers only read its cache.

const (
	// fleetCIPushWindow is how old a push may be when the watch starts and
	// still be tracked: the dashboard answers "did my last push pass" for a
	// day of work. A finished push costs one request, then settles.
	fleetCIPushWindow = 24 * time.Hour
	// fleetCIConcurrency bounds parallel run lookups, so a first poll over a
	// day of pushes fits the fleetRepoTimeout budget of settleFirst.
	fleetCIConcurrency  = 8
	fleetCIPollInterval = 3 * time.Second
	// fleetCIReflogTail bounds the read of a long-lived reflog; the newest
	// tip is always the last entry.
	fleetCIReflogTail = 64 << 10
)

type fleetCIRunJSON struct {
	ID         int64  `json:"id"`
	Workflow   string `json:"workflow"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`
}

// fleetCIJSON is the CI of a worktree branch's remote tip. State is pending (no
// run yet), running, success, failure, none (no run started within
// orgExpectTimeout), or off (Reason says why).
type fleetCIJSON struct {
	State  string           `json:"state"`
	SHA    string           `json:"sha,omitempty"`
	Reason string           `json:"reason,omitempty"`
	Runs   []fleetCIRunJSON `json:"runs,omitempty"`
}

type fleetPush struct {
	slug string
	sha  string
	at   time.Time
}

// lastBranchTip returns the newest remote-tracking tip of branch across the
// remotes recorded in common's reflogs, and when that ref moved. ok is false
// for reftable repositories, which keep no reflog files, and for branches
// with no remote-tracking ref.
func lastBranchTip(common, branch string) (remote, sha string, at time.Time, ok bool) {
	dir := filepath.Join(common, "logs", "refs", "remotes")
	remotes, err := os.ReadDir(dir)
	if err != nil {
		return "", "", time.Time{}, false
	}
	for _, r := range remotes {
		if !r.IsDir() {
			continue
		}
		s, t, found := lastTipInReflog(filepath.Join(dir, r.Name(), filepath.FromSlash(branch)))
		if found && t.After(at) {
			remote, sha, at, ok = r.Name(), s, t, true
		}
	}
	return remote, sha, at, ok
}

func lastTipInReflog(path string) (sha string, at time.Time, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}, false
	}
	defer f.Close()
	seeked := false
	if st, err := f.Stat(); err == nil && st.Size() > fleetCIReflogTail {
		if _, err := f.Seek(-fleetCIReflogTail, io.SeekEnd); err != nil {
			return "", time.Time{}, false
		}
		seeked = true
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", time.Time{}, false
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if seeked && len(lines) > 0 {
		lines = lines[1:] // starts mid-line: its first field is not an old SHA
	}
	for i := len(lines) - 1; i >= 0; i-- {
		head, _, found := strings.Cut(lines[i], "\t")
		if !found {
			continue
		}
		fields := strings.Fields(head)
		if len(fields) < 4 || strings.Trim(fields[1], "0") == "" {
			continue // a deletion leaves no tip to ask about
		}
		unix, err := strconv.ParseInt(fields[len(fields)-2], 10, 64)
		if err != nil {
			continue
		}
		return fields[1], time.Unix(unix, 0), true
	}
	return "", time.Time{}, false
}

type fleetCIClient interface {
	ListWorkflowRunsForSHA(ctx context.Context, owner, repo, sha, etag string) ([]ghapi.WorkflowRun, string, bool, error)
}

type fleetCIKey struct{ slug, sha string }

type fleetCIState struct {
	pushedAt     time.Time
	lastActivity time.Time
	etag         string
	runs         []ghapi.WorkflowRun
	settled      bool
}

type fleetCITracker struct {
	client  fleetCIClient // nil when no token: every tracked push reads "off"
	started time.Time

	mu       sync.Mutex
	states   map[fleetCIKey]*fleetCIState
	slugs    map[string]string // common dir + remote → owner/repo, "" = not GitHub
	backoff  time.Time
	loopOnce sync.Once
}

func newFleetCITracker(client fleetCIClient, started time.Time) *fleetCITracker {
	return &fleetCITracker{client: client, started: started, states: map[fleetCIKey]*fleetCIState{}, slugs: map[string]string{}}
}

var (
	fleetCIOnce sync.Once
	fleetCIProc *fleetCITracker
)

// fleetCI is the process-wide tracker. Like `gk actions watch` it reads only
// GH_TOKEN / GITHUB_TOKEN, never gh's stored credentials.
func fleetCI() *fleetCITracker {
	fleetCIOnce.Do(func() {
		var client fleetCIClient
		if token := actionsToken(); token != "" {
			client = &ghapi.Client{Token: token}
		}
		fleetCIProc = newFleetCITracker(client, time.Now())
	})
	return fleetCIProc
}

// pushFor resolves a worktree branch's newest tracked push. The remote URL is
// read once per repository and remote: it costs a git fork, and a remote that
// moves to another GitHub repository mid-watch is not worth a fork per poll.
func (t *fleetCITracker) pushFor(ctx context.Context, common, branch string) (fleetPush, bool) {
	remote, sha, at, ok := lastBranchTip(common, branch)
	if !ok || at.Before(t.started.Add(-fleetCIPushWindow)) {
		return fleetPush{}, false
	}
	key := common + "\x00" + remote
	t.mu.Lock()
	slug, known := t.slugs[key]
	t.mu.Unlock()
	if !known {
		slug = githubSlugForRemote(ctx, common, remote)
		t.mu.Lock()
		t.slugs[key] = slug
		t.mu.Unlock()
	}
	if slug == "" {
		return fleetPush{}, false
	}
	return fleetPush{slug: slug, sha: sha, at: at}, true
}

func githubSlugForRemote(ctx context.Context, common, remote string) string {
	runner := &git.ExecRunner{Dir: common, ExtraEnv: []string{"GIT_OPTIONAL_LOCKS=0"}}
	out, _, err := runner.Run(ctx, "config", "--get", "remote."+remote+".url")
	if err != nil {
		return ""
	}
	meta := config.ParseRemoteMeta(strings.TrimSpace(string(out)))
	if meta.Owner == "" || meta.Repo == "" || !isGitHubHost(meta.Host) {
		return ""
	}
	return meta.Owner + "/" + meta.Repo
}

// annotate attaches the cached CI state of e's push, registering the push on
// first sight. It never calls GitHub.
func (t *fleetCITracker) annotate(e *fleetEntryJSON, now time.Time) {
	p := e.push
	if p == nil {
		e.CI = nil
		return
	}
	if t.client == nil {
		e.CI = &fleetCIJSON{State: "off", SHA: p.sha, Reason: "set GH_TOKEN or GITHUB_TOKEN to show CI"}
		return
	}
	key := fleetCIKey{p.slug, p.sha}
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.states[key]
	if !ok {
		// Activity starts at the push, not at first sight: an hours-old push
		// with no run reads "none" at once instead of after another timeout.
		st = &fleetCIState{pushedAt: p.at, lastActivity: p.at}
		t.states[key] = st
	}
	e.CI = st.view(p.sha)
}

func (st *fleetCIState) view(sha string) *fleetCIJSON {
	ci := &fleetCIJSON{SHA: sha}
	running, failed := false, false
	for _, r := range st.runs {
		ci.Runs = append(ci.Runs, fleetCIRunJSON{ID: r.ID, Workflow: r.Name, Status: r.Status, Conclusion: r.Conclusion, URL: r.HTMLURL})
		if r.Status != "completed" {
			running = true
		} else if ciRunFailed(r.Conclusion) {
			failed = true
		}
	}
	switch {
	case len(st.runs) == 0 && st.settled:
		ci.State = "none"
	case len(st.runs) == 0:
		ci.State = "pending"
	case running:
		ci.State = "running"
	case failed:
		ci.State = "failure"
	default:
		ci.State = "success"
	}
	return ci
}

// annotateAll re-reads the cache for entries gathered before a poll.
func (t *fleetCITracker) annotateAll(entries []fleetEntryJSON, now time.Time) {
	for i := range entries {
		t.annotate(&entries[i], now)
	}
}

// poll refreshes every unsettled push once. A push settles when its runs
// have all finished and orgExpectTimeout passed without a new one (a
// workflow_run follow-up still gets seen), or when no run started at all.
func (t *fleetCITracker) poll(ctx context.Context, now time.Time) error {
	if t.client == nil {
		return nil
	}
	t.mu.Lock()
	if now.Before(t.backoff) {
		t.mu.Unlock()
		return nil
	}
	type job struct {
		key  fleetCIKey
		etag string
	}
	var jobs []job
	for k, st := range t.states {
		if !st.settled {
			jobs = append(jobs, job{k, st.etag})
		}
	}
	t.mu.Unlock()

	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
		rateErr  error
	)
	sem := make(chan struct{}, fleetCIConcurrency)
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			err := t.refresh(ctx, j.key, j.etag, now)
			if err == nil {
				return
			}
			errMu.Lock()
			defer errMu.Unlock()
			var rl *ghapi.RateLimitError
			if errors.As(err, &rl) {
				t.mu.Lock()
				t.backoff = rl.Reset
				t.mu.Unlock()
				rateErr = err
			} else if firstErr == nil {
				firstErr = err
			}
		}(j)
	}
	wg.Wait()
	if rateErr != nil {
		return rateErr
	}
	return firstErr
}

func (t *fleetCITracker) refresh(ctx context.Context, key fleetCIKey, etag string, now time.Time) error {
	t.mu.Lock()
	backoff := now.Before(t.backoff)
	t.mu.Unlock()
	if backoff {
		return nil
	}
	owner, repo, _ := strings.Cut(key.slug, "/")
	runs, newETag, notModified, err := t.client.ListWorkflowRunsForSHA(ctx, owner, repo, key.sha, etag)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.states[key]
	if !notModified {
		st.etag = newETag
		if runsChanged(st.runs, runs) {
			st.lastActivity = now
		}
		st.runs = runs
	}
	allDone := true
	for _, r := range st.runs {
		if r.Status != "completed" {
			allDone = false
		}
	}
	if allDone && now.Sub(st.lastActivity) >= orgExpectTimeout {
		st.settled = true
	}
	return nil
}

func runsChanged(prev, curr []ghapi.WorkflowRun) bool {
	if len(prev) != len(curr) {
		return true
	}
	byID := make(map[int64]ghapi.WorkflowRun, len(prev))
	for _, r := range prev {
		byID[r.ID] = r
	}
	for _, r := range curr {
		p, ok := byID[r.ID]
		if !ok || p.Status != r.Status || p.Conclusion != r.Conclusion {
			return true
		}
	}
	return false
}

// start runs poll in the background until ctx ends. Safe to call more than
// once; only the first call starts a loop.
func (t *fleetCITracker) start(ctx context.Context) {
	if t.client == nil {
		return
	}
	t.loopOnce.Do(func() {
		go func() {
			tick := time.NewTicker(fleetCIPollInterval)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					pctx, cancel := context.WithTimeout(ctx, fleetRepoTimeout)
					_ = t.poll(pctx, time.Now()) // a failed poll retries next tick; rate limits set backoff
					cancel()
				}
			}
		}()
	})
}

// settleFirst answers the first gather of a session synchronously, so a run
// that finished before the watch started is the baseline, not a fresh event.
// Bounded like a repo gather: `gk watch --json` is an agent's one-turn
// orientation and must not hang on a stalled GitHub connection.
func (t *fleetCITracker) settleFirst(ctx context.Context, entries []fleetEntryJSON) {
	if t.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, fleetRepoTimeout)
	defer cancel()
	now := time.Now()
	_ = t.poll(ctx, now) // best effort: on failure the background loop catches up
	t.annotateAll(entries, now)
}

// withFleetCIBaseline settles the first gather of an event stream, which is
// the stream's silent baseline.
func withFleetCIBaseline(t *fleetCITracker, gather func(context.Context) ([]fleetEntryJSON, error)) func(context.Context) ([]fleetEntryJSON, error) {
	first := true
	return func(ctx context.Context) ([]fleetEntryJSON, error) {
		entries, err := gather(ctx)
		if err == nil && first {
			first = false
			t.settleFirst(ctx, entries)
		}
		return entries, err
	}
}

// fleetCIGlyph marks a row's CI next to its branch: ◌ waiting or running,
// ✓ passed, ✗ failed. none and off draw nothing; the detail panel says why.
func fleetCIGlyph(ci *fleetCIJSON) string {
	if ci == nil {
		return ""
	}
	switch ci.State {
	case "pending", "running":
		return "◌"
	case "success":
		return "✓"
	case "failure":
		return "✗"
	}
	return ""
}

// fleetBranchCell is the branch column's text: the name and its markers
// (* current, ⏸ paused, CI), with the name clipped so the markers always fit
// the cols.branch+3 cell both tables reserve.
func fleetBranchCell(e fleetEntryJSON, w int) string {
	suffix := ""
	if e.Current {
		suffix += "*"
	}
	if e.Operation != "" {
		suffix += " ⏸"
	}
	if g := fleetCIGlyph(e.CI); g != "" {
		suffix += " " + g
	}
	if extra := len([]rune(suffix)) - 3; extra > 0 {
		w -= extra
	}
	return clip(e.Branch, w) + suffix
}

func fleetCIDetail(ci *fleetCIJSON) string {
	if ci.State == "off" {
		return "off · " + ci.Reason
	}
	sha := ci.SHA
	if len(sha) > 7 {
		sha = sha[:7]
	}
	parts := []string{ci.State + " " + sha}
	for _, r := range ci.Runs {
		st := r.Status
		if r.Status == "completed" {
			st = r.Conclusion
		}
		parts = append(parts, r.Workflow+" "+st)
	}
	return strings.Join(parts, " · ")
}

func fleetCIFailedURL(ci *fleetCIJSON) string {
	for _, r := range ci.Runs {
		if r.Status == "completed" && ciRunFailed(r.Conclusion) {
			return r.URL
		}
	}
	return ""
}

// ciRunFailed reads a completed run's conclusion. skipped and neutral are not
// failures: a workflow that filtered itself out did not break anything.
func ciRunFailed(conclusion string) bool {
	return conclusion != "success" && conclusion != "skipped" && conclusion != "neutral"
}
