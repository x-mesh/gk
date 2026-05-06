package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLatestTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/repos/x-mesh/gk/releases/latest"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if accept := r.Header.Get("Accept"); accept != "application/vnd.github+json" {
			t.Errorf("Accept = %q", accept)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.30.0","name":"v0.30.0"}`))
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	tag, err := c.LatestTag(context.Background())
	if err != nil {
		t.Fatalf("LatestTag: %v", err)
	}
	if tag != "v0.30.0" {
		t.Errorf("tag = %q, want v0.30.0", tag)
	}
}

func TestLatestTagHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"rate limit"}`))
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	_, err := c.LatestTag(context.Background())
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %q, want it to mention 403", err)
	}
}

func TestLatestTagEmptyTagName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := &Client{APIBase: srv.URL}
	_, err := c.LatestTag(context.Background())
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
