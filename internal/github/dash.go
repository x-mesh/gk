package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNoToken is returned before any request when the client has no token:
// GitHub rejects an anonymous GraphQL request with 403.
var ErrNoToken = errors.New("github token required")

// QueryTimeoutError reports a GraphQL query that GitHub did not finish in
// time (502/504) or that hit the caller's deadline. A large repository can
// push a heavy query past GitHub's limit, so it is reported apart from other
// failures instead of being retried with a smaller page.
type QueryTimeoutError struct {
	Query  string
	Status string
}

func (e *QueryTimeoutError) Error() string {
	return fmt.Sprintf("github %s query timed out (%s)", e.Query, e.Status)
}

// dashPage is the page size of every dashboard block. A 50-item query timed
// out with 502 on a repository with 56 open PRs and 1077 open issues; 20
// items split into two queries answered in about 6 s and 2.5 s.
const dashPage = 20

// DashQuery holds the search strings of a dashboard read. Callers add the
// scope prefix (repo:, org:, user:). Owner and Repo, when set, add the
// repository block and the Unassigned search.
type DashQuery struct {
	MyPRs      string
	ReviewPRs  string
	Assigned   string
	Mentioned  string
	Authored   string
	Unassigned string
	Owner      string
	Repo       string
}

func (q DashQuery) hasRepo() bool { return q.Owner != "" && q.Repo != "" }

// DashCheck is one CI check of a PR head: a check run or a commit status.
// State and Conclusion are GitHub's raw values: a check run has a status
// (QUEUED, IN_PROGRESS, COMPLETED) and a conclusion, a status context has
// only a state.
type DashCheck struct {
	Kind        string // "check_run" | "status"
	Name        string
	State       string
	Conclusion  string
	Description string
	URL         string
}

// DashPR is one pull request with the fields the dashboard needs.
type DashPR struct {
	Number         int
	Title          string
	URL            string
	Repo           string // owner/name
	Author         string
	Draft          bool
	State          string // OPEN | CLOSED | MERGED
	HeadRef        string
	HeadOID        string
	BaseRef        string
	BaseOID        string
	CrossRepo      bool
	ReviewDecision string // "" when the repository sets no review rule
	UpdatedAt      time.Time
	Rollup         string // statusCheckRollup.state, "" when the head has no checks
	Checks         []DashCheck
	ChecksTotal    int
	ClosingIssues  []int
}

// DashLinkedPR is a pull request that references an issue.
type DashLinkedPR struct {
	Number int
	State  string
	Merged bool
}

// DashIssue is one issue with the fields the dashboard needs.
type DashIssue struct {
	Number    int
	Title     string
	URL       string
	Repo      string
	Author    string
	UpdatedAt time.Time
	Assignees int
	LinkedPRs []DashLinkedPR
}

// DashPRList is one PR block: Total is GitHub's count and Items holds at most
// one page of it.
type DashPRList struct {
	Total int
	Items []DashPR
}

// DashIssueList is one issue block, as DashPRList.
type DashIssueList struct {
	Total int
	Items []DashIssue
}

// DashResult is one dashboard read. When only one of the two queries fails,
// PRErr or IssueErr holds why, and the other side's blocks are filled.
// Notes carry GraphQL errors that came with partial data.
type DashResult struct {
	MyPRs      DashPRList
	ReviewPRs  DashPRList
	RepoOpen   DashPRList
	RepoMerged DashPRList
	Assigned   DashIssueList
	Mentioned  DashIssueList
	Authored   DashIssueList
	Unassigned DashIssueList
	RepoIssues int
	HasRepo    bool
	PRErr      error
	IssueErr   error
	Notes      []string
}

const dashPRFragment = `fragment DashPR on PullRequest {
  number title url isDraft state headRefName headRefOid baseRefName baseRefOid isCrossRepository reviewDecision updatedAt
  repository { nameWithOwner }
  author { login }
  closingIssuesReferences(first: 5) { nodes { number } }
  commits(last: 1) { nodes { commit { statusCheckRollup { state contexts(first: 20) { totalCount nodes {
    __typename
    ... on CheckRun { name status conclusion detailsUrl }
    ... on StatusContext { context state description targetUrl }
  } } } } } }
}`

