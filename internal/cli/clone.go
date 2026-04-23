package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
)

var (
	cloneForceSSH   bool
	cloneForceHTTPS bool
)

// Pre-compiled URL scheme list. Any arg starting with `<scheme>://` is
// treated as a fully-qualified URL and handed to git unchanged.
var cloneURLSchemes = []string{"http://", "https://", "ssh://", "git://", "file://"}

func init() {
	cmd := &cobra.Command{
		Use:   "clone <owner/repo | alias:owner/repo | url> [target]",
		Short: "Clone a repository with short-form URL expansion",
		Long: `gk clone expands short-form inputs into a git URL before handing off to
git clone, so you rarely type the full host or protocol:

  gk clone JINWOO-J/playground        # git@github.com:JINWOO-J/playground.git
  gk clone JINWOO-J/playground --https # https://github.com/...
  gk clone gl:group/service            # resolves 'gl' from clone.hosts
  gk clone https://example.com/x/y     # fully-qualified URL is passed through
  gk clone git@host:x/y.git            # SCP-style URL is also passed through

Config keys (in .gk.yaml):

  clone:
    default_protocol: ssh       # or https
    default_host: github.com
    root: ~/work                # optional Go-style layout: <root>/<host>/<owner>/<repo>
    hosts:
      gl: { host: gitlab.com, protocol: ssh }
      work: { host: git.company.internal, protocol: https }
    post_actions: [hooks-install, doctor]

With --dry-run, gk prints the resolved URL + target and exits without
touching the network.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: runClone,
	}
	cmd.Flags().BoolVar(&cloneForceSSH, "ssh", false, "force ssh URL for this invocation (overrides clone.default_protocol)")
	cmd.Flags().BoolVar(&cloneForceHTTPS, "https", false, "force https URL for this invocation (overrides clone.default_protocol)")
	rootCmd.AddCommand(cmd)
}

// cloneMeta carries the resolved pieces used by downstream logic (target
// dir computation, post-action invocation). RepoName is empty for URLs
// that gk cannot parse structurally (e.g., arbitrary ssh://host/path
// strings) — callers fall back to git's own default in that case.
type cloneMeta struct {
	Host  string // "github.com"
	Owner string // "JINWOO-J"
	Repo  string // "playground"
}

func runClone(cmd *cobra.Command, args []string) error {
	if cloneForceSSH && cloneForceHTTPS {
		return errors.New("--ssh and --https are mutually exclusive")
	}

	cfg, _ := config.Load(cmd.Flags())

	spec := args[0]
	explicitTarget := ""
	if len(args) == 2 {
		explicitTarget = args[1]
	}

	url, meta, err := resolveCloneURL(spec, cfg.Clone, cloneForceSSH, cloneForceHTTPS)
	if err != nil {
		return err
	}
	Dbg("clone: spec=%q → url=%s host=%s owner=%s repo=%s", spec, url, meta.Host, meta.Owner, meta.Repo)

	target := computeCloneTarget(cfg.Clone, explicitTarget, meta)
	Dbg("clone: target=%q (explicit=%q root=%q)", target, explicitTarget, cfg.Clone.Root)

	out := cmd.OutOrStdout()
	faint := color.New(color.Faint).SprintFunc()
	bold := color.New(color.Bold).SprintFunc()

	fmt.Fprintf(out, "%s %s\n", faint("url:"), bold(url))
	if target != "" {
		fmt.Fprintf(out, "%s %s\n", faint("into:"), bold(target))
	}

	if DryRun() {
		fmt.Fprintln(out, faint("(dry-run: git clone not executed)"))
		return nil
	}

	gitArgs := []string{"clone", url}
	if target != "" {
		gitArgs = append(gitArgs, target)
	}
	gc := exec.CommandContext(cmd.Context(), "git", gitArgs...)
	gc.Stdout = out
	gc.Stderr = cmd.ErrOrStderr()
	if err := gc.Run(); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}

	// Determine the cloned directory. When target was explicit we use it
	// verbatim; otherwise ask git for the basename it produced.
	clonedDir := target
	if clonedDir == "" {
		clonedDir = inferClonedDir(url)
	}

	return runClonePostActions(cmd, cfg.Clone.PostActions, clonedDir)
}

// resolveCloneURL turns one positional argument into a canonical git URL.
// Dispatch order matters — URL schemes and SCP URLs must passthrough
// before we attempt alias or `owner/repo` expansion, otherwise a
// legitimate ssh URL like `git@host:owner/repo` would be double-parsed.
func resolveCloneURL(spec string, cfg config.CloneConfig, forceSSH, forceHTTPS bool) (string, cloneMeta, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", cloneMeta{}, errors.New("clone spec is empty")
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

	// 3. Alias-prefixed shorthand `alias:owner/repo`.
	if colon := strings.Index(spec, ":"); colon > 0 && !strings.ContainsRune(spec[:colon], '/') {
		aliasKey := spec[:colon]
		rest := spec[colon+1:]
		if cfg.Hosts != nil {
			if alias, ok := cfg.Hosts[aliasKey]; ok {
				owner, repo, err := splitOwnerRepo(rest)
				if err != nil {
					return "", cloneMeta{}, fmt.Errorf("alias %q: %w", aliasKey, err)
				}
				proto := pickProtocol(cfg, alias.Protocol, forceSSH, forceHTTPS)
				host := alias.Host
				if host == "" {
					host = cfg.DefaultHost
				}
				return buildURL(proto, host, owner, repo), cloneMeta{Host: host, Owner: owner, Repo: repo}, nil
			}
		}
		// Colon but unknown alias — fall through; git may still know what
		// to do (e.g., host:port/path). Treat as passthrough.
		return spec, cloneMeta{}, nil
	}

	// 4. Plain `owner/repo`.
	owner, repo, err := splitOwnerRepo(spec)
	if err != nil {
		return "", cloneMeta{}, err
	}
	proto := pickProtocol(cfg, "", forceSSH, forceHTTPS)
	host := cfg.DefaultHost
	if host == "" {
		host = "github.com"
	}
	return buildURL(proto, host, owner, repo), cloneMeta{Host: host, Owner: owner, Repo: repo}, nil
}

// splitOwnerRepo validates `owner/repo` shape. `.git` suffix is tolerated
// and stripped so buildURL can reattach it deterministically.
func splitOwnerRepo(s string) (string, string, error) {
	s = strings.TrimSuffix(s, ".git")
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected <owner>/<repo>, got %q", s)
	}
	return parts[0], parts[1], nil
}

func pickProtocol(cfg config.CloneConfig, aliasProto string, forceSSH, forceHTTPS bool) string {
	switch {
	case forceHTTPS:
		return "https"
	case forceSSH:
		return "ssh"
	case aliasProto != "":
		return aliasProto
	case cfg.DefaultProtocol != "":
		return cfg.DefaultProtocol
	default:
		return "ssh"
	}
}

func buildURL(protocol, host, owner, repo string) string {
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
func parseCloneMetaFromURL(u string) cloneMeta {
	for _, s := range cloneURLSchemes {
		if strings.HasPrefix(u, s) {
			rest := strings.TrimPrefix(u, s)
			// Strip user info if present (user@host/...).
			if at := strings.Index(rest, "@"); at > 0 && at < strings.Index(rest, "/") {
				rest = rest[at+1:]
			}
			slash := strings.Index(rest, "/")
			if slash <= 0 {
				return cloneMeta{}
			}
			host := rest[:slash]
			path := strings.TrimPrefix(rest[slash:], "/")
			path = strings.TrimSuffix(path, ".git")
			owner, repo, err := splitOwnerRepo(path)
			if err != nil {
				return cloneMeta{}
			}
			return cloneMeta{Host: host, Owner: owner, Repo: repo}
		}
	}
	return cloneMeta{}
}

// parseCloneMetaFromSCP extracts host/owner/repo from `user@host:owner/repo(.git)?`.
func parseCloneMetaFromSCP(u string) cloneMeta {
	at := strings.Index(u, "@")
	if at < 0 {
		return cloneMeta{}
	}
	rest := u[at+1:]
	colon := strings.Index(rest, ":")
	if colon <= 0 {
		return cloneMeta{}
	}
	host := rest[:colon]
	path := strings.TrimSuffix(rest[colon+1:], ".git")
	owner, repo, err := splitOwnerRepo(path)
	if err != nil {
		return cloneMeta{}
	}
	return cloneMeta{Host: host, Owner: owner, Repo: repo}
}

// computeCloneTarget decides the on-disk directory for the clone.
//
//   - Explicit positional target wins (caller said exactly where).
//   - clone.root + structured meta → Go-style `<root>/<host>/<owner>/<repo>`.
//   - Otherwise empty, which lets `git clone` default to `./<repo>` in cwd.
//
// When clone.root expansion is requested but we lack structured meta
// (opaque URL passthrough), we fall back to the empty string so git picks.
func computeCloneTarget(cfg config.CloneConfig, explicit string, meta cloneMeta) string {
	if explicit != "" {
		return explicit
	}
	if cfg.Root == "" {
		return ""
	}
	if meta.Host == "" || meta.Owner == "" || meta.Repo == "" {
		return ""
	}
	root := expandHome(cfg.Root)
	return filepath.Join(root, meta.Host, meta.Owner, meta.Repo)
}

// expandHome resolves a leading `~/` against $HOME. Any other form is
// returned unchanged so absolute and relative paths work as written.
func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// inferClonedDir matches git's own directory-naming rule so post-actions
// can cd into the right spot when clone.root is off and no explicit
// target was passed. The convention is: basename of the URL, stripped of
// a trailing `.git`.
func inferClonedDir(url string) string {
	base := url
	if i := strings.LastIndexAny(url, "/:"); i >= 0 {
		base = url[i+1:]
	}
	return strings.TrimSuffix(base, ".git")
}

// runClonePostActions runs configured post-clone gk subcommands inside
// the freshly cloned repo. We shell out via os.Executable() to the same
// gk binary with `--repo <target>` so the commands see the new checkout
// without us having to rewire global flag state in-process.
//
// Unknown action names produce a warning and are skipped — the clone
// itself is the load-bearing operation; diagnostics are a bonus.
func runClonePostActions(cmd *cobra.Command, actions []string, target string) error {
	if len(actions) == 0 {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: cannot locate gk binary for post-actions: %v\n", err)
		return nil
	}
	out := cmd.OutOrStdout()
	faint := color.New(color.Faint).SprintFunc()

	for _, a := range actions {
		var argv []string
		switch a {
		case "hooks-install":
			argv = []string{"--repo", target, "hooks", "install", "--all"}
		case "doctor":
			argv = []string{"--repo", target, "doctor"}
		default:
			fmt.Fprintf(cmd.ErrOrStderr(), "warn: unknown clone.post_actions entry %q (skipped)\n", a)
			continue
		}

		fmt.Fprintf(out, "\n%s %s\n", faint("post-action:"), a)
		pc := exec.CommandContext(cmd.Context(), self, argv...)
		pc.Stdout = out
		pc.Stderr = cmd.ErrOrStderr()
		if err := pc.Run(); err != nil {
			// Post-actions are advisory — surface the error but don't
			// fail the whole clone, which already succeeded.
			fmt.Fprintf(cmd.ErrOrStderr(), "warn: post-action %q failed: %v\n", a, err)
		}
	}
	return nil
}
