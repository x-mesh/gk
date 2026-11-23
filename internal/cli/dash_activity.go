package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
)

// dashActivityDays is how many days the activity line covers, today included.
const dashActivityDays = 7

// dashSeriesJSON is one count for each day of the activity line, the oldest
// day first. Partial says the counts are a lower bound. Fallback says the
// org commit search was rate limited, so the counts are those of the most
// recently pushed repositories. Pending is set on the screen until the first
// read ends, and Error keeps the days of the read before it.
type dashSeriesJSON struct {
	Total    int    `json:"total"`
	Days     []int  `json:"days"`
	Partial  bool   `json:"partial,omitempty"`
	Fallback bool   `json:"fallback,omitempty"`
	Pending  bool   `json:"pending,omitempty"`
	Error    string `json:"error,omitempty"`
}

func newDashSeries(days []int) *dashSeriesJSON {
	s := &dashSeriesJSON{Days: days}
	for _, n := range days {
		s.Total += n
	}
	return s
}

// dashActivityJSON is the pull requests opened and merged and the commits of
// the last dashActivityDays days, in the local time zone. Since is the start
// of the first day. In a repository the commits are those of the branches
// that PRs merge into, from the last fetch; for an org or account they are
// those of the default branches.
type dashActivityJSON struct {
	Since   time.Time       `json:"since"`
	Opened  *dashSeriesJSON `json:"opened,omitempty"`
	Merged  *dashSeriesJSON `json:"merged,omitempty"`
	Commits *dashSeriesJSON `json:"commits,omitempty"`
}

// dashDayRanges returns the last n days up to the day of now, in now's time
// zone, the oldest first.
func dashDayRanges(now time.Time, n int) []ghapi.DayRange {
	y, m, d := now.Date()
	days := make([]ghapi.DayRange, n)
	for i := range days {
		start := time.Date(y, m, d-(n-1-i), 0, 0, 0, 0, now.Location())
		days[i] = ghapi.DayRange{Start: start, End: time.Date(y, m, d-(n-1-i)+1, 0, 0, 0, 0, now.Location()).Add(-time.Second)}
	}
	return days
}

// localCommitSeries counts the commits of <remote>/<base>, for each base, by
// the day of their commit date. A commit on several of the branches counts
// one time. It returns nil when no base has a remote branch.
func localCommitSeries(ctx context.Context, runner git.Runner, remote string, bases []string, days []ghapi.DayRange) (*dashSeriesJSON, error) {
	args := []string{"log", "--format=%ct", "--since=" + days[0].Start.Format(time.RFC3339)}
	tips := 0
	for _, b := range bases {
		if ref := "refs/remotes/" + remote + "/" + b; refExists(ctx, runner, ref) {
			args = append(args, ref)
			tips++
		}
	}
	if tips == 0 {
		return nil, nil
	}
	out, stderr, err := runner.Run(ctx, args...)
	if err != nil {
		return nil, dashGitErr("git log", stderr, err)
	}
	counts := make([]int, len(days))
	for _, line := range strings.Fields(string(out)) {
		sec, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			continue
		}
		// --since left out what is before the first day.
		t := time.Unix(sec, 0)
		for i, d := range days {
			if !t.After(d.End) {
				counts[i]++
				break
			}
		}
	}
	return newDashSeries(counts), nil
}

// dashSpark draws one block for each day, scaled to the busiest day. A day
// with nothing is a dot, so it does not read as a small count.
func dashSpark(days []int) string {
	blocks := []rune("▁▂▃▄▅▆▇█")
	peak := 0
	for _, n := range days {
		peak = max(peak, n)
	}
	var b strings.Builder
	for _, n := range days {
		if n <= 0 {
			b.WriteRune('·')
			continue
		}
		b.WriteRune(blocks[(n*len(blocks)-1)/peak])
	}
	return b.String()
}

// renderDashActivity prints the activity line, and under it why a count
// could not be read.
func renderDashActivity(w io.Writer, snap dashSnapshot) {
	a := snap.Activity
	if a == nil || (a.Opened == nil && a.Merged == nil && a.Commits == nil) {
		return
	}
	series := func(label string, s *dashSeriesJSON) string {
		switch {
		case s.Days == nil && s.Error != "":
			return cellFaint(label) + " ?"
		case s.Days == nil:
			return cellFaint(label + " …")
		}
		total := commaInt(s.Total)
		if s.Partial {
			total += "+"
		}
		return cellFaint(label) + " " + total + " " + cellBlue(dashSpark(s.Days))
	}
	parts := []string{cellFaint(strconv.Itoa(dashActivityDays) + "d")}
	prs := "PRs "
	var failed []string
	for _, p := range []struct {
		label string
		s     *dashSeriesJSON
	}{{"opened", a.Opened}, {"merged", a.Merged}, {"commits", a.Commits}} {
		if p.s == nil {
			continue
		}
		label := p.label
		if label != "commits" {
			label, prs = prs+label, ""
		}
		parts = append(parts, series(label, p.s))
		if p.s.Error != "" && (len(failed) == 0 || failed[len(failed)-1] != p.s.Error) {
			failed = append(failed, p.s.Error)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s  %s\n", cellBold("Activity"), strings.Join(parts, cellFaint(" · ")))
	for _, e := range failed {
		fmt.Fprintf(w, "  %s\n", cellRed("could not read: "+e))
	}
}
