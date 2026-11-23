package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/x-mesh/gk/internal/commitlint"
	ghapi "github.com/x-mesh/gk/internal/github"
)

// dashRepoUnreleasedRow is one repository with commits after its latest tag.
// PRs counts the pull requests in the newest commits GitHub returned;
// PRsPartial says the branch has more commits than that, so there can be more
// PRs. TagOffBranch says the tag is on neither the branch nor the default
// branch, as a tag of a maintenance branch is: Commits is then not a count
// since a release of the branch. NoReleaseChanges says every commit after the
// tag was read and is a docs, chore, ci, test or style commit without a
// breaking change, so the commits may not need a release: the screen hides the
// row.
type dashRepoUnreleasedRow struct {
	Repo             string `json:"repo"`
	Since            string `json:"since"`
	Branch           string `json:"branch"`
	Commits          int    `json:"commits"`
	PRs              int    `json:"prs"`
	PRsPartial       bool   `json:"prs_partial,omitempty"`
	TagOffBranch     bool   `json:"tag_off_branch,omitempty"`
	NoReleaseChanges bool   `json:"no_release_changes,omitempty"`
}

// dashOrgUnreleasedJSON is the unreleased summary of an org or account:
// Scanned of its Total repositories were read, the newest push first. Pending
// is set on the screen until the first read ends. Error keeps the rows of the
// read before it, or the rows GitHub answered next to a partial error.
// Commits and PRs add up the rows without NoReleaseChanges; PRsPartial says
// the count of one of these rows is partial, so the sum is a lower bound.
// NoReleaseRepos counts the rows with NoReleaseChanges.
type dashOrgUnreleasedJSON struct {
	Total          int                     `json:"total"`
	Scanned        int                     `json:"scanned"`
	Commits        int                     `json:"commits"`
	PRs            int                     `json:"prs"`
	PRsPartial     bool                    `json:"prs_partial,omitempty"`
	NoReleaseRepos int                     `json:"no_release_repos,omitempty"`
	Items          []dashRepoUnreleasedRow `json:"items"`
	Pending        bool                    `json:"pending,omitempty"`
	Error          string                  `json:"error,omitempty"`
}

// orgUnreleasedJSON keeps one row for each repository that has commits after
// its tag: the branch with the most, the default branch on a tie.
func orgUnreleasedJSON(res *ghapi.OwnerUnreleased) *dashOrgUnreleasedJSON {
	j := &dashOrgUnreleasedJSON{Total: res.Total, Scanned: res.Scanned, Items: []dashRepoUnreleasedRow{}, Error: strings.Join(res.Notes, "; ")}
	for _, r := range res.Repos {
		var best *ghapi.TagComparison
		tagOnDefault := false
		for i := range r.Branches {
			b := &r.Branches[i]
			if b.Ahead > 0 && (best == nil || b.Ahead > best.Ahead) {
				best = b
			}
			tagOnDefault = tagOnDefault || (b.Default && b.Behind == 0)
		}
		if best == nil {
			continue
		}
		// A release often leaves a commit on the default branch only, so the
		// other branch is behind a tag that is still its release.
		row := dashRepoUnreleasedRow{
			Repo: r.Repo, Since: r.Tag, Branch: best.Branch, Commits: best.Ahead,
			PRs: comparisonPRs(*best), PRsPartial: best.Ahead > len(best.Commits), TagOffBranch: best.Behind > 0 && !tagOnDefault,
			NoReleaseChanges: onlyNoReleaseCommits(*best),
		}
		j.Items = append(j.Items, row)
		if row.NoReleaseChanges {
			j.NoReleaseRepos++
			continue
		}
		j.Commits += row.Commits
		j.PRs += row.PRs
		j.PRsPartial = j.PRsPartial || row.PRsPartial
	}
	return j
}

// noReleaseTypes are the Conventional Commits types that change nothing a
// release ships.
var noReleaseTypes = map[string]bool{"docs": true, "chore": true, "ci": true, "test": true, "style": true}

