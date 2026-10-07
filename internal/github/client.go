package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
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
	PushedAt    time.Time
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

// newRequest builds an API request with the headers every endpoint needs.
// Both get and post go through it so the Accept/version/auth headers can
// never drift apart between read and write paths.
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.apiBase()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return req, nil
}

func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return c.doer().Do(req)
}

// post sends payload as a JSON body. Unlike get, this mutates state on
// GitHub, so every caller must have already established that it has a token —
// an anonymous POST fails with a 401 that reads like a bug.
func (c *Client) post(ctx context.Context, path string, payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
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

	return decodeRepoList(resp.Body)
}

func decodeRepoList(body io.Reader) ([]Repo, error) {
	var payload []struct {
		Name        string `json:"name"`
		FullName    string `json:"full_name"`
		Description string `json:"description"`
		UpdatedAt   string `json:"updated_at"`
		PushedAt    string `json:"pushed_at"`
		Private     bool   `json:"private"`
		Owner       struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
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
		if t, err := time.Parse(time.RFC3339, p.PushedAt); err == nil {
			r.PushedAt = t
		}
		repos = append(repos, r)
	}
	return repos, nil
}

// ViewerLogin returns the login of the token's owner, or "" when
// unauthenticated (or the lookup failed). Memoized for the client's lifetime.
func (c *Client) ViewerLogin(ctx context.Context) string {
	return c.authenticatedLogin(ctx)
}

// ListMyOrgs returns the organization logins the token's user belongs to
// (GET /user/orgs). It needs a token: unauthenticated callers get an empty
// list and no error, so a scope picker can simply offer fewer choices.
func (c *Client) ListMyOrgs(ctx context.Context) ([]string, error) {
	if c.Token == "" {
		return nil, nil
	}
	resp, err := c.get(ctx, "/user/orgs?per_page=100")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("github orgs returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var payload []struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode orgs: %w", err)
	}
	out := make([]string, 0, len(payload))
	for _, o := range payload {
		if o.Login != "" {
			out = append(out, o.Login)
		}
	}
	return out, nil
}

// getConditional is get with If-None-Match. GitHub does not charge a 304
// against the primary rate limit, which is what makes tight org-wide polling
// affordable.
func (c *Client) getConditional(ctx context.Context, path, etag string) (*http.Response, error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	return c.doer().Do(req)
}

// RateLimitError reports a primary or secondary rate-limit rejection. Reset is
// when GitHub says the caller may try again, so a long-running poller can
// sleep instead of failing.
type RateLimitError struct {
	Status string
	Reset  time.Time
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("github rate limit (%s), retry after %s", e.Status, e.Reset.Format(time.RFC3339))
}

// asRateLimit returns a *RateLimitError when resp is a rate-limit rejection,
// nil otherwise. Unlike rateLimitError (search), the caller gets the reset
// time to sleep on. A 403 with quota left is a permission failure, not a limit.
func asRateLimit(resp *http.Response, now time.Time) *RateLimitError {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
		return &RateLimitError{Status: resp.Status, Reset: now.Add(time.Duration(secs) * time.Second)}
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		reset := now.Add(time.Minute)
		if unix, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			reset = time.Unix(unix, 0)
		}
		return &RateLimitError{Status: resp.Status, Reset: reset}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return &RateLimitError{Status: resp.Status, Reset: now.Add(time.Minute)}
	}
	return nil
}

// PushedRepoPoller lists one owner's repositories newest-push-first, one
// page per Poll. It remembers which endpoint answered and that endpoint's
// ETag, so a steady-state poll is a single conditional request.
type PushedRepoPoller struct {
	Client *Client
	Owner  string
	Limit  int

	path string
	etag string
	last []Repo
}

// Poll returns the newest-pushed repositories. changed is false when GitHub
// answered 304 and repos is the previous result.
func (p *PushedRepoPoller) Poll(ctx context.Context) (repos []Repo, changed bool, err error) {
	if p.path == "" {
		if err := p.resolvePath(ctx); err != nil {
			return nil, false, err
		}
	}
	resp, err := p.Client.getConditional(ctx, p.path, p.etag)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return p.last, false, nil
	}
	if rl := asRateLimit(resp, time.Now()); rl != nil {
		return nil, false, rl
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, false, fmt.Errorf("github repo list returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	repos, err = decodeRepoList(resp.Body)
	if err != nil {
		return nil, false, err
	}
	if strings.HasPrefix(p.path, "/user/repos") {
		repos = filterByOwner(repos, p.Owner)
	}
	p.etag = resp.Header.Get("ETag")
	p.last = repos
	return repos, true, nil
}

// resolvePath picks the endpoint once, with the same org → own account →
// public user fallback as ListRepos.
func (p *PushedRepoPoller) resolvePath(ctx context.Context) error {
	limit := p.Limit
	if limit <= 0 {
		limit = 30
	}
	query := fmt.Sprintf("sort=pushed&direction=desc&per_page=%d", limit)
	orgPath := fmt.Sprintf("/orgs/%s/repos?type=all&%s", url.PathEscape(p.Owner), query)
	resp, err := p.Client.get(ctx, orgPath)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		p.path = orgPath
		return nil
	}
	if login := p.Client.authenticatedLogin(ctx); login != "" && strings.EqualFold(login, p.Owner) {
		p.path = "/user/repos?affiliation=owner&" + query
		return nil
	}
	p.path = fmt.Sprintf("/users/%s/repos?%s", url.PathEscape(p.Owner), query)
	return nil
}
