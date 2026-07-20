package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:     "local",
		Aliases: []string{"lo"},
		Short:   "Show only what exists locally (uncommitted + unpushed + stash)",
		Long: "Roll up everything that lives only on this machine into one screen:\n" +
			"working-tree changes (unstaged/staged/conflicts), commits not on any\n" +
			"remote (unpushed — uses @{upstream}, falling back to any remote-tracking\n" +
			"ref when no upstream is set), and stash entries.\n\n" +
			"--all widens the scope from the current branch to the whole repository:\n" +
			"every local branch (including ones with no upstream, which no other\n" +
			"machine can see) and every worktree. Use it to answer \"is any work\n" +
			"stranded on this machine\" before switching machines — the current\n" +
			"branch alone cannot answer that.",
		RunE: runLocal,
	}
	cmd.Flags().IntP("limit", "n", 10, "max commits/files to list per section (0 = unlimited)")
	cmd.Flags().Bool("all", false, "scan every local branch and worktree, not just the current branch")
	rootCmd.AddCommand(cmd)
}

// localCommit is one unpushed commit row in --json output.
type localCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Age     string `json:"age"`
}

type localReport struct {
	// Scope names what the counters and Clean describe: "branch" (the
	// default — the checked-out branch only) or "repo" (--all). Callers
	// that gate on Clean must check this, since a branch-scoped clean says
	// nothing about work stranded on other branches.
	Scope         string              `json:"scope"`
	Branch        string              `json:"branch"`
	Upstream      string              `json:"upstream,omitempty"`
	Unstaged      int                 `json:"unstaged"`
	Staged        int                 `json:"staged"`
	Conflicts     int                 `json:"conflicts"`
	Unpushed      int                 `json:"unpushed"`
	UnpushedKnown bool                `json:"unpushed_known"`
	Stash         int                 `json:"stash"`
	Commits       []localCommit       `json:"unpushed_commits,omitempty"`
	Branches      []localBranchReport `json:"branches,omitempty"`
	Clean         bool                `json:"clean"`
}

// localBranchReport is one branch's local-only state under --all.
type localBranchReport struct {
	Branch string `json:"branch"`
	// Upstream is the configured tracking ref; NoUpstream marks a branch
	// that has never been pushed anywhere, which is the case no other
	// machine can discover on its own.
	Upstream   string `json:"upstream,omitempty"`
	NoUpstream bool   `json:"no_upstream,omitempty"`
	Unpushed   int    `json:"unpushed"`
	// Worktree is the path holding this branch, empty when it is not
	// checked out anywhere. Working-tree counts are only meaningful — and
	// only collected — when it is.
	Worktree  string `json:"worktree,omitempty"`
	Unstaged  int    `json:"unstaged,omitempty"`
	Staged    int    `json:"staged,omitempty"`
	Conflicts int    `json:"conflicts,omitempty"`
	// Unknown marks a branch whose state could not be determined (a
	// worktree that is gone, a status that failed). It forces Clean false
	// rather than reporting a reassuring answer we did not verify.
	Unknown bool `json:"unknown,omitempty"`
	Clean   bool `json:"clean"`
}

