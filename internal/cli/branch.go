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

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/branchclean"
	"github.com/x-mesh/gk/internal/branchparent"
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
	cleanCmd.Flags().Bool("worktrees", false, "also delete branches checked out in a worktree (removes the clean worktree first; dirty ones are skipped)")
	cleanCmd.Flags().BoolP("yes", "y", false, "skip TUI confirmation")

	pickCmd := &cobra.Command{
		Use:   "pick",
		Short: "Interactively choose a branch to checkout",
		RunE:  runBranchPick,
	}

	setParentCmd := &cobra.Command{
		Use:   "set-parent <parent>",
		Short: "Record the fork-parent of the current branch",
		Long: `Records ` + "`branch.<current>.gk-parent = <parent>`" + ` so commands like
gk status compare divergence against the actual parent branch instead of
the repository's mainline. Useful for stacked workflows where a feature
branch is forked off another feature branch rather than from main.

Validations applied before write:
  - parent must be a non-empty local branch name
  - parent != current branch (no self-parent)
  - parent must not be a remote-tracking ref (e.g. origin/main)
  - parent must be a real local branch (not a tag, must exist)
  - assigning parent must not create a cycle, and the resulting parent
    chain must be ≤ 10 hops deep

If the named parent does not exist, the closest local branch name is
suggested (Levenshtein-based fuzzy match) so common typos are caught.`,
		Args: cobra.ExactArgs(1),
		RunE: runBranchSetParent,
	}

	unsetParentCmd := &cobra.Command{
		Use:   "unset-parent",
		Short: "Clear the fork-parent metadata of the current branch",
		Long: `Removes ` + "`branch.<current>.gk-parent`" + ` from git config. Idempotent:
running it on a branch with no parent set succeeds silently. Status
output reverts to base-relative divergence on the next invocation.`,
		Args: cobra.NoArgs,
		RunE: runBranchUnsetParent,
	}

	branchCmd.AddCommand(listCmd, cleanCmd, pickCmd, setParentCmd, unsetParentCmd)
	rootCmd.AddCommand(branchCmd)
}

type branchInfo struct {
	Name             string
	Upstream         string
	LastCommit       time.Time
	Hash             string // 7-char short commit hash
	Ahead            int    // commits this branch has that upstream lacks
	Behind           int    // commits upstream has that this branch lacks
	Gone             bool   // upstream configured but missing on remote
	UpstreamInferred bool   // Upstream filled by same-named remote fallback
	ForkBranch       string // base branch of the fork (e.g. "main")
	ForkPoint        string // 7-char short hash of the merge-base with ForkBranch
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
		if len(parts) >= 4 {
			if strings.Contains(parts[3], "gone") {
				bi.Gone = true
			}
			bi.Ahead, bi.Behind = parseUpstreamTrack(parts[3])
		}
		if len(parts) >= 5 {
			bi.Hash = parts[4]
		}
		out = append(out, bi)
	}
	return out, nil
}

// parseUpstreamTrack extracts ahead/behind counts from the
// %(upstream:track) field emitted by `git for-each-ref`. Examples:
//
//	""                       → 0, 0  (synced, or no upstream)
//	"[gone]"                 → 0, 0  (callers also set Gone via Contains)
//	"[ahead 3]"              → 3, 0
//	"[behind 5]"             → 0, 5
//	"[ahead 3, behind 5]"    → 3, 5
func parseUpstreamTrack(s string) (ahead, behind int) {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "gone") {
		return 0, 0
	}
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		var n int
		if _, err := fmt.Sscanf(p, "ahead %d", &n); err == nil {
			ahead = n
			continue
		}
		if _, err := fmt.Sscanf(p, "behind %d", &n); err == nil {
			behind = n
			continue
		}
	}
	return ahead, behind
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
	ctx := cmd.Context()
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

	branches, err := listLocalBranches(ctx, runner)
	if err != nil {
		return err
	}

	var merged, unmerged map[string]bool
	base := ""
	if onlyMerged || onlyUnmerged {
		base, err = client.DefaultBranch(ctx, cfg.Remote)
		if err != nil {
			return fmt.Errorf("could not determine default branch: %w", err)
		}
		if onlyMerged {
			merged, err = mergedBranches(ctx, runner, base)
		} else {
			unmerged, err = unmergedBranches(ctx, runner, base)
		}
		if err != nil {
			return err
		}
	}
	current, _ := client.CurrentBranch(ctx)

	rows := buildBranchListRows(branches, branchListFilter{
		Base:         base,
		Current:      current,
		OnlyMerged:   onlyMerged,
		OnlyUnmerged: onlyUnmerged,
		OnlyGone:     onlyGone,
		Merged:       merged,
		Unmerged:     unmerged,
		Stale:        stale > 0,
		Cutoff:       time.Now().AddDate(0, 0, -stale),
	})

	w := cmd.OutOrStdout()
	var filterNote string
	switch {
	case onlyMerged:
		filterNote = " · merged into " + base
	case onlyUnmerged:
		filterNote = " · not merged into " + base
	}
	if onlyGone {
		filterNote += " · gone upstream"
	}
	summary := fmt.Sprintf("%d %s%s", len(rows), pluralize(len(rows), "branch", "branches"), filterNote)

	fmt.Fprint(w, ui.RenderSection("branches", summary, renderBranchListRows(rows), ui.SectionOpts{
		Layout: ui.SectionLayoutBar,
		Color:  ui.SectionInfo,
	}))
	return nil
}

