package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/branchparent"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:     "switch [branch]",
		Aliases: []string{"sw"},
		Short:   "Switch branches; interactive picker when no name is given",
		Args:    cobra.MaximumNArgs(1),
		RunE:    runSwitch,
	}
	cmd.Flags().BoolP("create", "c", false, "create a new branch with the given name before switching")
	cmd.Flags().BoolP("force", "f", false, "discard local changes (git switch --discard-changes)")
	cmd.Flags().Bool("detach", false, "detach HEAD at the ref instead of switching to a branch")
	cmd.Flags().Bool("fetch", false, "refresh remote branches before switching")
	cmd.Flags().BoolP("main", "m", false, "switch to the detected main/master branch")
	cmd.Flags().Bool("develop", false, "switch to the develop/dev branch")
	rootCmd.AddCommand(cmd)
}

const switchFetchTimeout = 10 * time.Second

// switchRemoteStaleAfter is how old the last fetch may be before the picker's
// `r` (show remotes) key refreshes first. Pressing `r` means "show me what's on
// the remote" — stale refs would hide a teammate's just-pushed branch — so we
// fetch when the view is older than this, but skip the network when a fetch
// happened recently (e.g. gk pull, or the `f` key moments ago) or when offline.
const switchRemoteStaleAfter = 60 * time.Second

// remoteFetchInfo reports the age of the last *successful* fetch and whether one
// has happened. It keys off FETCH_HEAD content, not just mtime: git truncates
// FETCH_HEAD to empty when a fetch fails to reach the remote, and writes the
// fetched ref list when it succeeds. Using mtime alone would treat a failed
// fetch as fresh — the staleness gate would then skip a real refresh, and the
// subtitle would claim "fetched just now" right after a failure.
func remoteFetchInfo(ctx context.Context, r git.Runner) (age time.Duration, ok bool) {
	stdout, _, err := r.Run(ctx, "rev-parse", "--git-path", "FETCH_HEAD")
	if err != nil {
		return 0, false
	}
	path := strings.TrimSpace(string(stdout))
	if path == "" {
		return 0, false
	}
	// `--git-path` returns a path relative to git's working dir (RepoFlag),
	// not this process's cwd — resolve it so os.Stat looks in the right place.
	if !filepath.IsAbs(path) {
		base := RepoFlag()
		if base == "" {
			base, _ = os.Getwd()
		}
		path = filepath.Join(base, path)
	}
	info, statErr := os.Stat(path)
	if statErr != nil || info.Size() == 0 {
		return 0, false
	}
	return time.Since(info.ModTime()), true
}

// fetchAgeLabel renders the FETCH_HEAD age for the picker subtitle.
func fetchAgeLabel(age time.Duration, exists bool) string {
	if !exists {
		return "never fetched"
	}
	switch {
	case age < time.Minute:
		return "fetched just now"
	case age < time.Hour:
		return fmt.Sprintf("fetched %dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("fetched %dh ago", int(age.Hours()))
	default:
		return fmt.Sprintf("fetched %dd ago", int(age.Hours()/24))
	}
}

func runSwitch(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	create, _ := cmd.Flags().GetBool("create")
	force, _ := cmd.Flags().GetBool("force")
	detach, _ := cmd.Flags().GetBool("detach")
	fetch, _ := cmd.Flags().GetBool("fetch")
	toMain, _ := cmd.Flags().GetBool("main")
	toDevelop, _ := cmd.Flags().GetBool("develop")

	if toMain && toDevelop {
		return fmt.Errorf("--main and --develop are mutually exclusive")
	}
	if (toMain || toDevelop) && (len(args) > 0 || create) {
		return fmt.Errorf("--main/--develop take no branch name and cannot combine with --create")
	}

	cfg, _ := config.Load(cmd.Flags())
	remote := configuredRemote(cfg)
	if fetch {
		if err := fetchSwitchBranches(ctx, runner, remote); err != nil {
			return err
		}
	}

	if toMain {
		name, err := resolveMainBranch(ctx, runner, client, cfg.Remote)
		if err != nil {
			return err
		}
		if !detach {
			if done, err := redirectIfWorktreeLocked(ctx, runner, name); err != nil || done {
				return err
			}
		}
		return doSwitch(ctx, runner, w, name, false, force, detach)
	}
	if toDevelop {
		name, err := resolveDevelopBranch(ctx, runner)
		if err != nil {
			return err
		}
		if !detach {
			if done, err := redirectIfWorktreeLocked(ctx, runner, name); err != nil || done {
				return err
			}
		}
		return doSwitch(ctx, runner, w, name, false, force, detach)
	}

	if len(args) == 1 {
		name := args[0]
		if !create && !detach {
			if done, err := redirectIfWorktreeLocked(ctx, runner, name); err != nil || done {
				return err
			}
		}
		err := doSwitch(ctx, runner, w, name, create, force, detach)
		// DWIM: a plain `gk sw <name>` against a non-existent ref is almost
		// always "start (or grab) that branch". Offer to track the remote's
		// copy when it has one, else to create it — rather than dead-ending on
		// git's "invalid reference".
		if err != nil && !create && !detach && isBranchNotFound(err) {
			return handleSwitchMiss(ctx, runner, w, cfg, name, force, detach)
		}
		return err
	}

	if create {
		return fmt.Errorf("--create requires a branch name")
	}

	pick, err := pickBranchForSwitch(ctx, runner, client, cfg, w, cmd, fetch)
	if err != nil {
		return err
	}
	if pick.Done {
		// Picker already performed the switch (e.g. `n` create-and-switch).
		return nil
	}
	// Remote-only picks need `git switch --track <remote>/<branch>` so DWIM
	// creates a local tracking branch. Local picks go straight through.
	if pick.Remote {
		return doSwitchTrack(ctx, runner, w, pick.TrackRef, force, detach)
	}
	return doSwitch(ctx, runner, w, pick.Name, false, force, detach)
}

// isBranchNotFound reports whether a failed switch was because the ref does
// not exist (vs. a dirty tree, lock, or non-branch ref).
func isBranchNotFound(err error) bool {
	s := err.Error()
	return strings.Contains(s, "invalid reference") ||
		strings.Contains(s, "did not match any") ||
		strings.Contains(s, "unknown revision")
}

// handleSwitchMiss turns a "branch not found" failure into a DWIM prompt: track
// the remote's copy when it has one, otherwise offer to create the branch. Off
// a TTY it returns a hinted error instead of prompting. Create defaults to "no"
// so a typo (gk sw mian) doesn't silently spawn a branch; track defaults to
// "yes" since the branch demonstrably exists upstream.
func handleSwitchMiss(ctx context.Context, r git.Runner, w io.Writer, cfg *config.Config, name string, force, detach bool) error {
	remote := configuredRemote(cfg)
	if remoteHasBranch(ctx, r, remote, name) {
		if !promptAllowed() {
			return WithHint(fmt.Errorf("branch %q not found locally", name),
				fmt.Sprintf("it exists on %s — run: gk sw --fetch %s", remote, name))
		}
		ok, cerr := ui.Confirm(fmt.Sprintf("%q is on %s but not local. Fetch and track it?", name, remote), true)
		if cerr != nil {
			return cerr
		}
		if !ok {
			return fmt.Errorf("aborted")
		}
		if ferr := fetchSwitchBranches(ctx, r, remote); ferr != nil {
			return ferr
		}
		return doSwitchTrack(ctx, r, w, remote+"/"+name, force, detach)
	}
	if !promptAllowed() {
		return WithHint(fmt.Errorf("branch %q not found", name),
			fmt.Sprintf("create it with: gk sw -c %s", name))
	}
	ok, cerr := ui.Confirm(fmt.Sprintf("Branch %q not found. Create it from HEAD?", name), false)
	if cerr != nil {
		return cerr
	}
	if !ok {
		return fmt.Errorf("aborted")
	}
	return doSwitch(ctx, r, w, name, true, force, detach)
}

// remoteHasBranch reports whether <remote> publishes refs/heads/<name>, via a
// single time-boxed ls-remote. Any error (offline, no such remote, no match)
// returns false, so the caller falls back to offering branch creation.
func remoteHasBranch(parent context.Context, r git.Runner, remote, name string) bool {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		remote = "origin"
	}
	ctx, cancel := context.WithTimeout(parent, switchFetchTimeout)
	defer cancel()
	stop := ui.StartBubbleSpinner(fmt.Sprintf("checking %s for %s...", remote, name))
	_, _, err := r.Run(ctx, "ls-remote", "--heads", "--exit-code", remote, "refs/heads/"+name)
	stop()
	return err == nil
}

