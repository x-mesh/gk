package cli

import (
	"context"
	"errors"
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

// knownHooks lists every hook gk can install. Keep the map iteration-order
// stable by using a slice.
func knownHooks() []hookSpec {
	return []hookSpec{
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
		Long: `Install or remove shim scripts under .git/hooks/ that invoke gk
(e.g., commit-msg → gk lint-commit, pre-push → gk preflight).

gk-managed hooks carry a marker comment. The installer refuses to overwrite
any hook missing that marker unless --force is passed (which also writes a
timestamped .bak backup).

Available hooks: commit-msg, pre-push`,
	}

	install := &cobra.Command{
		Use:   "install",
		Short: "Write shim scripts into .git/hooks/",
		RunE:  runHooksInstall,
	}
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

// selectHooks resolves --commit-msg/--pre-push/--all into a concrete slice.
// Returns an error when nothing was selected.
func selectHooks(cmd *cobra.Command) ([]hookSpec, error) {
	all, _ := cmd.Flags().GetBool("all")
	commitMsg, _ := cmd.Flags().GetBool("commit-msg")
	prePush, _ := cmd.Flags().GetBool("pre-push")

	if !all && !commitMsg && !prePush {
		return nil, errors.New("select at least one hook (--commit-msg, --pre-push, or --all)")
	}

	picked := make([]hookSpec, 0, 2)
	for _, h := range knownHooks() {
		if all || (h.Name == "commit-msg" && commitMsg) || (h.Name == "pre-push" && prePush) {
			picked = append(picked, h)
		}
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

