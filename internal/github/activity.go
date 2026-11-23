package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// activityRepos is how many of the owner's most recently pushed repositories
// historyCommitActivity reads. Seven day counts for 30 repositories took 3.2
// to 4.2 s on accounts of 40 to 174 repositories; 100 repositories took 8.4 s
// on kubernetes and answered 502 on vercel.
const activityRepos = 30

// DayRange is one day to count in: Start is its first instant and End its
// last. The caller picks the time zone.
type DayRange struct {
	Start time.Time
	End   time.Time
}

func (d DayRange) search() string {
	return d.Start.Format(time.RFC3339) + ".." + d.End.Format(time.RFC3339)
}

// PRActivity counts, for each day, the pull requests of scope (repo:o/r,
// org:o, user:o) opened in it and merged in it. One query carries a search
// for each count, so it costs one point for any number of repositories.
func (c *Client) PRActivity(ctx context.Context, scope string, days []DayRange) (opened, merged []int, err error) {
	if c.Token == "" {
		return nil, nil, ErrNoToken
	}
	if len(days) == 0 {
		return nil, nil, nil
	}
	var decl, body strings.Builder
	vars := map[string]any{}
	for i, d := range days {
		for _, kind := range []struct{ alias, qualifier string }{{"o", "created"}, {"m", "merged"}} {
			name := fmt.Sprintf("%s%d", kind.alias, i)
			if decl.Len() > 0 {
				decl.WriteString(", ")
			}
			fmt.Fprintf(&decl, "$%s: String!", name)
			fmt.Fprintf(&body, "  %s: search(type: ISSUE, query: $%s, first: 1) { issueCount }\n", name, name)
			vars[name] = fmt.Sprintf("%s is:pr %s:%s", scope, kind.qualifier, d.search())
		}
	}
	var data map[string]struct {
		IssueCount int `json:"issueCount"`
	}
	notes, err := c.graphql(ctx, "activity", "query("+decl.String()+") {\n"+body.String()+"}\n", vars, &data)
	if err != nil {
		return nil, nil, err
	}
	// A search that failed is null in data: a zero would read as a quiet day.
	if len(notes) > 0 {
		return nil, nil, fmt.Errorf("github %s", notes[0])
	}
	opened, merged = make([]int, len(days)), make([]int, len(days))
	for i := range days {
		opened[i] = data[fmt.Sprintf("o%d", i)].IssueCount
		merged[i] = data[fmt.Sprintf("m%d", i)].IssueCount
	}
	return opened, merged, nil
}

// CommitActivity is the commits on the default branches of an owner's
// repositories for each day. Fallback says the commit search was rate limited
// and the counts come from the activityRepos most recently pushed
// repositories. Partial says that more repositories than those were pushed
// since the first day, so the counts are a lower bound.
type CommitActivity struct {
	Days     []int
	Partial  bool
	Fallback bool
}

// OwnerCommitActivity counts, for each day, the commits on the default
// branches of scope (org:o or user:o). It asks the commit search for one
// count a day, all at the same time: that took 0.5 to 0.75 s on accounts of
// 40 to 174 repositories and counts every repository, against 3.2 to 4.2 s
// for the newest activityRepos with historyCommitActivity.
func (c *Client) OwnerCommitActivity(ctx context.Context, scope string, days []DayRange) (*CommitActivity, error) {
	if c.Token == "" {
		return nil, ErrNoToken
	}
	if len(days) == 0 {
		return &CommitActivity{Days: []int{}}, nil
	}
	res, err := c.searchCommitActivity(ctx, scope, days)
	var limited *RateLimitError
	if !errors.As(err, &limited) {
		return res, err
	}
	// The search API allows 30 requests a minute for a token, shared with
	// every other search: when it is used up, count the repositories.
	_, owner, _ := strings.Cut(scope, ":")
	res, err = c.historyCommitActivity(ctx, owner, days)
	if err != nil {
		return nil, err
	}
	res.Fallback = true
	return res, nil
}