func configuredRemote(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.Remote) != "" {
		return cfg.Remote
	}
	return "origin"
}

func fetchSwitchBranches(parent context.Context, r git.Runner, remote string) error {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		remote = "origin"
	}
	ctx, cancel := context.WithTimeout(parent, switchFetchTimeout)
	defer cancel()
	_, stderr, err := r.Run(ctx,
		"fetch",
		"--quiet",
		"--prune",
		"--no-tags",
		"--no-recurse-submodules",
		remote,
	)
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		return WithHint(fmt.Errorf("fetch %s failed: %s", remote, msg),
			"check network/credentials, then retry `gk sw --fetch`")
	}
	return nil
}

// resolveMainBranch picks the repo's canonical main branch.
// Order: DefaultBranch() result → local "main" → local "master".
func resolveMainBranch(ctx context.Context, r git.Runner, client *git.Client, remote string) (string, error) {
	if name, err := client.DefaultBranch(ctx, remote); err == nil {
		if localBranchExists(ctx, r, name) {
			return name, nil
		}
	}
	for _, cand := range []string{"main", "master"} {
		if localBranchExists(ctx, r, cand) {
			return cand, nil
		}
	}
	return "", WithHint(errors.New("no main/master branch found"),
		"check with: git branch")
}

// resolveDevelopBranch picks the repo's canonical develop branch.
// Tries "develop" then "dev".
func resolveDevelopBranch(ctx context.Context, r git.Runner) (string, error) {
	for _, cand := range []string{"develop", "dev"} {
		if localBranchExists(ctx, r, cand) {
			return cand, nil
		}
	}
	return "", WithHint(errors.New("no develop/dev branch found"),
		"check with: git branch")
}

// localBranchExists reports whether refs/heads/<name> exists.
func localBranchExists(ctx context.Context, r git.Runner, name string) bool {
	_, _, err := r.Run(ctx, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

// switchPick is the result of the interactive picker. Remote=true means
// the chosen branch exists only on a remote and should be created via
// `git switch --track` using TrackRef ("<remote>/<branch>"). Done=true
// means the picker already executed the switch (or terminated cleanly
// after an action like `n`); the caller should NOT run doSwitch again.
type switchPick struct {
	Name     string // local branch name, or short name for remote-only pick
	TrackRef string // "origin/foo" for remote-only picks; empty for local
	Remote   bool
	Done     bool
}

// remoteBranchInfo captures the bits of a refs/remotes/* entry needed for
// the picker. Name is the short name (e.g. "feature/foo"); TrackRef is
// the full "<remote>/<branch>" string passed to `git switch --track`.
type remoteBranchInfo struct {
	Name       string
	TrackRef   string
	Remote     string
	LastCommit time.Time
	Hash       string // 7-char short commit hash
}

// listRemoteOnlyBranches enumerates refs/remotes/* branches that do NOT
// have a corresponding local branch, so the picker only surfaces ones
// the user hasn't already checked out. HEAD aliases (e.g.
// refs/remotes/origin/HEAD → origin/main) are skipped since they'd be
// duplicates of the real ref they point at.
func listRemoteOnlyBranches(ctx context.Context, r git.Runner, local []branchInfo) ([]remoteBranchInfo, error) {
	stdout, stderr, err := r.Run(ctx,
		"for-each-ref",
		"--format=%(refname:short)%00%(committerdate:unix)%00%(symref)%00%(objectname:short)",
		"refs/remotes",
	)
	if err != nil {
		return nil, fmt.Errorf("for-each-ref refs/remotes: %s: %w",
			strings.TrimSpace(string(stderr)), err)
	}

	localSet := make(map[string]struct{}, len(local))
	for _, b := range local {
		localSet[b.Name] = struct{}{}
	}

	var out []remoteBranchInfo
	for _, line := range strings.Split(strings.TrimRight(string(stdout), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) < 2 {
			continue
		}
		// Skip HEAD aliases — they have a non-empty symref field.
		if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
			continue
		}
		trackRef := parts[0] // "origin/feature/foo"
		slash := strings.IndexByte(trackRef, '/')
		if slash < 0 || slash == len(trackRef)-1 {
			continue
		}
		remoteName := trackRef[:slash]
		shortName := trackRef[slash+1:]
		if shortName == "HEAD" {
			continue
		}
		// Hide remote entries that duplicate a local branch — user
		// should pick the local one (or create a differently-named
		// branch explicitly via `gk sw -c <name>`).
		if _, dup := localSet[shortName]; dup {
			continue
		}
		ts, _ := strconv.ParseInt(parts[1], 10, 64)
		hash := ""
		if len(parts) >= 4 {
			hash = parts[3]
		}
		out = append(out, remoteBranchInfo{
			Name:       shortName,
			TrackRef:   trackRef,
			Remote:     remoteName,
			LastCommit: time.Unix(ts, 0),
			Hash:       hash,
		})
	}
	return out, nil
}

// Key format distinguishes row types without requiring the picker UI
// to carry auxiliary metadata:
//
//	local  → "local:<name>"
//	remote → "remote:<trackRef>"   (e.g. remote:origin/feature/foo)
const (
	keyLocalPrefix  = "local:"
	keyRemotePrefix = "remote:"
)

// switchWorktreeMap captures the worktree topology relevant to the
// switch picker. Worktrees are first-class navigation targets — every
// non-current worktree surfaces as its own row.
type switchWorktreeMap struct {
	// byBranch maps branch name → the OTHER worktree holding it. Used
	// for smart handoff when a user picks a branch that's checked out
	// elsewhere (git would refuse the switch).
	byBranch map[string]WorktreeEntry
	// others is every worktree EXCEPT the one we're running in. Each
	// surfaces as a "worktree:<path>" row in the picker.
	others []WorktreeEntry
	// current is the entry matching cwd, if any. Empty when cwd is
	// outside any worktree the git CLI knows about.
	current WorktreeEntry
	// linked is true when the current entry is NOT the main worktree
	// (the first entry in `git worktree list --porcelain`).
	linked bool
}

func loadSwitchWorktrees(ctx context.Context, runner git.Runner) switchWorktreeMap {
	m := switchWorktreeMap{byBranch: map[string]WorktreeEntry{}}
	out, _, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return m
	}
	entries := parseWorktreePorcelain(string(out))
	if len(entries) == 0 {
		return m
	}
	// Ask git which worktree the current operation targets. This honors
	// --repo and any GIT_DIR override more reliably than os.Getwd().
	top, _, terr := runner.Run(ctx, "rev-parse", "--show-toplevel")
	cur := ""
	if terr == nil {
		cur = canonPath(strings.TrimSpace(string(top)))
	}

	for i, e := range entries {
		ep := canonPath(e.Path)
		if cur != "" && ep == cur {
			m.current = e
			m.linked = i > 0
			continue
		}
		if e.Bare {
			continue
		}
		m.others = append(m.others, e)
		if e.Branch != "" && !e.Detached {
			m.byBranch[e.Branch] = e
		}
	}
	return m
}

