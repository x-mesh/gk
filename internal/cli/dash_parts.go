package cli

import (
	"context"
	"strings"
	"time"
)

const (
	// dashSlowBudget bounds one slow read. The org unreleased read took up to
	// 8.7 s on a large account, and a read that runs out is reported, not
	// waited for.
	dashSlowBudget = 15 * time.Second
	// dashSlowMaxAge is how old a slow read may get on the screen before an
	// auto-refresh repeats it. The org unreleased read costs 13 GraphQL
	// points, about twice a whole refresh, and none of these blocks changes
	// from one refresh to the next.
	dashSlowMaxAge = 5 * time.Minute
)

// dashPart is a block that is read apart from the dashboard queries, because
// its query is slower or costs more. One snapshot waits for it; the screen
// shows the PRs and issues first and fills the block when its read ends.
//
// fetch returns what writes the result into a snapshot. pending writes the
// block as it is before the first result. failed marks the block after a
// failed read: the last result is already written, and failed must copy what
// it changes, because snapshots share a result.
type dashPart struct {
	fetch   func(context.Context) (func(*dashSnapshot), error)
	pending func(*dashSnapshot)
	failed  func(*dashSnapshot, error)

	inFlight bool
	at       time.Time           // when the last read ended; zero before it
	write    func(*dashSnapshot) // of the last read that succeeded
	err      error               // of the last read
}

// read runs the part's fetch within dashSlowBudget and keeps the outcome.
func (p *dashPart) read(ctx context.Context) {
	p.done(p.fetchWithin(ctx))
}

func (p *dashPart) fetchWithin(ctx context.Context) (func(*dashSnapshot), error) {
	ctx, cancel := context.WithTimeout(ctx, dashSlowBudget)
	defer cancel()
	return p.fetch(ctx)
}

// done keeps the outcome of a read. A failure keeps the last good result.
func (p *dashPart) done(write func(*dashSnapshot), err error) {
	p.err = err
	if err == nil {
		p.write = write
	}
}

// apply writes the part's block into s: the last result, or the pending block
// before the first one, and then the failure of the last read.
func (p *dashPart) apply(s *dashSnapshot) {
	switch {
	case p.write != nil:
		p.write(s)
	case p.err == nil:
		p.pending(s)
	}
	if p.err != nil {
		p.failed(s, p.err)
	}
}

// dashActivityOf returns the activity block of s, and adds it when a snapshot
// has none.
func dashActivityOf(s *dashSnapshot) *dashActivityJSON {
	if s.Activity == nil {
		s.Activity = &dashActivityJSON{}
	}
	return s.Activity
}

// failedSeries is a copy of last with the error, or an empty series with it.
func failedSeries(last *dashSeriesJSON, err error) *dashSeriesJSON {
	s := dashSeriesJSON{}
	if last != nil {
		s = *last
	}
	s.Pending, s.Error = false, err.Error()
	return &s
}

// dashSlowParts lists the slow reads of env: the pull request activity for
// every scope, and for an org or account the commit activity and the
// unreleased summary. A repository counts its commits and its unreleased PRs
// from the local repository, in the main read.
func dashSlowParts(env dashEnv, now func() time.Time) []*dashPart {
	parts := []*dashPart{{
		fetch: func(ctx context.Context) (func(*dashSnapshot), error) {
			opened, merged, err := env.client.PRActivity(ctx, env.prefix, dashDayRanges(now(), dashActivityDays))
			if err != nil {
				return nil, err
			}
			o, m := newDashSeries(opened), newDashSeries(merged)
			return func(s *dashSnapshot) { a := dashActivityOf(s); a.Opened, a.Merged = o, m }, nil
		},
		pending: func(s *dashSnapshot) {
			a := dashActivityOf(s)
			a.Opened, a.Merged = &dashSeriesJSON{Pending: true}, &dashSeriesJSON{Pending: true}
		},
		failed: func(s *dashSnapshot, err error) {
			a := dashActivityOf(s)
			a.Opened, a.Merged = failedSeries(a.Opened, err), failedSeries(a.Merged, err)
		},
	}}
	if strings.HasPrefix(env.prefix, "repo:") {
		return parts
	}
	_, owner, _ := strings.Cut(env.prefix, ":")
	return append(parts, &dashPart{
		fetch: func(ctx context.Context) (func(*dashSnapshot), error) {
			res, err := env.client.OwnerCommitActivity(ctx, env.prefix, dashDayRanges(now(), dashActivityDays))
			if err != nil {
				return nil, err
			}
			c := newDashSeries(res.Days)
			c.Partial, c.Fallback = res.Partial, res.Fallback
			return func(s *dashSnapshot) { dashActivityOf(s).Commits = c }, nil
		},
		pending: func(s *dashSnapshot) { dashActivityOf(s).Commits = &dashSeriesJSON{Pending: true} },
		failed: func(s *dashSnapshot, err error) {
			a := dashActivityOf(s)
			a.Commits = failedSeries(a.Commits, err)
		},
	}, &dashPart{
		fetch: func(ctx context.Context) (func(*dashSnapshot), error) {
			res, err := env.client.OwnerUnreleased(ctx, owner)
			if err != nil {
				return nil, err
			}
			u := orgUnreleasedJSON(res)
			return func(s *dashSnapshot) { s.UnreleasedRepos = u }, nil
		},
		pending: func(s *dashSnapshot) { s.UnreleasedRepos = &dashOrgUnreleasedJSON{Pending: true} },
		failed: func(s *dashSnapshot, err error) {
			u := dashOrgUnreleasedJSON{Items: []dashRepoUnreleasedRow{}}
			if s.UnreleasedRepos != nil {
				u = *s.UnreleasedRepos
			}
			u.Pending, u.Error = false, err.Error()
			s.UnreleasedRepos = &u
		},
	})
}