func runLocal(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	runner := &git.ExecRunner{Dir: RepoFlag()}
	limit, _ := cmd.Flags().GetInt("limit")

	st, err := git.NewClient(runner).Status(ctx)
	if err != nil {
		return fmt.Errorf("gk local: %w", err)
	}
	g := groupEntries(st.Entries)
	unstaged := len(g.Modified) + len(g.Untracked)
	staged := len(g.Staged)
	conflicts := len(g.Unmerged)

	commits, unpushedOK := unpushedCommits(ctx, runner, limitArg(limit))
	stashN := stashCount(ctx, runner)
	all, _ := cmd.Flags().GetBool("all")

	// "clean" requires that we could actually determine push state — with no
	// remote to compare against we cannot assert "nothing local-only."
	clean := unpushedOK && unstaged == 0 && staged == 0 && conflicts == 0 && len(commits) == 0 && stashN == 0

	scope := "branch"
	var branches []localBranchReport
	if all {
		scope = "repo"
		var allKnown bool
		branches, allKnown = collectAllLocal(ctx, runner)
		unpushedOK = allKnown
		// Repo scope: the rollup answers for every branch and worktree, so
		// a clean current branch no longer makes the repo clean. The stash
		// is repo-global and counts here too.
		clean = allKnown && stashN == 0
		for _, b := range branches {
			if !b.Clean {
				clean = false
				break
			}
		}
	}

	if JSONOut() {
		rep := localReport{
			Scope:         scope,
			Branch:        st.Branch,
			Upstream:      st.Upstream,
			Unstaged:      unstaged,
			Staged:        staged,
			Conflicts:     conflicts,
			Unpushed:      len(commits),
			UnpushedKnown: unpushedOK,
			Stash:         stashN,
			Commits:       commits,
			Branches:      branches,
			Clean:         clean,
		}
		return emitAgentResult(cmd.OutOrStdout(), rep)
	}

	if all {
		return renderLocalAll(cmd.OutOrStdout(), branches, stashN, unpushedOK, clean)
	}

	w := cmd.OutOrStdout()
	faint := color.New(color.Faint).SprintFunc()
	bold := color.New(color.Bold).SprintFunc()
	head := faint("LOCAL") + "  " + faint("— only on this machine")
	if st.Branch != "" {
		head = bold(st.Branch) + "  " + faint("— local only")
	}
	fmt.Fprintln(w, head)

	// Working tree.
	wtParts := []string{}
	if unstaged > 0 {
		wtParts = append(wtParts, fmt.Sprintf("%d unstaged", unstaged))
	}
	if staged > 0 {
		wtParts = append(wtParts, color.GreenString("%d staged", staged))
	}
	if conflicts > 0 {
		wtParts = append(wtParts, color.RedString("%d conflicts", conflicts))
	}
	if len(wtParts) == 0 {
		fmt.Fprintf(w, "  %s  %s\n", faint("working tree"), faint("clean"))
	} else {
		fmt.Fprintf(w, "  %s  %s\n", faint("working tree"), strings.Join(wtParts, faint(" · ")))
		shown := 0
		for _, e := range committableEntries(st.Entries) {
			if limit > 0 && shown >= limit {
				fmt.Fprintf(w, "      %s\n", faint(fmt.Sprintf("… and %d more", countCommittable(st.Entries)-shown)))
				break
			}
			fmt.Fprintf(w, "      %s %s\n", localStateGlyph(e), e.Path)
			shown++
		}
	}

	// Unpushed commits.
	switch {
	case !unpushedOK:
		fmt.Fprintf(w, "  %s  %s\n", faint("unpushed"), faint("no remote to compare against"))
	case len(commits) == 0:
		fmt.Fprintf(w, "  %s  %s\n", faint("unpushed"), faint("in sync — nothing local-only"))
	default:
		label := fmt.Sprintf("%d commit", len(commits))
		if len(commits) != 1 {
			label += "s"
		}
		fmt.Fprintf(w, "  %s  %s\n", faint("unpushed"), label)
		for _, c := range commits {
			fmt.Fprintf(w, "      %s %s  %s\n",
				color.YellowString("◇"),
				color.YellowString(c.SHA),
				c.Subject+"  "+faint("("+c.Age+")"))
		}
	}

	// Stash.
	if stashN > 0 {
		if summary := renderStashSummary(ctx, runner); summary != "" {
			fmt.Fprintf(w, "  %s  %s\n", faint("stash"), summary)
		} else {
			fmt.Fprintf(w, "  %s  %d entries\n", faint("stash"), stashN)
		}
	}

	if clean {
		fmt.Fprintln(w, faint("  ✓ nothing local-only — everything is committed and pushed"))
	}
	return nil
}

