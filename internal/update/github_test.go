package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// noFollowClient mirrors the http.Client newUpdateHTTPClient produces in the
// cli layer — required for the redirect path to surface the 302 Location
// header instead of following it.
func noFollowClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func TestLatestTagRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/x-mesh/gk/releases/latest"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if r.Method != http.MethodHead {
			t.Errorf("method = %q, want HEAD", r.Method)
		}
		w.Header().Set("Location", "/x-mesh/gk/releases/tag/v0.42.0")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	c := &Client{HTTP: noFollowClient(), DownloadBase: srv.URL}
	tag, err := c.LatestTag(context.Background())
	if err != nil {
		t.Fatalf("LatestTag: %v", err)
	}
	if tag != "v0.42.0" {
		t.Errorf("tag = %q, want v0.42.0", tag)
	}
}

func TestLatestTagFallsBackToAPIWhenRedirectFails(t *testing.T) {
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// No Location header — redirect resolution must fail and trigger
		// the API fallback.
		w.WriteHeader(http.StatusFound)
	}))
	defer dl.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/repos/x-mesh/gk/releases/latest"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.30.0"}`))
	}))
	defer api.Close()

	c := &Client{HTTP: noFollowClient(), DownloadBase: dl.URL, APIBase: api.URL}
	tag, err := c.LatestTag(context.Background())
	if err != nil {
		t.Fatalf("LatestTag: %v", err)
	}
	if tag != "v0.30.0" {
		t.Errorf("tag = %q, want v0.30.0", tag)
	}
}

func TestLatestTagSurfacesBothErrors(t *testing.T) {
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer dl.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"rate limit"}`))
	}))
	defer api.Close()

	c := &Client{HTTP: noFollowClient(), DownloadBase: dl.URL, APIBase: api.URL}
	_, err := c.LatestTag(context.Background())
	if err == nil {
		t.Fatal("expected combined error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "redirect failed") || !strings.Contains(msg, "api failed") {
		t.Errorf("error = %q, want both redirect+api failure mentioned", msg)
	}
	if !strings.Contains(msg, "403") {
		t.Errorf("error = %q, want 403 from api fallback", msg)
	}
}

func TestLatestTagRejectsLatestSegment(t *testing.T) {
	// If the redirect target accidentally points back at /releases/latest
	// (e.g. a misconfigured proxy), we must not return "latest" as the tag.
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/x-mesh/gk/releases/latest")
		w.WriteHeader(http.StatusFound)
	}))
	defer dl.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.30.0"}`))
	}))
	defer api.Close()

	c := &Client{HTTP: noFollowClient(), DownloadBase: dl.URL, APIBase: api.URL}
	tag, err := c.LatestTag(context.Background())
	if err != nil {
		t.Fatalf("LatestTag: %v", err)
	}
	if tag != "v0.30.0" {
		t.Errorf("tag = %q, want fallback to api v0.30.0", tag)
	}
}

func TestLatestTagAPIDirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if accept := r.Header.Get("Accept"); accept != "application/vnd.github+json" {
			t.Errorf("Accept = %q", accept)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.30.0"}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: noFollowClient(), APIBase: srv.URL}
	tag, err := c.latestTagAPI(context.Background())
	if err != nil {
		t.Fatalf("latestTagAPI: %v", err)
	}
	if tag != "v0.30.0" {
		t.Errorf("tag = %q, want v0.30.0", tag)
	}
}

func TestLatestTagAPIEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: noFollowClient(), APIBase: srv.URL}
	_, err := c.latestTagAPI(context.Background())
	if err == nil {
		t.Fatal("expected error on empty tag_name")
	}
}

func TestAssetURL(t *testing.T) {
	c := &Client{}
	got := c.AssetURL("v0.30.0", "gk_linux_amd64.tar.gz")
	want := "https://github.com/x-mesh/gk/releases/download/v0.30.0/gk_linux_amd64.tar.gz"
	if got != want {
		t.Errorf("AssetURL = %q, want %q", got, want)
	}
}
