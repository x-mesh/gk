package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/x-mesh/gk/internal/config"
	ghapi "github.com/x-mesh/gk/internal/github"
)

func TestGithubProfileAliasesFiltersToGithubOwners(t *testing.T) {
	cfg := remoteTestCfg() // personal→JINWOO-J (github.com), work→42tape (github.com), legacy→gitlab.com, no owner
	got := githubProfileAliases(cfg)

	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
	if got[0].alias != "personal" || got[0].owner != "JINWOO-J" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].alias != "work" || got[1].owner != "42tape" {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestGithubProfileAliasesEmptyWithNoProfiles(t *testing.T) {
	cfg := config.CloneConfig{
		DefaultHost: "github.com",
		Hosts: map[string]config.HostAlias{
			"gl": {Host: "gitlab.com"}, // no owner
		},
	}
	if got := githubProfileAliases(cfg); len(got) != 0 {
		t.Errorf("githubProfileAliases() = %+v, want empty", got)
	}
}

func TestProfileOwners(t *testing.T) {
	profiles := []hostProfile{{alias: "personal", owner: "JINWOO-J"}, {alias: "work", owner: "x-mesh"}}
	got := profileOwners(profiles)
	if len(got) != 2 || got[0] != "JINWOO-J" || got[1] != "x-mesh" {
		t.Errorf("profileOwners() = %v", got)
	}
}

func TestCloneCandidateItemsMergesAcrossProfilesAndReportsFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/orgs/x-mesh/repos":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]any{
				{"name": "gk", "description": "git kit", "updated_at": "2026-01-02T00:00:00Z", "private": false, "owner": map[string]any{"login": "x-mesh"}},
			})
		case "/orgs/JINWOO-J/repos":
			w.WriteHeader(http.StatusNotFound)
		case "/users/JINWOO-J/repos":
			w.WriteHeader(http.StatusInternalServerError) // force this profile into the warnings path
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := &ghapi.Client{APIBase: srv.URL}
	profiles := []hostProfile{
		{alias: "x-mesh", owner: "x-mesh"},
		{alias: "personal", owner: "JINWOO-J"},
	}

	items, warnings := cloneCandidateItems(context.Background(), client, profiles)

	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1: %+v", len(items), items)
	}
	if items[0].Key != "x-mesh:x-mesh/gk" {
		t.Errorf("items[0].Key = %q, want %q", items[0].Key, "x-mesh:x-mesh/gk")
	}
	if len(warnings) != 1 {
		t.Fatalf("len(warnings) = %d, want 1 (personal's 500 should surface as a warning): %v", len(warnings), warnings)
	}
}

func TestBrowseCloneCandidatesErrorsWithNoProfiles(t *testing.T) {
	cfg := config.CloneConfig{
		DefaultHost: "github.com",
		Hosts: map[string]config.HostAlias{
			"gl": {Host: "gitlab.com"},
		},
	}
	var errOut bytes.Buffer
	_, err := browseCloneCandidates(context.Background(), cfg, &errOut)
	if err == nil {
		t.Fatal("expected error when no github.com profiles are configured")
	}
}
