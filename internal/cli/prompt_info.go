package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:   "prompt-info",
		Short: "Emit a compact worktree indicator for shell prompts",
		Long: `Emit a compact indicator describing the current worktree, intended
for shell prompt integration.

Default (plain) output is empty when running outside a repo or inside the
primary worktree — keeping prompts clean in the common case — and prints
"wt:<name>" when inside a linked worktree, where <name> is the worktree's
directory basename.

Use --format=json for structured output suitable for prompt frameworks
like p10k or starship that consume external segments.

Detection uses git rev-parse --git-dir vs --git-common-dir; a mismatch
means we're in a linked worktree. This is much faster than enumerating
all worktrees, so it's safe to call from a prompt that re-renders on
every keystroke.

Examples:

  # zsh prompt segment — show a marker only in linked worktrees
  function gk_wt() {
      local info=$(gk prompt-info 2>/dev/null)
      [[ -n "$info" ]] && print -n " %F{yellow}⎇ $info%f"
  }

  # starship: configure a custom command segment
  [custom.gk_worktree]
  command = "gk prompt-info"
  when = "gk prompt-info"`,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE:          runPromptInfo,
	}
	cmd.Flags().String("format", "plain", "output format: plain|json")
	rootCmd.AddCommand(cmd)
}

// promptInfo is the structured payload returned by `gk prompt-info --format=json`.
// Linked is the load-bearing bit; Name/Path/Branch are populated only when
// inside a linked worktree so callers can render richer segments without
// extra git calls.
type promptInfo struct {
	Linked bool   `json:"linked"`
	Name   string `json:"name,omitempty"`
	Path   string `json:"path,omitempty"`
	Branch string `json:"branch,omitempty"`
}

func runPromptInfo(cmd *cobra.Command, args []string) error {
	format, _ := cmd.Flags().GetString("format")
	runner := &git.ExecRunner{Dir: RepoFlag()}
	info := detectPromptInfo(cmd.Context(), runner)
	w := cmd.OutOrStdout()

	switch format {
	case "json":
		return json.NewEncoder(w).Encode(info)
	case "plain", "":
		if info.Linked && info.Name != "" {
			fmt.Fprintln(w, "wt:"+info.Name)
		}
		return nil
	default:
		return fmt.Errorf("unknown format %q (want plain|json)", format)
	}
}

// detectPromptInfo identifies the current worktree's role using the
// minimum git calls needed: --git-dir vs --git-common-dir. Different
// paths mean we're in a linked worktree; same path means primary
// (or single-worktree repo). All git errors collapse to "not in a repo"
// so prompts never see noise.
func detectPromptInfo(ctx context.Context, r git.Runner) promptInfo {
	gitDirOut, _, err := r.Run(ctx, "rev-parse", "--git-dir")
	if err != nil {
		return promptInfo{}
	}
	commonDirOut, _, err := r.Run(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return promptInfo{}
	}
	gd := resolveAbs(strings.TrimSpace(string(gitDirOut)))
	cd := resolveAbs(strings.TrimSpace(string(commonDirOut)))
	if gd == "" || cd == "" || gd == cd {
		return promptInfo{Linked: false}
	}

	topOut, _, _ := r.Run(ctx, "rev-parse", "--show-toplevel")
	top := strings.TrimSpace(string(topOut))
	branchOut, _, _ := r.Run(ctx, "branch", "--show-current")
	branch := strings.TrimSpace(string(branchOut))

	return promptInfo{
		Linked: true,
		Name:   filepath.Base(top),
		Path:   top,
		Branch: branch,
	}
}

// resolveAbs normalizes git's mixed relative/absolute path output so
// the --git-dir vs --git-common-dir comparison isn't tripped up by
// "." or relative-from-cwd shenanigans.
func resolveAbs(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}
