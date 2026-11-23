package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	ghapi "github.com/x-mesh/gk/internal/github"
)

const (
	// dashOrgUnreleasedBudget bounds the read. It took up to 8.7 s on a large
	// account, and a read that runs out is reported, not waited for.
	dashOrgUnreleasedBudget = 15 * time.Second
	// dashOrgUnreleasedMaxAge is how old the read may get on the screen before
	// an auto-refresh repeats it. It costs 13 GraphQL points, about twice a
	// whole refresh, and changes only on a push or a new tag.
	dashOrgUnreleasedMaxAge = 5 * time.Minute
)

// dashRepoUnreleasedRow is one repository with commits after its latest tag.
// PRs counts the pull requests in the newest commits GitHub returned;
// PRsPartial says the branch has more commits than that, so there can be more
// PRs. TagOffBranch says the tag is on neither the branch nor the default
// branch, as a tag of a maintenance branch is: Commits is then not a count
// since a release of the branch.
type dashRepoUnreleasedRow struct {
	Repo         string `json:"repo"`
	Since        string `json:"since"`
	Branch       string `json:"branch"`
	Commits      int    `json:"commits"`
	PRs          int    `json:"prs"`
	PRsPartial   bool   `json:"prs_partial,omitempty"`
	TagOffBranch bool   `json:"tag_off_branch,omitempty"`
}

// dashOrgUnreleasedJSON is the unreleased summary of an org or account:
// Scanned of its Total repositories were read, the newest push first. Pending
// is set on the screen until the first read ends. Error keeps the rows of the
// read before it, or the rows GitHub answered next to a partial error.
type dashOrgUnreleasedJSON struct {
	Total   int                     `json:"total"`
	Scanned int                     `json:"scanned"`
	Items   []dashRepoUnreleasedRow `json:"items"`
	Pending bool                    `json:"pending,omitempty"`
	Error   string                  `json:"error,omitempty"`
}

// collectDashOrgUnreleased reads the summary for an org: or user: scope.
func collectDashOrgUnreleased(ctx context.Context, client *ghapi.Client, scope string) (*dashOrgUnreleasedJSON, error) {
	_, owner, _ := strings.Cut(scope, ":")
	ctx, cancel := context.WithTimeout(ctx, dashOrgUnreleasedBudget)
	defer cancel()
	res, err := client.OwnerUnreleased(ctx, owner)
	if err != nil {
		return nil, err
	}
	return orgUnreleasedJSON(res), nil
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
		j.Items = append(j.Items, dashRepoUnreleasedRow{
			Repo: r.Repo, Since: r.Tag, Branch: best.Branch, Commits: best.Ahead,
			PRs: comparisonPRs(*best), PRsPartial: best.Ahead > len(best.Commits), TagOffBranch: best.Behind > 0 && !tagOnDefault,
		})
	}
	return j
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
	summary := fmt.Sprintf("%d repo(s) with commits after the latest tag · %d repos", len(u.Items), u.Scanned)
	if u.Scanned < u.Total {
		summary = fmt.Sprintf("%d repo(s) with commits after the latest tag · newest %d of %d repos", len(u.Items), u.Scanned, u.Total)
	}
	fmt.Fprintf(w, "%s  %s\n", cellBold("Unreleased"), cellFaint(summary))
	if u.Error != "" {
		fmt.Fprintf(w, "  %s\n", cellRed("could not read: "+u.Error))
	}
	if len(u.Items) == 0 {
		if u.Error == "" {
			fmt.Fprintf(w, "  %s\n", cellFaint("none"))
		}
		return
	}
	headers := []string{"repo", "range", "commits", "PRs"}
	notes := false
	for _, r := range u.Items {
		notes = notes || r.TagOffBranch
	}
	if notes {
		headers = append(headers, "note")
	}
	var rows [][]dashCell
	for _, r := range u.Items {
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
