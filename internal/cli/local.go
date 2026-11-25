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
	"github.com/x-mesh/gk/internal/gitstate"
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
			"branch alone cannot answer that.\n\n" +
			"Outside a git repo, gk local checks every repo below the current\n" +
			"directory (depth 2) with the --all rules and lists the ones with\n" +
			"stranded work. --scan and --repos pick the repos from anywhere.\n" +
			"In agent mode (GK_AGENT=1) the implicit scan is off: outside a repo\n" +
			"the command fails with a --scan remedy. --exit-code exits 1 when\n" +
			"anything is stranded.",
		RunE: runLocal,
	}
	cmd.Flags().IntP("limit", "n", 10, "max commits/files to list per section (0 = unlimited)")
	cmd.Flags().Bool("all", false, "scan every local branch and worktree, not just the current branch")
	cmd.Flags().Bool("exit-code", false, "exit 1 when anything is stranded (after printing the result)")
	cmd.Flags().StringSlice("scan", nil, "check every git repo found under these directories (repeatable)")
	cmd.Flags().StringSlice("repos", nil, "check these git repos (repeatable)")
	cmd.Flags().Int("depth", 0, "how deep --scan looks for repos (default: fleet.depth, else 2)")
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
	// Operation names a paused rebase/merge/cherry-pick/revert/bisect in the
	// checked-out worktree ("rebase 2/5"); Resume is how to finish it.
	Operation string `json:"operation,omitempty"`
	Resume    string `json:"resume,omitempty"`
	Clean     bool   `json:"clean"`

	entries []git.StatusEntry
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
	// Detached marks a worktree whose HEAD is on no branch — a rebase in
	// progress, or a plain detached checkout. Its Unpushed counts commits
	// reachable from that HEAD and from no branch or remote.
	Detached  bool   `json:"detached,omitempty"`
	Operation string `json:"operation,omitempty"`
	Resume    string `json:"resume,omitempty"`
	Clean     bool   `json:"clean"`
}

func runLocal(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	set, err := resolveLocalRepos(ctx, cmd)
	if err != nil {
		return err
	}
	if set != nil {
		return runLocalMulti(cmd, set)
	}
	limit, _ := cmd.Flags().GetInt("limit")
	all, _ := cmd.Flags().GetBool("all")

	rep, err := collectLocal(ctx, RepoFlag(), all, limit)
	if err != nil {
		return err
	}
	if JSONOut() {
		if err := emitAgentResult(cmd.OutOrStdout(), rep); err != nil {
			return err
		}
		return localExitCode(cmd, rep.Clean)
	}
	if all {
		renderLocalAll(cmd.OutOrStdout(), rep)
		return localExitCode(cmd, rep.Clean)
	}
	renderLocalBranch(ctx, cmd.OutOrStdout(), &git.ExecRunner{Dir: RepoFlag()}, rep, limit)
	return localExitCode(cmd, rep.Clean)
}

// localExitCode implements --exit-code: after the result is rendered, a
// non-clean result exits 1 without printing anything more.
func localExitCode(cmd *cobra.Command, clean bool) error {
	if on, _ := cmd.Flags().GetBool("exit-code"); on && !clean {
		return &ExitError{Code: 1}
	}
	return nil
}

