package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// cloneURLSchemes lists the URL prefixes that mark a spec as fully
// qualified. Any spec starting with one of these is handed to git
// unchanged.
var cloneURLSchemes = []string{"http://", "https://", "ssh://", "git://", "file://"}

// CloneMeta carries the structured pieces of a resolved clone/remote URL.
// Host is always the canonical host (never an ssh_host alias), so path
// layouts like clone.root stay stable regardless of the SSH transport.
// The zero value means the spec was opaque (passthrough URL gk could not
// parse structurally).
type CloneMeta struct {
	Host  string // "github.com"
	Owner string // "JINWOO-J"
	Repo  string // "playground"
}

// ResolveURL turns one spec argument into a canonical git URL using this
// CloneConfig's defaults and host aliases. It backs both `gk clone` and
// `gk init --remote`. Dispatch order matters — URL schemes and SCP URLs
// must passthrough before alias or `owner/repo` expansion, otherwise a
// legitimate ssh URL like `git@host:owner/repo` would be double-parsed.
//
// Supported spec forms:
//
//	https://host/owner/repo(.git)  → passthrough
//	user@host:owner/repo(.git)     → passthrough (SCP style)
//	alias:owner/repo               → clone.hosts lookup
//	alias:repo                     → clone.hosts lookup + owner from the
//	                                 alias's `owner` field (error when unset)
//	owner/repo                     → default_host + default_protocol
func (c CloneConfig) ResolveURL(spec string, forceSSH, forceHTTPS bool) (string, CloneMeta, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", CloneMeta{}, errors.New("clone spec is empty")
	}

	// 1. Full URL with scheme → passthrough, no expansion.
	for _, s := range cloneURLSchemes {
		if strings.HasPrefix(spec, s) {
			return spec, parseCloneMetaFromURL(spec), nil
		}
	}

	// 2. SCP-style `user@host:path` → passthrough.
	if at := strings.Index(spec, "@"); at > 0 {
		if colon := strings.Index(spec[at:], ":"); colon > 0 {
			return spec, parseCloneMetaFromSCP(spec), nil
		}
	}

	// 3. Alias-prefixed shorthand `alias:owner/repo` or `alias:repo`.
	if colon := strings.Index(spec, ":"); colon > 0 && !strings.ContainsRune(spec[:colon], '/') {
		aliasKey := spec[:colon]
		rest := spec[colon+1:]
		if c.Hosts != nil {
			if alias, ok := c.Hosts[aliasKey]; ok {
				owner, repo, err := splitOwnerRepo(rest)
				if err != nil && rest != "" && !strings.Contains(rest, "/") {
					// `alias:repo` — complete the owner from the alias.
					if alias.Owner == "" {
						return "", CloneMeta{}, fmt.Errorf(
							"alias %q has no owner configured; use %s:owner/repo or set clone.hosts.%s.owner",
							aliasKey, aliasKey, aliasKey)
					}
					owner, repo, err = alias.Owner, strings.TrimSuffix(rest, ".git"), nil
				}
				if err != nil {
					return "", CloneMeta{}, fmt.Errorf("alias %q: %w", aliasKey, err)
				}
				proto := c.pickProtocol(alias.Protocol, forceSSH, forceHTTPS)
				host := alias.Host
				if host == "" {
					host = c.DefaultHost
				}
				// ssh_host swaps the transport host (an ~/.ssh/config
				// alias carrying the right key) into ssh URLs only; the
				// meta keeps the canonical host for layout purposes.
				urlHost := host
				if proto != "https" && alias.SSHHost != "" {
					urlHost = alias.SSHHost
				}
				return buildCloneURL(proto, urlHost, owner, repo), CloneMeta{Host: host, Owner: owner, Repo: repo}, nil
			}
		}
		// Colon but unknown alias — fall through; git may still know what
		// to do (e.g., host:port/path). Treat as passthrough.
		return spec, CloneMeta{}, nil
	}

	// 4. Plain `owner/repo`.
	owner, repo, err := splitOwnerRepo(spec)
	if err != nil {
		return "", CloneMeta{}, err
	}
	proto := c.pickProtocol("", forceSSH, forceHTTPS)
	host := c.DefaultHost
	if host == "" {
		host = "github.com"
	}
	return buildCloneURL(proto, host, owner, repo), CloneMeta{Host: host, Owner: owner, Repo: repo}, nil
}

