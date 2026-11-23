package cli

import (
	"sort"
	"strconv"
	"strings"
	"time"

	ghapi "github.com/x-mesh/gk/internal/github"
)

// Row relations. A PR or issue that matches several relations is shown once,
// under the first relation in this order.
const (
	dashRelMine       = "mine"
	dashRelReview     = "review"
	dashRelCleanup    = "cleanup"
	dashRelAssigned   = "assigned"
	dashRelMentioned  = "mentioned"
	dashRelAuthored   = "authored"
	dashRelUnassigned = "unassigned"
)

// dashQuery builds the six searches for a scope prefix (repo:o/r, org:o,
// user:o). The repository block and the unassigned search need a repo: scope.
func dashQuery(prefix string) ghapi.DashQuery {
	pr := func(f githubSearchFilters) string {
		f.typeFilter, f.state = "is:pr", "open"
		return buildSearchQuery(prefix, f)
	}
	issue := func(f githubSearchFilters) string {
		f.typeFilter, f.state = "is:issue", "open"
		return buildSearchQuery(prefix, f)
	}
	q := ghapi.DashQuery{
		MyPRs:     pr(githubSearchFilters{mine: true}),
		ReviewPRs: pr(githubSearchFilters{review: true}),
		Assigned:  issue(githubSearchFilters{assigned: true}),
		Mentioned: issue(githubSearchFilters{raw: "mentions:@me"}),
		Authored:  issue(githubSearchFilters{mine: true}),
	}
	if slug, ok := strings.CutPrefix(prefix, "repo:"); ok {
		if owner, name, ok := strings.Cut(slug, "/"); ok {
			q.Owner, q.Repo = owner, name
		}
	}
	return q
}

type dashBranchRef struct {
	Name     string `json:"name"`
	Worktree string `json:"worktree,omitempty"`
	Current  bool   `json:"current,omitempty"`
	Tree     string `json:"tree,omitempty"`
}

