package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/update"
)

// TestBrewUpgradeArgs guards the cask migration: a Caskroom binary
// must dispatch `brew upgrade --cask x-mesh/tap/gk`, otherwise brew
// matches the legacy Formula/gk.rb pinned to v0.54.0 and the upgrade
// silently no-ops on an old version.
func TestBrewUpgradeArgs(t *testing.T) {
	cases := []struct {
		name string
		kind update.BrewKind
		want []string
	}{
		{"cask", update.BrewKindCask, []string{"upgrade", "--cask", "x-mesh/tap/gk"}},
		{"formula", update.BrewKindFormula, []string{"upgrade", "x-mesh/tap/gk"}},
		// Zero value falls back to formula. Historical safety:
		// `brew upgrade` without `--cask` works on pre-v0.55 installs.
		{"empty kind", update.BrewKindNone, []string{"upgrade", "x-mesh/tap/gk"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := brewUpgradeArgs(tc.kind)
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("brewUpgradeArgs(%q) = %v, want %v", tc.kind, got, tc.want)
			}
		})
	}
}

// TestUpdateHTTPClientFollowsAssetRedirect is the regression guard for the
// bug where newUpdateHTTPClient's CheckRedirect unconditionally returned
// ErrUseLastResponse: GitHub 302-redirects every /releases/download/ asset to
// objects.githubusercontent.com, so checksums.txt and the release archive
// must follow that hop instead of surfacing the bare 302.
func TestUpdateHTTPClientFollowsAssetRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/x-mesh/gk/releases/download/"):
			// Mirror GitHub: assets 302 to a separate blob host.
			http.Redirect(w, r, "/blob"+r.URL.Path, http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/blob/"):
			_, _ = w.Write([]byte("asset-body"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := newUpdateHTTPClient()
	resp, err := c.Get(srv.URL + "/x-mesh/gk/releases/download/v0.48.0/checksums.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %s, want 200 (redirect should have been followed)", resp.Status)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "asset-body" {
		t.Errorf("body = %q, want asset-body", body)
	}
}

// TestUpdateHTTPClientStopsAtLatestRedirect confirms the /releases/latest
// probe still gets the raw 302 — latestTagRedirect reads the Location header
// rather than following through to the rendered release page.
func TestUpdateHTTPClientStopsAtLatestRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			w.Header().Set("Location", "/x-mesh/gk/releases/tag/v0.48.0")
			w.WriteHeader(http.StatusFound)
			return
		}
		t.Errorf("unexpected follow to %s", r.URL.Path)
	}))
	defer srv.Close()

	c := newUpdateHTTPClient()
	resp, err := c.Get(srv.URL + "/x-mesh/gk/releases/latest")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %s, want 302 (redirect must not be followed)", resp.Status)
	}
	if loc := resp.Header.Get("Location"); loc != "/x-mesh/gk/releases/tag/v0.48.0" {
		t.Errorf("Location = %q, want the tag path", loc)
	}
}
