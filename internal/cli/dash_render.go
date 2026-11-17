package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// dashCell is one table cell: plain text for width math and its colored form.
type dashCell struct{ plain, colored string }

func plainCell(s string) dashCell { return dashCell{s, s} }

const (
	dashMinTitle = 10
	dashMaxTitle = 60
	dashGap      = "  "
)

// renderDash prints the snapshot as a header, the PR table, the issue table,
// and the forecast lines. Columns are sized on plain text, so color codes
// never skew them; the title column gives way first on a narrow terminal.
func renderDash(w io.Writer, snap dashSnapshot, width int) {
	repo := strings.HasPrefix(snap.Scope, "repo:")
	fmt.Fprintf(w, "%s %s\n", cellBold("gk dash · "+humanGitHubScope(snap.Scope)), cellFaint("· "+snap.FetchedAt.Local().Format("15:04:05")))
	for _, b := range snap.BaseBehind {
		fmt.Fprintf(w, "%s\n", cellYellow(fmt.Sprintf("%s is %d commit(s) behind %s/%s", b.Base, b.Behind, b.Remote, b.Base))+cellFaint(" → gk pull"))
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s  %s\n", cellBold("PRs"), cellFaint(dashCounts(snap.PRs.Counts)))
	switch {
	case snap.PRs.Error != "":
		fmt.Fprintf(w, "  %s\n", cellRed("could not read: "+snap.PRs.Error))
	case len(snap.PRs.Items) == 0:
		fmt.Fprintf(w, "  %s\n", cellFaint("none"))
	default:
		headers := []string{"#", "relation", "title", "review", "CI"}
		if repo {
			headers = append(headers, "branch")
		}
		headers = append(headers, "next")
		var rows [][]dashCell
		for _, r := range snap.PRs.Items {
			row := []dashCell{plainCell("#" + strconv.Itoa(r.Number)), relationCell(r.Relation), plainCell(r.Title), reviewCell(r), ciCell(r)}
			if repo {
				row = append(row, branchCell(r.Branches))
			}
			rows = append(rows, append(row, actionCell(r.Action)))
		}
		writeDashTable(w, headers, rows, 2, width)
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s  %s\n", cellBold("Issues"), cellFaint(dashCounts(snap.Issues.Counts)))
	switch {
	case snap.Issues.Error != "":
		fmt.Fprintf(w, "  %s\n", cellRed("could not read: "+snap.Issues.Error))
	case len(snap.Issues.Items) == 0:
		fmt.Fprintf(w, "  %s\n", cellFaint("none"))
	default:
		var rows [][]dashCell
		for _, r := range snap.Issues.Items {
			rows = append(rows, []dashCell{
				plainCell("#" + strconv.Itoa(r.Number)), relationCell(r.Relation), plainCell(r.Title),
				faintCell(dashAge(snap.FetchedAt, r.UpdatedAt)), linkedPRCell(r.LinkedPRs), actionCell(r.Action),
			})
		}
		writeDashTable(w, []string{"#", "relation", "title", "updated", "PR", "next"}, rows, 2, width)
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
	for _, n := range snap.Notes {
		fmt.Fprintf(w, "%s\n", cellFaint("note: "+n))
	}
}

func dashCounts(cs []dashCount) string {
	var parts []string
	for _, c := range cs {
		s := fmt.Sprintf("%s %d", c.Relation, c.Total)
		if c.truncated() {
			s += fmt.Sprintf(" (%d shown)", c.Shown)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

func forecastSummary(f dashForecastJSON) string {
	s := fmt.Sprintf("%s · %d PR(s)", f.Base, f.Considered)
	if f.Considered < f.Total {
		s = fmt.Sprintf("%s · newest %d of %d PRs", f.Base, f.Considered, f.Total)
	}
	return s
}

func faintCell(s string) dashCell { return dashCell{s, cellFaint(s)} }

func relationCell(rel string) dashCell {
	switch rel {
	case dashRelReview, dashRelMentioned:
		return dashCell{rel, cellCyan(rel)}
	case dashRelCleanup, dashRelUnassigned:
		return faintCell(rel)
	}
	return plainCell(rel)
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

func actionCell(a dashAction) dashCell {
	if a.Text == "" {
		return plainCell("")
	}
	if a.Command == "" {
		return plainCell(a.Text)
	}
	return dashCell{a.Text + " → " + a.Command, a.Text + cellFaint(" → ") + cellCyan(a.Command)}
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
// column at index 2 is the title: it takes what the other columns leave of
// width, between dashMinTitle and dashMaxTitle. The last column is ragged.
func writeDashTable(w io.Writer, headers []string, rows [][]dashCell, indent, width int) {
	const title = 2
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
	widths[title] = min(widths[title], dashMaxTitle, max(dashMinTitle, width-others))

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