// buildSwitchSubtitle composes the picker's ambient context line:
//
//   - "on: <current>" — always, so the user knows where they are.
//   - "worktree: <path>" — when running inside a linked worktree.
//   - "hidden: 2 remote (r)" — when the remote toggle would reveal
//     more rows; surfaces the hotkey.
//   - "fetched 3m ago" — when remotes are shown, so the user can judge
//     how current the list is (and reach for `f` if it's stale). Shows
//     "fetch failed" instead when the last attempt this session errored.
func buildSwitchSubtitle(cur string, wt switchWorktreeMap, allRemotes []remoteBranchInfo, showRemotes bool, fetchAge time.Duration, fetched, fetchFailed bool) string {
	parts := make([]string, 0, 3)
	if cur != "" {
		parts = append(parts, "on: "+cur)
	}
	if wt.linked && wt.current.Path != "" {
		parts = append(parts, "worktree: "+wt.current.Path)
	}
	if showRemotes {
		if fetchFailed {
			parts = append(parts, "fetch failed")
		} else {
			parts = append(parts, fetchAgeLabel(fetchAge, fetched))
		}
	} else if len(allRemotes) > 0 {
		parts = append(parts, fmt.Sprintf("hidden: %d remote (r)", len(allRemotes)))
	}
	return strings.Join(parts, "  ·  ")
}

// loadWorktreeDirtyStates queries `git status --porcelain` in every
// known worktree concurrently and returns a map keyed by branch name.
// Branches without an associated worktree (or with a stale/missing
// path) are absent from the map; callers treat that as "no signal".
//
// Each per-worktree call is bounded by a 200ms context so a slow path
// (NFS, USB drive spun-down) doesn't block picker entry. We pass
// `--no-optional-locks` to coexist cleanly with concurrent editor git
// plugins. Errors are swallowed to "no signal" — this is informational
// UI, not a correctness gate.
func loadWorktreeDirtyStates(ctx context.Context, wt switchWorktreeMap) map[string]git.DirtyFlags {
	type entry struct {
		branch string
		path   string
	}
	var targets []entry
	if wt.current.Path != "" && wt.current.Branch != "" && !wt.current.Detached && !wt.current.Bare {
		targets = append(targets, entry{branch: wt.current.Branch, path: wt.current.Path})
	}
	for _, e := range wt.others {
		if e.Bare || e.Detached || e.Branch == "" || e.Path == "" {
			continue
		}
		targets = append(targets, entry{branch: e.Branch, path: e.Path})
	}
	if len(targets) == 0 {
		return nil
	}

	out := make(map[string]git.DirtyFlags, len(targets))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(branch, path string) {
			defer wg.Done()
			if _, err := os.Stat(path); err != nil {
				return
			}
			callCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer cancel()
			r := &git.ExecRunner{Dir: path}
			stdout, _, err := r.Run(callCtx,
				"--no-optional-locks", "status", "--porcelain", "-z",
			)
			if err != nil {
				return
			}
			flags := git.ParsePorcelainV1(stdout)
			if flags.Clean() {
				return
			}
			mu.Lock()
			out[branch] = flags
			mu.Unlock()
		}(t.branch, t.path)
	}
	wg.Wait()
	return out
}

func canonPath(p string) string {
	if p == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(p)
}

