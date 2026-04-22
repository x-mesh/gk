package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func init() {
	branchCmd := &cobra.Command{
		Use:   "branch",
		Short: "Branch management helpers",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List branches with optional stale/merged/unmerged/gone filters",
		RunE:  runBranchList,
	}
	listCmd.Flags().IntP("stale", "s", 0, "only show branches with last commit older than N days (0 = all)")
	listCmd.Flags().Bool("merged", false, "only show branches merged into base")
	listCmd.Flags().Bool("unmerged", false, "only show branches NOT merged into base")
	listCmd.Flags().Bool("gone", false, "only show branches whose upstream is gone (deleted on remote)")

	cleanCmd := &cobra.Command{
		Use:   "clean",
		Short: "Delete merged or gone-upstream branches (respecting protected list)",
		RunE:  runBranchClean,
	}
	cleanCmd.Flags().Bool("dry-run", false, "show what would be deleted")
	cleanCmd.Flags().Bool("force", false, "use git branch -D")
	cleanCmd.Flags().Bool("gone", false, "target branches whose upstream is gone instead of merged ones")

	pickCmd := &cobra.Command{
		Use:   "pick",
		Short: "Interactively choose a branch to checkout",
		RunE:  runBranchPick,
	}

	branchCmd.AddCommand(listCmd, cleanCmd, pickCmd)
	rootCmd.AddCommand(branchCmd)
}

type branchInfo struct {
	Name       string
	Upstream   string
	LastCommit time.Time
	Gone       bool // upstream configured but missing on remote
}