// branchListRow is one rendered row of `gk branch list`. Ahead/Behind are
// carried as raw counts (not a pre-formatted string) so the renderer can
// derive both the plain form — for column-width measurement — and the
// coloured form (green ↑ / red ↓) from the same source.
type branchListRow struct {
	Current  bool
	Name     string
	Upstream string
	Ahead    int
	Behind   int
	Age      string
}

// branchListFilter carries every knob buildBranchListRows needs, decoupled
// from cobra/git so the filtering + row-shaping logic is unit-testable
// without a real repo. Merged/Unmerged are membership sets keyed by branch
// name (nil when neither --merged nor --unmerged was requested).
type branchListFilter struct {
	Base                     string // "" when --merged/--unmerged wasn't requested
	Current                  string
	OnlyMerged, OnlyUnmerged bool
	OnlyGone                 bool
	Merged, Unmerged         map[string]bool
	Cutoff                   time.Time // zero-stale (stale=0) callers pass a zero-value cutoff filter via Stale
	Stale                    bool      // true when --stale was given (Cutoff should be applied)
}

// buildBranchListRows filters branches and shapes them into display rows.
// Sorted by name. The base branch is always excluded (see the inline
// comment at the call site: it's trivially "merged into base" and pure
// noise in --merged output — the exact complaint that motivated this).
func buildBranchListRows(branches []branchInfo, f branchListFilter) []branchListRow {
	sorted := make([]branchInfo, len(branches))
	copy(sorted, branches)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	rows := make([]branchListRow, 0, len(sorted))
	for _, b := range sorted {
		if f.Base != "" && b.Name == f.Base {
			continue
		}
		if f.OnlyMerged && !f.Merged[b.Name] {
			continue
		}
		if f.OnlyUnmerged && !f.Unmerged[b.Name] {
			continue
		}
		if f.OnlyGone && !b.Gone {
			continue
		}
		if f.Stale && b.LastCommit.After(f.Cutoff) {
			continue
		}
		upstream := b.Upstream
		switch {
		case upstream == "":
			upstream = "-"
		case b.Gone:
			upstream += " (gone)"
		}
		rows = append(rows, branchListRow{
			Current:  b.Name == f.Current,
			Name:     b.Name,
			Upstream: upstream,
			Ahead:    b.Ahead,
			Behind:   b.Behind,
			Age:      ifZeroTime(b.LastCommit),
		})
	}
	return rows
}