// pickBranchForSwitch runs the interactive switch picker as an outer
// action loop. The picker shows local branches (current branch
// included with a star marker for context) and remote-only branches
// (toggled by `r`). Selecting a branch checked out in another
// worktree triggers a smart handoff to that worktree's subshell —
// since git would refuse the switch otherwise. Hotkeys (n/d/f/r) exit
// the picker so we can drive sub-prompts (text input, confirm),
// then re-enter on the next iteration.
func pickBranchForSwitch(ctx context.Context, runner git.Runner, client *git.Client, cfg *config.Config, w io.Writer, cmd *cobra.Command, showRemotes bool) (switchPick, error) {

	// Hoist loop-invariant probes outside the picker re-entry loop.
	// `cur` and `defaultBr` don't change across n/d actions (n exits the
	// loop entirely; d never deletes the current branch). `wt`/`dirty` are
	// likewise reused per iteration to avoid a per-worktree `git status`
	// stall on every render — the one action that DOES mutate worktree
	// topology (d on a branch parked in another worktree → worktree
	// removal) reloads both before it re-lists. Refreshing them every
	// iteration was the dominant source of post-action stalls.
	cur, _ := client.CurrentBranch(ctx)
	remote := "origin"
	if cfg != nil && cfg.Remote != "" {
		remote = cfg.Remote
	}
	var defaultBr string
	var wt switchWorktreeMap
	{
		var setupWg sync.WaitGroup
		setupWg.Add(2)
		go func() {
			defer setupWg.Done()
			defaultBr, _ = resolveMainBranch(ctx, runner, client, remote)
		}()
		go func() {
			defer setupWg.Done()
			wt = loadSwitchWorktrees(ctx, runner)
		}()
		setupWg.Wait()
	}
	// Dirty state derives from wt and is expensive (per-worktree git
	// status). Compute once — concurrent editor edits between picker
	// renders won't reflect, but that's an informational signal, not
	// a correctness gate. After a delete, dirty entries for the removed
	// branch are simply unused.
	dirty := loadWorktreeDirtyStates(ctx, wt)

	// fetchFailed records that the most recent fetch attempt in this picker
	// session failed, so the subtitle can say "fetch failed" instead of
	// reading a misleading freshness off FETCH_HEAD.
	fetchFailed := false

	// currentFilter carries the residual filter query across picker
	// re-entries so a delete/bulk action doesn't reset the narrowed view —
	// the user keeps cleaning the same subset without re-typing.
	var currentFilter string

	// protected branches (main/master/develop + config) are blocked from
	// d-delete; D (force) overrides. Loop-invariant, so built once here.
	protected := map[string]bool{}
	if cfg != nil {
		for _, p := range cfg.Branch.Protected {
			protected[p] = true
		}
	}

	for {
		local, err := listLocalBranches(ctx, runner)
		if err != nil {
			return switchPick{}, err
		}
		// Always enumerate remotes — we need the count to surface a
		// "N remote hidden (r)" hint even when not displaying them.
		// Sorted once here so the `r` toggle's first press shows
		// recency-ordered rows just like a re-entered loop would.
		allRemotes, rerr := listRemoteOnlyBranches(ctx, runner, local)
		if rerr != nil {
			allRemotes = nil
		}
		sort.Slice(allRemotes, func(i, j int) bool {
			return allRemotes[i].LastCommit.After(allRemotes[j].LastCommit)
		})
		var remotes []remoteBranchInfo
		if showRemotes {
			remotes = allRemotes
		}

		merged, _ := mergedBranches(ctx, runner, defaultBr)

		// Fallback divergence for branches without an upstream:
		// compare against same-named remote ref when one exists. This
		// covers the common case of `git switch -c feat/x` where the
		// user later pushed to origin without `--set-upstream`.
		applyUntrackedFallback(local, scanUntrackedDivergent(ctx, runner, remote))

		// Fork point — for branches still without any upstream/inferred
		// signal, compute merge-base vs default so the picker can show
		// "from main@abc1234" instead of a flat "(local)".
		computeForkPoints(ctx, runner, defaultBr, local)

		// Pin the current branch to the top so the user always sees
		// "where am I" on the first row; sort the rest by recency.
		sort.SliceStable(local, func(i, j int) bool {
			if local[i].Name == cur {
				return true
			}
			if local[j].Name == cur {
				return false
			}
			return local[i].LastCommit.After(local[j].LastCommit)
		})
		// `remotes` shares storage with `allRemotes`, which is sorted
		// once at enumeration above — no need to sort again here.

		items := buildSwitchItems(local, remotes, cur, wt, dirty)
		wtCol := switchHasWorktreeCol(wt)
		if len(items) == 0 {
			placeholder := "(no branches — press n to create)"
			cells := []string{placeholder, "", "", ""}
			if wtCol {
				cells = []string{placeholder, "", "", "", ""}
			}
			items = append(items, ui.PickerItem{
				Key:     "local:__placeholder__",
				Cells:   cells,
				Display: placeholder,
			})
		}

		extras := buildSwitchExtras()
		fetchAge, fetched := remoteFetchInfo(ctx, runner)
		subtitle := buildSwitchSubtitle(cur, wt, allRemotes, showRemotes, fetchAge, fetched, fetchFailed)
		var filterItems []ui.PickerItem
		if !showRemotes && len(allRemotes) > 0 {
			filterItems = buildSwitchItems(nil, allRemotes, cur, wt, dirty)
		}
		headers := []string{"BRANCH", "UPSTREAM", "HASH", "AGE"}
		if wtCol {
			headers = []string{"BRANCH", "UPSTREAM", "WORKTREE", "HASH", "AGE"}
		}
		picker := &ui.TablePicker{
			Headers:        headers,
			Extras:         extras,
			Subtitle:       subtitle,
			FilterItems:    filterItems,
			InitialFilter:  currentFilter,
			ColumnPriority: switchColumnPriority(),
		}
		choice, err := picker.Pick(ctx, "switch", items)
		if err != nil {
			if errors.Is(err, ui.ErrPickerAborted) {
				return switchPick{}, WithHint(errors.New("aborted"), "pass a branch name directly: gk switch <name>")
			}
			return switchPick{}, err
		}
		// Preserve the residual filter for the next iteration (delete/bulk
		// actions re-enter the loop and re-seed it).
		currentFilter = choice.FilterValue

		switch choice.ExtraAction {
		case "":
			pick, err := decodeSwitchChoice(choice)
			if err != nil {
				return switchPick{}, err
			}
			// Selecting the current branch is a no-op — the user is
			// already there. Bail out cleanly so they're not confused
			// by a "fatal: invalid reference" error from git.
			if !pick.Remote && pick.Name == cur {
				fmt.Fprintf(w, "already on %s\n", cur)
				return switchPick{Done: true}, nil
			}
			// Branch is checked out elsewhere → smart handoff.
			if entry, locked := wt.byBranch[pick.Name]; locked && !pick.Remote {
				done, err := handleWorktreeRedirect(ctx, cmd, entry)
				if err != nil {
					return switchPick{}, err
				}
				if done {
					return switchPick{Done: true}, nil
				}
				continue
			}
			return pick, nil
		case "n":
			pick, handled, err := promptCreateBranch(ctx, runner, w)
			if err != nil {
				return switchPick{}, err
			}
			if handled {
				return pick, nil
			}
			continue
		case "d":
			// A branch checked out in another worktree can't be removed with
			// `git branch -d` — git refuses it ("cannot delete branch 'x' used
			// by worktree at ..."). Pressing d on such a row means "get rid
			// of that worktree", so redirect to the worktree-removal flow
			// (lock/dirty-aware, then offers to drop the now-freed branch).
			// This mutates worktree topology, so refresh the hoisted wt/dirty
			// before re-listing or the picker would keep a phantom WORKTREE
			// column and re-route a now-free branch here forever.
			if entry, isWT := worktreeDeleteTarget(choice, wt); isWT {
				if rerr := worktreeTUIRemove(ctx, runner, w, entry, cfgProtected(cfg)); rerr != nil {
					fmt.Fprintln(w, "✗ "+rerr.Error())
				}
				wt = loadSwitchWorktrees(ctx, runner)
				dirty = loadWorktreeDirtyStates(ctx, wt)
				continue
			}
			if err := handleDeleteAction(ctx, runner, cfg, w, choice, cur, defaultBr, protected, merged); err != nil {
				if errors.Is(err, ui.ErrPickerAborted) || errors.Is(err, errSwitchActionRetry) {
					continue
				}
				return switchPick{}, err
			}
		case "f":
			stop := ui.StartBubbleSpinner("refreshing " + remote + "...")
			err := fetchSwitchBranches(ctx, runner, remote)
			stop()
			if err != nil {
				fmt.Fprintln(w, "✗ "+err.Error())
				if h := HintFrom(err); h != "" {
					fmt.Fprintln(w, "  hint: "+h)
				}
				fetchFailed = true
				continue
			}
			showRemotes = true
			fetchFailed = false
			fmt.Fprintf(w, "refreshed %s\n", remote)
		case "r":
			// Toggle remote visibility. Turning the view ON refreshes first
			// when the last fetch is stale — pressing `r` means "show me the
			// remote", which is misleading against cached refs. A failed fetch
			// (offline / no creds) still reveals the cached remotes plus a
			// warning rather than blocking. Turning OFF never touches the net.
			if showRemotes {
				showRemotes = false
				continue
			}
			if age, ok := remoteFetchInfo(ctx, runner); !ok || age > switchRemoteStaleAfter {
				stop := ui.StartBubbleSpinner("refreshing " + remote + "...")
				err := fetchSwitchBranches(ctx, runner, remote)
				stop()
				if err != nil {
					fmt.Fprintln(w, "✗ "+err.Error())
					if h := HintFrom(err); h != "" {
						fmt.Fprintln(w, "  hint: "+h)
					}
					fmt.Fprintln(w, "  showing cached remote branches")
					fetchFailed = true
				} else {
					fmt.Fprintf(w, "refreshed %s\n", remote)
					fetchFailed = false
				}
			}
			showRemotes = true
		}
	}
}

// handleWorktreeRedirect prompts the user to enter the worktree where
// `entry.Branch` is checked out. Returns (true, nil) when the subshell
// completed (caller should treat the switch as done), (false, nil) when
// the user declined or non-TTY (re-enter picker).
func handleWorktreeRedirect(ctx context.Context, cmd *cobra.Command, entry WorktreeEntry) (bool, error) {
	title := fmt.Sprintf("Branch %q lives in another worktree", entry.Branch)
	desc := fmt.Sprintf("enter %s? (a subshell opens; type `exit` to return)", entry.Path)
	ok, err := ui.ConfirmTUI(ctx, title, desc, true)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) || errors.Is(err, ui.ErrNonInteractive) {
			return false, nil
		}
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := enterWorktreeSubshell(cmd, entry.Path); err != nil {
		return false, err
	}
	return true, nil
}