const dashIssueFragment = `fragment DashIssue on Issue {
  number title url updatedAt
  repository { nameWithOwner }
  author { login }
  assignees { totalCount }
  closedByPullRequestsReferences(first: 3, includeClosedPrs: true) { nodes { number state merged } }
}`

func dashPRQuery(hasRepo bool) string {
	var b strings.Builder
	b.WriteString(dashPRFragment + "\n")
	if hasRepo {
		b.WriteString("query($mine: String!, $review: String!, $owner: String!, $name: String!) {\n")
	} else {
		b.WriteString("query($mine: String!, $review: String!) {\n")
	}
	fmt.Fprintf(&b, "  mine: search(type: ISSUE, query: $mine, first: %d) { issueCount nodes { ...DashPR } }\n", dashPage)
	fmt.Fprintf(&b, "  review: search(type: ISSUE, query: $review, first: %d) { issueCount nodes { ...DashPR } }\n", dashPage)
	if hasRepo {
		b.WriteString("  repository(owner: $owner, name: $name) {\n")
		fmt.Fprintf(&b, "    open: pullRequests(states: OPEN, first: %d, orderBy: {field: UPDATED_AT, direction: DESC}) { totalCount nodes { ...DashPR } }\n", dashPage)
		fmt.Fprintf(&b, "    merged: pullRequests(states: MERGED, first: %d, orderBy: {field: UPDATED_AT, direction: DESC}) { totalCount nodes { ...DashPR } }\n", dashPage)
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func dashIssueQuery(hasRepo bool) string {
	var b strings.Builder
	b.WriteString(dashIssueFragment + "\n")
	if hasRepo {
		b.WriteString("query($assigned: String!, $mentioned: String!, $authored: String!, $unassigned: String!, $owner: String!, $name: String!) {\n")
	} else {
		b.WriteString("query($assigned: String!, $mentioned: String!, $authored: String!) {\n")
	}
	for _, alias := range []string{"assigned", "mentioned", "authored"} {
		fmt.Fprintf(&b, "  %s: search(type: ISSUE, query: $%s, first: %d) { issueCount nodes { ...DashIssue } }\n", alias, alias, dashPage)
	}
	if hasRepo {
		fmt.Fprintf(&b, "  unassigned: search(type: ISSUE, query: $unassigned, first: %d) { issueCount nodes { ...DashIssue } }\n", dashPage)
		b.WriteString("  repository(owner: $owner, name: $name) { issues(states: OPEN) { totalCount } }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// Dashboard reads the dashboard blocks with two GraphQL queries, one for PRs
// and one for issues, sent at the same time.
//
// A rate-limit error on either query wins, so a poller waits for the reset.
// When both fail the errors are joined; when one fails its side is empty and
// PRErr or IssueErr says why.
func (c *Client) Dashboard(ctx context.Context, q DashQuery) (*DashResult, error) {
	if c.Token == "" {
		return nil, ErrNoToken
	}
	prVars := map[string]any{"mine": q.MyPRs, "review": q.ReviewPRs}
	issueVars := map[string]any{"assigned": q.Assigned, "mentioned": q.Mentioned, "authored": q.Authored}
	if q.hasRepo() {
		prVars["owner"], prVars["name"] = q.Owner, q.Repo
		issueVars["owner"], issueVars["name"] = q.Owner, q.Repo
		issueVars["unassigned"] = q.Unassigned
	}

	var prData dashPRData
	var issueData dashIssueData
	var prNotes, issueNotes []string
	var prErr, issueErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		prNotes, prErr = c.graphql(ctx, "pr", dashPRQuery(q.hasRepo()), prVars, &prData)
	}()
	go func() {
		defer wg.Done()
		issueNotes, issueErr = c.graphql(ctx, "issue", dashIssueQuery(q.hasRepo()), issueVars, &issueData)
	}()
	wg.Wait()

	var rl *RateLimitError
	if errors.As(prErr, &rl) || errors.As(issueErr, &rl) {
		return nil, rl
	}
	if prErr != nil && issueErr != nil {
		return nil, errors.Join(prErr, issueErr)
	}

	res := &DashResult{HasRepo: q.hasRepo(), PRErr: prErr, IssueErr: issueErr}
	res.Notes = append(append(res.Notes, prNotes...), issueNotes...)
	if prErr == nil {
		res.MyPRs = prData.Mine.prList()
		res.ReviewPRs = prData.Review.prList()
		if prData.Repository != nil {
			res.RepoOpen = prData.Repository.Open.prList()
			res.RepoMerged = prData.Repository.Merged.prList()
		}
	}
	if issueErr == nil {
		res.Assigned = issueData.Assigned.issueList()
		res.Mentioned = issueData.Mentioned.issueList()
		res.Authored = issueData.Authored.issueList()
		if issueData.Unassigned != nil {
			res.Unassigned = issueData.Unassigned.issueList()
		}
		if issueData.Repository != nil {
			res.RepoIssues = issueData.Repository.Issues.TotalCount
		}
	}
	return res, nil
}

// graphql posts one query and decodes data into out. It returns the GraphQL
// error messages that came with data as notes. data null is an error.
func (c *Client) graphql(ctx context.Context, name, query string, vars map[string]any, out any) ([]string, error) {
	resp, err := c.post(ctx, "/graphql", map[string]any{"query": query, "variables": vars})
	if err != nil {
		if isTimeout(err) {
			return nil, &QueryTimeoutError{Query: name, Status: "deadline exceeded"}
		}
		return nil, fmt.Errorf("github %s query: %w", name, err)
	}
	defer resp.Body.Close()
	if rl := asRateLimit(resp, time.Now()); rl != nil {
		return nil, rl
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusBadGateway, http.StatusGatewayTimeout:
		return nil, &QueryTimeoutError{Query: name, Status: resp.Status}
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("github %s query returned %s: %s", name, resp.Status, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		if isTimeout(err) {
			return nil, &QueryTimeoutError{Query: name, Status: "deadline exceeded"}
		}
		return nil, fmt.Errorf("decode github %s query: %w", name, err)
	}
	// GraphQL reports an exhausted primary limit as 200 with errors.
	if len(payload.Errors) > 0 && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != "") {
		return nil, &RateLimitError{Status: resp.Status, Reset: graphqlRetryAt(resp, time.Now())}
	}
	var notes []string
	for _, e := range payload.Errors {
		notes = append(notes, fmt.Sprintf("%s query: %s", name, e.Message))
	}
	if len(payload.Data) == 0 || string(payload.Data) == "null" {
		if len(notes) > 0 {
			return nil, errors.New("github " + notes[0])
		}
		return nil, fmt.Errorf("github %s query returned no data", name)
	}
	if err := json.Unmarshal(payload.Data, out); err != nil {
		return nil, fmt.Errorf("decode github %s query: %w", name, err)
	}
	return notes, nil
}

func graphqlRetryAt(resp *http.Response, now time.Time) time.Time {
	if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
		return now.Add(time.Duration(secs) * time.Second)
	}
	if unix, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && unix > 0 {
		return time.Unix(unix, 0)
	}
	return now.Add(time.Minute)
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

type dashLogin struct {
	Login string `json:"login"`
}

type dashRepoName struct {
	NameWithOwner string `json:"nameWithOwner"`
}

type dashPRNode struct {
	Number            int          `json:"number"`
	Title             string       `json:"title"`
	URL               string       `json:"url"`
	IsDraft           bool         `json:"isDraft"`
	State             string       `json:"state"`
	HeadRefName       string       `json:"headRefName"`
	HeadRefOid        string       `json:"headRefOid"`
	BaseRefName       string       `json:"baseRefName"`
	BaseRefOid        string       `json:"baseRefOid"`
	IsCrossRepository bool         `json:"isCrossRepository"`
	ReviewDecision    string       `json:"reviewDecision"`
	UpdatedAt         time.Time    `json:"updatedAt"`
	Repository        dashRepoName `json:"repository"`
	Author            *dashLogin   `json:"author"`
	Closing           struct {
		Nodes []struct {
			Number int `json:"number"`
		} `json:"nodes"`
	} `json:"closingIssuesReferences"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				Rollup *struct {
					State    string `json:"state"`
					Contexts struct {
						TotalCount int `json:"totalCount"`
						Nodes      []struct {
							Typename    string `json:"__typename"`
							Name        string `json:"name"`
							Status      string `json:"status"`
							Conclusion  string `json:"conclusion"`
							DetailsURL  string `json:"detailsUrl"`
							Context     string `json:"context"`
							State       string `json:"state"`
							Description string `json:"description"`
							TargetURL   string `json:"targetUrl"`
						} `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

func (n dashPRNode) pr() DashPR {
	p := DashPR{
		Number: n.Number, Title: n.Title, URL: n.URL, Repo: n.Repository.NameWithOwner,
		Draft: n.IsDraft, State: n.State, HeadRef: n.HeadRefName, HeadOID: n.HeadRefOid,
		BaseRef: n.BaseRefName, BaseOID: n.BaseRefOid, CrossRepo: n.IsCrossRepository,
		ReviewDecision: n.ReviewDecision, UpdatedAt: n.UpdatedAt,
	}
	if n.Author != nil {
		p.Author = n.Author.Login
	}
	for _, c := range n.Closing.Nodes {
		p.ClosingIssues = append(p.ClosingIssues, c.Number)
	}
	if len(n.Commits.Nodes) > 0 {
		if r := n.Commits.Nodes[0].Commit.Rollup; r != nil {
			p.Rollup = r.State
			p.ChecksTotal = r.Contexts.TotalCount
			for _, c := range r.Contexts.Nodes {
				switch c.Typename {
				case "CheckRun":
					p.Checks = append(p.Checks, DashCheck{Kind: "check_run", Name: c.Name, State: c.Status, Conclusion: c.Conclusion, URL: c.DetailsURL})
				case "StatusContext":
					p.Checks = append(p.Checks, DashCheck{Kind: "status", Name: c.Context, State: c.State, Description: c.Description, URL: c.TargetURL})
				}
			}
		}
	}
	return p
}

type dashPRConn struct {
	IssueCount int          `json:"issueCount"`
	TotalCount int          `json:"totalCount"`
	Nodes      []dashPRNode `json:"nodes"`
}

func (c dashPRConn) prList() DashPRList {
	l := DashPRList{Total: max(c.IssueCount, c.TotalCount)}
	for _, n := range c.Nodes {
		// A search node that is not a PR decodes with number 0.
		if n.Number != 0 {
			l.Items = append(l.Items, n.pr())
		}
	}
	return l
}

type dashPRData struct {
	Mine       dashPRConn `json:"mine"`
	Review     dashPRConn `json:"review"`
	Repository *struct {
		Open   dashPRConn `json:"open"`
		Merged dashPRConn `json:"merged"`
	} `json:"repository"`
}

type dashIssueNode struct {
	Number     int          `json:"number"`
	Title      string       `json:"title"`
	URL        string       `json:"url"`
	UpdatedAt  time.Time    `json:"updatedAt"`
	Repository dashRepoName `json:"repository"`
	Author     *dashLogin   `json:"author"`
	Assignees  struct {
		TotalCount int `json:"totalCount"`
	} `json:"assignees"`
	ClosedBy struct {
		Nodes []struct {
			Number int    `json:"number"`
			State  string `json:"state"`
			Merged bool   `json:"merged"`
		} `json:"nodes"`
	} `json:"closedByPullRequestsReferences"`
}

type dashIssueConn struct {
	IssueCount int             `json:"issueCount"`
	Nodes      []dashIssueNode `json:"nodes"`
}

func (c dashIssueConn) issueList() DashIssueList {
	l := DashIssueList{Total: c.IssueCount}
	for _, n := range c.Nodes {
		if n.Number == 0 {
			continue
		}
		is := DashIssue{
			Number: n.Number, Title: n.Title, URL: n.URL, Repo: n.Repository.NameWithOwner,
			UpdatedAt: n.UpdatedAt, Assignees: n.Assignees.TotalCount,
		}
		if n.Author != nil {
			is.Author = n.Author.Login
		}
		for _, p := range n.ClosedBy.Nodes {
			is.LinkedPRs = append(is.LinkedPRs, DashLinkedPR{Number: p.Number, State: p.State, Merged: p.Merged})
		}
		l.Items = append(l.Items, is)
	}
	return l
}

type dashIssueData struct {
	Assigned   dashIssueConn  `json:"assigned"`
	Mentioned  dashIssueConn  `json:"mentioned"`
	Authored   dashIssueConn  `json:"authored"`
	Unassigned *dashIssueConn `json:"unassigned"`
	Repository *struct {
		Issues struct {
			TotalCount int `json:"totalCount"`
		} `json:"issues"`
	} `json:"repository"`
}
