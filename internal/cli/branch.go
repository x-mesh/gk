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

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/branchclean"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
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
	cleanCmd.Flags().Bool("no-ai", false, "disable AI analysis")
	cleanCmd.Flags().Int("stale", 0, "include branches with last commit older than N days")
	cleanCmd.Flags().Bool("all", false, "include merged, gone, stale, and squash-merged branches")
	cleanCmd.Flags().Bool("remote", false, "run git remote prune")
	cleanCmd.Flags().Bool("include-remote", false, "include remote-only branches as candidates (deleted via git push --delete)")
	cleanCmd.Flags().Bool("squash-merged", false, "include squash-merged branches")
	cleanCmd.Flags().BoolP("yes", "y", false, "skip TUI confirmation")

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
	Hash       string // 7-char short commit hash
	Gone       bool   // upstream configured but missing on remote
}

func listLocalBranches(ctx context.Context, r git.Runner) ([]branchInfo, error) {
	stdout, stderr, err := r.Run(ctx,
		"for-each-ref",
		"--format=%(refname:short)%00%(upstream:short)%00%(committerdate:unix)%00%(upstream:track)%00%(objectname:short)",
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
		if len(parts) >= 5 {
			bi.Hash = parts[4]
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
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	cfg, _ := config.Load(cmd.Flags())

	// 플래그 읽기
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")
	goneMode, _ := cmd.Flags().GetBool("gone")
	noAI, _ := cmd.Flags().GetBool("no-ai")
	stale, _ := cmd.Flags().GetInt("stale")
	all, _ := cmd.Flags().GetBool("all")
	remote, _ := cmd.Flags().GetBool("remote")
	includeRemote, _ := cmd.Flags().GetBool("include-remote")
	squashMerged, _ := cmd.Flags().GetBool("squash-merged")
	yes, _ := cmd.Flags().GetBool("yes")

	// --stale 유효성 검사
	if stale < 0 {
		return fmt.Errorf("gk branch clean: invalid --stale value: must be > 0")
	}

	// TTY 감지: non-TTY + !yes + !force + !dryRun → 에러
	isTTY := ui.IsTerminal()
	if !isTTY && !yes && !force && !dryRun {
		return fmt.Errorf("gk branch clean: non-interactive mode requires --yes or --force")
	}

	// CleanOptions 매핑
	opts := branchclean.CleanOptions{
		DryRun:        dryRun,
		Force:         force,
		Yes:           yes,
		NoAI:          noAI,
		Gone:          goneMode,
		Stale:         stale,
		All:           all,
		SquashMerged:  squashMerged,
		Remote:        remote,
		IncludeRemote: includeRemote,
		RemoteName:    cfg.Remote,
		Protected:     cfg.Branch.Protected,
		StaleDays:     cfg.Branch.StaleDays,
		Lang:          cfg.AI.Lang,
	}

	// AI provider 구성
	var prov provider.Provider
	if cfg.AI.Enabled && !noAI {
		p, err := provider.NewProvider(ctx, aiFactoryOptions(cfg))
		if err == nil {
			// BranchAnalyzer type assertion으로 AI 지원 여부 확인
			if _, ok := p.(provider.BranchAnalyzer); ok {
				prov = p
			}
		}
		// provider 생성 실패 시 AI 없이 진행 (graceful)
	}

	cleaner := &branchclean.Cleaner{
		Runner:   runner,
		Client:   client,
		Provider: prov,
		Stderr:   cmd.ErrOrStderr(),
		Stdout:   cmd.OutOrStdout(),
	}

	result, err := cleaner.Run(ctx, opts)
	if err != nil {
		return err
	}

	w := cmd.OutOrStdout()

	// dry-run 결과 출력
	if dryRun {
		if len(result.DryRun) == 0 {
			fmt.Fprintln(w, "no branches to clean")
			return nil
		}
		for _, c := range result.DryRun {
			fmt.Fprintf(w, "would delete: %s\n", c.Name)
		}
		return nil
	}

	// --remote만 단독 실행 시
	if result.Pruned && len(result.Deleted) == 0 && len(result.DryRun) == 0 && len(result.Failed) == 0 {
		return nil
	}

	// TUI 모드: TTY + !yes + candidates가 있는 경우
	// Cleaner.Run이 --yes 모드에서 이미 삭제를 수행했으므로,
	// TUI는 --yes가 아닌 경우에만 필요하다.
	// Cleaner.Run은 !yes일 때 toDelete가 비어있으므로 삭제를 수행하지 않는다.
	// 이 경우 candidates를 다시 빌드하여 TUI를 표시해야 한다.
	if isTTY && !yes && !dryRun {
		// Always pull remote-only candidates so the in-TUI 'i' toggle
		// can reveal them without an extra round-trip to git.
		dryOpts := opts
		dryOpts.DryRun = true
		dryOpts.IncludeRemote = true
		dryResult, err := cleaner.Run(ctx, dryOpts)
		if err != nil {
			return err
		}
		if len(dryResult.DryRun) == 0 {
			fmt.Fprintln(w, "no branches to clean")
			return nil
		}

		selected, err := RunCleanTUI(dryResult.DryRun, includeRemote)
		if err != nil {
			return err
		}
		if len(selected) == 0 {
			fmt.Fprintln(w, "no branches selected")
			return nil
		}

		// 선택된 브랜치 삭제 — local은 git branch -d/-D, remote-only는
		// git push <remote> --delete <name>.
		deleteFlag := "-d"
		if force {
			deleteFlag = "-D"
		}
		for _, key := range selected {
			name, isRemote, remoteName := ParseCandidateKey(key)
			if isRemote {
				if remoteName == "" {
					remoteName = cfg.Remote
					if remoteName == "" {
						remoteName = "origin"
					}
				}
				if _, stderr, derr := runner.Run(ctx, "push", remoteName, "--delete", name); derr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "failed to delete %s/%s: %s\n", remoteName, name, strings.TrimSpace(string(stderr)))
					continue
				}
				fmt.Fprintf(w, "deleted: %s/%s\n", remoteName, name)
				continue
			}
			if _, stderr, derr := runner.Run(ctx, "branch", deleteFlag, name); derr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "failed to delete %s: %s\n", name, strings.TrimSpace(string(stderr)))
				continue
			}
			fmt.Fprintf(w, "deleted: %s\n", name)
		}
		return nil
	}

	// --yes 모드 결과 출력
	if len(result.Deleted) == 0 && len(result.Failed) == 0 {
		fmt.Fprintln(w, "no branches to clean")
		return nil
	}
	for _, name := range result.Deleted {
		fmt.Fprintf(w, "deleted: %s\n", name)
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