// computeForkPoints attaches ForkBranch/ForkPoint to local branches
// that have no upstream (real or inferred). The fork anchor is the
// recorded gk-parent when one is set (so stacked branches show their
// real parent instead of the trunk), else defaultBr; the fork point is
// `merge-base <branch> <anchor>`. Skipped when defaultBr is empty
// (bare/fresh repos). Runs in parallel; failures are silently
// ignored — fork info is informational, not load-bearing.
func computeForkPoints(ctx context.Context, runner git.Runner, defaultBr string, local []branchInfo) {
	if defaultBr == "" {
		return
	}
	// One batch read (not GetParent per branch) keeps the subprocess
	// count flat on branch-heavy repos; a read failure degrades every
	// anchor to defaultBr rather than dropping the column.
	parents, _ := branchparent.NewConfig(git.NewClient(runner)).AllParents(ctx)
	type result struct {
		idx    int
		anchor string
		hash   string
	}
	// Tip hashes come free with the branch listing — they are what makes the
	// fork-point cache exact (see forkPointCache).
	tips := make(map[string]string, len(local))
	for _, b := range local {
		tips[b.Name] = b.Hash
	}
	keys := make(map[int]string, len(local)) // idx → cache key, for the misses we compute
	out := make(chan result, len(local))
	// Bound concurrency at NumCPU so a repo with hundreds of stale
	// local branches doesn't fork hundreds of `git merge-base`
	// processes simultaneously (fd / pid pressure + context-switch tax).
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for i, b := range local {
		if b.Upstream != "" || b.Name == defaultBr {
			continue
		}
		anchor := defaultBr
		if p := parents[b.Name]; p != "" && p != b.Name {
			anchor = p
		}
		// A live dashboard re-derives fork points on every poll, and each one is
		// a `merge-base` fork — on a fleet this scaled with BRANCH count, not
		// worktree count (measured: ~21 subprocesses per poll). The answer is a
		// pure function of the two commits, so a hit here is exact, not stale.
		key, cacheable := forkPointKey(runner, b.Name, b.Hash, anchor, tips[anchor])
		if cacheable {
			if hit, ok := forkPointCache.Load(key); ok {
				if r, valid := hit.(forkPoint); valid {
					local[i].ForkBranch, local[i].ForkPoint = r.anchor, r.hash
					continue
				}
			}
			keys[i] = key
		}
		wg.Add(1)
		go func(idx int, branch, anchor string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			callCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
			defer cancel()
			stdout, _, err := runner.Run(callCtx, "merge-base", branch, anchor)
			if err != nil && anchor != defaultBr {
				// Recorded parent unusable (ref deleted?) — fall back
				// to the trunk anchor, same policy as Resolver.
				anchor = defaultBr
				stdout, _, err = runner.Run(callCtx, "merge-base", branch, anchor)
			}
			if err != nil {
				return
			}
			h := strings.TrimSpace(string(stdout))
			if len(h) > 7 {
				h = h[:7]
			}
			if h == "" {
				return
			}
			out <- result{idx, anchor, h}
		}(i, b.Name, anchor)
	}
	wg.Wait()
	close(out)
	for r := range out {
		local[r.idx].ForkBranch = r.anchor
		local[r.idx].ForkPoint = r.hash
		if key, ok := keys[r.idx]; ok {
			forkPointCache.Store(key, forkPoint{anchor: r.anchor, hash: r.hash})
		}
	}
}

// forkPoint is one memoised `merge-base` outcome. The anchor is stored beside
// the hash because computeForkPoints falls back to the trunk when a recorded
// parent ref is unusable — the anchor that ANSWERED is not always the one asked
// for, and a cache that dropped it would re-attribute the fork on every hit.
type forkPoint struct{ anchor, hash string }

// forkPointCache memoises merge-base by its inputs: the two commits it joins.
// Keyed by content, so an entry can never go stale — a moved tip is a different
// key, not a wrong answer. It exists for the live dashboard, which otherwise
// re-forked one `merge-base` per upstream-less branch per repo on every poll.
var forkPointCache sync.Map // key → forkPoint

// forkPointKey identifies a merge-base by (repo, branch tip, anchor tip). It
// reports false when either tip is unknown — an anchor whose ref was deleted has
// no tip to key on, so that branch is computed fresh every time rather than
// cached against an input we cannot see change.
//
// The repo directory is part of the key because branchInfo.Hash is a SHORT
// hash: 7 hex chars collide across repositories far too easily to key a
// process-wide cache on alone.
func forkPointKey(runner git.Runner, branch, branchTip, anchor, anchorTip string) (string, bool) {
	if branchTip == "" || anchorTip == "" || branch == "" || anchor == "" {
		return "", false
	}
	dir := ""
	if er, ok := runner.(*git.ExecRunner); ok {
		dir = er.Dir
	}
	return strings.Join([]string{dir, branch, branchTip, anchor, anchorTip}, "\x00"), true
}

// applyUntrackedFallback patches Ahead/Behind on branches that have
// no configured upstream but a same-named remote ref that differs.
// Mutates `local` in place.
func applyUntrackedFallback(local []branchInfo, fallback []untrackedDivergent) {
	if len(fallback) == 0 {
		return
	}
	byName := make(map[string]untrackedDivergent, len(fallback))
	for _, u := range fallback {
		byName[u.Branch] = u
	}
	for i := range local {
		if local[i].Upstream != "" {
			continue
		}
		if u, ok := byName[local[i].Name]; ok {
			local[i].Ahead = u.Ahead
			local[i].Behind = u.Behind
			// Surface the implicit remote in UPSTREAM cell so the user
			// sees what the diff is being measured against. Marked
			// inferred so the renderer uses a `~` prefix instead of
			// `↑` — visually distinct from a configured upstream.
			local[i].Upstream = u.Implicit
			local[i].UpstreamInferred = true
		}
	}
}

// formatSwitchDiff renders ahead/behind vs upstream as "↑3 ↓5" /
// "↑3" / "↓5" / "" (clean or no upstream).
func formatSwitchDiff(ahead, behind int) string {
	switch {
	case ahead == 0 && behind == 0:
		return ""
	case ahead == 0:
		return fmt.Sprintf("↓%d", behind)
	case behind == 0:
		return fmt.Sprintf("↑%d", ahead)
	default:
		return fmt.Sprintf("↑%d ↓%d", ahead, behind)
	}
}

// colorSwitchDiff is the cell-safe coloured counterpart: green ↑,
// red ↓. Uses fg-only-reset helpers so bubbles/table's Selected-row
// background isn't broken mid-span by an embedded `\x1b[0m`.
func colorSwitchDiff(ahead, behind int) string {
	parts := make([]string, 0, 2)
	if ahead > 0 {
		parts = append(parts, cellGreen(fmt.Sprintf("↑%d", ahead)))
	}
	if behind > 0 {
		parts = append(parts, cellRed(fmt.Sprintf("↓%d", behind)))
	}
	return strings.Join(parts, " ")
}

// buildSwitchItems renders the picker as a branch list with four
// columns: BRANCH, UPSTREAM, HASH, AGE. The current branch is
// included with a "★" marker so users can see "where am I" at a
// glance — selecting it is a no-op handled by the caller. Local
// branches checked out in another worktree show "wt: <basename>" in
// the upstream column; selecting them triggers the smart-handoff
// prompt to enter the holding worktree. The UPSTREAM cell embeds
// divergence vs the default branch (e.g. "↑3 ↓1  origin/feat/x").
// formatDirtyMarker turns DirtyFlags into a compact glyph cluster:
//
//	"*"   modified
//	"±"   staged
//	"!"   conflict
//	"*±"  modified + staged (combine compactly)
//	""    clean / no signal
func formatDirtyMarker(d git.DirtyFlags) string {
	var b strings.Builder
	if d.Modified {
		b.WriteString("*")
	}
	if d.Staged {
		b.WriteString("±")
	}
	if d.Conflict {
		b.WriteString("!")
	}
	return b.String()
}