// renderBranchListRows formats rows as fixed-width columns, widths computed
// from the actual content (mirrors renderWorktreeRows) so the table reads
// cleanly whether or not every branch has an upstream — the ragged
// alignment a bare `%-40s  %s  %s` produced when some rows had no upstream
// is exactly the bug this fixes. The current branch is starred, matching
// `gk sw` / `gk worktree list`.
func renderBranchListRows(rows []branchListRow) []string {
	if len(rows) == 0 {
		return nil
	}
	wName, wUpstream, wDiff := len("BRANCH"), len("UPSTREAM"), len("DIFF")
	for _, r := range rows {
		if w := runeLen(r.Name); w > wName {
			wName = w
		}
		if w := runeLen(r.Upstream); w > wUpstream {
			wUpstream = w
		}
		// Measure the plain (uncoloured) diff; colorSwitchDiff only adds
		// ANSI, so the visible width matches formatSwitchDiff.
		if w := runeLen(formatSwitchDiff(r.Ahead, r.Behind)); w > wDiff {
			wDiff = w
		}
	}
	faint := color.New(color.Faint).SprintFunc()
	yellow := color.YellowString
	header := fmt.Sprintf("  %s  %s  %s  %s",
		padRight("BRANCH", wName),
		padRight("UPSTREAM", wUpstream),
		padRight("DIFF", wDiff),
		"AGE",
	)
	out := make([]string, 0, len(rows)+1)
	out = append(out, faint(header))
	for _, r := range rows {
		marker := "  "
		nameCell := r.Name
		if r.Current {
			marker = yellow("★") + " "
			nameCell = yellow(r.Name)
		}
		out = append(out, fmt.Sprintf("%s%s  %s  %s  %s",
			marker,
			padRightVisible(nameCell, wName),
			padRight(r.Upstream, wUpstream),
			// colorSwitchDiff carries ANSI, so pad by visible width.
			padRightVisible(colorSwitchDiff(r.Ahead, r.Behind), wDiff),
			r.Age,
		))
	}
	return out
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
	worktrees, _ := cmd.Flags().GetBool("worktrees")
	yes, _ := cmd.Flags().GetBool("yes")

	// --stale 유효성 검사
	if stale < 0 {
		return fmt.Errorf("gk branch clean: invalid --stale value: must be > 0")
	}

	// TTY 감지: non-TTY + !yes + !force + !dryRun → 에러
	isTTY := promptAllowed()
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
		Worktrees:     worktrees,
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
			if c.Worktree != "" {
				if worktrees {
					fmt.Fprintf(w, "would remove worktree %s and delete %s\n", c.Worktree, c.Name)
				} else {
					fmt.Fprintf(w, "skip: %s (checked out in worktree — use --worktrees, or gk wt remove %s)\n", c.Name, c.Name)
				}
				continue
			}
			// protected 브랜치는 --force일 때만 후보에 뜨고, 기본 미선택이라
			// 자동 삭제되지 않는다(TUI에서 직접 체크해야 함).
			if c.Protected {
				fmt.Fprintf(w, "protected: %s (check in the picker to force-delete; not auto-deleted)\n", c.Name)
				continue
			}
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

		// name → worktree path 매핑 (--worktrees 모드에서 worktree를 먼저
		// 제거하기 위해). worktree 점유 브랜치는 --worktrees 없이는 후보로
		// 떠도 미선택이라 여기 도달하지 않는다.
		wtByName := map[string]string{}
		for _, c := range dryResult.DryRun {
			if c.Worktree != "" {
				wtByName[c.Name] = c.Worktree
			}
		}

		// 선택된 브랜치 삭제 — local은 git branch -d/-D, remote-only는
		// git push <remote> --delete <name>.
		deleteFlag := "-d"
		if force {
			deleteFlag = "-D"
		}
		notMergedCount := 0
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
				fmt.Fprintln(w, successLinef("deleted", "%s/%s", remoteName, name))
				continue
			}
			// worktree 점유 브랜치: worktree를 먼저 제거(dirty면 git이 거부 →
			// skip + 경고). 성공해야 아래 branch -d가 통한다.
			if wp := wtByName[name]; wp != "" {
				if werr := branchclean.RemoveWorktreeForBranchDelete(ctx, runner, wp); werr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "skip %s: %v\n", name, werr)
					continue
				}
				fmt.Fprintln(w, successLinef("removed worktree", "%s", wp))
			}
			if _, stderr, derr := runner.Run(ctx, "branch", deleteFlag, name); derr != nil {
				raw := strings.TrimSpace(string(stderr))
				msg, notMerged := branchclean.ClassifyDeleteError(raw)
				fmt.Fprintf(cmd.ErrOrStderr(), "failed to delete %s: %s\n", name, msg)
				if notMerged {
					notMergedCount++
				}
				if h := branchclean.WorktreeHint(name, raw); h != "" {
					fmt.Fprintln(cmd.ErrOrStderr(), h)
				}
				continue
			}
			fmt.Fprintln(w, successLine("deleted", name))
		}
		if !force {
			if h := branchclean.ForceDeleteHint(notMergedCount); h != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), h)
			}
		}
		return nil
	}

	// --yes 모드 결과 출력
	if len(result.Deleted) == 0 && len(result.Failed) == 0 {
		fmt.Fprintln(w, "no branches to clean")
		return nil
	}
	for _, name := range result.Deleted {
		fmt.Fprintln(w, successLine("deleted", name))
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

func runBranchSetParent(cmd *cobra.Command, args []string) error {
	parent := strings.TrimSpace(args[0])
	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)

	current, err := client.CurrentBranch(cmd.Context())
	if err != nil {
		return fmt.Errorf("could not determine current branch: %w", err)
	}
	if current == "" {
		return fmt.Errorf("HEAD is detached; check out a branch first")
	}

	if err := branchparent.ValidateSet(cmd.Context(), client, current, parent); err != nil {
		return err
	}
	if err := branchparent.NewConfig(client).SetParent(cmd.Context(), current, parent); err != nil {
		return fmt.Errorf("failed to set parent: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), successLinef("set parent", "%s → %s", current, parent))
	return nil
}

func runBranchUnsetParent(cmd *cobra.Command, _ []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)

	current, err := client.CurrentBranch(cmd.Context())
	if err != nil {
		return fmt.Errorf("could not determine current branch: %w", err)
	}
	if current == "" {
		return fmt.Errorf("HEAD is detached; check out a branch first")
	}

	if err := branchparent.NewConfig(client).UnsetParent(cmd.Context(), current); err != nil {
		return fmt.Errorf("failed to unset parent: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), successLine("unset parent", current))
	return nil
}
