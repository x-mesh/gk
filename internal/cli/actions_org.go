package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	ghapi "github.com/x-mesh/gk/internal/github"
)

const (
	orgDiscoveryLimit = 30
	// orgExpectTimeout bounds the wait for a run after a push. A push whose
	// workflows all filter it out (branches, paths) never starts one.
	orgExpectTimeout = 90 * time.Second
	// orgCatchUpWindow activates repositories pushed shortly before the watch
	// started, so a run already in flight is reported instead of missed.
	orgCatchUpWindow = 15 * time.Minute
	// orgRunCreatedSkew tolerates clock skew between pushed_at and a run's
	// created_at, which come from different GitHub services.
	orgRunCreatedSkew = 30 * time.Second
)

type actionsOrgClient interface {
	ListRecentWorkflowRuns(ctx context.Context, owner, repo, etag string) ([]ghapi.WorkflowRun, string, bool, error)
	GetWorkflowRun(ctx context.Context, owner, repo string, runID int64) (ghapi.WorkflowRun, error)
}

// actionsOrgEvent is one NDJSON line of `gk actions watch --org`. Kind is
// ci-expecting (a push was seen), ci-start, ci-end (with conclusion), or
// ci-none (no run started within orgExpectTimeout of the push).
type actionsOrgEvent struct {
	TS         string `json:"ts"`
	Kind       string `json:"kind"`
	Repo       string `json:"repo"`
	PushedAt   string `json:"pushed_at,omitempty"`
	RunID      int64  `json:"run_id,omitempty"`
	Workflow   string `json:"workflow,omitempty"`
	Branch     string `json:"branch,omitempty"`
	SHA        string `json:"sha,omitempty"`
	Event      string `json:"event,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`
}

type orgRepoWatch struct {
	pushedAt     time.Time
	lastActivity time.Time
	etag         string
	// announced is true when ci-expecting went out for the current push, so
	// a run-less timeout owes a ci-none. Catch-up activations announce nothing.
	announced bool
	matched   bool
	// catchUp marks the first run poll after a catch-up activation: runs that
	// already finished before the watch started are recorded, not reported.
	catchUp  bool
	inFlight map[int64]ghapi.WorkflowRun
	seen     map[int64]bool
}

type orgActionsWatcher struct {
	owner    string
	workflow string
	started  time.Time
	discover func(context.Context) ([]ghapi.Repo, bool, error)
	client   actionsOrgClient

	baselined bool
	pushedAt  map[string]time.Time
	active    map[string]*orgRepoWatch
}

func newOrgActionsWatcher(owner, workflow string, discover func(context.Context) ([]ghapi.Repo, bool, error), client actionsOrgClient, started time.Time) *orgActionsWatcher {
	return &orgActionsWatcher{
		owner:    owner,
		workflow: workflow,
		started:  started,
		discover: discover,
		client:   client,
		pushedAt: map[string]time.Time{},
		active:   map[string]*orgRepoWatch{},
	}
}

// step runs one poll: discover pushes, then refresh every active repository.
// A per-repository failure does not stop the others; the first one is
// returned alongside the events that did succeed.
func (w *orgActionsWatcher) step(ctx context.Context, now time.Time) ([]actionsOrgEvent, error) {
	repos, changed, err := w.discover(ctx)
	if err != nil {
		return nil, err
	}
	var evs []actionsOrgEvent
	if !w.baselined {
		for _, r := range repos {
			w.pushedAt[r.Name] = r.PushedAt
			if !r.PushedAt.IsZero() && now.Sub(r.PushedAt) <= orgCatchUpWindow {
				w.activate(r.Name, r.PushedAt, now, false)
			}
		}
		w.baselined = true
	} else if changed {
		for _, r := range repos {
			prev, known := w.pushedAt[r.Name]
			if !r.PushedAt.After(prev) || (!known && r.PushedAt.Before(w.started)) {
				continue
			}
			w.pushedAt[r.Name] = r.PushedAt
			w.activate(r.Name, r.PushedAt, now, true)
			evs = append(evs, w.event(now, "ci-expecting", r.Name, r.PushedAt, ghapi.WorkflowRun{}))
		}
	}

	names := make([]string, 0, len(w.active))
	for name := range w.active {
		names = append(names, name)
	}
	sort.Strings(names)
	var firstErr error
	for _, name := range names {
		repoEvs, err := w.refresh(ctx, name, now)
		evs = append(evs, repoEvs...)
		if err != nil {
			var rl *ghapi.RateLimitError
			if errors.As(err, &rl) {
				return evs, err
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("%s/%s: %w", w.owner, name, err)
			}
		}
	}
	return evs, firstErr
}

func (w *orgActionsWatcher) activate(name string, pushedAt, now time.Time, announced bool) {
	st, ok := w.active[name]
	if !ok {
		st = &orgRepoWatch{inFlight: map[int64]ghapi.WorkflowRun{}, seen: map[int64]bool{}, catchUp: !announced}
		w.active[name] = st
	}
	st.pushedAt = pushedAt
	st.lastActivity = now
	st.announced = announced
	st.matched = false
}

