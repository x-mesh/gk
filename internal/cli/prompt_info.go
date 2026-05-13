package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

Formats:

  plain    (default) Linked-worktree marker. Empty outside any repo and
           in the primary worktree; "wt" when inside a linked worktree
           whose directory name matches the current branch (the common
           case — the branch name is already in the prompt, so repeating
           it as "wt:fix-bug" is noise); "wt:<name>" when the worktree
           directory disagrees with the branch (rare, but worth showing).

  segment  "<repo>/<branch>" when inside any git repo, empty otherwise.
           Designed to replace starship's $directory + $git_branch with
           a single, deduplicated label that always tells you both the
           project and the branch.

  json     Structured payload (linked, repo, name, path, branch) for
           prompt frameworks that compose their own segments.

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
	cmd.Flags().String("format", "plain", "output format: plain|segment|json")
	rootCmd.AddCommand(cmd)
}

// promptInfo is the structured payload returned by `gk prompt-info --format=json`.
// Linked is the load-bearing bit; Name/Path/Branch are populated only when
// inside a linked worktree so callers can render richer segments without
// extra git calls. Repo is populated whenever we're inside any repo (primary
// or linked) so prompts can show a "<repo>/<branch>" segment without an
// extra `git rev-parse` round-trip.
type promptInfo struct {
	Linked bool   `json:"linked"`
	Repo   string `json:"repo,omitempty"`
	Name   string `json:"name,omitempty"`
	Path   string `json:"path,omitempty"`
	Branch string `json:"branch,omitempty"`
}

func runPromptInfo(cmd *cobra.Command, args []string) error {
	format, _ := cmd.Flags().GetString("format")
	runner := &git.ExecRunner{Dir: RepoFlag()}
	info := detectPromptInfo(cmd.Context(), runner)
	return formatPromptInfo(cmd.OutOrStdout(), info, format)
}

// formatPromptInfo renders a detected promptInfo according to the named
// format. Split out from runPromptInfo so tests can drive the format
// logic with a fabricated promptInfo and skip the git plumbing.
func formatPromptInfo(w io.Writer, info promptInfo, format string) error {
	switch format {
	case "json":
		return json.NewEncoder(w).Encode(info)
	case "plain", "":
		if !info.Linked || info.Name == "" {
			return nil
		}
		// When the worktree dir matches the branch (gk's default
		// layout: ~/.gk/worktree/<repo>/<branch>) the branch name is
		// already in the shell prompt next door, so "wt:<name>" just
		// duplicates it. Collapse to "wt" — still unmissable as a
		// linked-worktree marker, without the redundant token.
		if info.Name == info.Branch {
			fmt.Fprintln(w, "wt")
		} else {
			fmt.Fprintln(w, "wt:"+info.Name)
		}
		return nil
	case "segment":
		if info.Repo == "" {
			return nil
		}
		if info.Branch != "" {
			fmt.Fprintln(w, info.Repo+"/"+info.Branch)
		} else {
			fmt.Fprintln(w, info.Repo)
		}
		return nil
	default:
		return fmt.Errorf("unknown format %q (want plain|segment|json)", format)
	}
}

// detectPromptInfo identifies the current worktree's role using the
// minimum git calls needed: --git-dir vs --git-common-dir. Different
// paths mean we're in a linked worktree; same path means primary
// (or single-worktree repo). All git errors collapse to "not in a repo"
// so prompts never see noise.
func detectPromptInfo(ctx context.Context, r git.Runner) promptInfo {
	// --path-format=absolute (git 2.31+) makes both outputs absolute
	// regardless of the runner's working directory, so the equality
	// check below is comparing apples to apples and Repo extraction
	// from common-dir doesn't depend on process cwd.
	gitDirOut, _, err := r.Run(ctx, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return promptInfo{}
	}
	commonDirOut, _, err := r.Run(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return promptInfo{}
	}
	gd := resolveAbs(strings.TrimSpace(string(gitDirOut)))
	cd := resolveAbs(strings.TrimSpace(string(commonDirOut)))
	if gd == "" || cd == "" {
		return promptInfo{}
	}
	repo := repoNameFromCommonDir(cd)
	// Branch is needed for the "segment" format ("<repo>/<branch>") even
	// in the primary worktree, so we fetch it unconditionally. Empty for
	// detached HEAD and brand-new repos with no commits — callers must
	// handle that.
	branchOut, _, _ := r.Run(ctx, "branch", "--show-current")
	branch := strings.TrimSpace(string(branchOut))

	if gd == cd {
		return promptInfo{Linked: false, Repo: repo, Branch: branch}
	}

	topOut, _, _ := r.Run(ctx, "rev-parse", "--show-toplevel")
	top := strings.TrimSpace(string(topOut))

	return promptInfo{
		Linked: true,
		Repo:   repo,
		Name:   filepath.Base(top),
		Path:   top,
		Branch: branch,
	}
}

// repoNameFromCommonDir mirrors the logic in detectRepoName (status_branch.go)
// but works on an already-resolved absolute path. Kept local to avoid an
// extra `git rev-parse --git-common-dir` round-trip — detectPromptInfo
// already has the common-dir output in hand.
func repoNameFromCommonDir(cd string) string {
	if cd == "" {
		return ""
	}
	base := filepath.Base(cd)
	if base == ".git" {
		return filepath.Base(filepath.Dir(cd))
	}
	return strings.TrimSuffix(base, ".git")
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