// colorDirtyMarker is the colored counterpart of formatDirtyMarker.
// Colours: red `*` (working tree), yellow `±` (staged for commit),
// red bold `!` (unmerged path — needs attention). Used inside Cells
// → fg-only-reset helpers preserve Selected-row background.
func colorDirtyMarker(d git.DirtyFlags) string {
	var b strings.Builder
	if d.Modified {
		b.WriteString(cellRed("*"))
	}
	if d.Staged {
		b.WriteString(cellYellow("±"))
	}
	if d.Conflict {
		b.WriteString(cellRedBold("!"))
	}
	return b.String()
}

// buildSwitchItems renders the picker as a branch list with four
// columns: BRANCH, UPSTREAM, HASH, AGE.
//
// BRANCH cell carries all per-branch state signals: marker (★/●/○),
// name, dirty glyphs (*/±/!), and divergence vs upstream (↑X ↓Y).
// UPSTREAM cell carries only the source descriptor (origin/X,
// wt:<basename>, (local), (gone), remote:<remote>).
//
// Selecting a worktree-locked local branch triggers the smart-handoff
// prompt; selecting the current branch is a no-op (handled by caller).
func buildSwitchItems(local []branchInfo, remotes []remoteBranchInfo, cur string, wt switchWorktreeMap, dirty map[string]git.DirtyFlags) []ui.PickerItem {
	items := make([]ui.PickerItem, 0, len(local)+len(remotes))
	wtCol := switchHasWorktreeCol(wt)

	for _, b := range local {
		entry, locked := wt.byBranch[b.Name]
		isCurrent := b.Name == cur
		// Resolve the underlying "what does this branch compare to"
		// info: upstream / inferred / fork / gone / nothing. Worktree
		// occupancy is NOT mixed in here — it gets its own WORKTREE
		// column so UPSTREAM stays a pure source descriptor.
		var coreSource, coreColored string
		switch {
		case b.Gone:
			coreSource = "(gone)"
			coreColored = cellFaint(coreSource)
		case b.Upstream != "":
			prefix := "↑ "
			if b.UpstreamInferred {
				prefix = "~ "
			}
			coreSource = prefix + b.Upstream
			coreColored = coreSource
		case b.ForkPoint != "":
			coreSource = fmt.Sprintf("from %s@%s", b.ForkBranch, b.ForkPoint)
			coreColored = coreSource
		default:
			coreSource = "(local)"
			coreColored = cellFaint(coreSource)
		}
		source := coreSource
		coloredSource := coreColored
		// WORKTREE cell: the basename of the worktree holding this
		// branch elsewhere (e.g. main checked out in the "improve-ui"
		// worktree). Empty for branches not locked to another worktree.
		wtLabel := ""
		if locked {
			wtLabel = filepath.Base(entry.Path)
		}
		// Display marker stays via fatih/color — it's only used by
		// the FallbackPicker path which doesn't have row-level styles.
		displayMarker := color.GreenString("●")
		cellMarker := cellGreen("●")
		if isCurrent {
			displayMarker = color.YellowString("★")
			cellMarker = cellYellow("★")
		}
		coloredDirtyTag := ""
		if d, ok := dirty[b.Name]; ok {
			coloredDirtyTag = " " + colorDirtyMarker(d)
		}
		diffPlain := formatSwitchDiff(b.Ahead, b.Behind)
		diffCell := colorSwitchDiff(b.Ahead, b.Behind)
		diffSuffix := ""
		coloredDiffSuffix := ""
		if diffPlain != "" {
			diffSuffix = " " + diffPlain
			coloredDiffSuffix = " " + diffCell
		}
		age := shortAge(b.LastCommit)
		branchCell := cellMarker + " " + b.Name + coloredDirtyTag + coloredDiffSuffix
		cells := []string{branchCell, coloredSource}
		if wtCol {
			cells = append(cells, cellYellow(wtLabel))
		}
		cells = append(cells, b.Hash, age)
		items = append(items, ui.PickerItem{
			Key:     keyLocalPrefix + b.Name,
			Cells:   cells,
			Display: switchDisplayRow(displayMarker, b.Name+coloredDirtyTag+diffSuffix, source, wtLabel, b.Hash, age, wtCol),
		})
	}

	for _, r := range remotes {
		age := shortAge(r.LastCommit)
		source := "remote: " + r.Remote
		coloredSource := cellCyan("remote: ") + r.Remote
		cells := []string{cellCyan("○") + " " + r.Name, coloredSource}
		if wtCol {
			cells = append(cells, "")
		}
		cells = append(cells, r.Hash, age)
		items = append(items, ui.PickerItem{
			Key:     keyRemotePrefix + r.TrackRef,
			Cells:   cells,
			Display: switchDisplayRow(color.CyanString("○"), r.Name, source, "", r.Hash, age, wtCol),
		})
	}
	return items
}

// switchHasWorktreeCol reports whether any local branch is checked out in
// another worktree, gating the optional WORKTREE column so a plain repo
// with no extra worktrees keeps the original four-column layout.
func switchHasWorktreeCol(wt switchWorktreeMap) bool {
	return len(wt.byBranch) > 0
}

// switchColumnPriority maps the switch picker's column titles to keep-weights.
// When the terminal is too narrow for every column, TablePicker drops the
// lowest-weight ones whole (see ui.TablePicker.ColumnPriority). BRANCH is the
// identity column and must survive; AGE ranks just below it — it is short yet
// the signal users most want at a glance — so HASH (a bare SHA) and then
// UPSTREAM/WORKTREE give way first, leaving "BRANCH … AGE" at the narrowest
// widths. Keyed by title, so it covers both the four- and five-column
// (WORKTREE present) layouts with one map.
func switchColumnPriority() map[string]int {
	return map[string]int{
		"BRANCH":   100,
		"AGE":      80,
		"UPSTREAM": 40,
		"WORKTREE": 30,
		"HASH":     10,
	}
}

// switchDisplayRow formats the fallback (non-table) picker row, inserting
// the WORKTREE column only when present.
func switchDisplayRow(marker, branch, source, worktree, hash, age string, wtCol bool) string {
	if wtCol {
		return fmt.Sprintf("%s  %-36s  %-32s  %-14s  %-8s  %s",
			marker, branch, source, worktree, hash, age)
	}
	return fmt.Sprintf("%s  %-36s  %-32s  %-8s  %s",
		marker, branch, source, hash, age)
}

// buildSwitchExtras wires the n/d/r/f hotkeys. All of them exit the
// picker so the caller can drive prompts/confirms (and, for r/f, a fetch
// spinner) outside the bubbletea program, then re-enter on the next loop
// iteration with a freshly enumerated branch list.
//
// Each carries a ctrl alias so the action is still reachable while the
// filter prompt is focused, where bare letters are swallowed as filter
// text. There is deliberately no force-delete key: ctrl cannot encode
// the d/D case distinction in a terminal, so `d` on a branch git refuses
// promotes to a force prompt instead (see handleDeleteAction).
func buildSwitchExtras() []ui.TablePickerExtraKey {
	return []ui.TablePickerExtraKey{
		{Key: "n", FilterKey: "ctrl+n", Help: "n new", Exit: true},
		{Key: "d", FilterKey: "ctrl+d", Help: "d delete", Exit: true},
		{Key: "f", FilterKey: "ctrl+f", Help: "f fetch", Exit: true},
		{Key: "r", FilterKey: "ctrl+r", Help: "r remotes", Exit: true},
	}
}

