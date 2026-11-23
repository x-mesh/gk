package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mattn/go-runewidth"
)

// dashCell is one table cell: plain text for width math and its colored form.
type dashCell struct{ plain, colored string }

func plainCell(s string) dashCell { return dashCell{s, s} }

const (
	dashMinTitle = 10
	dashGap      = "  "
)

// renderDash prints the snapshot as a header, the PR table, the issue table,
// and the forecast lines. Columns are sized on plain text, so color codes
// never skew them; the title column gives way first on a narrow terminal.
func renderDash(w io.Writer, snap dashSnapshot, width int) {
	snap = dashTerminalSafe(snap)
	repo := strings.HasPrefix(snap.Scope, "repo:")
	fmt.Fprintf(w, "%s %s %s\n", cellBold("gk dash ·"), cellCyanBold(humanGitHubScope(snap.Scope)), cellFaint("· "+snap.FetchedAt.Local().Format("15:04:05")))
	for _, b := range snap.BaseBehind {
		fmt.Fprintf(w, "%s\n", cellYellow(fmt.Sprintf("%s is %d commit(s) behind %s/%s", b.Base, b.Behind, b.Remote, b.Base))+cellFaint(" → gk pull"))
	}
	renderDashActivity(w, snap)

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s  %s\n", cellBold("PRs"), dashCounts(snap.PRs.Counts))
	switch {
	case snap.PRs.Error != "":
		fmt.Fprintf(w, "  %s\n", cellRed("could not read: "+snap.PRs.Error))
	case len(snap.PRs.Items) == 0:
		fmt.Fprintf(w, "  %s\n", cellFaint("none"))
	default:
		headers := []string{"#"}
		if !repo {
			headers = append(headers, "repo")
		}
		headers = append(headers, "relation", "title", "review", "CI")
		if repo {
			headers = append(headers, "branch")
		}
		headers = append(headers, "next")
		var rows [][]dashCell
		for _, r := range snap.PRs.Items {
			row := []dashCell{numberCell(r.Number)}
			if !repo {
				row = append(row, repoCell(snap.Scope, r.Repo))
			}
			row = append(row, relationCell(r.Relation), titleCell(r.Title, r.Draft), reviewCell(r), ciCell(r))
			if repo {
				row = append(row, branchCell(r.Branches))
			}
			rows = append(rows, append(row, prActionCell(r)))
		}
		writeDashTable(w, headers, rows, 2, width)
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s  %s\n", cellBold("Issues"), dashCounts(snap.Issues.Counts))
	switch {
	case snap.Issues.Error != "":
		fmt.Fprintf(w, "  %s\n", cellRed("could not read: "+snap.Issues.Error))
	case len(snap.Issues.Items) == 0:
		fmt.Fprintf(w, "  %s\n", cellFaint("none"))
	default:
		headers := []string{"#"}
		if !repo {
			headers = append(headers, "repo")
		}
		headers = append(headers, "relation", "title", "updated", "PR", "next")
		var rows [][]dashCell
		for _, r := range snap.Issues.Items {
			row := []dashCell{numberCell(r.Number)}
			if !repo {
				row = append(row, repoCell(snap.Scope, r.Repo))
			}
			rows = append(rows, append(row, relationCell(r.Relation), titleCell(r.Title, false),
				ageCell(snap.FetchedAt, r.UpdatedAt), linkedPRCell(r.LinkedPRs), issueActionCell(r.Action)))
		}
		writeDashTable(w, headers, rows, 2, width)
	}
	if o := snap.Issues.Others; o != nil && *o > 0 {
		fmt.Fprintf(w, "  %s\n", cellFaint(fmt.Sprintf("%d more assigned to others · gk issue", *o)))
	}

	for _, f := range snap.Forecast {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s  %s\n", cellBold("Forecast"), cellFaint(forecastSummary(f)))
		for _, c := range f.BaseConflicts {
			fmt.Fprintf(w, "  %s %s\n", cellRed(fmt.Sprintf("#%d ↔ %s:", c.PR, f.Base)), strings.Join(c.Files, ", "))
		}
		for _, p := range f.Pairs {
			fmt.Fprintf(w, "  %s %s\n", cellYellow(fmt.Sprintf("#%d ↔ #%d:", p.A, p.B)), strings.Join(p.Files, ", "))
		}
		if len(f.Pairs) == 0 && len(f.BaseConflicts) == 0 && len(f.Unknown) == 0 && f.Uncomputed == 0 && f.Note == "" {
			fmt.Fprintf(w, "  %s\n", cellGreen("no conflicts"))
		}
		for _, u := range f.Unknown {
			subject := fmt.Sprintf("#%d", u.PR)
			if u.PairWith != 0 {
				subject += fmt.Sprintf(" ↔ #%d", u.PairWith)
			}
			fmt.Fprintf(w, "  %s\n", cellFaint(subject+": unknown, "+u.Reason))
		}
		if f.Uncomputed > 0 {
			fmt.Fprintf(w, "  %s\n", cellFaint(fmt.Sprintf("%d pair(s) not computed: time limit", f.Uncomputed)))
		}
		if f.Note != "" {
			fmt.Fprintf(w, "  %s\n", cellFaint(f.Note))
		}
	}
	renderDashUnreleased(w, snap, width)
	renderDashOrgUnreleased(w, snap, width)
	for _, n := range snap.Notes {
		fmt.Fprintf(w, "%s\n", cellFaint("note: "+n))
	}
}

// renderDashUnreleased prints one block for each branch with merged pull
// requests that no release holds yet, or with a merged PR that is not fetched.
// When no branch has one, a single line says so for the first branch, so a
// read that found nothing shows as that.
func renderDashUnreleased(w io.Writer, snap dashSnapshot, width int) {
	header := func(u dashUnreleasedJSON) {
		summary := u.Base
		if u.Commits > 0 {
			summary += " · " + pluralCount(u.Commits, "commit", "commits")
		}
		if u.Total > 0 {
			summary += fmt.Sprintf(" · %d merged PR(s) since %s", u.Total, u.Since)
		} else {
			summary += fmt.Sprintf(" · no merged PR since %s", u.Since)
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "%s  %s\n", cellBold("Unreleased"), cellFaint(summary))
	}
	shown := false
	for _, u := range snap.Unreleased {
		if u.Total == 0 && len(u.NotFetched) == 0 {
			continue
		}
		shown = true
		header(u)
		if len(u.Items) > 0 {
			var rows [][]dashCell
			for _, r := range u.Items {
				rows = append(rows, []dashCell{numberCell(r.Number), titleCell(r.Title, false), ageCell(snap.FetchedAt, r.MergedAt)})
			}
			writeDashTable(w, []string{"#", "title", "merged"}, rows, 2, width)
		}
		if more := u.Total - len(u.Items); more > 0 {
			fmt.Fprintf(w, "  %s\n", cellFaint(fmt.Sprintf("%d more · gk log %s..%s/%s", more, u.Since, u.Remote, u.Base)))
		}
		if len(u.NotFetched) > 0 {
			nums := make([]string, len(u.NotFetched))
			for i, n := range u.NotFetched {
				nums[i] = "#" + strconv.Itoa(n)
			}
			fmt.Fprintf(w, "  %s\n", cellYellow(strings.Join(nums, " ")+" merged on GitHub, not in the local repository")+cellFaint(" → gk dash --fetch"))
		}
	}
	if !shown && len(snap.Unreleased) > 0 {
		header(snap.Unreleased[0])
	}
}

// dashCounts writes the relation counts of a section header. A relation
// with rows stands out; an empty one stays faint.
func dashCounts(cs []dashCount) string {
	var parts []string
	for _, c := range cs {
		s := fmt.Sprintf("%s %d", c.Relation, c.Total)
		if c.truncated() {
			s += fmt.Sprintf(" (%d shown)", c.Shown)
		}
		if c.Total > 0 {
			parts = append(parts, relationColor(c.Relation, s))
		} else {
			parts = append(parts, cellFaint(s))
		}
	}
	return strings.Join(parts, cellFaint(" · "))
}

func forecastSummary(f dashForecastJSON) string {
	s := fmt.Sprintf("%s · %d PR(s)", f.Base, f.Considered)
	if f.Considered < f.Total {
		s = fmt.Sprintf("%s · newest %d of %d PRs", f.Base, f.Considered, f.Total)
	}
	return s
}

func faintCell(s string) dashCell { return dashCell{s, cellFaint(s)} }

func relationColor(rel, s string) string {
	switch rel {
	case dashRelMine, dashRelAssigned:
		return cellBlue(s)
	case dashRelReview, dashRelMentioned:
		return cellMagenta(s)
	case dashRelUnassigned:
		return cellYellow(s)
	case dashRelCleanup:
		return cellFaint(s)
	}
	return s
}

func relationCell(rel string) dashCell { return dashCell{rel, relationColor(rel, rel)} }

func numberCell(n int) dashCell {
	s := "#" + strconv.Itoa(n)
	return dashCell{s, cellCyan(s)}
}

func titleCell(title string, draft bool) dashCell {
	if draft {
		return faintCell(title)
	}
	return plainCell(title)
}

// repoCell names the repository of a row in an org or user scope, without
// the owner when it is the scope's owner.
func repoCell(scope, repo string) dashCell {
	owner := scope[strings.Index(scope, ":")+1:]
	s := strings.TrimPrefix(repo, owner+"/")
	return dashCell{s, cellBlue(s)}
}

// ageCell colors an update time: green within a day, faint after a month.
func ageCell(now, t time.Time) dashCell {
	s := dashAge(now, t)
	switch d := now.Sub(t); {
	case t.IsZero():
		return faintCell(s)
	case d < 24*time.Hour:
		return dashCell{s, cellGreen(s)}
	case d > 30*24*time.Hour:
		return faintCell(s)
	}
	return plainCell(s)
}

func reviewCell(r dashPRRow) dashCell {
	if r.State == "MERGED" {
		return faintCell("merged")
	}
	rv := r.Review
	switch {
	case rv.Source == dashReviewNone:
		return faintCell("·")
	case rv.passed():
		return dashCell{"✓", cellGreen("✓")}
	case rv.State == "pending" || rv.State == "expected" || rv.State == "review_required":
		return dashCell{"…", cellYellow("…")}
	case rv.waiting():
		return dashCell{"✗", cellRed("✗")}
	}
	return plainCell(rv.State)
}

func ciCell(r dashPRRow) dashCell {
	if r.State == "MERGED" {
		return faintCell("·")
	}
	switch r.CI.State {
	case dashCISuccess:
		return dashCell{"✓", cellGreen("✓")}
	case dashCIFailure:
		return dashCell{"✗", cellRed("✗")}
	case dashCIPending:
		return dashCell{"…", cellYellow("…")}
	case dashCINone:
		return faintCell("·")
	}
	return plainCell(r.CI.State)
}

func branchCell(bs []dashBranchRef) dashCell {
	if len(bs) == 0 {
		return faintCell("—")
	}
	b := bs[0]
	s := b.Name
	switch {
	case b.Current:
		s = "(current)"
	case b.Worktree != "":
		s = filepath.Base(b.Worktree)
	}
	if b.Tree == dashTreeDirty || b.Tree == dashTreeUnknown {
		s += "·" + map[string]string{dashTreeDirty: "dirty", dashTreeUnknown: "?"}[b.Tree]
	}
	if len(bs) > 1 {
		s += fmt.Sprintf(" +%d", len(bs)-1)
	}
	if b.Tree == dashTreeDirty {
		return dashCell{s, cellYellow(s)}
	}
	return plainCell(s)
}

// actionCell writes the next action in tint, with its command in cyan.
func actionCell(a dashAction, tint func(string) string) dashCell {
	if a.Text == "" {
		return plainCell("")
	}
	if a.Command == "" {
		return dashCell{a.Text, tint(a.Text)}
	}
	return dashCell{a.Text + " → " + a.Command, tint(a.Text) + cellFaint(" → ") + cellCyan(a.Command)}
}

func noTint(s string) string { return s }

// prActionCell colors a PR's next action by urgency: red for a conflict or a
// failed check, yellow for a review to do, green when ready, faint for work
// in progress and cleanup.
func prActionCell(r dashPRRow) dashCell {
	tint := noTint
	switch {
	case r.Relation == dashRelReview:
		tint = cellYellow
	case r.Action.Rule == 2 || r.Action.Rule == 3:
		tint = cellRedBold
	case r.Action.Rule == 5:
		tint = cellRed
	case r.Action.Rule == 4:
		tint = cellYellow
	case r.Action.Rule == 8:
		tint = cellGreenBold
	case r.Action.Rule == 9:
		tint = cellGreen
	case r.Action.Rule == 1 || r.Action.Rule == 6 || r.Action.Rule == 7:
		tint = cellFaint
	}
	return actionCell(r.Action, tint)
}

// issueActionCell colors an issue's next action: yellow when it waits on
// you, green when it can be closed, faint otherwise.
func issueActionCell(a dashAction) dashCell {
	tint := cellFaint
	switch a.Rule {
	case 2:
		tint = cellGreen
	case 3, 4:
		tint = cellYellow
	}
	return actionCell(a, tint)
}

func linkedPRCell(ps []dashLinkedPRJSON) dashCell {
	if len(ps) == 0 {
		return faintCell("—")
	}
	p := ps[0]
	state := p.State
	if p.Merged {
		state = "merged"
	}
	return plainCell(fmt.Sprintf("#%d %s", p.Number, state))
}

func dashAge(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return relativeTime(now.Sub(t))
}

// writeDashTable writes headers and rows indented by indent spaces. The
// "title" column takes what the other columns leave of width, at least
// dashMinTitle and at most its longest title. The last column is ragged.
func writeDashTable(w io.Writer, headers []string, rows [][]dashCell, indent, width int) {
	title := -1
	for i, h := range headers {
		if h == "title" {
			title = i
		}
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = runewidth.StringWidth(h)
	}
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], runewidth.StringWidth(c.plain))
		}
	}
	others := indent + len(dashGap)*(len(headers)-1)
	for i, wd := range widths {
		if i != title {
			others += wd
		}
	}
	if title >= 0 {
		widths[title] = min(widths[title], max(dashMinTitle, width-others))
	}

	line := func(cells []dashCell, faint bool) {
		var b strings.Builder
		b.WriteString(strings.Repeat(" ", indent))
		for i, c := range cells {
			plain, colored := c.plain, c.colored
			if i == title && runewidth.StringWidth(plain) > widths[i] {
				plain = runewidth.Truncate(plain, widths[i], "…")
				colored = plain
			}
			if faint {
				colored = cellFaint(plain)
			}
			b.WriteString(colored)
			if i < len(cells)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-runewidth.StringWidth(plain)) + dashGap)
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	hc := make([]dashCell, len(headers))
	for i, h := range headers {
		hc[i] = plainCell(h)
	}
	line(hc, true)
	for _, r := range rows {
		line(r, false)
	}
}

