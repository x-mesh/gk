package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// HTTPDoer is the subset of http.Client this package depends on. Lets
// tests substitute a recording transport without a network round-trip.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Repo is the subset of GitHub's repository JSON `gk clone`'s picker needs.
type Repo struct {
	Owner       string
	Name        string
	Description string
	UpdatedAt   time.Time
	Private     bool
}

// Client talks to api.github.com. The zero value works unauthenticated
// (public repos only, subject to the 60/hour anonymous rate limit); set
// Token (see ResolveToken) to see private repos too.
type Client struct {
	HTTP    HTTPDoer // http.DefaultClient when nil
	APIBase string   // "https://api.github.com" when ""
	Token   string

	loginOnce sync.Once
	login     string
}

// errNotFound is returned by fetchPage on a 404 so ListRepos can
// distinguish "this owner isn't an org" from a real failure and fall
// through to the next endpoint it tries.
var errNotFound = errors.New("not found")

func (c *Client) doer() HTTPDoer {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) apiBase() string {
	if c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/")
	}
	return "https://api.github.com"
}

func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return c.doer().Do(req)
}

// authenticatedLogin returns the login of the token's owner, memoized for
// the lifetime of the Client. Empty when there is no token or the lookup
// fails — callers treat that as "unknown", not an error, since it only
// gates an optimization (seeing your own private repos via /user/repos).
func (c *Client) authenticatedLogin(ctx context.Context) string {
	if c.Token == "" {
		return ""
	}
	c.loginOnce.Do(func() {
		resp, err := c.get(ctx, "/user")
		if err != nil {
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return
		}
		var payload struct {
			Login string `json:"login"`
		}
		if json.NewDecoder(resp.Body).Decode(&payload) == nil {
			c.login = payload.Login
		}
	})
	return c.login
}

// ListRepos lists the repositories visible to this client under owner,
// whether owner is a GitHub org or a user.
//
// It tries the org endpoint first — /orgs/{owner}/repos also returns
// private repos when the token belongs to an org member. A 404 there
// means owner isn't an org, so it falls through:
//
//   - if the token's own login matches owner, /user/repos (filtered to
//     that owner) is used so the caller's own private repos show up —
//     the public-only /users/{owner}/repos can't see them even for the
//     token's own account.
//   - otherwise /users/{owner}/repos (public repos only; seeing someone
//     else's private repos requires being added as a collaborator, which
//     this endpoint does not surface).
func (c *Client) ListRepos(ctx context.Context, owner string) ([]Repo, error) {
	repos, err := c.fetchAllPages(ctx, fmt.Sprintf("/orgs/%s/repos", owner), "type=all")
	if err == nil {
		return repos, nil
	}
	if !errors.Is(err, errNotFound) {
		return nil, err
	}

	if login := c.authenticatedLogin(ctx); login != "" && strings.EqualFold(login, owner) {
		repos, err := c.fetchAllPages(ctx, "/user/repos", "affiliation=owner")
		if err != nil {
			return nil, err
		}
		return filterByOwner(repos, owner), nil
	}

	repos, err = c.fetchAllPages(ctx, fmt.Sprintf("/users/%s/repos", owner), "")
	if err != nil {
		return nil, err
	}
	return repos, nil
}

func filterByOwner(repos []Repo, owner string) []Repo {
	out := repos[:0]
	for _, r := range repos {
		if strings.EqualFold(r.Owner, owner) {
			out = append(out, r)
		}
	}
	return out
}

// maxPages caps pagination at 500 repos (5 pages of 100) — comfortably
// past any single owner a human picks from a TUI list, and a hard stop
// against paginating forever on an API that never returns an empty page.
const maxPages = 5

func (c *Client) fetchAllPages(ctx context.Context, path, extraQuery string) ([]Repo, error) {
	var all []Repo
	for page := 1; page <= maxPages; page++ {
		query := fmt.Sprintf("per_page=100&sort=updated&page=%d", page)
		if extraQuery != "" {
			query += "&" + extraQuery
		}
		batch, err := c.fetchPage(ctx, path+"?"+query)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			break
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].UpdatedAt.After(all[j].UpdatedAt) })
	return all, nil
}

func (c *Client) fetchPage(ctx context.Context, pathWithQuery string) ([]Repo, error) {
	resp, err := c.get(ctx, pathWithQuery)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("github api returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload []struct {
		Name        string `json:"name"`
		FullName    string `json:"full_name"`
		Description string `json:"description"`
		UpdatedAt   string `json:"updated_at"`
		Private     bool   `json:"private"`
		Owner       struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode repo list: %w", err)
	}

	repos := make([]Repo, 0, len(payload))
	for _, p := range payload {
		r := Repo{
			Owner:       p.Owner.Login,
			Name:        p.Name,
			Description: p.Description,
			Private:     p.Private,
		}
		if t, err := time.Parse(time.RFC3339, p.UpdatedAt); err == nil {
			r.UpdatedAt = t
		}
		repos = append(repos, r)
	}
	return repos, nil
}
