package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
)

// followEventCursor is the persisted poll position for a repo's event stream:
// the last ETag (for cheap 304 polling) and the id of the last event handled
// (so a restart never re-fires past events).
type followEventCursor struct {
	ETag   string `json:"etag"`
	LastID string `json:"last_id"`
}

func followCursorPath(ctx context.Context, runner git.Runner, slug string) (string, bool) {
	return githubCacheFile(ctx, runner, "follow-events:"+slug)
}

func readFollowCursor(ctx context.Context, runner git.Runner, slug string) (followEventCursor, bool) {
	f, ok := followCursorPath(ctx, runner, slug)
	if !ok {
		return followEventCursor{}, false
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return followEventCursor{}, false
	}
	var c followEventCursor
	if json.Unmarshal(b, &c) != nil {
		return followEventCursor{}, false
	}
	return c, true
}

func writeFollowCursor(ctx context.Context, runner git.Runner, slug string, c followEventCursor) {
	f, ok := followCursorPath(ctx, runner, slug)
	if !ok {
		return
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return
	}
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	tmp := f + ".tmp"
	if os.WriteFile(tmp, b, 0o644) != nil {
		return
	}
	_ = os.Rename(tmp, f)
}

// eventPoller fetches events with a conditional ETag. Injected so the loop is
// testable without a network round-trip.
type eventPoller func(ctx context.Context, etag string) (events []ghapi.RepoEvent, newETag string, notModified bool, err error)

// followEventsOpts is the resolved config for the events engine loop.
type followEventsOpts struct {
	slug     string // owner/repo, for the cursor + messages
	branch   string // non-empty → mirror-on-merge target base
	triggers []followTrigger
	interval time.Duration
	once     bool
	poll     eventPoller
	// dispatch runs the hook with the event env; nil = no hook (log only).
	dispatch func(ctx context.Context, env []string) (int, error)
	// mirror mirrors opts.branch to its remote tip before a pr:merged hook;
	// nil when no branch was given (pure event→hook, non-destructive).
	mirror func(ctx context.Context) (backupRef string, err error)
	agent  bool
	now    func() time.Time
	out    io.Writer
	runner git.Runner
}

// followEventsLoop is the events-engine counterpart to followLoop: poll the
// GitHub event stream, fire the hook on matching triggers, with the same
// backoff / --once / clean-shutdown behavior.
func followEventsLoop(ctx context.Context, opts followEventsOpts) error {
	if opts.now == nil {
		opts.now = time.Now
	}
	cur, _ := readFollowCursor(ctx, opts.runner, opts.slug)
	// A missing cursor means first-ever run: baseline (record the newest event
	// without firing) so we don't deploy the entire recent backlog on startup.
	baseline := cur.LastID == ""
	backoff := time.Duration(0)
	maxBackoff := 10 * opts.interval

	for {
		newCur, cerr := followEventsCycle(ctx, opts, cur, &baseline)
		cur = newCur

		if cerr != nil {
			backoff = nextBackoff(backoff, opts.interval, maxBackoff)
		} else {
			backoff = 0
		}

		if opts.once {
			return cerr
		}
		if cerr != nil {
			followEmitErr(opts.out, followOpts{agent: opts.agent}, cerr)
		}

		wait := opts.interval
		if backoff > 0 {
			wait = backoff
		}
		if !sleepCtx(ctx, wait) {
			if !opts.agent {
				fmt.Fprintln(opts.out, "follow: stopped")
			}
			return nil
		}
	}
}

