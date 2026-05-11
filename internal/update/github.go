package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
)

// DefaultRepo is the upstream GitHub `owner/repo` target. Tests can swap in
// their own mux server by passing a custom Client.
const DefaultRepo = "x-mesh/gk"

// Client talks to GitHub's release API and download host.
//
// Default zero-value Client uses http.DefaultClient and DefaultRepo and is
// fine for production. Tests inject a custom HTTPDoer + APIBase + DownloadBase
// to redirect traffic to httptest.Server without a network round-trip.
type Client struct {
	HTTP         HTTPDoer // http.DefaultClient when nil
	Repo         string   // "x-mesh/gk" when ""
	APIBase      string   // "https://api.github.com" when ""
	DownloadBase string   // "https://github.com" when ""
}

// HTTPDoer is the subset of http.Client we depend on. Lets tests substitute
// a recording transport without juggling the full http.Client zoo.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func (c *Client) doer() HTTPDoer {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) repo() string {
	if c.Repo != "" {
		return c.Repo
	}
	return DefaultRepo
}

func (c *Client) apiBase() string {
	if c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/")
	}
	return "https://api.github.com"
}

func (c *Client) downloadBase() string {
	if c.DownloadBase != "" {
		return strings.TrimRight(c.DownloadBase, "/")
	}
	return "https://github.com"
}

// LatestTag returns the most recent release's tag (e.g. "v0.29.1").
//
// Tries github.com's /releases/latest 302 redirect first — the redirect
// Location resolves to /<owner>/<repo>/releases/tag/<tag> and is served by
// the regular github.com host, which is not subject to api.github.com's
// 60/hour anonymous rate limit. This is the same trick install.sh uses.
//
// Falls back to the api.github.com /releases/latest JSON endpoint when the
// redirect path fails (e.g. unexpected Location format), so we keep
// behaviour for environments that proxy github.com but not api.github.com.
func (c *Client) LatestTag(ctx context.Context) (string, error) {
	tag, redirectErr := c.latestTagRedirect(ctx)
	if redirectErr == nil {
		return tag, nil
	}
	tag, apiErr := c.latestTagAPI(ctx)
	if apiErr == nil {
		return tag, nil
	}
	// Surface both failure modes so the user sees that we tried the cheap
	// path before falling through to the rate-limited one.
	return "", fmt.Errorf("look up latest release: redirect failed (%v); api failed (%v)", redirectErr, apiErr)
}

// latestTagRedirect resolves the latest tag via github.com's release-latest
// redirect. The caller-supplied doer must NOT auto-follow redirects (set
// http.Client.CheckRedirect = http.ErrUseLastResponse); otherwise we end up
// reading the rendered release page instead of the Location header.
func (c *Client) latestTagRedirect(ctx context.Context) (string, error) {
	url := fmt.Sprintf("%s/%s/releases/latest", c.downloadBase(), c.repo())
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.doer().Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch redirect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return "", fmt.Errorf("expected redirect, got %s", resp.Status)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", errors.New("redirect missing Location header")
	}
	tag := path.Base(loc)
	if tag == "" || tag == "." || tag == "/" || tag == "latest" {
		return "", fmt.Errorf("unexpected redirect target %q", loc)
	}
	return tag, nil
}

// latestTagAPI hits the JSON release endpoint. Used as a fallback when the
// redirect path can't reach github.com (e.g. corp proxy that only allows
// api.github.com). Anonymous, so subject to the 60/hour limit.
func (c *Client) latestTagAPI(ctx context.Context) (string, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", c.apiBase(), c.repo())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.doer().Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Cap body to keep error messages bounded; GitHub error JSON is
		// tiny but a misconfigured proxy could return arbitrarily large
		// HTML.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("github api returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode release: %w", err)
	}
	if payload.TagName == "" {
		return "", fmt.Errorf("github api returned empty tag_name")
	}
	return payload.TagName, nil
}

// AssetURL returns the direct-download URL for a release asset.
// Mirrors the documented `releases/download/<tag>/<asset>` layout used by
// goreleaser, the install.sh script, and the Homebrew tap formula.
func (c *Client) AssetURL(tag, asset string) string {
	return fmt.Sprintf("%s/%s/releases/download/%s/%s", c.downloadBase(), c.repo(), tag, asset)
}