func (w *orgActionsWatcher) refresh(ctx context.Context, name string, now time.Time) ([]actionsOrgEvent, error) {
	st := w.active[name]
	runs, etag, notModified, err := w.client.ListRecentWorkflowRuns(ctx, w.owner, name, st.etag)
	if err != nil {
		return nil, err
	}
	var evs []actionsOrgEvent
	if !notModified {
		st.etag = etag
		runs, err = w.withMissingInFlight(ctx, name, st, runs)
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			if w.workflow != "" && run.Name != w.workflow {
				continue
			}
			// The gate keys on the latest push, so it must not drop a run an
			// earlier push started: that run would never report ci-end.
			if _, flying := st.inFlight[run.ID]; !flying && run.CreatedAt.Before(st.pushedAt.Add(-orgRunCreatedSkew)) {
				continue
			}
			done := run.Status == "completed"
			if !st.seen[run.ID] {
				st.seen[run.ID] = true
				st.matched = true
				st.lastActivity = now
				if st.catchUp && done {
					continue
				}
				evs = append(evs, w.event(now, "ci-start", name, time.Time{}, run))
				if !done {
					st.inFlight[run.ID] = run
					continue
				}
			} else if _, flying := st.inFlight[run.ID]; !flying || !done {
				continue
			}
			delete(st.inFlight, run.ID)
			st.lastActivity = now
			evs = append(evs, w.event(now, "ci-end", name, time.Time{}, run))
		}
		st.catchUp = false
	}
	if len(st.inFlight) == 0 && now.Sub(st.lastActivity) >= orgExpectTimeout {
		if st.announced && !st.matched {
			evs = append(evs, w.event(now, "ci-none", name, st.pushedAt, ghapi.WorkflowRun{}))
		}
		delete(w.active, name)
	}
	return evs, nil
}

// withMissingInFlight appends in-flight runs that fell off the recent-runs
// page (a busy repository can push one past it before it finishes), fetched
// one by one so their ci-end is not lost.
func (w *orgActionsWatcher) withMissingInFlight(ctx context.Context, name string, st *orgRepoWatch, runs []ghapi.WorkflowRun) ([]ghapi.WorkflowRun, error) {
	listed := make(map[int64]bool, len(runs))
	for _, run := range runs {
		listed[run.ID] = true
	}
	for id := range st.inFlight {
		if listed[id] {
			continue
		}
		run, err := w.client.GetWorkflowRun(ctx, w.owner, name, id)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func (w *orgActionsWatcher) event(now time.Time, kind, name string, pushedAt time.Time, run ghapi.WorkflowRun) actionsOrgEvent {
	ev := actionsOrgEvent{TS: now.UTC().Format(time.RFC3339), Kind: kind, Repo: w.owner + "/" + name}
	if !pushedAt.IsZero() {
		ev.PushedAt = pushedAt.UTC().Format(time.RFC3339)
	}
	if run.ID != 0 {
		ev.RunID, ev.Workflow, ev.Branch, ev.SHA = run.ID, run.Name, run.HeadBranch, run.HeadSHA
		ev.Event, ev.URL = run.Event, run.HTMLURL
		if kind == "ci-end" {
			ev.Conclusion = run.Conclusion
		}
	}
	return ev
}

func runActionsWatchOrg(cmd *cobra.Command, client *ghapi.Client, opts actionsWatchOptions) error {
	ctx, stop := signal.NotifyContext(cmdCtx(cmd), os.Interrupt, syscall.SIGTERM)
	defer stop()

	poller := &ghapi.PushedRepoPoller{Client: client, Owner: opts.org, Limit: orgDiscoveryLimit}
	w := newOrgActionsWatcher(opts.org, opts.workflow, poller.Poll, client, time.Now())

	out := cmd.OutOrStdout()
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	if AgentOut() {
		header := struct {
			Schema int    `json:"schema"`
			State  string `json:"state"`
			Result any    `json:"result"`
		}{Schema: 1, State: "streaming", Result: map[string]any{"mode": "actions-org", "org": opts.org}}
		if err := enc.Encode(header); err != nil {
			return err
		}
	}
	emit := func(ev actionsOrgEvent) error {
		if JSONOut() {
			return enc.Encode(ev)
		}
		return writeActionsOrgLine(out, ev)
	}
	warn := func(err error) { fmt.Fprintf(cmd.ErrOrStderr(), "gk actions watch: %v\n", err) }
	if !JSONOut() {
		fmt.Fprintf(cmd.ErrOrStderr(), "watching GitHub Actions for %s — Ctrl-C to stop\n", opts.org)
	}
	return runOrgActionsLoop(ctx, w, opts.interval, emit, warn, waitActionsInterval)
}

// runOrgActionsLoop polls until ctx ends and returns nil then. Only the first
// poll's error is fatal: it means the owner or token is wrong, not that the
// network blinked.
func runOrgActionsLoop(ctx context.Context, w *orgActionsWatcher, interval time.Duration, emit func(actionsOrgEvent) error, warn func(error), wait func(context.Context, time.Duration) error) error {
	for {
		evs, err := w.step(ctx, time.Now())
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && !w.baselined {
			return err
		}
		for _, ev := range evs {
			if eerr := emit(ev); eerr != nil {
				return eerr
			}
		}
		delay := interval
		if err != nil {
			warn(err)
			var rl *ghapi.RateLimitError
			if errors.As(err, &rl) {
				if d := time.Until(rl.Reset); d > delay {
					delay = d
				}
			}
		}
		if werr := wait(ctx, delay); werr != nil {
			return nil
		}
	}
}

func writeActionsOrgLine(out io.Writer, ev actionsOrgEvent) error {
	ts := ev.TS
	if t, err := time.Parse(time.RFC3339, ev.TS); err == nil {
		ts = t.Local().Format("15:04:05")
	}
	detail := ""
	switch ev.Kind {
	case "ci-expecting":
		detail = "push seen, waiting for runs"
	case "ci-none":
		detail = "no run started"
	case "ci-start":
		detail = fmt.Sprintf("%s (%s %s)  %s", ev.Workflow, ev.Event, ev.Branch, ev.URL)
	case "ci-end":
		detail = fmt.Sprintf("%s → %s  %s", ev.Workflow, ev.Conclusion, ev.URL)
	}
	_, err := fmt.Fprintf(out, "%s  %-12s  %s  %s\n", ts, ev.Kind, ev.Repo, detail)
	return err
}