// searchCommitActivity sends one commit search for each day and reads its
// total_count. Any failed search fails the read: a missing day would show as
// a day with no commit.
func (c *Client) searchCommitActivity(ctx context.Context, scope string, days []DayRange) (*CommitActivity, error) {
	res := &CommitActivity{Days: make([]int, len(days))}
	errs := make([]error, len(days))
	var wg sync.WaitGroup
	for i, d := range days {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res.Days[i], errs[i] = c.commitSearchCount(ctx, scope+" committer-date:"+d.search())
		}()
	}
	wg.Wait()
	// A rate limit comes first, so the caller counts the repositories instead.
	var limited *RateLimitError
	for _, err := range errs {
		if errors.As(err, &limited) {
			return nil, err
		}
	}
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return res, nil
}

// commitSearchCount returns how many commits match query. GitHub searches the
// default branch of each repository and leaves forks out.
func (c *Client) commitSearchCount(ctx context.Context, query string) (int, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("per_page", "1")
	resp, err := c.get(ctx, "/search/commits?"+q.Encode())
	if err != nil {
		return 0, fmt.Errorf("github commit search: %w", err)
	}
	defer resp.Body.Close()
	if rl := asRateLimit(resp, time.Now()); rl != nil {
		return 0, rl
	}
	if resp.StatusCode == http.StatusUnprocessableEntity {
		return 0, scopeError(resp)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("github commit search returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out struct {
		TotalCount int `json:"total_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, fmt.Errorf("decode github commit search: %w", err)
	}
	return out.TotalCount, nil
}

// historyCommitActivity counts, for each day, the commits on the default
// branch of the owner's activityRepos most recently pushed repositories. Each
// repository is counted and added. Forks and archived repositories are left
// out.
func (c *Client) historyCommitActivity(ctx context.Context, owner string, days []DayRange) (*CommitActivity, error) {
	res := &CommitActivity{Days: make([]int, len(days))}
	var decl, history strings.Builder
	vars := map[string]any{"owner": owner}
	for i, d := range days {
		fmt.Fprintf(&decl, ", $s%[1]d: GitTimestamp!, $u%[1]d: GitTimestamp!", i)
		fmt.Fprintf(&history, " d%[1]d: history(since: $s%[1]d, until: $u%[1]d) { totalCount }", i)
		vars[fmt.Sprintf("s%d", i)] = d.Start.Format(time.RFC3339)
		vars[fmt.Sprintf("u%d", i)] = d.End.Format(time.RFC3339)
	}
	query := fmt.Sprintf(`query($owner: String!%s) {
  repositoryOwner(login: $owner) {
    repositories(first: %d, orderBy: {field: PUSHED_AT, direction: DESC}, isArchived: false, isFork: false, ownerAffiliations: OWNER) {
      totalCount
      nodes { pushedAt defaultBranchRef { target { ... on Commit {%s } } } }
    }
  }
}
`, decl.String(), activityRepos, history.String())

	var data struct {
		RepositoryOwner *struct {
			Repositories struct {
				TotalCount int `json:"totalCount"`
				Nodes      []struct {
					PushedAt         time.Time `json:"pushedAt"`
					DefaultBranchRef *struct {
						Target map[string]struct {
							TotalCount int `json:"totalCount"`
						} `json:"target"`
					} `json:"defaultBranchRef"`
				} `json:"nodes"`
			} `json:"repositories"`
		} `json:"repositoryOwner"`
	}
	notes, err := c.graphql(ctx, "commit activity", query, vars, &data)
	if err != nil {
		return nil, err
	}
	if len(notes) > 0 {
		return nil, fmt.Errorf("github %s", notes[0])
	}
	if data.RepositoryOwner == nil {
		return nil, fmt.Errorf("github commit activity query: owner %q not found", owner)
	}
	repos := data.RepositoryOwner.Repositories
	for _, n := range repos.Nodes {
		if n.DefaultBranchRef == nil {
			continue
		}
		for i := range days {
			res.Days[i] += n.DefaultBranchRef.Target[fmt.Sprintf("d%d", i)].TotalCount
		}
	}
	// The list is newest push first: when the last one read was still pushed
	// in the window, a repository after it can be too.
	if n := len(repos.Nodes); n > 0 && n < repos.TotalCount {
		res.Partial = !repos.Nodes[n-1].PushedAt.Before(days[0].Start)
	}
	return res, nil
}