// followEventsCycle performs one poll and dispatches any newly-matching events
// oldest-first. It advances the cursor only past events it fully handled: on a
// hook failure it stops and leaves the failed event unconsumed so the next
// cycle (after backoff) retries it — at-least-once dispatch.
func followEventsCycle(ctx context.Context, opts followEventsOpts, cur followEventCursor, baseline *bool) (followEventCursor, error) {
	events, newETag, notModified, err := opts.poll(ctx, cur.ETag)
	newCur := cur
	newCur.ETag = newETag
	if err != nil {
		return newCur, err
	}
	if notModified || len(events) == 0 {
		return newCur, nil
	}

	newestID := cur.LastID
	for _, e := range events {
		if ghapi.EventIDLess(newestID, e.ID) {
			newestID = e.ID
		}
	}

	// First run: establish the baseline at the newest event and fire nothing.
	if *baseline {
		newCur.LastID = newestID
		*baseline = false
		writeFollowCursor(ctx, opts.runner, opts.slug, newCur)
		if !opts.agent {
			fmt.Fprintf(opts.out, "follow: baseline set at event %s — watching %s for new activity\n", newestID, opts.slug)
		}
		return newCur, nil
	}

	// Events arrive newest-first; process the ones newer than the cursor
	// oldest-first so hooks run in chronological order.
	var fresh []ghapi.RepoEvent
	for _, e := range events {
		if ghapi.EventIDLess(cur.LastID, e.ID) {
			fresh = append(fresh, e)
		}
	}
	for i, j := 0, len(fresh)-1; i < j; i, j = i+1, j-1 {
		fresh[i], fresh[j] = fresh[j], fresh[i]
	}

	lastGood := cur.LastID
	for _, e := range fresh {
		t := firstMatchingTrigger(opts.triggers, e, opts.branch)
		if t == nil {
			lastGood = e.ID
			continue
		}
		code, derr := dispatchFollowEvent(ctx, opts, e, *t)
		if derr != nil {
			newCur.LastID = lastGood
			writeFollowCursor(ctx, opts.runner, opts.slug, newCur)
			return newCur, fmt.Errorf("follow: %s hook failed to start: %w", t.raw, derr)
		}
		if code != 0 {
			newCur.LastID = lastGood
			writeFollowCursor(ctx, opts.runner, opts.slug, newCur)
			return newCur, fmt.Errorf("follow: %s hook exited %d", t.raw, code)
		}
		lastGood = e.ID
	}
	newCur.LastID = lastGood
	writeFollowCursor(ctx, opts.runner, opts.slug, newCur)
	return newCur, nil
}

// dispatchFollowEvent handles one matching event: mirror the branch first for a
// pr:merged trigger (when a branch was given), then run the hook with the event
// context in the environment.
func dispatchFollowEvent(ctx context.Context, opts followEventsOpts, ev ghapi.RepoEvent, t followTrigger) (int, error) {
	if opts.mirror != nil && t.kind == "pr" && t.verb == "merged" {
		if _, err := opts.mirror(ctx); err != nil {
			return -1, err
		}
	}
	if opts.dispatch == nil {
		if !opts.agent {
			fmt.Fprintf(opts.out, "follow: %s fired (%s) — no hook configured\n", t.raw, eventDesc(ev))
		}
		return 0, nil
	}
	if !opts.agent {
		fmt.Fprintf(opts.out, "follow: %s → running hook (%s)\n", t.raw, eventDesc(ev))
	}
	return opts.dispatch(ctx, followEventEnv(ev, t))
}

// followEventEnv exposes the event to the hook as GK_* environment variables.
func followEventEnv(ev ghapi.RepoEvent, t followTrigger) []string {
	env := []string{
		"GK_TRIGGER=" + t.raw,
		"GK_EVENT_TYPE=" + ev.Type,
		"GK_EVENT_ACTION=" + ev.Action,
		"GK_ACTOR=" + ev.Actor,
	}
	if ev.PRNumber > 0 {
		env = append(env,
			"GK_PR_NUMBER="+strconv.Itoa(ev.PRNumber),
			"GK_PR_TITLE="+ev.PRTitle,
			"GK_PR_BASE="+ev.PRBase,
			"GK_PR_HEAD="+ev.PRHead,
			"GK_PR_MERGED="+strconv.FormatBool(ev.PRMerged),
		)
	}
	if ev.IssueNumber > 0 {
		env = append(env,
			"GK_ISSUE_NUMBER="+strconv.Itoa(ev.IssueNumber),
			"GK_ISSUE_TITLE="+ev.IssueTitle,
		)
	}
	if ev.Label != "" {
		env = append(env, "GK_LABEL="+ev.Label)
	}
	if ev.ReviewState != "" {
		env = append(env, "GK_REVIEW_STATE="+ev.ReviewState)
	}
	return env
}

// eventDesc is a short human label for progress output.
func eventDesc(ev ghapi.RepoEvent) string {
	switch {
	case ev.PRNumber > 0:
		return fmt.Sprintf("PR #%d %s", ev.PRNumber, ev.PRTitle)
	case ev.IssueNumber > 0:
		return fmt.Sprintf("issue #%d %s", ev.IssueNumber, ev.IssueTitle)
	default:
		return ev.Type
	}
}
