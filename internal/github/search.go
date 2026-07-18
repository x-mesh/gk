package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Issue is one row from the GitHub issue/PR search index. GitHub models
// pull requests as issues, so a single /search/issues query returns both;
// IsPR distinguishes them — the raw payload carries a `pull_request` object
// only for PRs.
type Issue struct {
	Number    int
	Title     string
	State     string // "open" | "closed"
	URL       string // html_url — the browser link, not the API url
	Owner     string // parsed from repository_url
	Repo      string
	Author    string
	UpdatedAt time.Time
	IsPR      bool
	Draft     bool
	Labels    []string
}

// searchMaxPages caps a search at 1000 results (10 pages of 100) — the hard
// ceiling the Search API itself enforces (it never returns past result
// 1000), so paging further is wasted round-trips.
const searchMaxPages = 10

// SearchIssues runs a GitHub issue/PR search (GET /search/issues) and
// returns every matching row across pages. query is the raw `q=` value,
// e.g. "repo:owner/name is:open is:pr" — callers build the qualifiers.
//
// A token is required for `@me` qualifiers and to see private repos;
// unauthenticated callers get public results only, under the stricter
// 10/min search bucket (vs 30/min authenticated).
func (c *Client) SearchIssues(ctx context.Context, query string) ([]Issue, error) {
	var all []Issue
	for page := 1; page <= searchMaxPages; page++ {
		q := url.Values{}
		q.Set("q", query)
		q.Set("per_page", "100")
		q.Set("sort", "updated")
		q.Set("order", "desc")
		q.Set("page", fmt.Sprintf("%d", page))

		batch, total, err := c.searchPage(ctx, "/search/issues?"+q.Encode())
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 || len(all) >= total {
			break
		}
	}
	return all, nil
}

// SearchCount returns just the total number of matches for a query, without
// paging the rows in — for callers (like `gk context`) that only need the
// count. It fetches a single minimal page and reads total_count.
func (c *Client) SearchCount(ctx context.Context, query string) (int, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("per_page", "1")
	_, total, err := c.searchPage(ctx, "/search/issues?"+q.Encode())
	return total, err
}

func (c *Client) searchPage(ctx context.Context, pathWithQuery string) ([]Issue, int, error) {
	resp, err := c.get(ctx, pathWithQuery)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, 0, rateLimitError(resp)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, 0, fmt.Errorf("github search returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	// The search endpoint wraps its rows in {total_count, items:[...]} —
	// unlike the plain array the /repos endpoints return.
	var payload struct {
		TotalCount int `json:"total_count"`
		Items      []struct {
			Number        int    `json:"number"`
			Title         string `json:"title"`
			State         string `json:"state"`
			HTMLURL       string `json:"html_url"`
			RepositoryURL string `json:"repository_url"`
			Draft         bool   `json:"draft"`
			UpdatedAt     string `json:"updated_at"`
			User          struct {
				Login string `json:"login"`
			} `json:"user"`
			PullRequest *struct {
				URL string `json:"url"`
			} `json:"pull_request"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, 0, fmt.Errorf("decode search result: %w", err)
	}

	issues := make([]Issue, 0, len(payload.Items))
	for _, it := range payload.Items {
		owner, repo := ownerRepoFromAPIURL(it.RepositoryURL)
		iss := Issue{
			Number: it.Number,
			Title:  it.Title,
			State:  it.State,
			URL:    it.HTMLURL,
			Owner:  owner,
			Repo:   repo,
			Author: it.User.Login,
			IsPR:   it.PullRequest != nil,
			Draft:  it.Draft,
		}
		if t, err := time.Parse(time.RFC3339, it.UpdatedAt); err == nil {
			iss.UpdatedAt = t
		}
		for _, l := range it.Labels {
			iss.Labels = append(iss.Labels, l.Name)
		}
		issues = append(issues, iss)
	}
	return issues, payload.TotalCount, nil
}

// OwnerType reports whether owner is an "Organization" or a "User" on
// GitHub, via GET /users/{owner} — the endpoint answers for both, and an
// org login resolves here too (with type "Organization"). Callers use it
// to pick the right search qualifier: `org:{owner}` vs `user:{owner}`.
func (c *Client) OwnerType(ctx context.Context, owner string) (string, error) {
	resp, err := c.get(ctx, "/users/"+url.PathEscape(owner))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("github user lookup returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode user type: %w", err)
	}
	return payload.Type, nil
}

// ownerRepoFromAPIURL pulls owner/repo out of a repository_url like
// "https://api.github.com/repos/octocat/hello-world". Returns empty strings
// when the shape is unexpected — callers treat that as "unknown repo", not
// a failure.
func ownerRepoFromAPIURL(u string) (string, string) {
	const marker = "/repos/"
	i := strings.LastIndex(u, marker)
	if i < 0 {
		return "", ""
	}
	parts := strings.SplitN(u[i+len(marker):], "/", 3)
	if len(parts) < 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// rateLimitError turns a 403/429 search response into a helpful message.
// A 403 is only a rate limit when the remaining count is zero (or a
// Retry-After is present); a 403 with quota left is a genuine permission
// error, so it falls through to the generic body.
func rateLimitError(resp *http.Response) error {
	retryAfter := resp.Header.Get("Retry-After")
	remaining := resp.Header.Get("X-RateLimit-Remaining")
	if resp.StatusCode == http.StatusTooManyRequests || remaining == "0" || retryAfter != "" {
		const hint = "the search API allows 30/min authenticated, 10/min anonymous — set GH_TOKEN to raise the limit"
		if retryAfter != "" {
			return fmt.Errorf("github search rate-limited (retry after %ss) — %s", retryAfter, hint)
		}
		return fmt.Errorf("github search rate-limited — %s", hint)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("github search returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
}