// onlyNoReleaseCommits reports whether every commit after the tag was read and
// is of a noReleaseTypes type without a breaking change. A breaking change is
// found anywhere in the message, as inferShipBump finds it. A commit that is not
// in Conventional Commits form, a merge commit among them, counts as a change.
// So does a dependency update (scope deps or deps-dev, as Dependabot writes
// it), because a release ships the new dependency.
func onlyNoReleaseCommits(c ghapi.TagComparison) bool {
	if len(c.Commits) == 0 || c.Ahead > len(c.Commits) {
		return false
	}
	for _, cm := range c.Commits {
		m := commitlint.Parse(cm.Message)
		if m.Breaking || strings.Contains(cm.Message, "BREAKING CHANGE:") || !noReleaseTypes[m.Type] || m.Scope == "deps" || m.Scope == "deps-dev" {
			return false
		}
	}
	return true
}

// comparisonPRs counts the pull requests merged in the first-parent commits
// of a comparison, from the branch head down to the oldest commit GitHub
// returned. It reads commit subjects the way listReleasePRs does.
func comparisonPRs(c ghapi.TagComparison) int {
	byOID := make(map[string]ghapi.CompareCommit, len(c.Commits))
	for _, cm := range c.Commits {
		byOID[cm.OID] = cm
	}
	seen := map[int]bool{}
	for oid := c.Head; ; {
		cm, ok := byOID[oid]
		if !ok {
			break
		}
		delete(byOID, oid) // a commit is read once, whatever the parents say
		subject, body, _ := strings.Cut(cm.Message, "\n")
		if pr, ok := parseReleasePR(subject, body); ok {
			seen[pr.Number] = true
		}
		oid = cm.Parent
	}
	return len(seen)
}

// renderDashOrgUnreleased prints the unreleased summary of an org scope.
func renderDashOrgUnreleased(w io.Writer, snap dashSnapshot, width int) {
	u := snap.UnreleasedRepos
	if u == nil {
		return
	}
	fmt.Fprintln(w)
	if u.Pending && u.Error == "" && len(u.Items) == 0 {
		fmt.Fprintf(w, "%s  %s\n", cellBold("Unreleased"), cellFaint("loading…"))
		return
	}
	prs := pluralCount(u.PRs, "PR", "PRs")
	if u.PRsPartial {
		prs = commaInt(u.PRs) + "+ PRs"
	}
	read := "all " + pluralCount(u.Scanned, "repo", "repos")
	if u.Scanned < u.Total {
		read = fmt.Sprintf("newest %d of %d repos", u.Scanned, u.Total)
	}
	// A repository with maintenance commits only needs no release: it has no
	// row, and the header counts it.
	var items []dashRepoUnreleasedRow
	for _, r := range u.Items {
		if !r.NoReleaseChanges {
			items = append(items, r)
		}
	}
	hidden := ""
	if n := len(u.Items) - len(items); n > 0 {
		hidden = pluralCount(n, "repo", "repos") + " docs/chore only"
	}
	summary := "nothing after the latest tag · " + read
	switch {
	case len(items) > 0:
		parts := []string{pluralCount(len(items), "repo", "repos"), pluralCount(u.Commits, "commit", "commits"), prs}
		if hidden != "" {
			parts = append(parts, hidden)
		}
		summary = strings.Join(append(parts, read), " · ")
	case u.Error != "":
		// Nothing was read: a summary would say that nothing waits.
		summary = ""
	case hidden != "":
		summary = hidden + " · " + read
	}
	fmt.Fprintln(w, strings.TrimRight(cellBold("Unreleased")+"  "+cellFaint(summary), " "))
	if u.Error != "" {
		fmt.Fprintf(w, "  %s\n", cellRed("could not read: "+u.Error))
	}
	if len(items) == 0 {
		if u.Error == "" {
			fmt.Fprintf(w, "  %s\n", cellFaint("none"))
		}
		return
	}
	headers := []string{"repo", "range", "commits", "PRs"}
	notes := false
	for _, r := range items {
		notes = notes || r.TagOffBranch
	}
	if notes {
		headers = append(headers, "note")
	}
	var rows [][]dashCell
	for _, r := range items {
		prs := strconv.Itoa(r.PRs)
		if r.PRsPartial {
			prs += "+"
		}
		row := []dashCell{repoCell(snap.Scope, r.Repo), plainCell(r.Since + ".." + r.Branch), plainCell(strconv.Itoa(r.Commits)), plainCell(prs)}
		if notes {
			note := ""
			if r.TagOffBranch {
				note = "tag is not on " + r.Branch
			}
			row = append(row, faintCell(note))
		}
		rows = append(rows, row)
	}
	writeDashTable(w, headers, rows, 2, width)
}
