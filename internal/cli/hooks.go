package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

// hooksManagedMarker tags hooks written by gk so we can safely overwrite them
// without clobbering a user's custom hook. Any hook missing this line is
// treated as foreign and requires --force to replace.
const hooksManagedMarker = "# managed by gk — edit via `gk hooks install --force` or remove via `gk hooks uninstall`"

// hookSpec describes a single hook that gk knows how to install.
type hookSpec struct {
	Name        string // git hook filename (commit-msg, pre-push, ...)
	Description string
	Script      string // POSIX shell body (without shebang / marker)
}

// knownHooks lists every hook gk can install. Keep the slice order stable —
// hooks are written in declaration order and the flag names match Name exactly.
func knownHooks() []hookSpec {
	return []hookSpec{
		{
			Name:        "pre-commit",
			Description: "run gk guard policy rules (secret scan, …) before every commit",
			Script:      `exec gk guard check`,
		},
		{
			Name:        "commit-msg",
			Description: "lint commit message against Conventional Commits rules",
			Script:      `exec gk lint-commit --file "$1"`,
		},
		{
			Name:        "pre-push",
			Description: "run the configured gk preflight check sequence",
			Script:      `exec gk preflight`,
		},
	}
}

func init() {
	hooks := &cobra.Command{
		Use:   "hooks",
		Short: "Manage git hook shims that invoke gk",
		Long: `Install or remove shim scripts under .git/hooks/ that invoke gk.

  pre-commit  → gk guard check   (policy rules: secrets, size, …)
  commit-msg  → gk lint-commit   (Conventional Commits linting)
  pre-push    → gk preflight     (configurable preflight sequence)

gk-managed hooks carry a marker comment. The installer refuses to overwrite
any hook missing that marker unless --force is passed (which also writes a
timestamped .bak backup).`,
	}

	install := &cobra.Command{
		Use:   "install",
		Short: "Write shim scripts into .git/hooks/",
		RunE:  runHooksInstall,
	}
	install.Flags().Bool("pre-commit", false, "install the pre-commit hook (gk guard check)")
	install.Flags().Bool("commit-msg", false, "install the commit-msg hook")
	install.Flags().Bool("pre-push", false, "install the pre-push hook")
	install.Flags().Bool("all", false, "install every hook gk knows about")
	install.Flags().Bool("force", false, "overwrite non-gk hooks (writes a .bak backup first)")
	hooks.AddCommand(install)

	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove gk-managed hook shims from .git/hooks/",
		RunE:  runHooksUninstall,
	}
	uninstall.Flags().Bool("pre-commit", false, "remove the pre-commit hook")
	uninstall.Flags().Bool("commit-msg", false, "remove the commit-msg hook")
	uninstall.Flags().Bool("pre-push", false, "remove the pre-push hook")
	uninstall.Flags().Bool("all", false, "remove every gk-managed hook")
	hooks.AddCommand(uninstall)

	rootCmd.AddCommand(hooks)
}

func runHooksInstall(cmd *cobra.Command, _ []string) error {
	selected, err := selectHooks(cmd)
	if err != nil {
		return err
	}
	force, _ := cmd.Flags().GetBool("force")

	hooksDir, err := resolveHooksDir(cmd.Context())
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(hooksDir, 0o755); mkErr != nil {
		return fmt.Errorf("create hooks dir: %w", mkErr)
	}

	w := cmd.OutOrStdout()
	for _, spec := range selected {
		path := filepath.Join(hooksDir, spec.Name)
		action, writeErr := writeHook(path, spec, force)
		if writeErr != nil {
			return fmt.Errorf("%s: %w", spec.Name, writeErr)
		}
		fmt.Fprintf(w, "%s %s  (%s)\n", action, spec.Name, path)
	}
	return nil
}

func runHooksUninstall(cmd *cobra.Command, _ []string) error {
	selected, err := selectHooks(cmd)
	if err != nil {
		return err
	}
	hooksDir, err := resolveHooksDir(cmd.Context())
	if err != nil {
		return err
	}

	w := cmd.OutOrStdout()
	for _, spec := range selected {
		path := filepath.Join(hooksDir, spec.Name)
		status, rmErr := removeHook(path)
		if rmErr != nil {
			return fmt.Errorf("%s: %w", spec.Name, rmErr)
		}
		fmt.Fprintf(w, "%s %s  (%s)\n", status, spec.Name, path)
	}
	return nil
}

