package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/initx"
)

// initRemoteName is the remote gk init wires up. Fixed on purpose:
// alternative remote names are a `git remote add` away and keeping a
// single well-known name lets every other gk verb (pull/push/context)
// find the upstream without configuration.
const initRemoteName = "origin"

// remoteFallbackName seeds the project-name prompt when the directory
// basename sanitizes down to nothing (e.g. a purely non-ASCII name).
const remoteFallbackName = "my-project"

// remotePlan captures the origin remote gk init intends to add — the
// counterpart of initx.FilePlan for the one non-file side effect init
// performs. Action is ActionCreate (add) or ActionSkip; Merge/Overwrite
// have no meaning here because init never rewrites an existing remote.
type remotePlan struct {
	RemoteName  string // "origin"
	Alias       string // clone.hosts alias used ("" for direct owner/repo or URL)
	URL         string
	Meta        config.CloneMeta
	Name        string // project name (repo part; may be "" for opaque URLs)
	Action      initx.FileAction
	ExistingURL string // current URL when the remote already exists (Action=Skip)
	Direct      bool   // born from the TUI's direct input → offer the config save
}

// buildRemotePlan resolves a remote spec into a concrete plan. Pure —
// no git, no TTY — so both the flag path and the TUI path share it.
//
// spec forms:
//
//	alias            registered clone.hosts entry; repo = name (required)
//	alias:repo       ResolveURL owner completion
//	alias:owner/repo ResolveURL alias expansion
//	owner/repo       ResolveURL default host/protocol
//	URL / scp URL    passthrough
//
// Unlike `gk clone`, an unknown alias is an error here (with the
// registered aliases as a hint) rather than a passthrough: `git remote
// add` records anything verbatim, so a typo would only surface much
// later as a failing pull. Callers that really want an opaque URL can
// pass a full scheme:// or user@host: form.
func buildRemotePlan(cfg config.CloneConfig, spec, name string, forceSSH, forceHTTPS bool) (*remotePlan, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("remote spec is empty (expected alias, owner/repo, or URL)")
	}

	// Bare alias (`--remote personal`): the repo name comes from `name`.
	if !strings.ContainsAny(spec, ":/@") {
		if _, ok := cfg.Hosts[spec]; !ok {
			return nil, unknownAliasErr(cfg, spec)
		}
		if name == "" {
			return nil, fmt.Errorf("alias %q needs a project name (--name or TUI prompt)", spec)
		}
		url, meta, err := cfg.ResolveURL(spec+":"+name, forceSSH, forceHTTPS)
		if err != nil {
			return nil, err
		}
		return &remotePlan{
			RemoteName: initRemoteName,
			Alias:      spec,
			URL:        url,
			Meta:       meta,
			Name:       meta.Repo,
			Action:     initx.ActionCreate,
		}, nil
	}

	url, meta, err := cfg.ResolveURL(spec, forceSSH, forceHTTPS)
	if err != nil {
		return nil, err
	}
	// ResolveURL passes unknown `alias:...` shorthands through verbatim
	// for git to interpret; for a remote registration that is a typo, not
	// a URL — reject it with the registered aliases as the hint.
	if url == spec && meta == (config.CloneMeta{}) && !isOpaqueRemoteURL(spec) {
		alias := spec[:strings.Index(spec, ":")]
		return nil, unknownAliasErr(cfg, alias)
	}

	plan := &remotePlan{
		RemoteName: initRemoteName,
		URL:        url,
		Meta:       meta,
		Name:       meta.Repo,
		Action:     initx.ActionCreate,
	}
	// When the spec used a registered alias, keep it for the JSON result.
	if colon := strings.Index(spec, ":"); colon > 0 && !strings.ContainsAny(spec[:colon], "/@") {
		if _, ok := cfg.Hosts[spec[:colon]]; ok {
			plan.Alias = spec[:colon]
		}
	}
	return plan, nil
}

// isOpaqueRemoteURL reports whether spec is a form git can consume
// directly without gk expansion — a scheme:// URL or an scp-style
// user@host:path.
func isOpaqueRemoteURL(spec string) bool {
	for _, s := range cloneURLSchemesForInit {
		if strings.HasPrefix(spec, s) {
			return true
		}
	}
	if at := strings.Index(spec, "@"); at > 0 {
		if colon := strings.Index(spec[at:], ":"); colon > 0 {
			return true
		}
	}
	return false
}

