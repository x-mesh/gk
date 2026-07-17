// Package github is a minimal, dependency-free client for the slice of the
// GitHub REST API gk needs (currently: listing an owner's repositories for
// `gk clone` with no arguments). It talks to api.github.com directly over
// net/http, so no `gh` binary is required at runtime — only reused, if
// present, as a source of an existing auth token.
package github

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ResolveToken finds a GitHub API token without shelling out to `gh` or
// requiring one to be installed. SSH keys authenticate the git wire
// protocol (clone/push) but not this REST API, so they are not a source
// here — listing repositories needs a bearer token.
//
// Resolution order:
//
//  1. GH_TOKEN / GITHUB_TOKEN env vars — the same names `gh`, GitHub
//     Actions, and most other GitHub tooling read.
//  2. gh's own stored auth (~/.config/gh/hosts.yml, or $GH_CONFIG_DIR) —
//     read as a plain file, not invoked as a binary, so a prior
//     `gh auth login` is reused even where the CLI itself isn't on PATH.
//
// Returns "" when none of the above yield a token; callers fall back to
// an unauthenticated request (public repos only, lower rate limit).
func ResolveToken() string {
	if t := os.Getenv("GH_TOKEN"); t != "" {
		return t
	}
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	return tokenFromGHConfig()
}

// ghHostEntry mirrors the one field of gh's hosts.yml we need. The real
// file carries more (user, git_protocol, users…); yaml.Unmarshal ignores
// keys we don't declare.
type ghHostEntry struct {
	OauthToken string `yaml:"oauth_token"`
}

func tokenFromGHConfig() string {
	dir := os.Getenv("GH_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config", "gh")
	}
	data, err := os.ReadFile(filepath.Join(dir, "hosts.yml"))
	if err != nil {
		return ""
	}
	var hosts map[string]ghHostEntry
	if err := yaml.Unmarshal(data, &hosts); err != nil {
		return ""
	}
	for host, entry := range hosts {
		if strings.EqualFold(host, "github.com") && entry.OauthToken != "" {
			return entry.OauthToken
		}
	}
	return ""
}