type dashPRRow struct {
	Number       int             `json:"number"`
	Title        string          `json:"title"`
	URL          string          `json:"url"`
	Repo         string          `json:"repo"`
	Relation     string          `json:"relation"`
	Author       string          `json:"author,omitempty"`
	Draft        bool            `json:"draft,omitempty"`
	State        string          `json:"state"`
	Base         string          `json:"base"`
	Head         string          `json:"head"`
	Review       dashReview      `json:"review"`
	CI           dashCI          `json:"ci"`
	Branches     []dashBranchRef `json:"branches,omitempty"`
	ClosesIssues []int           `json:"closes_issues,omitempty"`
	Action       dashAction      `json:"action"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type dashLinkedPRJSON struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Merged bool   `json:"merged,omitempty"`
}

type dashIssueRow struct {
	Number    int                `json:"number"`
	Title     string             `json:"title"`
	URL       string             `json:"url"`
	Repo      string             `json:"repo"`
	Relation  string             `json:"relation"`
	Author    string             `json:"author,omitempty"`
	LinkedPRs []dashLinkedPRJSON `json:"linked_prs,omitempty"`
	Action    dashAction         `json:"action"`
	UpdatedAt time.Time          `json:"updated_at"`
}

// dashCount is one relation of a section: Total is GitHub's count, Shown the
// rows fetched for it.
type dashCount struct {
	Relation string `json:"relation"`
	Total    int    `json:"total"`
	Shown    int    `json:"shown"`
}

func (c dashCount) truncated() bool { return c.Total > c.Shown }

type dashPRSection struct {
	Counts []dashCount `json:"counts"`
	Items  []dashPRRow `json:"items"`
	Error  string      `json:"error,omitempty"`
}

type dashIssueSection struct {
	Counts []dashCount    `json:"counts"`
	Items  []dashIssueRow `json:"items"`
	Others *int           `json:"others_assigned,omitempty"` // open issues assigned to someone else; nil when unknown
	Error  string         `json:"error,omitempty"`
}

type dashForecastPairJSON struct {
	A     int      `json:"a"`
	B     int      `json:"b"`
	Files []string `json:"files"`
}

type dashForecastBaseJSON struct {
	PR    int      `json:"pr"`
	Files []string `json:"files"`
}

type dashForecastUnknownJSON struct {
	PR       int    `json:"pr"`
	PairWith int    `json:"pair_with,omitempty"`
	Reason   string `json:"reason"`
}

type dashForecastJSON struct {
	Base          string                    `json:"base"`
	Total         int                       `json:"total"`
	Considered    int                       `json:"considered"`
	Pairs         []dashForecastPairJSON    `json:"pairs"`
	BaseConflicts []dashForecastBaseJSON    `json:"base_conflicts,omitempty"`
	Unknown       []dashForecastUnknownJSON `json:"unknown,omitempty"`
	Uncomputed    int                       `json:"uncomputed,omitempty"`
	Note          string                    `json:"note,omitempty"`
}

type dashBehindJSON struct {
	Base   string `json:"base"`
	Remote string `json:"remote"`
	Behind int    `json:"behind"`
}

// dashUnreleasedRow is one merged pull request that no release holds yet.
// Title, SHA, and MergedAt come from the merge or squash commit; URL and
// Author are set when GitHub's merged PRs include the number.
type dashUnreleasedRow struct {
	Number   int       `json:"number"`
	Title    string    `json:"title"`
	URL      string    `json:"url,omitempty"`
	Author   string    `json:"author,omitempty"`
	SHA      string    `json:"sha"`
	MergedAt time.Time `json:"merged_at"`
}

// dashUnreleasedJSON is the unreleased pull requests of one branch: Total is
// every one found after Since, Items the newest releasePRListCap of them.
// NotFetched names the PRs GitHub merged into the branch whose commit is not
// in the local repository; Total and Items do not include them.
type dashUnreleasedJSON struct {
	Base       string              `json:"base"`
	Remote     string              `json:"remote"`
	Since      string              `json:"since"`
	Total      int                 `json:"total"`
	Items      []dashUnreleasedRow `json:"items"`
	NotFetched []int               `json:"not_fetched,omitempty"`
}

// dashSnapshot is one gk dash read: the table, the screen, and --json all
// render it.
type dashSnapshot struct {
	Scope      string               `json:"scope"`
	FetchedAt  time.Time            `json:"fetched_at"`
	PRs        dashPRSection        `json:"prs"`
	Issues     dashIssueSection     `json:"issues"`
	Forecast   []dashForecastJSON   `json:"forecast,omitempty"`
	Unreleased []dashUnreleasedJSON `json:"unreleased,omitempty"`
	BaseBehind []dashBehindJSON     `json:"base_behind,omitempty"`
	Notes      []string             `json:"notes,omitempty"`
}

type dashInputs struct {
	Scope         string
	Now           time.Time
	Res           *ghapi.DashResult
	Local         *dashLocal // nil outside the repository or with --org
	Forecast      []dashForecastGroup
	Unreleased    []dashUnreleasedGroup
	ReviewContext string
}

// forecastInputs lists the open PRs of the repository for the forecast.
func forecastInputs(res *ghapi.DashResult) []dashForecastPR {
	var out []dashForecastPR
	for _, p := range res.RepoOpen.Items {
		out = append(out, dashForecastPR{Number: p.Number, HeadOID: p.HeadOID, Base: p.BaseRef, UpdatedAt: p.UpdatedAt})
	}
	return out
}

func buildDashSnapshot(in dashInputs) dashSnapshot {
	snap := dashSnapshot{Scope: in.Scope, FetchedAt: in.Now, Notes: append([]string(nil), in.Res.Notes...)}
	snap.PRs = buildPRSection(in)
	snap.Issues = buildIssueSection(in)
	for _, g := range in.Forecast {
		snap.Forecast = append(snap.Forecast, forecastJSON(g))
	}
	if in.Local != nil {
		for _, b := range in.Local.Behind {
			if b.Behind > 0 {
				snap.BaseBehind = append(snap.BaseBehind, dashBehindJSON{Base: b.Base, Remote: in.Local.Remote, Behind: b.Behind})
			}
		}
		for _, g := range in.Unreleased {
			snap.Unreleased = append(snap.Unreleased, unreleasedJSON(g, in.Local.Remote, in.Res.RepoMerged.Items))
		}
	}
	return snap
}

// unreleasedJSON adds the URL and the author of GitHub's merged PR to each
// row. The base branch must match too: in a fork, a commit subject can carry a
// PR number of the upstream repository.
func unreleasedJSON(g dashUnreleasedGroup, remote string, merged []ghapi.DashPR) dashUnreleasedJSON {
	j := dashUnreleasedJSON{Base: g.Base, Remote: remote, Since: g.Since, Total: len(g.PRs), Items: []dashUnreleasedRow{}, NotFetched: g.NotFetched}
	for _, p := range g.PRs[:min(len(g.PRs), releasePRListCap)] {
		row := dashUnreleasedRow{Number: p.Number, Title: p.Title, SHA: p.SHA}
		if t, err := time.Parse(time.RFC3339, p.Date); err == nil {
			row.MergedAt = t
		}
		for _, m := range merged {
			if m.Number == p.Number && m.BaseRef == g.Base {
				row.URL, row.Author = m.URL, m.Author
				break
			}
		}
		j.Items = append(j.Items, row)
	}
	return j
}

func forecastJSON(g dashForecastGroup) dashForecastJSON {
	j := dashForecastJSON{Base: g.Base, Total: g.Total, Considered: g.Considered, Pairs: []dashForecastPairJSON{}, Uncomputed: g.Uncomputed, Note: g.Note}
	for _, p := range g.Pairs {
		j.Pairs = append(j.Pairs, dashForecastPairJSON(p))
	}
	for _, c := range g.BaseConflicts {
		j.BaseConflicts = append(j.BaseConflicts, dashForecastBaseJSON{PR: c.Number, Files: c.Files})
	}
	for _, u := range g.Unknown {
		j.Unknown = append(j.Unknown, dashForecastUnknownJSON{PR: u.A, PairWith: u.B, Reason: u.Reason})
	}
	return j
}

func rowKey(repo string, number int) string { return repo + "#" + strconv.Itoa(number) }

func buildPRSection(in dashInputs) dashPRSection {
	sec := dashPRSection{Items: []dashPRRow{}}
	res := in.Res
	if res.PRErr != nil {
		sec.Error = res.PRErr.Error()
		return sec
	}

	// What the forecast says about each PR of the current repository.
	baseConflict := map[int][]string{}
	baseClean := map[int]bool{}
	pairWith := map[int]dashPairConflict{}
	for _, g := range in.Forecast {
		for _, n := range g.Clean {
			baseClean[n] = true
		}
		for _, c := range g.BaseConflicts {
			baseConflict[c.Number] = c.Files
		}
		for _, p := range g.Pairs {
			if _, ok := pairWith[p.A]; !ok {
				pairWith[p.A] = p
			}
			if _, ok := pairWith[p.B]; !ok {
				pairWith[p.B] = dashPairConflict{A: p.B, B: p.A, Files: p.Files}
			}
		}
	}

	local := res.HasRepo && in.Local != nil
	seen := map[string]bool{}
	add := func(p ghapi.DashPR, relation string) {
		k := rowKey(p.Repo, p.Number)
		if seen[k] {
			return
		}
		seen[k] = true
		f := dashPRFacts{pr: p, review: reviewVerdict(p, in.ReviewContext), ci: ciSummary(p, in.ReviewContext)}
		if local {
			if relation == dashRelCleanup {
				for _, b := range in.Local.Branches {
					if b.Tip == p.HeadOID {
						f.branches = append(f.branches, b)
					}
				}
			} else {
				f.branches = in.Local.linkedBranches(p)
			}
			f.baseConflict = baseConflict[p.Number]
			f.baseClean = baseClean[p.Number]
			if pc, ok := pairWith[p.Number]; ok {
				f.pairWith, f.pairFiles = pc.B, pc.Files
			}
		}
		row := dashPRRow{
			Number: p.Number, Title: p.Title, URL: p.URL, Repo: p.Repo, Relation: relation, Author: p.Author,
			Draft: p.Draft, State: p.State, Base: p.BaseRef, Head: p.HeadRef, Review: f.review, CI: f.ci,
			ClosesIssues: p.ClosingIssues, UpdatedAt: p.UpdatedAt,
		}
		for _, b := range f.branches {
			row.Branches = append(row.Branches, dashBranchRef{Name: b.Name, Worktree: b.Worktree, Current: b.Current, Tree: b.Tree})
		}
		if relation == dashRelReview {
			row.Action = dashAction{Rule: 4, Text: "review requested", Command: "gk pr checkout " + strconv.Itoa(p.Number), URL: p.URL}
		} else {
			row.Action = prAction(f)
		}
		sec.Items = append(sec.Items, row)
	}
	for _, p := range res.MyPRs.Items {
		add(p, dashRelMine)
	}
	for _, p := range res.ReviewPRs.Items {
		add(p, dashRelReview)
	}
	cleanup := 0
	if in.Local != nil {
		for _, p := range res.RepoMerged.Items {
			for _, b := range in.Local.Branches {
				if p.HeadOID != "" && b.Tip == p.HeadOID && !seen[rowKey(p.Repo, p.Number)] {
					add(p, dashRelCleanup)
					cleanup++
					break
				}
			}
		}
	}
	sec.Counts = []dashCount{
		{Relation: dashRelMine, Total: res.MyPRs.Total, Shown: len(res.MyPRs.Items)},
		{Relation: dashRelReview, Total: res.ReviewPRs.Total, Shown: len(res.ReviewPRs.Items)},
	}
	if in.Local != nil {
		sec.Counts = append(sec.Counts, dashCount{Relation: dashRelCleanup, Total: cleanup, Shown: cleanup})
	}
	sort.SliceStable(sec.Items, func(i, j int) bool {
		a, b := sec.Items[i], sec.Items[j]
		ra, rb := prRank(a.Relation, a.Action), prRank(b.Relation, b.Action)
		if ra != rb {
			return ra < rb
		}
		return a.UpdatedAt.After(b.UpdatedAt)
	})
	return sec
}

func buildIssueSection(in dashInputs) dashIssueSection {
	sec := dashIssueSection{Items: []dashIssueRow{}}
	res := in.Res
	if res.IssueErr != nil {
		sec.Error = res.IssueErr.Error()
		return sec
	}
	blocks := []struct {
		relation string
		list     ghapi.DashIssueList
	}{
		{dashRelAssigned, res.Assigned},
		{dashRelMentioned, res.Mentioned},
		{dashRelAuthored, res.Authored},
	}
	if res.HasRepo {
		blocks = append(blocks, struct {
			relation string
			list     ghapi.DashIssueList
		}{dashRelUnassigned, res.Unassigned})
	}
	seen := map[string]bool{}
	for _, b := range blocks {
		sec.Counts = append(sec.Counts, dashCount{Relation: b.relation, Total: b.list.Total, Shown: len(b.list.Items)})
		for _, is := range b.list.Items {
			k := rowKey(is.Repo, is.Number)
			if seen[k] {
				continue
			}
			seen[k] = true
			row := dashIssueRow{
				Number: is.Number, Title: is.Title, URL: is.URL, Repo: is.Repo, Relation: b.relation,
				Author: is.Author, Action: issueAction(b.relation, is), UpdatedAt: is.UpdatedAt,
			}
			for _, p := range is.LinkedPRs {
				row.LinkedPRs = append(row.LinkedPRs, dashLinkedPRJSON{Number: p.Number, State: strings.ToLower(p.State), Merged: p.Merged})
			}
			sec.Items = append(sec.Items, row)
		}
	}
	relRank := map[string]int{dashRelAssigned: 0, dashRelMentioned: 1, dashRelAuthored: 2, dashRelUnassigned: 3}
	sort.SliceStable(sec.Items, func(i, j int) bool {
		a, b := sec.Items[i], sec.Items[j]
		if relRank[a.Relation] != relRank[b.Relation] {
			return relRank[a.Relation] < relRank[b.Relation]
		}
		return a.UpdatedAt.After(b.UpdatedAt)
	})
	// GitHub search ignores -no:assignee, so the issues assigned to someone
	// else are counted by subtraction.
	if res.HasRepo {
		others := max(res.RepoIssues-res.Unassigned.Total-res.Assigned.Total, 0)
		sec.Others = &others
	}
	return sec
}