// selectHooks resolves --<hook-name>/--all flags into a concrete slice.
// Flag names match hookSpec.Name exactly so adding a new hook only requires
// registering the flag and appending to knownHooks().
func selectHooks(cmd *cobra.Command) ([]hookSpec, error) {
	all, _ := cmd.Flags().GetBool("all")

	anyExplicit := false
	picked := make([]hookSpec, 0, len(knownHooks()))
	for _, h := range knownHooks() {
		flagVal, _ := cmd.Flags().GetBool(h.Name)
		if flagVal {
			anyExplicit = true
		}
		if all || flagVal {
			picked = append(picked, h)
		}
	}

	if !all && !anyExplicit {
		names := make([]string, 0, len(knownHooks()))
		for _, h := range knownHooks() {
			names = append(names, "--"+h.Name)
		}
		return nil, fmt.Errorf("select at least one hook (%s, or --all)", strings.Join(names, ", "))
	}
	return picked, nil
}

// resolveHooksDir returns .git/hooks (or the configured hooksPath) for the
// current repo. Works for worktrees — uses --git-common-dir.
func resolveHooksDir(ctx context.Context) (string, error) {
	runner := &git.ExecRunner{Dir: RepoFlag()}

	// Prefer `core.hooksPath` when configured.
	if out, _, err := runner.Run(ctx, "config", "--get", "core.hooksPath"); err == nil {
		p := strings.TrimSpace(string(out))
		if p != "" {
			return absPath(p), nil
		}
	}

	out, _, err := runner.Run(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		if repo := RepoFlag(); repo != "" {
			gitDir = filepath.Join(repo, gitDir)
		}
	}
	return filepath.Join(gitDir, "hooks"), nil
}

// writeHook writes `spec` to `path`, returning a human-readable action verb.
//
// States:
//   - missing → write, return "installed"
//   - gk-managed exists → overwrite in-place, return "updated"
//   - foreign exists + !force → refuse with actionable error
//   - foreign exists + force → back up to <path>.bak.<unix>, then write, return "replaced"
func writeHook(path string, spec hookSpec, force bool) (string, error) {
	action := "installed"
	if existing, err := os.ReadFile(path); err == nil {
		if isGkManaged(existing) {
			action = "updated"
		} else if !force {
			return "", fmt.Errorf("hook exists and is not managed by gk — re-run with --force to overwrite (existing file will be backed up)")
		} else {
			backup := fmt.Sprintf("%s.bak.%d", path, time.Now().Unix())
			if err := os.WriteFile(backup, existing, 0o755); err != nil {
				return "", fmt.Errorf("write backup %s: %w", backup, err)
			}
			action = "replaced (backup: " + filepath.Base(backup) + ")"
		}
	}

	body := renderHook(spec)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		return "", fmt.Errorf("write hook: %w", err)
	}
	return action, nil
}

// removeHook deletes a gk-managed hook. Refuses to remove foreign hooks.
func removeHook(path string) (string, error) {
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "not-installed", nil
		}
		return "", fmt.Errorf("read hook: %w", err)
	}
	if !isGkManaged(existing) {
		return "", fmt.Errorf("hook is not managed by gk — refusing to delete; remove %s manually if needed", path)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("remove hook: %w", err)
	}
	return "removed", nil
}

// renderHook produces the shell body for a hookSpec.
func renderHook(spec hookSpec) string {
	return fmt.Sprintf(`#!/bin/sh
%s
# purpose: %s
%s
`, hooksManagedMarker, spec.Description, spec.Script)
}

// isGkManaged tests whether the hook body contains the gk marker line.
func isGkManaged(body []byte) bool {
	return strings.Contains(string(body), hooksManagedMarker)
}

// absPath normalizes a path against RepoFlag() when it is relative.
func absPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if repo := RepoFlag(); repo != "" {
		return filepath.Join(repo, p)
	}
	return p
}
