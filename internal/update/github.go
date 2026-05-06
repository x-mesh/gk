package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// LatestTag fetches the most recent release's `tag_name` (e.g. "v0.29.1").
//
// Hits the unauthenticated /releases/latest endpoint, which excludes pre-
// releases and drafts — exactly the surface we want users to land on by
// default. Anonymous calls are limited to 60/hour per IP; that is fine for
// `gk update` since users invoke it manually.
func (c *Client) LatestTag(ctx context.Context) (string, error) {
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