// cloneURLSchemesForInit mirrors the scheme list ResolveURL treats as
// passthrough. Kept here (not exported from config) because it is a
// detection detail, not part of the config contract.
var cloneURLSchemesForInit = []string{"http://", "https://", "ssh://", "git://", "file://"}

func unknownAliasErr(cfg config.CloneConfig, alias string) error {
	names := make([]string, 0, len(cfg.Hosts))
	for k := range cfg.Hosts {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("alias %q is not registered (clone.hosts is empty; use owner/repo or a URL, or add the alias to clone.hosts)", alias)
	}
	return fmt.Errorf("alias %q is not registered (available: %s)", alias, strings.Join(names, ", "))
}

// sanitizeRepoName maps an arbitrary directory basename onto GitHub's
// repository-name character set: ASCII letters, digits, `-`, `_`, `.`.
// Whitespace runs become a single `-`; everything else (control chars,
// path separators, non-ASCII) is dropped. Leading/trailing `.`/`-` are
// trimmed (GitHub rejects leading dots), a trailing `.git` is stripped,
// and the result is capped at 100 runes. An input that sanitizes to
// nothing falls back to remoteFallbackName so the prompt is never
// seeded empty.
func sanitizeRepoName(name string) string {
	name = strings.TrimSuffix(name, ".git")

	var b strings.Builder
	lastDash := false
	for _, r := range name {
		var out rune
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '.':
			out = r
		case r == '-', r == ' ', r == '\t':
			out = '-'
		default:
			continue // drop control chars, separators, non-ASCII
		}
		if out == '-' {
			if lastDash {
				continue
			}
			lastDash = true
		} else {
			lastDash = false
		}
		b.WriteRune(out)
	}

	out := strings.Trim(b.String(), "-.")
	if len(out) > 100 {
		out = strings.Trim(out[:100], "-.")
	}
	if out == "" {
		return remoteFallbackName
	}
	return out
}

// planInitRemote decides the remote step for runInit. Flags win; without
// a `--remote` spec the interactive flow (RunInitTUI) owns the decision
// and this returns nil so nothing is planned here. An existing origin
// always short-circuits to a Skip plan — init reports it but never
// rewrites a remote, --force included.
//
// Returned errors are real failures (bad spec, or `--only=remote` with
// no way to proceed); a nil plan with nil error simply means "no remote
// step on this path".
func planInitRemote(ctx context.Context, cfg config.CloneConfig, dir string, gitRunner initx.GitRunner, spec, name string, forceSSH, forceHTTPS, interactive bool, only string) (*remotePlan, error) {
	if gitRunner != nil {
		if remotes, ok := configuredRemotes(ctx, gitRunner); ok && hasRemote(remotes, initRemoteName) {
			return &remotePlan{
				RemoteName:  initRemoteName,
				ExistingURL: remoteURL(ctx, gitRunner, initRemoteName),
				Action:      initx.ActionSkip,
			}, nil
		}
	}

	if spec != "" {
		if name == "" {
			name = sanitizeRepoName(filepath.Base(dir))
		}
		return buildRemotePlan(cfg, spec, name, forceSSH, forceHTTPS)
	}

	if interactive {
		return nil, nil // the TUI step collects the account + name
	}
	if only == "remote" {
		return nil, WithBlocked(
			fmt.Errorf("--only=remote needs a remote spec in non-interactive mode"),
			"init_remote_spec_missing",
			"pass the target explicitly; clone.hosts aliases, owner/repo, and full URLs are accepted",
			errRemedy{Command: selfCmd("init --only=remote --remote <alias|owner/repo|url>"), Safety: "safe"},
		)
	}
	return nil, nil
}

// remoteResultJSON is the `result.remote` fragment of gk init's JSON
// output. Status: added | existing | skipped | dry-run | failed.
type remoteResultJSON struct {
	Status string `json:"status"`
	Name   string `json:"name,omitempty"`
	URL    string `json:"url,omitempty"`
	Alias  string `json:"alias,omitempty"`
	Error  string `json:"error,omitempty"`
}