func listLocalBranches(ctx context.Context, r git.Runner) ([]branchInfo, error) {
	stdout, stderr, err := r.Run(ctx,
		"for-each-ref",
		"--format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track)",
		"refs/heads",
	)
	if err != nil {
		return nil, fmt.Errorf("for-each-ref: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	var out []branchInfo
	for _, line := range strings.Split(strings.TrimRight(string(stdout), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) < 3 {
			continue
		}
		n, _ := strconv.ParseInt(parts[2], 10, 64)
		bi := branchInfo{
			Name: parts[0], Upstream: parts[1],
			LastCommit: time.Unix(n, 0),
		}
		if len(parts) >= 4 && strings.Contains(parts[3], "gone") {
			bi.Gone = true
		}
		out = append(out, bi)
	}
	return out, nil
}

// unmergedBranches returns the set of branch names NOT merged into base.
func unmergedBranches(ctx context.Context, r git.Runner, base string) (map[string]bool, error) {
	stdout, stderr, err := r.Run(ctx, "branch", "--no-merged", base, "--format=%(refname:short)")
	if err != nil {
		return nil, fmt.Errorf("branch --no-merged: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	m := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimRight(string(stdout), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			m[line] = true
		}
	}
	return m, nil
}

func mergedBranches(ctx context.Context, r git.Runner, base string) (map[string]bool, error) {
	stdout, stderr, err := r.Run(ctx, "branch", "--merged", base, "--format=%(refname:short)")
	if err != nil {
		return nil, fmt.Errorf("branch --merged: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	m := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimRight(string(stdout), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			m[line] = true
		}
	}
	return m, nil
}

func runBranchList(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	cfg, _ := config.Load(cmd.Flags())

	stale, _ := cmd.Flags().GetInt("stale")
	onlyMerged, _ := cmd.Flags().GetBool("merged")
	onlyUnmerged, _ := cmd.Flags().GetBool("unmerged")
	onlyGone, _ := cmd.Flags().GetBool("gone")

	if onlyMerged && onlyUnmerged {
		return fmt.Errorf("--merged and --unmerged are mutually exclusive")
	}

	branches, err := listLocalBranches(cmd.Context(), runner)
	if err != nil {
		return err
	}

	var merged, unmerged map[string]bool
	if onlyMerged || onlyUnmerged {
		base, berr := client.DefaultBranch(cmd.Context(), cfg.Remote)
		if berr != nil {
			return fmt.Errorf("could not determine default branch: %w", berr)
		}
		if onlyMerged {
			merged, err = mergedBranches(cmd.Context(), runner, base)
		} else {
			unmerged, err = unmergedBranches(cmd.Context(), runner, base)
		}
		if err != nil {
			return err
		}
	}

	cutoff := time.Now().AddDate(0, 0, -stale)
	w := cmd.OutOrStdout()
	sort.Slice(branches, func(i, j int) bool { return branches[i].Name < branches[j].Name })
	for _, b := range branches {
		if onlyMerged && !merged[b.Name] {
			continue
		}
		if onlyUnmerged && !unmerged[b.Name] {
			continue
		}
		if onlyGone && !b.Gone {
			continue
		}
		if stale > 0 && b.LastCommit.After(cutoff) {
			continue
		}
		fmt.Fprintf(w, "%-40s  %s  %s\n", b.Name, b.Upstream, b.LastCommit.Format("2006-01-02"))
	}
	return nil
}

func runBranchClean(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	cfg, _ := config.Load(cmd.Flags())

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")
	goneMode, _ := cmd.Flags().GetBool("gone")

	// protected set
	protected := map[string]bool{}
	for _, p := range cfg.Branch.Protected {
		protected[p] = true
	}
	// current branch is always protected
	if cur, err := client.CurrentBranch(cmd.Context()); err == nil {
		protected[cur] = true
	}

	var targets []string
	if goneMode {
		branches, err := listLocalBranches(cmd.Context(), runner)
		if err != nil {
			return err
		}
		for _, b := range branches {
			if !b.Gone || protected[b.Name] {
				continue
			}
			targets = append(targets, b.Name)
		}
	} else {
		base, err := client.DefaultBranch(cmd.Context(), cfg.Remote)
		if err != nil {
			return fmt.Errorf("could not determine default branch: %w", err)
		}
		protected[base] = true
		merged, err := mergedBranches(cmd.Context(), runner, base)
		if err != nil {
			return err
		}
		for name := range merged {
			if protected[name] {
				continue
			}
			targets = append(targets, name)
		}
	}
	sort.Strings(targets)

	if len(targets) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no branches to clean")
		return nil
	}

	for _, t := range targets {
		if dryRun {
			fmt.Fprintf(cmd.OutOrStdout(), "would delete: %s\n", t)
			continue
		}
		flag := "-d"
		if force {
			flag = "-D"
		}
		if _, stderr, err := runner.Run(cmd.Context(), "branch", flag, t); err != nil {
			fmt.Fprintf(os.Stderr, "failed to delete %s: %s\n", t, strings.TrimSpace(string(stderr)))
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "deleted: %s\n", t)
	}
	return nil
}

func runBranchPick(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	branches, err := listLocalBranches(cmd.Context(), runner)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(branches))
	for _, b := range branches {
		names = append(names, b.Name)
	}
	sort.Strings(names)

	// fzf if available
	if fzfPath, err := exec.LookPath("fzf"); err == nil {
		return pickWithFzf(cmd.Context(), fzfPath, runner, names, cmd.InOrStdin(), cmd.OutOrStdout())
	}
	// fallback: simple numeric prompt
	picked, err := pickWithPrompt(names, cmd.InOrStdin(), cmd.OutOrStdout())
	if err != nil {
		return err
	}
	_, _, err = runner.Run(cmd.Context(), "checkout", picked)
	return err
}

func pickWithFzf(ctx context.Context, fzfPath string, runner git.Runner, names []string, in io.Reader, out io.Writer) error {
	cmd := exec.CommandContext(ctx, fzfPath)
	cmd.Stdin = strings.NewReader(strings.Join(names, "\n"))
	cmd.Stderr = os.Stderr
	// fzf requires TTY to work properly; when piped out this is best-effort.
	stdout := &strings.Builder{}
	cmd.Stdout = stdout
	if err := cmd.Run(); err != nil {
		return err
	}
	choice := strings.TrimSpace(stdout.String())
	if choice == "" {
		return fmt.Errorf("no selection")
	}
	_, _, err := runner.Run(ctx, "checkout", choice)
	return err
}

func pickWithPrompt(names []string, in io.Reader, out io.Writer) (string, error) {
	for i, n := range names {
		fmt.Fprintf(out, "%2d) %s\n", i+1, n)
	}
	fmt.Fprint(out, "> ")
	s := bufio.NewScanner(in)
	if !s.Scan() {
		return "", fmt.Errorf("no input")
	}
	idx, err := strconv.Atoi(strings.TrimSpace(s.Text()))
	if err != nil || idx < 1 || idx > len(names) {
		return "", fmt.Errorf("invalid selection %q", s.Text())
	}
	return names[idx-1], nil
}