// dashSafe replaces control characters with '?'. Titles, branch names, and
// conflicting paths come from other people's PRs, and an ESC or OSC sequence
// in one would reach the terminal as a command.
func dashSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

func dashSafeAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = dashSafe(s)
	}
	return out
}

// dashTerminalSafe returns a copy of snap with every text field that reaches
// the terminal passed through dashSafe.
func dashTerminalSafe(snap dashSnapshot) dashSnapshot {
	snap.Scope = dashSafe(snap.Scope)
	snap.Notes = dashSafeAll(snap.Notes)
	snap.PRs.Error = dashSafe(snap.PRs.Error)
	snap.Issues.Error = dashSafe(snap.Issues.Error)
	safeAction := func(a dashAction) dashAction {
		a.Text, a.Command, a.URL = dashSafe(a.Text), dashSafe(a.Command), dashSafe(a.URL)
		return a
	}
	prs := make([]dashPRRow, len(snap.PRs.Items))
	for i, r := range snap.PRs.Items {
		r.Title, r.Repo, r.Author, r.Base, r.Head = dashSafe(r.Title), dashSafe(r.Repo), dashSafe(r.Author), dashSafe(r.Base), dashSafe(r.Head)
		r.Review.State, r.Review.Description = dashSafe(r.Review.State), dashSafe(r.Review.Description)
		r.CI.State = dashSafe(r.CI.State)
		r.Action = safeAction(r.Action)
		branches := make([]dashBranchRef, len(r.Branches))
		for j, b := range r.Branches {
			b.Name, b.Worktree = dashSafe(b.Name), dashSafe(b.Worktree)
			branches[j] = b
		}
		r.Branches = branches
		prs[i] = r
	}
	snap.PRs.Items = prs
	issues := make([]dashIssueRow, len(snap.Issues.Items))
	for i, r := range snap.Issues.Items {
		r.Title, r.Repo, r.Author = dashSafe(r.Title), dashSafe(r.Repo), dashSafe(r.Author)
		r.Action = safeAction(r.Action)
		issues[i] = r
	}
	snap.Issues.Items = issues
	fc := make([]dashForecastJSON, len(snap.Forecast))
	for i, f := range snap.Forecast {
		f.Base, f.Note = dashSafe(f.Base), dashSafe(f.Note)
		pairs := make([]dashForecastPairJSON, len(f.Pairs))
		for j, p := range f.Pairs {
			p.Files = dashSafeAll(p.Files)
			pairs[j] = p
		}
		f.Pairs = pairs
		bc := make([]dashForecastBaseJSON, len(f.BaseConflicts))
		for j, c := range f.BaseConflicts {
			c.Files = dashSafeAll(c.Files)
			bc[j] = c
		}
		f.BaseConflicts = bc
		un := make([]dashForecastUnknownJSON, len(f.Unknown))
		for j, u := range f.Unknown {
			u.Reason = dashSafe(u.Reason)
			un[j] = u
		}
		f.Unknown = un
		fc[i] = f
	}
	snap.Forecast = fc
	un := make([]dashUnreleasedJSON, len(snap.Unreleased))
	for i, u := range snap.Unreleased {
		u.Base, u.Remote, u.Since = dashSafe(u.Base), dashSafe(u.Remote), dashSafe(u.Since)
		items := make([]dashUnreleasedRow, len(u.Items))
		for j, r := range u.Items {
			r.Title, r.URL, r.Author, r.SHA = dashSafe(r.Title), dashSafe(r.URL), dashSafe(r.Author), dashSafe(r.SHA)
			items[j] = r
		}
		u.Items = items
		un[i] = u
	}
	snap.Unreleased = un
	if a := snap.Activity; a != nil {
		safe := *a
		for _, s := range []**dashSeriesJSON{&safe.Opened, &safe.Merged, &safe.Commits} {
			if *s != nil {
				c := **s
				c.Error = dashSafe(c.Error)
				*s = &c
			}
		}
		snap.Activity = &safe
	}
	if o := snap.UnreleasedRepos; o != nil {
		safe := *o
		safe.Error = dashSafe(o.Error)
		safe.Items = make([]dashRepoUnreleasedRow, len(o.Items))
		for i, r := range o.Items {
			r.Repo, r.Since, r.Branch = dashSafe(r.Repo), dashSafe(r.Since), dashSafe(r.Branch)
			safe.Items[i] = r
		}
		snap.UnreleasedRepos = &safe
	}
	bb := make([]dashBehindJSON, len(snap.BaseBehind))
	for i, b := range snap.BaseBehind {
		b.Base, b.Remote = dashSafe(b.Base), dashSafe(b.Remote)
		bb[i] = b
	}
	snap.BaseBehind = bb
	return snap
}