// executeRemotePlan runs (or reports) the planned remote step after the
// file plans have been written. A remote-add failure degrades to a
// warning — the scaffold already succeeded and the user can rerun with
// `--only=remote` — so the only error-shaped outcome is in the result.
func executeRemotePlan(ctx context.Context, w io.Writer, gitRunner initx.GitRunner, plan *remotePlan, confirmed, dryRun bool) *remoteResultJSON {
	if plan == nil {
		return nil
	}
	res := &remoteResultJSON{Name: plan.Name, URL: plan.URL, Alias: plan.Alias}

	switch {
	case plan.ExistingURL != "":
		res.Status = "existing"
		res.URL = plan.ExistingURL
		res.Name = ""
		fmt.Fprintf(w, "remote: %s → %s (existing)\n", plan.RemoteName, plan.ExistingURL)
	case !confirmed || plan.Action == initx.ActionSkip:
		res.Status = "skipped"
	case dryRun:
		res.Status = "dry-run"
		fmt.Fprintf(w, "(dry-run) would add remote %s → %s\n", plan.RemoteName, plan.URL)
	default:
		if err := addRemote(ctx, gitRunner, plan.RemoteName, plan.URL); err != nil {
			res.Status = "failed"
			res.Error = err.Error()
			fmt.Fprintf(w, "warn: %v (scaffold files are unaffected; retry with %s)\n",
				err, selfCmd("init --only=remote --remote <alias|owner/repo|url>"))
			return res
		}
		res.Status = "added"
		fmt.Fprintln(w, successLine("added", fmt.Sprintf("remote %s → %s", plan.RemoteName, plan.URL)))
		printGHCreateHint(w, plan)
	}
	return res
}

// printGHCreateHint shows the copy-pasteable follow-up for repos that do
// not exist on the host yet. Informational only — creating the remote
// repository is out of scope for init (network, auth, and visibility
// decisions belong to the user / gh CLI).
//
// The hint deliberately does NOT use `gh repo create --source . --push`:
// init has already registered the remote (plan.RemoteName), and
// `--source .` makes gh try to add its own `origin`, which fails with
// "Unable to add remote origin" the instant a remote of that name exists —
// aborting the push it was supposed to do. So we split it: create the bare
// repo, then push over the remote init already set up. (push_create_remote's
// own ghRepoCreate takes the same --private-only approach for the same
// reason.)
func printGHCreateHint(w io.Writer, plan *remotePlan) {
	if plan.Meta.Owner == "" || plan.Meta.Repo == "" {
		return
	}
	fmt.Fprintf(w, "\nIf the repository does not exist on %s yet:\n", plan.Meta.Host)
	fmt.Fprintf(w, "  gh repo create %s/%s --private\n", plan.Meta.Owner, plan.Meta.Repo)
	fmt.Fprintf(w, "  git push -u %s HEAD\n", plan.RemoteName)
}

// validProfileAlias reports whether name is safe as a clone.hosts key.
// The alias travels through SetValue's dot-separated key path AND ends
// up as a YAML mapping key, so anything beyond ASCII letters, digits,
// `-`, `_` (dots, spaces, colons, non-ASCII) is rejected outright —
// a dot in particular would silently split the key path.
func validProfileAlias(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// saveRemoteProfile persists the plan's account as clone.hosts.<alias>
// in the config file at path — one SetValue per field, never a whole-
// file marshal, so the template's comments survive. The protocol is
// derived from the planned URL's shape.
func saveRemoteProfile(path, alias string, plan *remotePlan) error {
	if plan == nil || plan.Meta.Host == "" || plan.Meta.Owner == "" {
		return fmt.Errorf("remote has no structured host/owner to save as a profile")
	}
	proto := "ssh"
	if strings.HasPrefix(plan.URL, "https://") {
		proto = "https"
	}
	for _, kv := range [][2]string{
		{"host", plan.Meta.Host},
		{"owner", plan.Meta.Owner},
		{"protocol", proto},
	} {
		if _, err := config.SetValue(path, "clone.hosts."+alias+"."+kv[0], kv[1]); err != nil {
			return fmt.Errorf("clone.hosts.%s.%s: %w", alias, kv[0], err)
		}
	}
	return nil
}

// addRemote registers name → url in the repository. The caller is
// expected to have planned around an existing remote already (Action=
// Skip); if one appeared in between, git's own "remote origin already
// exists" failure is returned for the caller to surface as a warning —
// init never overwrites or set-urls an existing remote.
func addRemote(ctx context.Context, runner initx.GitRunner, name, url string) error {
	if _, stderr, err := runner.Run(ctx, "remote", "add", name, url); err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git remote add %s: %s", name, msg)
	}
	return nil
}