// collectLocal gathers the local-only state of the repository at dir ("" is
// the current directory): the branch scope by default, every branch and
// worktree when all is set.
func collectLocal(ctx context.Context, dir string, all bool, limit int) (localReport, error) {
	runner := &git.ExecRunner{Dir: dir}
	st, err := git.NewClient(runner).Status(ctx)
	if err != nil {
		return localReport{}, fmt.Errorf("gk local: %w", err)
	}
	g := groupEntries(st.Entries)
	unstaged := len(g.Modified) + len(g.Untracked)
	staged := len(g.Staged)
	conflicts := len(g.Unmerged)

	commits, unpushedOK := unpushedCommits(ctx, runner, limitArg(limit))
	stashN := stashCount(ctx, runner)
	op, resume, opKnown := worktreeOperation(ctx, dir)
	if !opKnown {
		return localReport{}, fmt.Errorf("gk local: cannot read the state of a paused git operation")
	}

	// "clean" requires that we could actually determine push state — with no
	// remote to compare against we cannot assert "nothing local-only."
	clean := unpushedOK && unstaged == 0 && staged == 0 && conflicts == 0 &&
		len(commits) == 0 && stashN == 0 && op == ""

	scope := "branch"
	var branches []localBranchReport
	if all {
		scope = "repo"
		var allKnown bool
		branches, allKnown = collectAllLocal(ctx, runner,
			&localWorktreeState{entries: st.Entries, op: op, resume: resume})
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

	return localReport{
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
		Operation:     op,
		Resume:        resume,
		Clean:         clean,
		entries:       st.Entries,
	}, nil
}

// localDetectFn reads the paused operation of a worktree; a test swaps it to
// make the read fail.
var localDetectFn = gitstate.Detect

// worktreeOperation reports a paused git operation in the worktree at path
// ("" is the current directory) and the command that finishes it. known is
// false when the state could not be read: that is not "no operation", and a
// caller must not call the worktree clean on it.
func worktreeOperation(ctx context.Context, path string) (op, resume string, known bool) {
	st, err := localDetectFn(ctx, path)
	if err != nil || st == nil {
		return "", "", false
	}
	if op = fleetOperationLabel(st); op == "" {
		return "", "", true
	}
	return op, selfCmd("continue"), true
}

// renderLocalBranch prints the default, current-branch view.
func renderLocalBranch(ctx context.Context, w io.Writer, runner *git.ExecRunner, rep localReport, limit int) {
	unstaged, staged, conflicts := rep.Unstaged, rep.Staged, rep.Conflicts
	commits, unpushedOK, stashN, clean := rep.Commits, rep.UnpushedKnown, rep.Stash, rep.Clean

	faint := color.New(color.Faint).SprintFunc()
	bold := color.New(color.Bold).SprintFunc()
	head := faint("LOCAL") + "  " + faint("— only on this machine")
	if rep.Branch != "" {
		head = bold(rep.Branch) + "  " + faint("— local only")
	}
	fmt.Fprintln(w, head)

	if rep.Operation != "" {
		fmt.Fprintf(w, "  %s  %s\n", faint("operation"), localOperationText(rep.Operation, rep.Resume))
	}

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
		for _, e := range committableEntries(rep.entries) {
			if limit > 0 && shown >= limit {
				fmt.Fprintf(w, "      %s\n", faint(fmt.Sprintf("… and %d more", countCommittable(rep.entries)-shown)))
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
}

// renderLocalAll prints the repo-wide view. Only branches with something
// stranded are listed: the question --all answers is "what is stuck
// here", and a roster of clean branches buries the answer.
func renderLocalAll(w io.Writer, rep localReport) {
	faint := color.New(color.Faint).SprintFunc()
	bold := color.New(color.Bold).SprintFunc()
	fmt.Fprintf(w, "%s  %s\n", bold("LOCAL"),
		faint(fmt.Sprintf("— %d branch(es) scanned, repo-wide", len(rep.Branches))))
	renderLocalAllRows(w, rep)
	if rep.Clean {
		fmt.Fprintln(w, faint("  ✓ nothing stranded — every branch is pushed and every worktree clean"))
	} else {
		fmt.Fprintln(w, faint(localPushHint))
	}
}

const localPushHint = "  hint: push what should travel — gk push --set-upstream — before switching machines"

// renderLocalAllRows prints one line per branch or worktree holding
// something stranded, then the stash. Shared by --all and the multi-repo
// view, which prints these rows under each repo's label.
func renderLocalAllRows(w io.Writer, rep localReport) {
	faint := color.New(color.Faint).SprintFunc()
	if !rep.UnpushedKnown {
		fmt.Fprintf(w, "  %s\n", faint("no remote to compare against — push state unknown"))
	}
	for _, b := range rep.Branches {
		if b.Clean {
			continue
		}
		line := fmt.Sprintf("  %-34s %s", b.Branch, strings.Join(localBranchParts(b), faint(" · ")))
		if b.Worktree != "" {
			line += "  " + faint("("+filepath.Base(b.Worktree)+")")
		}
		fmt.Fprintln(w, line)
	}
	if rep.Stash > 0 {
		fmt.Fprintf(w, "  %-34s %s\n", faint("(stash)"), color.YellowString("%d entr(ies)", rep.Stash))
	}
}

// localBranchParts lists what is stranded on one branch row, most urgent
// first: a paused operation blocks everything else on that worktree.
func localBranchParts(b localBranchReport) []string {
	var parts []string
	if b.Operation != "" {
		parts = append(parts, localOperationText(b.Operation, b.Resume))
	}
	if b.Unknown {
		parts = append(parts, color.RedString("state unknown"))
	}
	if b.NoUpstream {
		parts = append(parts, color.YellowString("no upstream"))
	}
	if b.Unpushed > 0 {
		label := "%d unpushed"
		if b.Detached {
			label = "%d on no branch"
		}
		parts = append(parts, color.YellowString(label, b.Unpushed))
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
	return parts
}

func localOperationText(op, resume string) string {
	if resume == "" {
		return color.RedString(op)
	}
	return color.RedString("%s → %s", op, resume)
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
// localWorktreeState is what collectLocal already read for the invoking
// worktree, so collectAllLocal does not run status and the operation probe
// on it a second time.
type localWorktreeState struct {
	entries    []git.StatusEntry
	op, resume string
}

func collectAllLocal(ctx context.Context, runner *git.ExecRunner, current *localWorktreeState) (out []localBranchReport, unpushedKnown bool) {
	branches, err := listLocalBranches(ctx, runner)
	if err != nil {
		return nil, false
	}
	unpushedKnown = anyRemoteTrackingRef(ctx, runner)
	var unpushed map[string]int
	var unpushedErr error
	if unpushedKnown {
		unpushed, unpushedErr = localUnpushedFn(ctx, runner)
	}
	wt := loadSwitchWorktrees(ctx, runner)

	// worktreeState reads one worktree's uncommitted counts and paused
	// operation; unknown is true when either could not be read.
	worktreeState := func(path string) (r localBranchReport) {
		if current != nil && path == wt.current.Path {
			g := groupEntries(current.entries)
			r.Unstaged = len(g.Modified) + len(g.Untracked)
			r.Staged, r.Conflicts = len(g.Staged), len(g.Unmerged)
			r.Operation, r.Resume = current.op, current.resume
			return r
		}
		r.Unstaged, r.Staged, r.Conflicts, r.Unknown = worktreeLocalCounts(ctx, path)
		var opKnown bool
		r.Operation, r.Resume, opKnown = worktreeOperation(ctx, path)
		r.Unknown = r.Unknown || !opKnown
		return r
	}

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
			var r localBranchReport
			if path := holder[b.Name]; path != "" {
				r = worktreeState(path)
				r.Worktree = path
			}
			r.Branch, r.Upstream, r.NoUpstream = b.Name, b.Upstream, b.Upstream == ""
			if unpushedKnown {
				r.Unpushed = unpushedLookup(unpushed, b.Name)
				r.Unknown = r.Unknown || unpushedErr != nil
			}
			r.Clean = localBranchClean(r, unpushedKnown)
			out[i] = r
		}(i, b)
	}
	wg.Wait()

	// A detached worktree holds no branch, so the loop above never visits
	// it — yet a rebase in progress is exactly that: its new commits and
	// its uncommitted edits live on no branch at all.
	var detached []WorktreeEntry
	if wt.current.Detached && wt.current.Path != "" {
		detached = append(detached, wt.current)
	}
	for _, e := range wt.others {
		if e.Detached && !e.Bare {
			detached = append(detached, e)
		}
	}
	for _, e := range detached {
		r := worktreeState(e.Path)
		r.Branch, r.Detached, r.Worktree = "(detached)", true, e.Path
		if !r.Unknown {
			var unknown bool
			r.Unpushed, unknown = detachedOnlyCount(ctx, e.Path)
			r.Unknown = unknown
		}
		r.Clean = localBranchClean(r, unpushedKnown)
		out = append(out, r)
	}
	return out, unpushedKnown
}

func localBranchClean(r localBranchReport, unpushedKnown bool) bool {
	return unpushedKnown && !r.Unknown && r.Operation == "" &&
		r.Unpushed == 0 && r.Unstaged == 0 && r.Staged == 0 && r.Conflicts == 0
}

// detachedOnlyCount counts commits reachable from a detached worktree's HEAD
// and from no branch or remote — work that only that HEAD still points at.
func detachedOnlyCount(ctx context.Context, path string) (n int, unknown bool) {
	out, _, err := (&git.ExecRunner{Dir: path}).Run(ctx,
		"rev-list", "--count", "HEAD", "--not", "--branches", "--remotes")
	if err != nil {
		return 0, true
	}
	n, err = strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, true
	}
	return n, false
}

// localUnpushedFn is unpushedByBranch; a test swaps it to make the read fail.
var localUnpushedFn = unpushedByBranch

// unpushedByBranch counts, for every local branch, the commits it reaches
// that exist on no remote, keyed by full refname. `--not --remotes` (rather
// than @{upstream}) is deliberate: a branch with no upstream still needs an
// answer, and "reachable from some remote ref" is the property that decides
// whether another machine can see the work.
//
// One git call lists every such commit with its parents, and one more lists
// the branch tips; a per-branch rev-list cost one process per branch, which
// dominated a scan over repos with hundreds of branches. The tips come from
// for-each-ref, not from the decorations of the log: decorations follow user
// config such as log.excludeDecoration, and a branch whose tip lost its
// decoration would count 0. The count walks parents inside that set only,
// and that is exact: an ancestor of a commit a remote reaches is reached by
// the remote too, so no unpushed commit hides behind a pushed one. A branch
// absent from the map, or whose tip is outside the set, has no unpushed
// commits.
func unpushedByBranch(ctx context.Context, runner *git.ExecRunner) (map[string]int, error) {
	heads, stderr, err := runner.Run(ctx, "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads/")
	if err != nil {
		return nil, fmt.Errorf("unpushed commits: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	out, stderr, err := runner.Run(ctx, "log", "--no-show-signature", "--branches", "--not", "--remotes",
		"--format=%H %P")
	if err != nil {
		return nil, fmt.Errorf("unpushed commits: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	index := map[string]int{}
	var parents [][]string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		index[f[0]] = len(parents)
		parents = append(parents, f[1:])
	}
	tips := map[string]int{}
	for _, line := range strings.Split(strings.TrimRight(string(heads), "\n"), "\n") {
		hash, ref, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if i, ok := index[hash]; ok {
			tips[ref] = i
		}
	}
	// Parents resolved to indexes once; a parent outside the set is pushed.
	edges := make([][]int, len(parents))
	for i, ps := range parents {
		for _, p := range ps {
			if j, ok := index[p]; ok {
				edges[i] = append(edges[i], j)
			}
		}
	}
	counts := make(map[string]int, len(tips))
	seen := make([]int, len(edges)) // generation marks: no clearing per tip
	stack := make([]int, 0, len(edges))
	gen := 0
	for ref, tip := range tips {
		gen++
		n := 0
		stack = append(stack[:0], tip)
		seen[tip] = gen
		for len(stack) > 0 {
			c := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			n++
			for _, p := range edges[c] {
				if seen[p] != gen {
					seen[p] = gen
					stack = append(stack, p)
				}
			}
		}
		counts[ref] = n
	}
	return counts, nil
}

// unpushedLookup finds a branch from listLocalBranches in unpushedByBranch's
// map. %(refname:short) writes "heads/x" when a tag x also exists.
func unpushedLookup(counts map[string]int, branch string) int {
	if n, ok := counts["refs/heads/"+branch]; ok {
		return n
	}
	return counts["refs/"+branch]
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