// renderLocalAll prints the repo-wide view. Only branches with something
// stranded are listed: the question --all answers is "what is stuck
// here", and a roster of clean branches buries the answer.
func renderLocalAll(w io.Writer, branches []localBranchReport, stashN int, unpushedKnown, clean bool) error {
	faint := color.New(color.Faint).SprintFunc()
	bold := color.New(color.Bold).SprintFunc()
	fmt.Fprintf(w, "%s  %s\n", bold("LOCAL"),
		faint(fmt.Sprintf("— %d branch(es) scanned, repo-wide", len(branches))))

	if !unpushedKnown {
		fmt.Fprintf(w, "  %s\n", faint("no remote to compare against — push state unknown"))
	}
	for _, b := range branches {
		if b.Clean {
			continue
		}
		var parts []string
		if b.Unknown {
			parts = append(parts, color.RedString("state unknown"))
		}
		if b.NoUpstream {
			parts = append(parts, color.YellowString("no upstream"))
		}
		if b.Unpushed > 0 {
			parts = append(parts, color.YellowString("%d unpushed", b.Unpushed))
		}
		if b.Unstaged > 0 {
			parts = append(parts, fmt.Sprintf("%d unstaged", b.Unstaged))
		}
		if b.Staged > 0 {
			parts = append(parts, color.GreenString("%d staged", b.Staged))
		}
		if b.Conflicts > 0 {
			parts = append(parts, color.RedString("%d conflicts", b.Conflicts))
		}
		line := fmt.Sprintf("  %-34s %s", b.Branch, strings.Join(parts, faint(" · ")))
		if b.Worktree != "" {
			line += "  " + faint("("+filepath.Base(b.Worktree)+")")
		}
		fmt.Fprintln(w, line)
	}
	if stashN > 0 {
		fmt.Fprintf(w, "  %-34s %s\n", faint("(stash)"), color.YellowString("%d entr(ies)", stashN))
	}

	if clean {
		fmt.Fprintln(w, faint("  ✓ nothing stranded — every branch is pushed and every worktree clean"))
	} else {
		fmt.Fprintln(w, faint("  hint: push what should travel — gk push --set-upstream — before switching machines"))
	}
	return nil
}

// collectAllLocal reports every local branch's local-only state plus the
// worktree holding it. This is what --all exists for: the current branch
// alone cannot answer "is any work stranded on this machine", because a
// branch that was never pushed is invisible from the current branch's
// point of view — and invisible from every other machine's too.
//
// unpushedKnown is false when the repo has no remote-tracking refs at
// all, in which case "not pushed" cannot be distinguished from "nothing
// to push" and no branch may be called clean.
func collectAllLocal(ctx context.Context, runner *git.ExecRunner) (out []localBranchReport, unpushedKnown bool) {
	branches, err := listLocalBranches(ctx, runner)
	if err != nil {
		return nil, false
	}
	unpushedKnown = anyRemoteTrackingRef(ctx, runner)
	wt := loadSwitchWorktrees(ctx, runner)

	// byBranch covers the *other* worktrees; the invoking one is held
	// separately but is just as capable of holding uncommitted work.
	holder := make(map[string]string, len(wt.byBranch)+1)
	for name, e := range wt.byBranch {
		holder[name] = e.Path
	}
	if b := wt.current.Branch; b != "" && wt.current.Path != "" {
		holder[b] = wt.current.Path
	}

	out = make([]localBranchReport, len(branches))
	var wg sync.WaitGroup
	for i, b := range branches {
		wg.Add(1)
		go func(i int, b branchInfo) {
			defer wg.Done()
			r := localBranchReport{
				Branch:     b.Name,
				Upstream:   b.Upstream,
				NoUpstream: b.Upstream == "",
				Worktree:   holder[b.Name],
			}
			if unpushedKnown {
				r.Unpushed = unpushedCountFor(ctx, runner, b.Name)
			}
			if r.Worktree != "" {
				r.Unstaged, r.Staged, r.Conflicts, r.Unknown = worktreeLocalCounts(ctx, r.Worktree)
			}
			r.Clean = unpushedKnown && !r.Unknown &&
				r.Unpushed == 0 && r.Unstaged == 0 && r.Staged == 0 && r.Conflicts == 0
			out[i] = r
		}(i, b)
	}
	wg.Wait()
	return out, unpushedKnown
}

