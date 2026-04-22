package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

func init() {
	wt := &cobra.Command{
		Use:     "worktree",
		Aliases: []string{"wt"},
		Short:   "Worktree management helpers",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List worktrees (table or --json)",
		RunE:  runWorktreeList,
	}

	add := &cobra.Command{
		Use:   "add <path> [branch]",
		Short: "Create a worktree at <path> checking out [branch] (or HEAD)",
		Long: `Create a worktree.

Without --new, [branch] must already exist (local or remote-tracking).
With --new (-b), a new branch named [branch] is created from --from (default HEAD).
`,
		Args: cobra.RangeArgs(1, 2),
		RunE: runWorktreeAdd,
	}
	add.Flags().BoolP("new", "b", false, "create a new branch named [branch] at --from")
	add.Flags().String("from", "", "base ref for the new branch (default: HEAD)")
	add.Flags().Bool("detach", false, "detach HEAD in the worktree instead of tracking a branch")

	rm := &cobra.Command{
		Use:   "remove <path>",
		Short: "Remove a worktree",
		Args:  cobra.ExactArgs(1),
		RunE:  runWorktreeRemove,
	}
	rm.Flags().BoolP("force", "f", false, "force remove even when the worktree is dirty or locked")

	prune := &cobra.Command{
		Use:   "prune",
		Short: "Prune worktree administrative records",
		RunE:  runWorktreePrune,
	}

	wt.AddCommand(list, add, rm, prune)
	rootCmd.AddCommand(wt)
}

// WorktreeEntry represents a single row in `gk worktree list --json`.
type WorktreeEntry struct {
	Path     string `json:"path"`
	Head     string `json:"head"`
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached"`
	Bare     bool   `json:"bare"`
	Locked   bool   `json:"locked"`
	Prunable bool   `json:"prunable"`
}

func runWorktreeList(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	stdout, stderr, err := runner.Run(cmd.Context(), "worktree", "list", "--porcelain")
	if err != nil {
		return fmt.Errorf("worktree list: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	entries := parseWorktreePorcelain(string(stdout))

	if JSONOut() {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}

	w := cmd.OutOrStdout()
	for _, e := range entries {
		label := e.Branch
		switch {
		case e.Bare:
			label = "(bare)"
		case e.Detached:
			label = "(detached HEAD)"
		case label == "":
			label = "-"
		}
		marks := ""
		if e.Locked {
			marks += " [locked]"
		}
		if e.Prunable {
			marks += " [prunable]"
		}
		short := e.Head
		if len(short) > 7 {
			short = short[:7]
		}
		fmt.Fprintf(w, "%-40s  %-8s  %s%s\n", e.Path, short, label, marks)
	}
	return nil
}

// parseWorktreePorcelain parses the output of `git worktree list --porcelain`.
// Records are separated by blank lines. Each record contains key/value lines:
//
//	worktree <path>
//	HEAD <sha>
//	branch refs/heads/<name>   (or: "detached" / "bare")
//	locked [reason...]
//	prunable [reason...]
func parseWorktreePorcelain(raw string) []WorktreeEntry {
	var out []WorktreeEntry
	var cur *WorktreeEntry
	flush := func() {
		if cur != nil && cur.Path != "" {
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			flush()
			continue
		}
		if cur == nil {
			cur = &WorktreeEntry{}
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			cur.Path = val
		case "HEAD":
			cur.Head = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached":
			cur.Detached = true
		case "bare":
			cur.Bare = true
		case "locked":
			cur.Locked = true
		case "prunable":
			cur.Prunable = true
		}
	}
	flush()
	return out
}

func runWorktreeAdd(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	path := args[0]
	branch := ""
	if len(args) == 2 {
		branch = args[1]
	}
	newBranch, _ := cmd.Flags().GetBool("new")
	from, _ := cmd.Flags().GetString("from")
	detach, _ := cmd.Flags().GetBool("detach")

	if newBranch && detach {
		return fmt.Errorf("--new and --detach are mutually exclusive")
	}
	if from != "" && !newBranch {
		return fmt.Errorf("--from requires --new")
	}

	gitArgs := []string{"worktree", "add"}
	if detach {
		gitArgs = append(gitArgs, "--detach")
	}
	if newBranch {
		if branch == "" {
			return fmt.Errorf("--new requires a branch name (e.g. gk worktree add <path> <branch> -b)")
		}
		gitArgs = append(gitArgs, "-b", branch)
	}
	gitArgs = append(gitArgs, path)

	if newBranch {
		if from != "" {
			gitArgs = append(gitArgs, from)
		}
	} else if !detach && branch != "" {
		gitArgs = append(gitArgs, branch)
	}

	stdout, stderr, err := runner.Run(ctx, gitArgs...)
	if err != nil {
		return fmt.Errorf("worktree add: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	if len(stdout) > 0 {
		_, _ = w.Write(stdout)
	}
	fmt.Fprintf(w, "added worktree at %s\n", path)
	return nil
}

func runWorktreeRemove(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	force, _ := cmd.Flags().GetBool("force")

	gitArgs := []string{"worktree", "remove"}
	if force {
		gitArgs = append(gitArgs, "--force")
	}
	gitArgs = append(gitArgs, args[0])

	if _, stderr, err := runner.Run(cmd.Context(), gitArgs...); err != nil {
		return fmt.Errorf("worktree remove: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "removed worktree %s\n", args[0])
	return nil
}

func runWorktreePrune(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	stdout, stderr, err := runner.Run(cmd.Context(), "worktree", "prune", "-v")
	if err != nil {
		return fmt.Errorf("worktree prune: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	if len(stdout) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "nothing to prune")
		return nil
	}
	_, _ = cmd.OutOrStdout().Write(stdout)
	return nil
}