func decodeSwitchChoice(choice ui.PickerItem) (switchPick, error) {
	switch {
	case strings.HasPrefix(choice.Key, keyRemotePrefix):
		trackRef := strings.TrimPrefix(choice.Key, keyRemotePrefix)
		short := trackRef
		if i := strings.IndexByte(trackRef, '/'); i >= 0 {
			short = trackRef[i+1:]
		}
		return switchPick{Name: short, TrackRef: trackRef, Remote: true}, nil
	case strings.HasPrefix(choice.Key, keyLocalPrefix):
		name := strings.TrimPrefix(choice.Key, keyLocalPrefix)
		if name == "__placeholder__" {
			return switchPick{}, WithHint(errors.New("no branch selected"),
				"press n to create a new branch")
		}
		return switchPick{Name: name}, nil
	default:
		return switchPick{Name: choice.Key}, nil
	}
}

// errSwitchActionRetry is returned by action handlers when the user
// cancelled inside a sub-prompt and the picker should re-enter rather
// than abort the whole flow.
var errSwitchActionRetry = errors.New("switch action: retry")

// promptCreateBranch runs the `n` action: prompt for a name, then run
// `git switch -c <name>`. Returns (pick, true, nil) on success so the
// outer loop terminates; (zero, false, nil) when the user aborted the
// name prompt (loop should re-enter the picker).
func promptCreateBranch(ctx context.Context, r git.Runner, w io.Writer) (switchPick, bool, error) {
	name, err := ui.PromptTextTUI(ctx, "new branch name", "feature/...", "")
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) || errors.Is(err, ui.ErrNonInteractive) {
			return switchPick{}, false, nil
		}
		return switchPick{}, false, err
	}
	if name == "" {
		return switchPick{}, false, nil
	}
	if err := doSwitch(ctx, r, w, name, true, false, false); err != nil {
		return switchPick{}, false, err
	}
	return switchPick{Name: name, Done: true}, true, nil
}

// targetBranchInfo decodes the cursor row into a deletion target.
type targetBranchInfo struct {
	Name        string // local branch name; empty if remote-only
	IsRemote    bool   // true if cursor was on a refs/remotes/* row
	Placeholder bool   // true if cursor was on the empty-list placeholder
}

func decodeBranchTarget(choice ui.PickerItem) targetBranchInfo {
	switch {
	case strings.HasPrefix(choice.Key, keyRemotePrefix):
		trackRef := strings.TrimPrefix(choice.Key, keyRemotePrefix)
		short := trackRef
		if i := strings.IndexByte(trackRef, '/'); i >= 0 {
			short = trackRef[i+1:]
		}
		return targetBranchInfo{Name: short, IsRemote: true}
	case strings.HasPrefix(choice.Key, keyLocalPrefix):
		name := strings.TrimPrefix(choice.Key, keyLocalPrefix)
		if name == "__placeholder__" {
			return targetBranchInfo{Placeholder: true}
		}
		return targetBranchInfo{Name: name}
	default:
		return targetBranchInfo{Name: choice.Key}
	}
}

// worktreeDeleteTarget reports whether a d cursor row targets a branch
// that lives in ANOTHER worktree. git refuses `branch -d` on such a branch
// ("used by worktree at ..."), and the user's real intent is to remove that
// worktree — so the picker routes these to the worktree-removal flow instead
// of branch deletion. Remote-only and placeholder rows never qualify.
func worktreeDeleteTarget(choice ui.PickerItem, wt switchWorktreeMap) (WorktreeEntry, bool) {
	target := decodeBranchTarget(choice)
	if target.IsRemote || target.Placeholder {
		return WorktreeEntry{}, false
	}
	entry, locked := wt.byBranch[target.Name]
	return entry, locked
}

// guardDelete returns nil if (target, force) is safe to delete,
// otherwise an error explaining why. Pure function — no I/O.
//
// Protection policy:
//   - the current branch is never deletable (git refuses it anyway);
//   - the default branch and any name in protected are blocked by default
//     but may be force-deleted with D;
//   - unmerged branches are blocked by default, force-deletable with D.
func guardDelete(target targetBranchInfo, current, defaultBr string, protected, merged map[string]bool, force bool) error {
	if target.Placeholder {
		return errors.New("nothing to delete — list is empty")
	}
	if target.IsRemote {
		return WithHint(errors.New("cannot delete remote branches from picker"),
			"use `gk branch clean --remote` or `git push <remote> --delete <name>`")
	}
	if target.Name == "" {
		return errors.New("no branch under cursor")
	}
	if target.Name == current {
		return errors.New("cannot delete the current branch")
	}
	if !force {
		if defaultBr != "" && target.Name == defaultBr {
			return WithHint(&forceableError{fmt.Sprintf("refusing to delete default branch %q", target.Name)},
				"confirm the force prompt to delete it anyway")
		}
		if protected[target.Name] {
			return WithHint(&forceableError{fmt.Sprintf("branch %q is protected", target.Name)},
				"confirm the force prompt to delete it anyway")
		}
		if !merged[target.Name] {
			return WithHint(&forceableError{fmt.Sprintf("branch %q has unmerged commits", target.Name)},
				"confirm the force prompt to delete it anyway (unmerged work will be lost)")
		}
	}
	return nil
}

// errDeleteNeedsForce marks a guardDelete rejection that `git branch -D`
// could still carry out. handleDeleteAction turns those into an in-place
// force prompt: the user already pressed `d` on this row, so the useful
// question is about the consequence, not about finding a second hotkey.
// Terminals cannot encode `ctrl+D` distinctly from `ctrl+d`, so a
// separate force key could not survive the move to modifier aliases.
var errDeleteNeedsForce = errors.New("force required")

// forceableError is a guardDelete rejection matching errDeleteNeedsForce
// while keeping Error() as the plain human reason, so the same string can
// be shown verbatim in the force prompt.
type forceableError struct{ reason string }

func (e *forceableError) Error() string        { return e.reason }
func (e *forceableError) Is(target error) bool { return target == errDeleteNeedsForce }

// handleDeleteAction runs the d action end-to-end: guard → confirm →
// `git branch -d|-D`. Returns nil on success (caller re-lists),
// errSwitchActionRetry when the user cancelled the confirm or the
// guard rejected, and a real error only on git failure.
//
// A guard rejection git could still honour with -D (unmerged, protected,
// default) does not bounce the user out: it promotes the confirm to a
// force prompt carrying the reason, and -D runs only if they accept.
func handleDeleteAction(ctx context.Context, r git.Runner, cfg *config.Config, w io.Writer, choice ui.PickerItem, current, defaultBr string, protected, merged map[string]bool) error {
	target := decodeBranchTarget(choice)
	force := false
	forceReason := ""
	if err := guardDelete(target, current, defaultBr, protected, merged, false); err != nil {
		if !errors.Is(err, errDeleteNeedsForce) {
			fmt.Fprintln(w, "✗ "+err.Error())
			if h := HintFrom(err); h != "" {
				fmt.Fprintln(w, "  hint: "+h)
			}
			return errSwitchActionRetry
		}
		force = true
		forceReason = err.Error()
	}

	title := fmt.Sprintf("Delete branch %q?", target.Name)
	desc := "merged into " + defaultBr
	if force {
		title = fmt.Sprintf("FORCE delete %q?", target.Name)
		desc = forceReason + " — this cannot be undone"
	}
	ok, err := ui.ConfirmTUI(ctx, title, desc, false)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return errSwitchActionRetry
		}
		return err
	}
	if !ok {
		return errSwitchActionRetry
	}

	// AllowProtected: guardDelete already applied the protected policy and
	// the user answered the force prompt for this exact branch, which is
	// the explicit approval the flag is meant to represent.
	if err := deleteBranchGuarded(ctx, r, cfg, target.Name, branchDeleteOpts{
		Force: force, AllowProtected: force,
	}); err != nil {
		fmt.Fprintln(w, "✗ delete failed: "+err.Error())
		if h := HintFrom(err); h != "" {
			fmt.Fprintln(w, "  hint: "+h)
		}
		return errSwitchActionRetry
	}
	fmt.Fprintf(w, "deleted %s\n", target.Name)
	return nil
}

