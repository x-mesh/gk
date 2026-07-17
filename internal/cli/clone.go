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
	"github.com/x-mesh/gk/internal/ui"
)

var (
	cloneForceSSH   bool
	cloneForceHTTPS bool
)

// cloneMeta aliases the structured URL parts shared with `gk init`;
// the resolution logic itself lives in config.CloneConfig.ResolveURL.
type cloneMeta = config.CloneMeta

func init() {
	cmd := &cobra.Command{
		Use:   "clone [owner/repo | alias:owner/repo | url] [target]",
		Short: "Clone a repository with short-form URL expansion",
		Long: `gk clone expands short-form inputs into a git URL before handing off to
git clone, so you rarely type the full host or protocol:

  gk clone JINWOO-J/playground        # git@github.com:JINWOO-J/playground.git
  gk clone JINWOO-J/playground --https # https://github.com/...
  gk clone gl:group/service            # resolves 'gl' from clone.hosts
  gk clone https://example.com/x/y     # fully-qualified URL is passed through
  gk clone git@host:x/y.git            # SCP-style URL is also passed through

With no arguments, gk lists the repos under every clone.hosts profile that
has an owner set (github.com only — it talks to api.github.com directly,
no gh binary required) and lets you pick one interactively:

  gk clone                             # browse + pick from configured profiles

It looks for a token in GH_TOKEN/GITHUB_TOKEN, then in gh's own stored
auth (~/.config/gh/hosts.yml) if you've run 'gh auth login' before; with
none of those it falls back to unauthenticated (public repos only).

Config keys (in .gk.yaml):

  clone:
    default_protocol: ssh       # or https
    default_host: github.com
    root: ~/work                # optional Go-style layout: <root>/<host>/<owner>/<repo>
    hosts:
      gl: { host: gitlab.com, protocol: ssh }
      work: { host: git.company.internal, protocol: https, owner: myorg }
    post_actions: [hooks-install, doctor]

With --dry-run, gk prints the resolved URL + target and exits without
touching the network.`,
		Args: cobra.RangeArgs(0, 2),
		RunE: runClone,
	}
	cmd.Flags().BoolVar(&cloneForceSSH, "ssh", false, "force ssh URL for this invocation (overrides clone.default_protocol)")
	cmd.Flags().BoolVar(&cloneForceHTTPS, "https", false, "force https URL for this invocation (overrides clone.default_protocol)")
	rootCmd.AddCommand(cmd)
}

func runClone(cmd *cobra.Command, args []string) error {
	if cloneForceSSH && cloneForceHTTPS {
		return errors.New("--ssh and --https are mutually exclusive")
	}

	cfg, _ := config.Load(cmd.Flags())

	var spec, explicitTarget string
	switch len(args) {
	case 0:
		picked, err := browseCloneCandidates(cmd.Context(), cfg.Clone, cmd.ErrOrStderr())
		if errors.Is(err, ui.ErrPickerAborted) {
			fmt.Fprintln(cmd.OutOrStdout(), "cancelled")
			return nil
		}
		if err != nil {
			return err
		}
		spec = picked
	case 1:
		spec = args[0]
	default:
		spec = args[0]
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
// Thin wrapper over config.CloneConfig.ResolveURL, kept so clone-side
// callers read naturally and the flag plumbing stays in one place.
func resolveCloneURL(spec string, cfg config.CloneConfig, forceSSH, forceHTTPS bool) (string, cloneMeta, error) {
	return cfg.ResolveURL(spec, forceSSH, forceHTTPS)
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