// unpushedCountFor counts commits on branch that exist on no remote.
// `--not --remotes` (rather than @{upstream}) is deliberate: a branch with
// no upstream still needs an answer, and "reachable from some remote ref"
// is the property that decides whether another machine can see the work.
func unpushedCountFor(ctx context.Context, runner *git.ExecRunner, branch string) int {
	out, _, err := runner.Run(ctx, "rev-list", "--count", branch, "--not", "--remotes")
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return n
}

// worktreeLocalCounts counts uncommitted work in one worktree. It does not
// reuse loadWorktreeDirtyStates: that helper serves a picker, so it caps
// itself at 200ms and treats a timeout as clean, and it drops untracked
// files as noise. Both are wrong for a gate — a slow disk must not read as
// "nothing stranded", and a file you wrote but never added is exactly the
// work that would be lost. unknown=true means "could not determine".
func worktreeLocalCounts(ctx context.Context, path string) (unstaged, staged, conflicts int, unknown bool) {
	if _, err := os.Stat(path); err != nil {
		return 0, 0, 0, true
	}
	st, err := git.NewClient(&git.ExecRunner{Dir: path}).Status(ctx)
	if err != nil {
		return 0, 0, 0, true
	}
	g := groupEntries(st.Entries)
	return len(g.Modified) + len(g.Untracked), len(g.Staged), len(g.Unmerged), false
}

// limitArg converts the user's 0-means-unlimited limit into a git -n value:
// returns 0 (no -n flag) when unlimited, else the limit.
func limitArg(limit int) int {
	if limit < 0 {
		return 0
	}
	return limit
}

// unpushedCommits lists commits that exist on no remote. Resolution mirrors
// collectPushedShas / sincePushSuffix: @{upstream} first, then any
// remote-tracking ref (--remotes) when no upstream is configured. ok=false
// means there is no remote to compare against at all.
func unpushedCommits(ctx context.Context, runner *git.ExecRunner, limit int) ([]localCommit, bool) {
	revArgs, ok := unpushedRevArgs(ctx, runner)
	if !ok {
		return nil, false
	}
	args := append([]string{"log"}, revArgs...)
	args = append(args, "--format=%h%x1f%s%x1f%cr")
	if limit > 0 {
		args = append(args, fmt.Sprintf("-n%d", limit))
	}
	out, _, err := runner.Run(ctx, args...)
	if err != nil {
		return nil, false
	}
	var commits []localCommit
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\x1f", 3)
		if len(f) != 3 {
			continue
		}
		commits = append(commits, localCommit{SHA: f[0], Subject: f[1], Age: f[2]})
	}
	return commits, true
}

// unpushedRevArgs returns the rev range that selects local-only commits.
func unpushedRevArgs(ctx context.Context, runner *git.ExecRunner) ([]string, bool) {
	if _, _, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", "@{u}"); err == nil {
		return []string{"@{u}..HEAD"}, true
	}
	if anyRemoteTrackingRef(ctx, runner) {
		return []string{"HEAD", "--not", "--remotes"}, true
	}
	return nil, false
}

// stashCount returns the number of stash entries (0 on error / none).
func stashCount(ctx context.Context, runner *git.ExecRunner) int {
	out, _, err := runner.Run(ctx, "stash", "list", "--format=%gd")
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// countCommittable counts non-submodule entries (the rows `gk local` lists).
func countCommittable(entries []git.StatusEntry) int {
	n := 0
	for _, e := range entries {
		if e.Kind != git.KindSubmodule {
			n++
		}
	}
	return n
}

// localStateGlyph maps an entry's state to a short colored marker for the
// working-tree list, reusing statusEntryState's classification.
func localStateGlyph(e git.StatusEntry) string {
	switch statusEntryState(e) {
	case "staged":
		return color.GreenString("+")
	case "conflict":
		return color.RedString("⚔")
	case "untracked":
		return color.New(color.Faint).Sprint("?")
	default:
		return color.YellowString("~")
	}
}