// redirectIfWorktreeLocked checks whether branch is already checked out in
// another worktree. Used by direct-name paths (gk sw <name>, --main,
// --develop) where the user's intent is to switch HERE.
// Returns (false, err) with a clear message when locked; (false, nil) when free.
// Worktree removal is intentionally left to the user — gk sw must not destroy
// worktrees as a side-effect of a branch switch.
func redirectIfWorktreeLocked(ctx context.Context, r git.Runner, branch string) (bool, error) {
	wt := loadSwitchWorktrees(ctx, r)
	entry, locked := wt.byBranch[branch]
	if !locked {
		return false, nil
	}
	dirtyMap := loadWorktreeDirtyStates(ctx, switchWorktreeMap{
		byBranch: map[string]WorktreeEntry{entry.Branch: entry},
		others:   []WorktreeEntry{entry},
	})
	// Present BOTH paths — go there to work, or remove to switch here.
	// Telling users only how to remove a worktree feels like the wrong
	// answer to "I just want to switch branches" — `cd` is often the
	// less destructive choice when their real intent is to use that branch.
	_, isDirty := dirtyMap[entry.Branch]
	state := ""
	if isDirty {
		state = " (has uncommitted changes there)"
	}
	hint := fmt.Sprintf(
		"work on it there → cd %s\n        bring it here → gk worktree remove %s",
		entry.Path, entry.Path,
	)
	if isDirty {
		hint = fmt.Sprintf(
			"work on it there → cd %s\n        bring it here → commit/stash there, then gk worktree remove %s",
			entry.Path, entry.Path,
		)
	}
	return false, WithHint(
		fmt.Errorf("branch %q is checked out in another worktree at %s%s", branch, entry.Path, state),
		hint,
	)
}

func doSwitch(ctx context.Context, r git.Runner, w io.Writer, branch string, create, force, detach bool) error {
	// Guard: moving a protected branch (main/master/develop) INTO a linked
	// worktree locks it there, making it unusable in every other worktree —
	// almost always an accident (the user just wanted to look at main).
	// Confirm first. Exempt: the primary worktree (where this is normal),
	// -c (creating a branch can't trap an existing one), --detach (a
	// detached checkout doesn't lock the branch), and --force (explicit
	// override).
	if !create && !detach && !force {
		proceed, gerr := confirmProtectedWorktreeSwitch(ctx, r, branch)
		if gerr != nil {
			return gerr
		}
		if !proceed {
			return nil
		}
	}

	args := []string{"switch"}
	if create {
		args = append(args, "-c")
	}
	if force {
		args = append(args, "--discard-changes")
	}
	if detach {
		args = append(args, "--detach")
	}
	args = append(args, branch)

	_, stderr, err := r.Run(ctx, args...)
	if err != nil {
		// git refuses to switch mid-operation and prints its own advice
		// (e.g. `git rebase --quit`), which is wrong for gk. When we recognize
		// the in-progress operation, replace git's whole message with a clean
		// one plus a hint pointing at gk continue / gk abort — don't echo git's
		// stderr (it would surface the misleading --quit advice and duplicate
		// the text already carried by the wrapped ExitError).
		if st, derr := gitstate.Detect(ctx, RepoFlag()); derr == nil {
			if op := inProgressOp(st); op != "" {
				return WithHint(
					fmt.Errorf("cannot switch to %s: a %s is in progress", branch, op),
					inProgressHint(st),
				)
			}
		}
		return fmt.Errorf("git switch failed: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprintln(w, successLine("switched to", branch))
	return nil
}

// confirmProtectedWorktreeSwitch gates moving a protected branch into a
// linked worktree behind a y/N confirmation. It returns (true, nil) for
// every non-risky case — the primary worktree, or a non-protected branch —
// so the common path is a single `rev-parse`. On a non-interactive stream
// it refuses (returns an error with a --detach/--force hint) rather than
// silently trapping the branch.
func confirmProtectedWorktreeSwitch(ctx context.Context, r git.Runner, branch string) (bool, error) {
	if !isLinkedWorktree(ctx, r) {
		return true, nil
	}
	cfg, _ := config.Load(nil)
	if !isProtectedBranchName(branch, cfg.Branch.Protected) {
		return true, nil
	}
	title := fmt.Sprintf("Move protected branch %q into this linked worktree?", branch)
	desc := "It would be locked here and unusable in other worktrees. Use --detach to just view it."
	ok, err := ui.ConfirmTUI(ctx, title, desc, false)
	switch {
	case errors.Is(err, ui.ErrNonInteractive):
		return false, WithHint(
			fmt.Errorf("refusing to move protected branch %q into a linked worktree", branch),
			"use --detach to view it here, or --force to move it anyway")
	case errors.Is(err, ui.ErrPickerAborted):
		return false, nil
	case err != nil:
		return false, err
	}
	return ok, nil
}

// isLinkedWorktree reports whether the runner's cwd is a linked worktree
// (not the primary one), via the --git-dir vs --git-common-dir mismatch —
// the same signal gk's prompt-info uses. Any git error collapses to false
// (treat as primary) so the guard never blocks on a non-repo edge case.
func isLinkedWorktree(ctx context.Context, r git.Runner) bool {
	out, _, err := r.Run(ctx, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 2 {
		return false
	}
	return strings.TrimSpace(lines[0]) != strings.TrimSpace(lines[1])
}

// cfgProtected returns the configured protected-branch list, or nil when
// cfg is nil. Lets call sites forward the repo-correct list (loaded with
// --repo honored) into worktree-removal's orphan-branch guard instead of
// re-loading config against the cwd.
func cfgProtected(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	return cfg.Branch.Protected
}

// isProtectedBranchName reports whether branch is on the configured
// protected list (default: main/master/develop).
func isProtectedBranchName(branch string, protected []string) bool {
	for _, p := range protected {
		if p == branch {
			return true
		}
	}
	return false
}

// doSwitchTrack creates a local tracking branch from a remote ref and
// switches to it. Used when the interactive picker returns a remote-only
// entry: `gk sw` with the user selecting `○ experimental (from origin)`
// becomes `git switch --track origin/experimental`.
func doSwitchTrack(ctx context.Context, r git.Runner, w io.Writer, trackRef string, force, detach bool) error {
	args := []string{"switch", "--track"}
	if force {
		args = append(args, "--discard-changes")
	}
	if detach {
		args = append(args, "--detach")
	}
	args = append(args, trackRef)

	_, stderr, err := r.Run(ctx, args...)
	if err != nil {
		return fmt.Errorf("git switch --track %s failed: %s: %w",
			trackRef, strings.TrimSpace(string(stderr)), err)
	}
	short := trackRef
	if i := strings.IndexByte(trackRef, '/'); i >= 0 {
		short = trackRef[i+1:]
	}
	fmt.Fprintln(w, successLinef("switched to", "%s (tracking %s)", short, trackRef))
	return nil
}