// cloneHostsOrder extracts the clone.hosts alias names from the given
// YAML files in document order — earlier files win position, later files
// only append new names. Load uses it to reconstruct the order viper's
// map decoding discards, so pickers can present profiles the way the
// user wrote them. Keys are lowercased to match viper's case-folded map
// keys. Order is advisory: unreadable or malformed files contribute
// nothing, never an error.
func cloneHostsOrder(paths ...string) []string {
	seen := map[string]bool{}
	var order []string
	for _, p := range paths {
		for _, k := range yamlMappingKeys(p, "clone", "hosts") {
			k = strings.ToLower(k)
			if !seen[k] {
				seen[k] = true
				order = append(order, k)
			}
		}
	}
	return order
}

// yamlMappingKeys returns the keys of the mapping reached by walking
// `path` from the document root of the YAML file, in document order.
// Any miss (missing file, parse error, non-mapping node) yields nil.
func yamlMappingKeys(file string, path ...string) []string {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	node := doc.Content[0]
	for _, seg := range path {
		if node == nil || node.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == seg {
				next = node.Content[i+1]
				break
			}
		}
		node = next
	}
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	keys := make([]string, 0, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		keys = append(keys, node.Content[i].Value)
	}
	return keys
}

// splitOwnerRepo validates `owner/repo` shape. `.git` suffix is tolerated
// and stripped so buildCloneURL can reattach it deterministically.
func splitOwnerRepo(s string) (string, string, error) {
	s = strings.TrimSuffix(s, ".git")
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected <owner>/<repo>, got %q", s)
	}
	return parts[0], parts[1], nil
}

// pickProtocol resolves the effective protocol: one-shot force flags win,
// then the alias's own protocol, then the configured default, then ssh.
func (c CloneConfig) pickProtocol(aliasProto string, forceSSH, forceHTTPS bool) string {
	switch {
	case forceHTTPS:
		return "https"
	case forceSSH:
		return "ssh"
	case aliasProto != "":
		return aliasProto
	case c.DefaultProtocol != "":
		return c.DefaultProtocol
	default:
		return "ssh"
	}
}

func buildCloneURL(protocol, host, owner, repo string) string {
	switch strings.ToLower(protocol) {
	case "https":
		return fmt.Sprintf("https://%s/%s/%s.git", host, owner, repo)
	default: // ssh
		return fmt.Sprintf("git@%s:%s/%s.git", host, owner, repo)
	}
}

// parseCloneMetaFromURL pulls host/owner/repo out of `https://host/owner/repo(.git)?`
// so clone.root and post-actions can operate on the structured view.
// Returns a zero value when the path does not look like `/owner/repo`.
func parseCloneMetaFromURL(u string) CloneMeta {
	for _, s := range cloneURLSchemes {
		if strings.HasPrefix(u, s) {
			rest := strings.TrimPrefix(u, s)
			// Strip user info if present (user@host/...).
			if at := strings.Index(rest, "@"); at > 0 && at < strings.Index(rest, "/") {
				rest = rest[at+1:]
			}
			slash := strings.Index(rest, "/")
			if slash <= 0 {
				return CloneMeta{}
			}
			host := rest[:slash]
			path := strings.TrimPrefix(rest[slash:], "/")
			path = strings.TrimSuffix(path, ".git")
			owner, repo, err := splitOwnerRepo(path)
			if err != nil {
				return CloneMeta{}
			}
			return CloneMeta{Host: host, Owner: owner, Repo: repo}
		}
	}
	return CloneMeta{}
}

// parseCloneMetaFromSCP extracts host/owner/repo from `user@host:owner/repo(.git)?`.
func parseCloneMetaFromSCP(u string) CloneMeta {
	at := strings.Index(u, "@")
	if at < 0 {
		return CloneMeta{}
	}
	rest := u[at+1:]
	colon := strings.Index(rest, ":")
	if colon <= 0 {
		return CloneMeta{}
	}
	host := rest[:colon]
	path := strings.TrimSuffix(rest[colon+1:], ".git")
	owner, repo, err := splitOwnerRepo(path)
	if err != nil {
		return CloneMeta{}
	}
	return CloneMeta{Host: host, Owner: owner, Repo: repo}
}
