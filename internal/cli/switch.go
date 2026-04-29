package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
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
	cmd.Flags().BoolP("main", "m", false, "switch to the detected main/master branch")
	cmd.Flags().Bool("develop", false, "switch to the develop/dev branch")
	rootCmd.AddCommand(cmd)
}

func runSwitch(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	create, _ := cmd.Flags().GetBool("create")
	force, _ := cmd.Flags().GetBool("force")
	detach, _ := cmd.Flags().GetBool("detach")
	toMain, _ := cmd.Flags().GetBool("main")
	toDevelop, _ := cmd.Flags().GetBool("develop")

	if toMain && toDevelop {
		return fmt.Errorf("--main and --develop are mutually exclusive")
	}
	if (toMain || toDevelop) && (len(args) > 0 || create) {
		return fmt.Errorf("--main/--develop take no branch name and cannot combine with --create")
	}

	if toMain {
		cfg, _ := config.Load(cmd.Flags())
		name, err := resolveMainBranch(ctx, runner, client, cfg.Remote)
		if err != nil {
			return err
		}
		return doSwitch(ctx, runner, w, name, false, force, detach)
	}
	if toDevelop {
		name, err := resolveDevelopBranch(ctx, runner)
		if err != nil {
			return err
		}
		return doSwitch(ctx, runner, w, name, false, force, detach)
	}

	if len(args) == 1 {
		return doSwitch(ctx, runner, w, args[0], create, force, detach)
	}

	if create {
		return fmt.Errorf("--create requires a branch name")
	}

	cfg, _ := config.Load(cmd.Flags())
	pick, err := pickBranchForSwitch(ctx, runner, client, cfg, w, cmd)
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
func buildSwitchSubtitle(cur string, wt switchWorktreeMap, allRemotes []remoteBranchInfo, showRemotes bool) string {
	parts := make([]string, 0, 3)
	if cur != "" {
		parts = append(parts, "on: "+cur)
	}
	if wt.linked && wt.current.Path != "" {
		parts = append(parts, "worktree: "+wt.current.Path)
	}
	if !showRemotes && len(allRemotes) > 0 {
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
// since git would refuse the switch otherwise. Hotkeys (n/d/D) exit
// the picker so we can drive sub-prompts (text input, confirm),
// then re-enter on the next iteration.
func pickBranchForSwitch(ctx context.Context, runner git.Runner, client *git.Client, cfg *config.Config, w io.Writer, cmd *cobra.Command) (switchPick, error) {
	showRemotes := false
	for {
		local, err := listLocalBranches(ctx, runner)
		if err != nil {
			return switchPick{}, err
		}
		// Always enumerate remotes — we need the count to surface a
		// "N remote hidden (r)" hint even when not displaying them.
		allRemotes, rerr := listRemoteOnlyBranches(ctx, runner, local)
		if rerr != nil {
			allRemotes = nil
		}
		var remotes []remoteBranchInfo
		if showRemotes {
			remotes = allRemotes
		}

		cur, _ := client.CurrentBranch(ctx)
		remote := "origin"
		if cfg != nil && cfg.Remote != "" {
			remote = cfg.Remote
		}
		defaultBr, _ := resolveMainBranch(ctx, runner, client, remote)
		merged, _ := mergedBranches(ctx, runner, defaultBr)
		wt := loadSwitchWorktrees(ctx, runner)

		// Per-branch divergence vs default. One rev-list call per ref;
		// typically <50ms total. Skipped silently when there's no
		// resolvable default branch (bare repos, fresh clones).
		diff := computeSwitchDivergence(ctx, runner, defaultBr, local, allRemotes)

		// Per-worktree dirty state. Parallel git status per worktree;
		// each call bounded to 200ms. Missing in map = clean / unknown.
		dirty := loadWorktreeDirtyStates(ctx, wt)

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
		sort.Slice(remotes, func(i, j int) bool {
			return remotes[i].LastCommit.After(remotes[j].LastCommit)
		})

		items := buildSwitchItems(local, remotes, cur, wt, defaultBr, diff, dirty)
		if len(items) == 0 {
			placeholder := "(no branches — press n to create)"
			items = append(items, ui.PickerItem{
				Key:     "local:__placeholder__",
				Cells:   []string{placeholder, "", "", ""},
				Display: placeholder,
			})
		}

		extras := buildSwitchExtras(&showRemotes, &local, &remotes, allRemotes, &wt, cur, defaultBr, diff, dirty)
		subtitle := buildSwitchSubtitle(cur, wt, allRemotes, showRemotes)
		picker := &ui.TablePicker{
			Headers:  []string{"BRANCH", "UPSTREAM", "HASH", "AGE"},
			Extras:   extras,
			Subtitle: subtitle,
		}
		choice, err := picker.Pick(ctx, "switch", items)
		if err != nil {
			if errors.Is(err, ui.ErrPickerAborted) {
				return switchPick{}, WithHint(errors.New("aborted"), "pass a branch name directly: gk switch <name>")
			}
			return switchPick{}, err
		}

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
		case "d", "D":
			force := choice.ExtraAction == "D"
			if err := handleDeleteAction(ctx, runner, w, choice, cur, defaultBr, merged, force); err != nil {
				if errors.Is(err, ui.ErrPickerAborted) || errors.Is(err, errSwitchActionRetry) {
					continue
				}
				return switchPick{}, err
			}
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

// switchDivergence keys "local:<name>" or "remote:<trackRef>" to
// (ahead, behind) vs the default branch. Empty means "no diff data
// for this ref" — rendered as a bare source descriptor.
type switchDivergence map[string][2]int

// computeSwitchDivergence runs `rev-list --left-right --count` per
// ref against defaultBr. Returns an empty map when defaultBr is
// missing (bare repos, no main/master) so callers can format
// gracefully without divergence info.
func computeSwitchDivergence(ctx context.Context, runner git.Runner, defaultBr string, local []branchInfo, remotes []remoteBranchInfo) switchDivergence {
	out := switchDivergence{}
	if defaultBr == "" {
		return out
	}
	for _, b := range local {
		if b.Name == defaultBr {
			continue
		}
		if a, bh, ok := branchDivergence(ctx, runner, defaultBr, b.Name); ok {
			out[keyLocalPrefix+b.Name] = [2]int{a, bh}
		}
	}
	for _, r := range remotes {
		if a, bh, ok := branchDivergence(ctx, runner, defaultBr, r.TrackRef); ok {
			out[keyRemotePrefix+r.TrackRef] = [2]int{a, bh}
		}
	}
	return out
}

// formatSwitchDiff renders divergence "↑3 ↓5" / "↑3" / "↓5" / "" .
func formatSwitchDiff(d [2]int) string {
	ahead, behind := d[0], d[1]
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

// colorSwitchDiff renders divergence with green ↑ and red ↓ for
// at-a-glance "ahead/behind default" cues. Empty on no diff. Used
// inside Cells, so it must use fg-only-reset helpers (cellGreen /
// cellRed) — `fatih/color`'s full reset would break the bubbles/table
// Selected-row background mid-cell.
func colorSwitchDiff(d [2]int) string {
	ahead, behind := d[0], d[1]
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

func buildSwitchItems(local []branchInfo, remotes []remoteBranchInfo, cur string, wt switchWorktreeMap, defaultBr string, diff switchDivergence, dirty map[string]git.DirtyFlags) []ui.PickerItem {
	items := make([]ui.PickerItem, 0, len(local)+len(remotes))

	for _, b := range local {
		entry, locked := wt.byBranch[b.Name]
		isCurrent := b.Name == cur
		isDefault := b.Name == defaultBr
		var source, coloredSource string
		switch {
		case isDefault:
			source = "(default)"
			coloredSource = cellFaint(source)
		case locked:
			tag := filepath.Base(entry.Path)
			source = "wt: " + tag
			coloredSource = cellYellow("wt: ") + tag
		case b.Gone:
			source = "(gone)"
			coloredSource = cellFaint(source)
		case b.Upstream != "":
			source = "↑ " + b.Upstream
			coloredSource = source
		default:
			source = "(local)"
			coloredSource = cellFaint(source)
		}
		dKey := keyLocalPrefix + b.Name
		upstream := composeUpstreamCell(diff[dKey], source, isDefault)
		coloredUpstream := composeColoredUpstreamCell(diff[dKey], coloredSource, isDefault)
		// Display marker stays via fatih/color — it's only used by
		// FzfPicker fallback which doesn't have row-level styles.
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
		age := shortAge(b.LastCommit)
		items = append(items, ui.PickerItem{
			Key:   keyLocalPrefix + b.Name,
			Cells: []string{cellMarker + " " + b.Name + coloredDirtyTag, coloredUpstream, b.Hash, age},
			Display: fmt.Sprintf("%s  %-36s  %-32s  %-8s  %s",
				displayMarker, b.Name+coloredDirtyTag, upstream, b.Hash, age,
			),
		})
	}

	for _, r := range remotes {
		age := shortAge(r.LastCommit)
		source := "remote: " + r.Remote
		coloredSource := cellCyan("remote: ") + r.Remote
		upstream := composeUpstreamCell(diff[keyRemotePrefix+r.TrackRef], source, false)
		coloredUpstream := composeColoredUpstreamCell(diff[keyRemotePrefix+r.TrackRef], coloredSource, false)
		items = append(items, ui.PickerItem{
			Key:   keyRemotePrefix + r.TrackRef,
			Cells: []string{cellCyan("○") + " " + r.Name, coloredUpstream, r.Hash, age},
			Display: fmt.Sprintf("%s  %-36s  %-32s  %-8s  %s",
				color.CyanString("○"), r.Name, upstream, r.Hash, age,
			),
		})
	}
	return items
}

// composeUpstreamCell merges divergence + source into a single cell:
//
//	"(default)"            — the default branch itself, never has diff
//	"<source>"             — synced or no diff data
//	"↑3 ↓1  <source>"      — diverged
func composeUpstreamCell(d [2]int, source string, isDefault bool) string {
	if isDefault {
		return source
	}
	diff := formatSwitchDiff(d)
	if diff == "" {
		return source
	}
	return diff + "  " + source
}

// composeColoredUpstreamCell is the colored version: green ↑, red ↓.
func composeColoredUpstreamCell(d [2]int, source string, isDefault bool) string {
	if isDefault {
		return source
	}
	diff := colorSwitchDiff(d)
	if diff == "" {
		return source
	}
	return diff + "  " + source
}

// buildSwitchExtras wires the n/d/D/r hotkeys. Only `r` mutates
// state in place (toggle remote visibility); the rest exit the
// picker so the caller can drive prompts/confirms outside the
// bubbletea program. allRemotes is the full enumerated set so the
// closure can populate *remotes on toggle-on without re-listing.
func buildSwitchExtras(showRemotes *bool, local *[]branchInfo, remotes *[]remoteBranchInfo, allRemotes []remoteBranchInfo, wt *switchWorktreeMap, cur, defaultBr string, diff switchDivergence, dirty map[string]git.DirtyFlags) []ui.TablePickerExtraKey {
	rebuild := func() ([]ui.PickerItem, []string, error) {
		items := buildSwitchItems(*local, *remotes, cur, *wt, defaultBr, diff, dirty)
		return items, []string{"BRANCH", "UPSTREAM", "HASH", "AGE"}, nil
	}
	return []ui.TablePickerExtraKey{
		{Key: "n", Help: "n new", Exit: true},
		{Key: "d", Help: "d delete", Exit: true},
		{Key: "D", Help: "D force", Exit: true},
		{
			Key:  "r",
			Help: "r remotes",
			OnPress: func() ([]ui.PickerItem, []string, error) {
				*showRemotes = !*showRemotes
				if *showRemotes {
					*remotes = allRemotes
				} else {
					*remotes = nil
				}
				return rebuild()
			},
		},
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

// guardDelete returns nil if (target, force) is safe to delete,
// otherwise an error explaining why. Pure function — no I/O.
func guardDelete(target targetBranchInfo, current, defaultBr string, merged map[string]bool, force bool) error {
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
	if defaultBr != "" && target.Name == defaultBr {
		return errors.New("refusing to delete default branch")
	}
	if !force && !merged[target.Name] {
		return WithHint(fmt.Errorf("branch %q has unmerged commits", target.Name),
			"press D to force delete (unmerged work will be lost)")
	}
	return nil
}

// handleDeleteAction runs the d/D action end-to-end: guard → confirm →
// `git branch -d|-D`. Returns nil on success (caller re-lists),
// errSwitchActionRetry when the user cancelled the confirm or the
// guard rejected, and a real error only on git failure.
func handleDeleteAction(ctx context.Context, r git.Runner, w io.Writer, choice ui.PickerItem, current, defaultBr string, merged map[string]bool, force bool) error {
	target := decodeBranchTarget(choice)
	if err := guardDelete(target, current, defaultBr, merged, force); err != nil {
		fmt.Fprintln(w, "✗ "+err.Error())
		if h := HintFrom(err); h != "" {
			fmt.Fprintln(w, "  hint: "+h)
		}
		return errSwitchActionRetry
	}

	title := fmt.Sprintf("Delete branch %q?", target.Name)
	desc := "merged into " + defaultBr
	if force {
		title = fmt.Sprintf("FORCE delete %q?", target.Name)
		desc = "unmerged work will be lost — this cannot be undone"
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

	flag := "-d"
	if force {
		flag = "-D"
	}
	if _, stderr, err := r.Run(ctx, "branch", flag, target.Name); err != nil {
		fmt.Fprintln(w, "✗ delete failed: "+strings.TrimSpace(string(stderr)))
		return errSwitchActionRetry
	}
	fmt.Fprintf(w, "deleted %s\n", target.Name)
	return nil
}

func doSwitch(ctx context.Context, r git.Runner, w io.Writer, branch string, create, force, detach bool) error {
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
		return fmt.Errorf("git switch failed: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprintf(w, "switched to %s\n", branch)
	return nil
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
	fmt.Fprintf(w, "switched to %s (tracking %s)\n", short, trackRef)
	return nil
}
