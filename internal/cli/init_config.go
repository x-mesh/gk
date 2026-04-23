package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
)

// initConfigCmd handles `gk init config [--force] [--out <path>]`.
//
// Purpose: drop a commented YAML template onto the global config path
// so new users have a single file to edit. By default we write to
// $XDG_CONFIG_HOME/gk/config.yaml (falling back to ~/.config/gk/).
// `--out` redirects; `--force` overwrites an existing file.
//
// Relationship to auto-init: cmd/gk/main.go also calls
// config.EnsureGlobalConfig() on every gk invocation, which creates the
// file silently the first time it's missing. `gk init config` is the
// explicit, discoverable counterpart — useful when users want to
// regenerate, write to a custom path, or learn where the file lives.
func init() {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Scaffold the default ~/.config/gk/config.yaml",
		Long: `Writes a fully-commented YAML template that documents every supported
field (ai, commit, log, status, branch, clone, worktree, …). Intended as
a starting point — edit to taste, uncomment the lines you care about.

Without --out, the file lands at $XDG_CONFIG_HOME/gk/config.yaml (falls
back to ~/.config/gk/config.yaml). Existing files are never overwritten
unless --force is passed.

A silent auto-init runs on every gk invocation, so you usually only
need this command to regenerate or to write a project-local config.
Use --out .gk.yaml inside a repo for the per-repo override file.`,
		RunE: runInitConfig,
	}
	cmd.Flags().Bool("force", false, "overwrite an existing file")
	cmd.Flags().String("out", "", "write to this path instead of the global default")

	// Attach under the existing `gk init` group that `init.go` already
	// registers. Find it so we don't duplicate the parent.
	if parent, _, err := rootCmd.Find([]string{"init"}); err == nil && parent != nil && parent != rootCmd {
		parent.AddCommand(cmd)
	} else {
		// `gk init` isn't registered yet → attach to root for safety.
		// init() order across files is not guaranteed; this branch is a
		// defensive fallback so the command still shows up.
		rootCmd.AddCommand(cmd)
	}
}

func runInitConfig(cmd *cobra.Command, _ []string) error {
	force, _ := cmd.Flags().GetBool("force")
	out, _ := cmd.Flags().GetString("out")

	path := out
	if path == "" {
		path = config.GlobalConfigPath()
	}
	if path == "" {
		return fmt.Errorf("init config: cannot determine target path; pass --out")
	}

	err := config.WriteDefaultConfig(path, force)
	switch {
	case err == nil:
		fmt.Fprintf(cmd.OutOrStdout(), "created: %s\n", path)
		return nil
	case errors.Is(err, config.ErrConfigExists):
		fmt.Fprintf(cmd.OutOrStdout(), "skipped: %s (already exists — pass --force to overwrite)\n", path)
		return nil
	default:
		return err
	}
}
