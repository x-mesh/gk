package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	// `gk fleet` is the deprecated spelling of `gk watch` (see watch.go for
	// the canonical command and its documentation). Kept for one release as a
	// hidden alias because orchestrators pipe `gk fleet --events` / `--json`:
	// the machine contracts keep working while the stderr notice steers
	// humans to the new name. One behavioral difference is preserved: fleet
	// never auto-routes a single-worktree repo into the plain watch feed.
	cmd := &cobra.Command{
		Use:        "fleet",
		Short:      "Deprecated alias of `gk watch` (always the dashboard view)",
		Hidden:     true,
		Deprecated: "use `gk watch` — same dashboard, clearer name. `gk fleet` will be removed in a future release.",
		Args:       cobra.NoArgs,
		RunE:       runFleet,
	}
	addFleetFlags(cmd)
	rootCmd.AddCommand(cmd)
}

// addFleetFlags registers the fleet flag set — shared with `gk watch`, whose
// resolve helpers look the flags up by name on whichever command ran.
func addFleetFlags(cmd *cobra.Command) {
	cmd.Flags().Int("interval", 2, "poll interval in seconds (TUI mode)")
	cmd.Flags().StringSlice("repos", nil, "explicit repo paths to watch (multi-repo)")
	cmd.Flags().StringSlice("scan", nil, "directory roots to scan for git repos (multi-repo)")
	cmd.Flags().Bool("all", false, "watch sibling repos of the current repo (multi-repo)")
	cmd.Flags().Int("depth", 2, "max scan recursion depth for --scan")
	cmd.Flags().Bool("feed-stats", true, "show +/- line counts and changed-function names in the change feed (--feed-stats=false to disable the extra git diff calls per poll)")
	cmd.Flags().Bool("events", false, "stream fleet changes as NDJSON events instead of a dashboard (for orchestrators)")
	cmd.Flags().String("filter", "", "initial view filter: all | active | busy | stuck (default: active in multi-repo, all in single-repo; f cycles)")
}

// fleetEntryJSON is the per-worktree fleet record — the contract `gk fleet
// --json` emits and the TUI consumes. Status is a derived at-a-glance roll-up
// so a reader does not have to interpret the dirty counts themselves. The
// enrichment fields below answer "who is stuck / stale / ready to land"
// without a separate probe; the schema is append-only.
type fleetEntryJSON struct {
	Path    string            `json:"path"`
	Branch  string            `json:"branch,omitempty"`
	Current bool              `json:"current,omitempty"`
	Ahead   int               `json:"ahead,omitempty"`
	Behind  int               `json:"behind,omitempty"`
	Dirty   *contextDirtyJSON `json:"dirty,omitempty"`
	Status  string            `json:"status"` // clean | dirty | conflict | paused | ahead | behind | diverged

	// ActiveAgoS is seconds since the worktree's last activity: the HEAD commit
	// time, advanced to the newest dirty-file mtime so an agent editing without
	// committing still reads fresh. Omitted (0) when unknown.
	ActiveAgoS int `json:"active_ago_s,omitempty"`
	// Operation names a paused rebase/merge/cherry-pick/revert ("rebase 2/5") —
	// the "who is stuck" signal a plain dirty count misses. Resume is how to
	// finish it.
	Operation string `json:"operation,omitempty"`
	Resume    string `json:"resume,omitempty"`
	// Parent is the gk-parent / fork branch; ParentBehind is how many commits
	// the parent has that this branch lacks (sync-before-land signal). LandReady
	// is true when the branch is already merged into base — safe to reap.
	Parent       string `json:"parent,omitempty"`
	ParentBehind int    `json:"parent_behind,omitempty"`
	LandReady    bool   `json:"land_ready,omitempty"`
	// LastChange is the most recently modified changed file (repo-relative) —
	// "what did the agent just touch", where ActiveAgoS only says when.
	LastChange string `json:"last_change,omitempty"`

	// Files/Added/Removed are the worktree's uncommitted diffstat: how much
	// work is in flight, where Dirty only counts files by state (S/U/?). Files
	// is free (the porcelain scan already lists them); Added/Removed come from
	// the feed-stats diff runs and stay 0 when --feed-stats=false.
	Files   int `json:"files,omitempty"`
	Added   int `json:"added,omitempty"`
	Removed int `json:"removed,omitempty"`

	// Repo/RepoRoot identify which repository this worktree belongs to in
	// multi-repo mode; filled in single-repo mode too so the JSON contract is
	// uniform across modes (consumers group by repo_root). Error is set only on a
	// synthetic entry standing in for a repo whose gather failed or timed out, so
	// a slow repo never silently vanishes from the flat snapshot.
	Repo     string `json:"repo,omitempty"`
	RepoRoot string `json:"repo_root,omitempty"`
	Error    string `json:"error,omitempty"`

	// lastActive is the absolute timestamp behind ActiveAgoS. Unexported (never
	// serialized) so the live TUI re-derives the relative age every clock tick
	// instead of freezing it at poll time.
	lastActive time.Time
	// sigs is the per-path change signature set from the consolidated scan —
	// the feed's diff input. Unexported: the JSON contract carries events, not
	// raw signatures.
	sigs map[string]fileSig
}

const fleetRepoTimeout = 3 * time.Second

func runFleet(cmd *cobra.Command, _ []string) error {
	return runFleetCore(cmd, false)
}

// runFleetCore backs both `gk fleet` and `gk watch`: identical machinery,
// except watch (autoSingleWatch) routes a single-worktree repo straight into
// the `gk status --watch` live feed — an overview adds nothing when there is
// only one thing to oversee. The machine-readable paths (--json / GK_AGENT /
// --events) never reroute: their contract is fleet's regardless of count.
func runFleetCore(cmd *cobra.Command, autoSingleWatch bool) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	// Multi-repo mode is opt-in (--repos / --scan / --all). The single-repo path
	// below is untouched, so a bare `gk fleet` inside a repo behaves as before.
	ids, multi, err := resolveFleetRepos(ctx, cmd)
	if err != nil {
		return err
	}
	feedStats := resolveFleetFeedStats(cmd)
	streamEvents, _ := cmd.Flags().GetBool("events")
	// Validate --filter up front: a typo should fail loudly on every path,
	// not just the interactive one (the static/JSON paths return earlier).
	if _, ferr := resolveFleetFilter(cmd, multi); ferr != nil {
		return ferr
	}
	if multi {
		sem := newFleetLimiter(fleetConcurrency())
		if streamEvents {
			gather := func(gctx context.Context) ([]fleetEntryJSON, error) {
				return gatherFleetMulti(gctx, ids, sem, fleetRepoTimeout, feedStats), nil
			}
			return runFleetEvents(ctx, cmd, gather,
				time.Duration(resolveFleetInterval(cmd, true))*time.Second, fleetNotifyConfig())
		}
		entries := gatherFleetMulti(ctx, ids, sem, fleetRepoTimeout, feedStats)
		if JSONOut() {
			return emitAgentResult(cmd.OutOrStdout(), entries)
		}
		// No TTY (pipe/redirect/CI): static grouped snapshot, all repos expanded.
		if !ui.IsTerminal() {
			fmt.Fprintln(cmd.OutOrStdout(), renderFleetGrouped(buildFleetRows(entries, nil), -1, time.Time{}, 0, fleetDetailOff, nil, 0, 0, fleetChurn{}, 0))
			return nil
		}
		interval := resolveFleetInterval(cmd, true)
		filter, _ := resolveFleetFilter(cmd, true) // validated above
		return runFleetMultiTUI(ctx, cmd, ids, sem, entries, time.Duration(interval)*time.Second, feedStats, filter)
	}

	// GIT_OPTIONAL_LOCKS=0 for every probe: fleet polls while agents commit in
	// those very worktrees — an optional-lock status would contend on their
	// .git/index.lock (multi-repo mode already runs this way).
	runner := &git.ExecRunner{Dir: RepoFlag(), ExtraEnv: []string{"GIT_OPTIONAL_LOCKS=0"}}

	if streamEvents {
		gather := func(gctx context.Context) ([]fleetEntryJSON, error) {
			return gatherFleet(gctx, runner, feedStats)
		}
		return runFleetEvents(ctx, cmd, gather,
			time.Duration(resolveFleetInterval(cmd, false))*time.Second, fleetNotifyConfig())
	}

	entries, err := gatherFleet(ctx, runner, feedStats)
	if err != nil {
		return err
	}

	// Machine-readable snapshot: emit once and exit. A GUI/agent polls this.
	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), entries)
	}

	// No TTY (pipe/redirect/CI): render a static snapshot rather than starting
	// an interactive program that cannot read input. No live clock there.
	if !ui.IsTerminal() {
		fmt.Fprintln(cmd.OutOrStdout(), renderFleet(fleetView{entries: entries, cursor: -1}))
		return nil
	}

	interval := resolveFleetInterval(cmd, false)

	// `gk watch` with one worktree: the single-worktree live feed IS the right
	// zoom level — skip the one-row table and open it directly.
	if autoSingleWatch && len(entries) == 1 {
		statusWatchInterval = time.Duration(interval) * time.Second
		return runChangeWatch(cmd)
	}
	filter, _ := resolveFleetFilter(cmd, false) // validated above
	return runFleetTUI(ctx, cmd, runner, entries, time.Duration(interval)*time.Second, feedStats, filter)
}

// fleetNotifyConfig loads the opt-in fleet.notify hook map (transition kind →
// shell command). Empty when unconfigured or config is unreadable.
func fleetNotifyConfig() map[string]string {
	if cfg, err := config.Load(nil); err == nil && cfg != nil {
		return cfg.Fleet.Notify
	}
	return nil
}

// resolveFleetFeedStats: the --feed-stats flag when set, else fleet.feed_stats
// from config, else ON. Default-on since the feed should read like
// `gk status --watch` (file · function · ±) out of the box; the cost — two
// `git diff -U0` runs per DIRTY worktree per poll — only accrues where work
// is actually happening, which is exactly where the detail matters.
func resolveFleetFeedStats(cmd *cobra.Command) bool {
	if cmd.Flags().Changed("feed-stats") {
		on, _ := cmd.Flags().GetBool("feed-stats")
		return on
	}
	if cfg, err := config.Load(nil); err == nil && cfg != nil && cfg.Fleet.FeedStats != nil {
		return *cfg.Fleet.FeedStats
	}
	return true
}

// gatherFleet builds the fleet snapshot, reusing the same worktree enrichment
// `gk worktree list` uses (porcelain parse + branch ahead/behind + per-path
// dirty probe) plus the supervision fields. The bare worktree is skipped — it
// holds no working state. Per-worktree enrichment runs concurrently: each entry
// is independent, so a handful of worktrees stay snappy under a 2s poll.
func gatherFleet(ctx context.Context, runner *git.ExecRunner, withStats bool) ([]fleetEntryJSON, error) {
	root := runner.Dir
	if root == "" {
		root, _ = os.Getwd()
	}
	if r, _, ok := repoRootAndCommonDir(ctx, root); ok {
		root = r
	}
	sem := newFleetLimiter(fleetConcurrency())
	return gatherFleetRepo(ctx, runner, filepath.Base(root), root, currentWorktreePath(ctx, runner), sem, withStats)
}

// gatherFleetRepo enriches one repo's worktrees, tagging every entry with the
// repo label/root. sem bounds the per-worktree enrich goroutines — the bulk of
// the git subprocesses — across every repo, so multi-repo mode does not spawn
// repo×worktree enrichers at once. The repo-level fan-out and each repo's
// one-shot `worktree list`/meta queries run outside sem; gating the repo level
// on the same semaphore would deadlock (repos holding every slot while their
// worktrees wait for one).
func gatherFleetRepo(ctx context.Context, runner *git.ExecRunner, label, root, current string, sem chan struct{}, withStats bool) ([]fleetEntryJSON, error) {
	stdout, stderr, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("fleet: worktree list: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	entries := parseWorktreePorcelain(string(stdout))
	meta, base := loadWorktreeBranchMetaWithBase(ctx, runner)
	now := time.Now()

	live := make([]WorktreeEntry, 0, len(entries))
	for _, e := range entries {
		if e.Bare {
			continue
		}
		live = append(live, e)
	}

	out := make([]fleetEntryJSON, len(live))
	var wg sync.WaitGroup
	for i := range live {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if sem != nil {
				sem <- struct{}{}
				defer func() { <-sem }()
			}
			e := enrichFleetEntry(ctx, live[i], meta, current, base, now, withStats)
			e.Repo = label
			e.RepoRoot = root
			out[i] = e
		}(i)
	}
	wg.Wait()

	// Current worktree first, then by branch — a stable order so the TUI cursor
	// does not jump around between polls.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Current != out[j].Current {
			return out[i].Current
		}
		return out[i].Branch < out[j].Branch
	})
	return out, nil
}

// resolveFleetRepos returns the discovered repo set and whether multi-repo mode
// is active. Multi-repo is opt-in via --repos, --scan, or --all; a bare call
// stays single-repo (multi=false), preserving the legacy behaviour.
func resolveFleetRepos(ctx context.Context, cmd *cobra.Command) ([]repoIdent, bool, error) {
	reposFlag, _ := cmd.Flags().GetStringSlice("repos")
	scanFlag, _ := cmd.Flags().GetStringSlice("scan")
	all, _ := cmd.Flags().GetBool("all")
	depth, _ := cmd.Flags().GetInt("depth")
	depthSet := cmd.Flags().Changed("depth")

	var fc config.FleetConfig
	if cfg, err := config.Load(nil); err == nil && cfg != nil {
		fc = cfg.Fleet
	}
	if !depthSet && fc.Depth > 0 {
		depth = fc.Depth
	}

	flagMulti := len(reposFlag) > 0 || len(scanFlag) > 0 || all
	var repos, scan []string
	switch {
	case flagMulti:
		repos, scan = reposFlag, scanFlag
		if all && len(repos) == 0 && len(scan) == 0 {
			// --all with no explicit targets: prefer config, else scan the parent
			// directory of the current repo so sibling projects show up (the common
			// multi-agent layout).
			if len(fc.Repos) > 0 || len(fc.Scan) > 0 {
				repos, scan = fc.Repos, fc.Scan
			} else if root := currentRepoRoot(ctx); root != "" {
				scan = []string{filepath.Dir(root)}
				if !depthSet && fc.Depth == 0 {
					depth = 1
				}
			}
		}
	default:
		// No multi flags. Inside a repo a bare run stays single-repo (use
		// --all to override). OUTSIDE a repo, multi-repo is the only sensible
		// reading: prefer the configured repo set, else scan the current
		// directory one level down — so `cd ~/work && gk watch` just works as
		// the "everything I'm running here" dashboard, zero flags. Depth 1
		// keeps the scan from wandering into vendored/archived trees; --scan
		// with --depth remains the explicit deeper form.
		if currentRepoRoot(ctx) != "" {
			return nil, false, nil
		}
		if len(fc.Repos) > 0 || len(fc.Scan) > 0 {
			repos, scan = fc.Repos, fc.Scan
		} else {
			cwd := RepoFlag()
			if cwd == "" {
				var werr error
				if cwd, werr = os.Getwd(); werr != nil {
					// "Can't tell where I am" must not degrade into a
					// misleading single-repo run that then fails with
					// "not a git repository".
					return nil, false, fmt.Errorf("gk watch: cannot determine the working directory: %w", werr)
				}
			}
			scan = []string{cwd}
			if !depthSet && fc.Depth == 0 {
				depth = 1
			}
		}
	}

	ids, err := discoverRepos(ctx, repos, scan, fc.Exclude, depth)
	if err != nil {
		return nil, true, err
	}
	if len(ids) == 0 {
		return nil, true, fmt.Errorf("gk watch: no git repositories found; pass --scan <dir> or --repos <path>")
	}
	return ids, true, nil
}

// processCwdWorktree returns the toplevel of the worktree the process is
// standing in (honoring --repo), or "" when not inside one.
func processCwdWorktree(ctx context.Context) string {
	dir := RepoFlag()
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if dir == "" {
		return ""
	}
	return repoToplevel(ctx, &git.ExecRunner{Dir: dir})
}

// currentRepoRoot returns the canonical root of the repo containing the current
// directory (or --repo), or "" when not inside a repo.
func currentRepoRoot(ctx context.Context) string {
	cwd := RepoFlag()
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if root, _, ok := repoRootAndCommonDir(ctx, cwd); ok {
		return root
	}
	return ""
}

// resolveFleetFilter is the dashboard's initial view filter: the --filter
// flag when set (unknown value = error), else fleet.filter from config
// (unknown value falls through — a config typo shouldn't brick the TUI),
// else the mode default: `active` for a multi-repo scan (mostly-idle project
// piles are the norm there; the dashboard is for what's being worked on),
// `all` for a single repo's few worktrees. f cycles at runtime either way.
func resolveFleetFilter(cmd *cobra.Command, multi bool) (int, error) {
	if cmd.Flags().Changed("filter") {
		name, _ := cmd.Flags().GetString("filter")
		mode, ok := fleetFilterByName(name)
		if !ok {
			return 0, WithHint(
				fmt.Errorf("gk watch: unknown --filter %q", name),
				"use one of: all, active, busy, stuck",
			)
		}
		return mode, nil
	}
	if cfg, err := config.Load(nil); err == nil && cfg != nil && cfg.Fleet.Filter != "" {
		if mode, ok := fleetFilterByName(cfg.Fleet.Filter); ok {
			return mode, nil
		}
	}
	if multi {
		return fleetFilterActive, nil
	}
	return fleetFilterAll, nil
}

// resolveFleetInterval is the TUI poll interval: the --interval flag when the
// user set it, else fleet.interval from config, else a default that scales
// with scope — 2s single-repo, 5s multi-repo. A multi gather fans out
// repo×worktree subprocesses (measured ~0.5s wall for 21 repos), so the
// polling FALLBACK (no fsnotify) shouldn't burn that every 2 seconds; with
// fsnotify active the heartbeat floor (12s) makes this moot either way.
func resolveFleetInterval(cmd *cobra.Command, multi bool) int {
	if cmd.Flags().Changed("interval") {
		if n, _ := cmd.Flags().GetInt("interval"); n >= 1 {
			return n
		}
		return 1
	}
	if cfg, err := config.Load(nil); err == nil && cfg != nil && cfg.Fleet.Interval > 0 {
		return cfg.Fleet.Interval
	}
	if multi {
		return 5
	}
	return 2
}

// gatherFleetMulti gathers several repos concurrently into one flat, sorted entry
// list. Each repo is isolated by a timeout; a failed or timed-out repo becomes a
// single synthetic status:"error" entry so it never silently vanishes from the
// snapshot. GIT_OPTIONAL_LOCKS=0 keeps fleet's read-only probes from contending
// on index.lock with the agents editing in those repos.
func gatherFleetMulti(ctx context.Context, repos []repoIdent, sem chan struct{}, perRepo time.Duration, withStats bool) []fleetEntryJSON {
	// Current must mean "the worktree the PROCESS is standing in" — computed
	// once from the process location, not per repo. Each scanned repo has a
	// checked-out root worktree, and marking those current (the old per-repo
	// probe) branded every repo "active", neutering the activity filter and
	// handing watchers to all of them. In a parent-directory scan the honest
	// answer is usually: none.
	current := processCwdWorktree(ctx)
	out := make([][]fleetEntryJSON, len(repos))
	var wg sync.WaitGroup
	for i := range repos {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := repos[i]
			rctx, cancel := context.WithTimeout(ctx, perRepo)
			defer cancel()
			runner := &git.ExecRunner{Dir: id.Root, ExtraEnv: []string{"GIT_OPTIONAL_LOCKS=0"}}
			entries, err := gatherFleetRepo(rctx, runner, id.Label, id.Root, current, sem, withStats)
			if err != nil {
				out[i] = []fleetEntryJSON{{
					Repo:     id.Label,
					RepoRoot: id.Root,
					Path:     id.Root,
					Status:   "error",
					Error:    fleetGatherErr(err),
				}}
				return
			}
			out[i] = entries
		}(i)
	}
	wg.Wait()

	var all []fleetEntryJSON
	for _, e := range out {
		all = append(all, e...)
	}
	// repo_root groups, then current-first / by-branch within each repo — stable
	// so the cursor does not jump between polls.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].RepoRoot != all[j].RepoRoot {
			return all[i].RepoRoot < all[j].RepoRoot
		}
		if all[i].Current != all[j].Current {
			return all[i].Current
		}
		return all[i].Branch < all[j].Branch
	})
	return all
}

func fleetGatherErr(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return strings.TrimSpace(err.Error())
}

// enrichFleetEntry turns one worktree porcelain record into the full fleet
// entry: dirty roll-up, paused-op detection, activity staleness, and
// parent/land-readiness. Every probe is best-effort — a failure degrades the
// affected field, never the whole entry.
func enrichFleetEntry(ctx context.Context, e WorktreeEntry, meta map[string]worktreeBranchMeta, current, base string, now time.Time, withStats bool) fleetEntryJSON {
	f := fleetEntryJSON{
		Path:    e.Path,
		Branch:  e.Branch,
		Current: current != "" && filepath.Clean(e.Path) == filepath.Clean(current),
	}
	if e.Detached {
		f.Branch = "(detached)"
	}
	m, hasMeta := meta[e.Branch]
	if hasMeta {
		f.Ahead, f.Behind = m.Ahead, m.Behind
		f.Parent = m.ForkBranch
	}
	wr := &git.ExecRunner{Dir: e.Path}

	// One consolidated scan replaces the former dirty-count + newest-mtime
	// pair of porcelain runs: counts, per-path signatures (the feed's diff
	// input), and the most recently touched file all come from a single
	// --no-optional-locks pass.
	scan := scanWorktreeChanges(ctx, wr, e.Path, withStats)
	f.Dirty = dirtyPtrIfAny(scan.dirty)
	f.sigs = scan.sigs
	f.LastChange = scan.newestPath
	f.Files, f.Added, f.Removed = scanTotals(scan.sigs)

	// Paused operation — the "who is stuck" signal a dirty count alone misses.
	if st, derr := gitstate.Detect(ctx, e.Path); derr == nil && st != nil {
		if op := fleetOperationLabel(st); op != "" {
			f.Operation = op
			f.Resume = selfCmd("continue")
		}
	}

	// Activity: HEAD commit time, advanced to the newest changed-file mtime so
	// an agent mid-edit (no commit yet) still reads "now".
	active := m.LastCommit // zero when meta missing
	if scan.newestMtime.After(active) {
		active = scan.newestMtime
	}
	if !active.IsZero() {
		f.lastActive = active
		if d := now.Sub(active); d > 0 {
			f.ActiveAgoS = int(d.Seconds())
		}
	}

	// Parent drift + land-readiness — "which can I sync / reap".
	if f.Parent != "" && !e.Detached && e.Branch != "" {
		if n, ok := revListCount(ctx, wr, e.Branch+".."+f.Parent); ok {
			f.ParentBehind = n
		}
	}
	if base != "" && e.Branch != "" && e.Branch != base && !e.Detached {
		// Merged into base ⇒ all commits are in base ⇒ safe to reap.
		if _, _, err := wr.Run(ctx, "merge-base", "--is-ancestor", e.Branch, base); err == nil {
			f.LandReady = true
		}
	}

	f.Status = fleetStatus(f)
	return f
}

// fleetOperationLabel renders a paused git operation as a compact label,
// carrying rebase progress (step/total) when git records it.
func fleetOperationLabel(st *gitstate.State) string {
	switch st.Kind {
	case gitstate.StateRebaseMerge, gitstate.StateRebaseApply:
		if st.Total > 0 {
			return fmt.Sprintf("rebase %d/%d", st.Current, st.Total)
		}
		return "rebase"
	case gitstate.StateMerge:
		return "merge"
	case gitstate.StateCherryPick:
		return "cherry-pick"
	case gitstate.StateRevert:
		return "revert"
	case gitstate.StateBisect:
		return "bisect"
	default:
		return ""
	}
}

// revListCount returns `git rev-list --count <range>`; ok is false on any error
// so callers leave the field at zero rather than guess.
func revListCount(ctx context.Context, runner *git.ExecRunner, rng string) (int, bool) {
	out, _, err := runner.Run(ctx, "rev-list", "--count", rng)
	if err != nil {
		return 0, false
	}
	n, perr := parsePositiveInt(strings.TrimSpace(string(out)))
	if perr != nil {
		return 0, false
	}
	return n, true
}

func fleetStatus(f fleetEntryJSON) string {
	// A paused operation outranks everything: it is the one state that needs an
	// explicit resume/abort before any other work can proceed.
	if f.Operation != "" {
		return "paused"
	}
	if f.Dirty != nil && f.Dirty.Conflicts > 0 {
		return "conflict"
	}
	if f.Dirty != nil && f.Dirty.Staged+f.Dirty.Unstaged+f.Dirty.Untracked > 0 {
		return "dirty"
	}
	switch {
	case f.Ahead > 0 && f.Behind > 0:
		return "diverged"
	case f.Ahead > 0:
		return "ahead"
	case f.Behind > 0:
		return "behind"
	default:
		return "clean"
	}
}

// --- rendering ---

// Fleet layout budget. The dashboard is a full-screen alt-screen view, so a
// wide terminal gets wide columns instead of an 80-column island in the corner:
// fleetWidth passes the real width through (floored so the fixed columns can't
// collide) and fleetColumns hands the slack to the two columns that actually
// carry information — the branch and the last-changed file.
const (
	fleetDefaultWidth = 80 // unknown terminal width (0): a sane static default
	fleetMinWidth     = 48
)

func fleetWidth(w int) int {
	if w <= 0 {
		return fleetDefaultWidth
	}
	if w < fleetMinWidth {
		return fleetMinWidth
	}
	return w
}

// fleetCols is the elastic part of a table row. stat is 0 when the diffstat
// column doesn't fit — a narrow terminal keeps the pre-diffstat layout.
type fleetCols struct{ branch, file, stat int }

// fleetStatW holds "+99,999 −9,999" without wrapping — an agent's bulk edit
// reaches five figures, and an overflowing cell would shove the age column out
// of line on that row alone.
const fleetStatW = 14

// fleetColumns sizes the elastic columns for a render width. indent is the row
// prefix cost (caret + dot, plus the group indent in multi-repo mode); the
// remaining columns (sync, dirty, age) and their gutters are fixed. The
// diffstat column is claimed first (it is the reason to look at the row at
// all), then branch grows — the row's identity — and the file column soaks the
// rest, both bounded so a 300-column terminal doesn't stretch them past
// readability.
func fleetColumns(width, indent int) fleetCols {
	const (
		branchMin, branchMax = 18, 34
		fileMin, fileMax     = 14, 64
		fixed                = 8 + 11 + 5 + 2*4 // sync + dirty + age + gutters
	)
	c := fleetCols{branch: branchMin, file: fileMin}
	spare := width - (indent + branchMin + fileMin + fixed)
	if spare >= fleetStatW+2 {
		c.stat = fleetStatW
		spare -= fleetStatW + 2
	}
	if spare <= 0 {
		return c
	}
	grow := min(spare, branchMax-branchMin)
	c.branch += grow
	c.file += min(spare-grow, fileMax-fileMin)
	return c
}

// fleetStatStart is the column the diffstat cell begins at, so a repo group's
// roll-up lands directly above the worktree numbers it sums.
func fleetStatStart(cols fleetCols, indent int) int {
	return indent + cols.branch + 3 + 2 + 8 + 2 + 11 + 2 + cols.file + 2
}

// fleetStatCell renders one row's uncommitted diffstat. Colored, so it must be
// padded by width (padCell), never by printf: %-*s counts ANSI bytes.
func fleetStatCell(added, removed int) string {
	if added == 0 && removed == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("·")
	}
	var parts []string
	if added > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render("+"+commaInt(added)))
	}
	if removed > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render("−"+commaInt(removed)))
	}
	return strings.Join(parts, " ")
}

// padCell right-pads a styled cell to w visible columns (a no-op when it
// already overflows — clipping colored text would cut an escape sequence).
func padCell(s string, w int) string {
	if gap := w - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// commaInt groups thousands: line counts run to five figures on an agent's
// bulk edit, and `+12483` is a number you have to stop and parse.
func commaInt(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func fleetStatusColor(status string) lipgloss.Color {
	switch status {
	case "error":
		return lipgloss.Color("196") // bright red — unreachable repo
	case "conflict":
		return lipgloss.Color("203") // red
	case "paused":
		return lipgloss.Color("213") // magenta
	case "dirty":
		return lipgloss.Color("214") // amber
	case "ahead":
		return lipgloss.Color("42") // green
	case "behind":
		return lipgloss.Color("39") // blue
	case "diverged":
		return lipgloss.Color("213") // magenta
	default:
		return lipgloss.Color("241") // dim
	}
}

func fleetDiffLabel(ahead, behind int) string {
	switch {
	case ahead > 0 && behind > 0:
		return fmt.Sprintf("↑%d ↓%d", ahead, behind)
	case ahead > 0:
		return fmt.Sprintf("↑%d", ahead)
	case behind > 0:
		return fmt.Sprintf("↓%d", behind)
	default:
		return "·"
	}
}

func fleetDirtyLabel(d *contextDirtyJSON) string {
	if d == nil {
		return "·"
	}
	parts := make([]string, 0, 4)
	if d.Staged > 0 {
		parts = append(parts, fmt.Sprintf("S%d", d.Staged))
	}
	if d.Unstaged > 0 {
		parts = append(parts, fmt.Sprintf("U%d", d.Unstaged))
	}
	if d.Untracked > 0 {
		parts = append(parts, fmt.Sprintf("?%d", d.Untracked))
	}
	if d.Conflicts > 0 {
		parts = append(parts, fmt.Sprintf("✗%d", d.Conflicts))
	}
	if len(parts) == 0 {
		return "·"
	}
	return strings.Join(parts, " ")
}

// fleetLastChangeLabel renders the last-changed-file column inside w columns:
// the full repo-relative path when the terminal has the room (a bare
// `ContentView.swift` is ambiguous the moment a repo has two of them), else the
// tail — `…/SpaceMeshApp/ContentView.swift` — because the basename is the part
// that identifies the file. Falls back to the clipped basename only when even
// the tail would be noise.
func fleetLastChangeLabel(path string, w int) string {
	if path == "" {
		return "·"
	}
	if w <= 0 {
		w = 14
	}
	if runewidth.StringWidth(path) <= w {
		return path
	}
	if base := filepath.Base(path); runewidth.StringWidth(base) > w {
		return clip(base, w)
	}
	return clipLeft(path, w)
}

// fleetActiveLabel renders the staleness column. With a live clock (now set)
// it re-derives the age from the absolute lastActive so it ticks up between
// polls; the static/JSON path falls back to the ActiveAgoS snapshot.
func fleetActiveLabel(e fleetEntryJSON, now time.Time) string {
	if e.lastActive.IsZero() && e.ActiveAgoS == 0 {
		return "·" // unknown
	}
	var d time.Duration
	if !now.IsZero() && !e.lastActive.IsZero() {
		d = now.Sub(e.lastActive)
	} else {
		d = time.Duration(e.ActiveAgoS) * time.Second
	}
	if age := formatAge(d); age != "" {
		return age
	}
	return "now"
}

// fleetActiveStyled colors the activity age green while the worktree counts
// as active (same window as the watcher budget) — the user-facing answer to
// "which of these is being worked on RIGHT NOW", separate from the status
// dot whose green means "ahead of upstream". Only safe on the row's LAST
// column: ANSI codes would break printf padding anywhere else.
func fleetActiveStyled(e fleetEntryJSON, now time.Time) string {
	lbl := fleetActiveLabel(e, now)
	if !now.IsZero() && !e.lastActive.IsZero() && now.Sub(e.lastActive) <= fleetActiveWindow {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render(lbl)
	}
	return lbl
}

// totalRepoCount is the unfiltered repo count behind the current view.
func (m fleetModel) totalRepoCount() int {
	seen := map[string]bool{}
	for _, e := range m.entries {
		seen[e.RepoRoot] = true
	}
	return len(seen)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// clipLeft trims from the left, keeping the tail — the informative end of a
// path (`…/SpaceMeshApp/ContentView.swift`), where clip keeps the head.
func clipLeft(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[len(r)-n:])
	}
	return "…" + string(r[len(r)-(n-1):])
}

// Detail-panel modes, cycled by enter: the field panel (default), the cursor
// worktree's own live change feed, or no panel. The zero value is the field
// panel so a zero fleetView keeps the legacy default.
const (
	fleetDetailFields = iota
	fleetDetailFeed
	fleetDetailOff
	fleetDetailModes
)

// fleetView carries everything renderFleet needs. A zero `now` suppresses the
// live wall-clock (static snapshot); a negative cursor renders no selection
// marker; detail picks the master-detail panel mode for the cursor row.
type fleetView struct {
	entries []fleetEntryJSON
	cursor  int
	now     time.Time
	width   int
	detail  int
	// feed feeds the detail panel's per-worktree event tail (may be nil).
	feed []fleetFeedEvent
	// churn/since drive the header's Δ reading (zero churn hides it).
	churn fleetChurn
	since time.Duration
}

// renderFleet draws the worktree dashboard: a full-width header (count + live
// clock), then the glanceable table, optionally joined with a detail panel for
// the cursor row.
func renderFleet(v fleetView) string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	width := fleetWidth(v.width)

	var b strings.Builder
	b.WriteString(renderFleetHeader(v.entries, v.now, width, dim, v.churn, v.since))
	b.WriteString("\n")

	if len(v.entries) == 0 {
		b.WriteString(dim.Render("  (no worktrees)"))
		return b.String()
	}

	// Master-detail: place the detail panel beside the table when there's room.
	// Below ~64 cols the panel is dropped so the table stays readable. The
	// panel is built first because its measured width is what the table's
	// elastic columns have to give up (fleetTableWidth).
	var panel string
	if v.detail != fleetDetailOff && v.cursor >= 0 && v.cursor < len(v.entries) && width >= 64 {
		e := v.entries[v.cursor]
		if v.detail == fleetDetailFeed {
			panel = renderFleetDetailFeed(e, v.now, fleetEventTail(v.feed, e.Path, fleetDetailFeedLines))
		} else {
			panel = renderFleetDetail(e, v.now, fleetEventTail(v.feed, e.Path, 3))
		}
	}
	table := renderFleetTable(v.entries, v.cursor, v.now,
		fleetStatCols(fleetTableWidth(width, panel), fleetIndentFlat, v.entries))

	if panel != "" {
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, table, "   ", panel))
		return b.String()
	}
	b.WriteString(table)
	return b.String()
}

// Row prefix cost the elastic columns must budget around: caret + status dot
// (+ the group indent when worktrees hang under a repo header).
const (
	fleetIndentFlat    = 4
	fleetIndentGrouped = 8
)

// fleetTableWidth is the width the table's elastic columns may spend: the
// render width minus the measured detail panel (and the join gutter). It can go
// negative on a narrow terminal — fleetColumns then pins every column to its
// minimum, which is what the fixed-width layout always did.
func fleetTableWidth(width int, panel string) int {
	if panel == "" {
		return width
	}
	return width - lipgloss.Width(panel) - 3 // 3 = the join gutter
}

// renderFleetHeader is the title line (count) with the live wall-clock pushed
// to the right edge, followed by a horizontal rule. The clock — green dot +
// HH:MM:SS — ticks every second so the dashboard visibly stays alive even when
// no worktree changes (mirrors `gk status --watch`).
func renderFleetHeader(entries []fleetEntryJSON, now time.Time, width int, dim lipgloss.Style, churn fleetChurn, since time.Duration) string {
	count := fmt.Sprintf("%d %s", len(entries), pluralize(len(entries), "worktree", "worktrees"))
	return renderFleetHeadline(count, entries, now, width, dim, churn, since)
}

// renderFleetHeadline is the shared title line: `gk watch` + the count, then
// the volume readings, then the clock at the right edge.
//
// The readings live on two different time axes, and reading them as one
// undifferentiated list of "+X −Y" was the old header's confusion. They are
// now split into two clusters with distinct visual identity:
//
//   - "not shipped yet" — what is stacked up waiting to leave the worktree:
//     the uncommitted diffstat (`~`, resets to zero when an agent commits)
//     and the unpushed commit count (`↑N`, committed but not pushed).
//   - "flow" — Δ, everything that went by since watch started, the one
//     reading a commit does not erase.
//
// A single ` │ ` divider separates the two; within a cluster segments join on
// a plain gap. Segments drop right-to-left when the terminal can't hold them
// (flow first, then unpushed, then uncommitted, never the count), so the
// divider — attached to flow, the rightmost segment — is only ever drawn when
// the pending segments it divides are all present.
func renderFleetHeadline(count string, entries []fleetEntryJSON, now time.Time, width int, dim lipgloss.Style, churn fleetChurn, since time.Duration) string {
	files, added, removed := fleetTotals(entries)
	ahead := fleetAheadTotal(entries)

	// headSeg is one reading plus the separator that precedes it.
	type headSeg struct {
		text string
		sep  string
	}
	segs := []headSeg{{text: dim.Render(count)}}

	// Pending cluster — what has not shipped yet.
	pending := false
	if files > 0 {
		seg := dim.Render("~ " + pluralCount(files, "file", "files"))
		if added > 0 || removed > 0 { // 0 outside feed-stats mode — don't print a bare "·"
			seg += " " + fleetStatCell(added, removed)
		}
		segs = append(segs, headSeg{text: seg, sep: "  "})
		pending = true
	}
	if ahead > 0 {
		// Green ↑ to match the "ahead" status color used across the table.
		up := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("↑"+strconv.Itoa(ahead)) +
			dim.Render(" unpushed")
		segs = append(segs, headSeg{text: up, sep: "  "})
		pending = true
	}

	// Flow cluster — Δ since watch started. The divider only appears when there
	// is pending work to separate it from; otherwise flow follows on a gap.
	if churn.any() {
		seg := dim.Render("Δ")
		if churn.added > 0 || churn.removed > 0 {
			seg += " " + fleetStatCell(churn.added, churn.removed)
		}
		seg += dim.Render(" · " + fleetSinceLabel(since))
		sep := "  "
		if pending {
			sep = dim.Render("  │  ")
		}
		segs = append(segs, headSeg{text: seg, sep: sep})
	}

	title := lipgloss.NewStyle().Bold(true).Render("gk watch")
	var clock string
	if !now.IsZero() {
		clock = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true).Render("●") +
			dim.Render(" "+now.Format("15:04:05"))
	}

	join := func(n int) string {
		var b strings.Builder
		b.WriteString(title + "  ")
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteString(segs[i].sep)
			}
			b.WriteString(segs[i].text)
		}
		return b.String()
	}

	// Widest prefix of segments that still leaves the clock its own space.
	header := join(1)
	for n := len(segs); n >= 1; n-- {
		cand := join(n)
		if lipgloss.Width(cand)+lipgloss.Width(clock)+2 <= width || n == 1 {
			header = cand
			break
		}
	}
	if clock != "" {
		gap := max(width-lipgloss.Width(header)-lipgloss.Width(clock), 1)
		header += strings.Repeat(" ", gap) + clock
	}
	return header + "\n" + dim.Render(strings.Repeat("─", width))
}

// fleetAheadTotal sums commits ahead of upstream across entries — the header's
// "unpushed" reading: work that is committed but not yet pushed, the push-stage
// sibling of the uncommitted diffstat.
func fleetAheadTotal(entries []fleetEntryJSON) int {
	n := 0
	for _, e := range entries {
		n += e.Ahead
	}
	return n
}

// fleetSinceLabel is how long watch has been up ("17m", "2h04m") — the
// denominator that turns Δ from a number into a rate you can feel.
func fleetSinceLabel(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func pluralCount(n int, one, many string) string {
	return fmt.Sprintf("%s %s", commaInt(n), pluralize(n, one, many))
}

// renderFleetTable draws one line per worktree: status dot, branch (with a `*`
// current marker and a `⏸` paused marker), sync, dirty, the last-changed file,
// and the staleness age. The cursor row is marked and bolded.
func renderFleetTable(entries []fleetEntryJSON, cursor int, now time.Time, cols fleetCols) string {
	var b strings.Builder
	for i, e := range entries {
		caret := "  "
		if i == cursor {
			caret = "› "
		}
		dot := lipgloss.NewStyle().Foreground(fleetStatusColor(e.Status)).Render("●")
		branch := clip(e.Branch, cols.branch)
		if e.Current {
			branch += "*"
		}
		if e.Operation != "" {
			branch += " ⏸"
		}
		row := fmt.Sprintf("%s%s %-*s  %-8s  %-11s  %-*s  %s%s",
			caret, dot, cols.branch+3, branch,
			fleetDiffLabel(e.Ahead, e.Behind),
			fleetDirtyLabel(e.Dirty),
			cols.file, fleetLastChangeLabel(e.LastChange, cols.file),
			fleetRowStat(e, cols),
			fleetActiveStyled(e, now),
		)
		if i == cursor {
			row = lipgloss.NewStyle().Bold(true).Render(row)
		}
		b.WriteString(row)
		if i < len(entries)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// fleetStatCols is fleetColumns with the diffstat column dropped when nothing
// would go in it: --feed-stats=false pays for no diff runs (so the counts are
// all zero), and a column of `·` is worse than no column.
func fleetStatCols(width, indent int, entries []fleetEntryJSON) fleetCols {
	cols := fleetColumns(width, indent)
	for _, e := range entries {
		if e.Added > 0 || e.Removed > 0 {
			return cols
		}
	}
	cols.stat = 0
	return cols
}

// fleetRowStat is the row's padded diffstat cell, or "" when the column was
// dropped for width — the caller concatenates it straight before the age.
func fleetRowStat(e fleetEntryJSON, cols fleetCols) string {
	if cols.stat == 0 {
		return ""
	}
	return padCell(fleetStatCell(e.Added, e.Removed), cols.stat) + "  "
}

// fleetEventTail returns the newest n feed events for one worktree, oldest
// first — the detail panel's "what just happened here".
func fleetEventTail(feed []fleetFeedEvent, wtPath string, n int) []fleetFeedEvent {
	var tail []fleetFeedEvent
	for i := len(feed) - 1; i >= 0 && len(tail) < n; i-- {
		if feed[i].wt == wtPath {
			tail = append(tail, feed[i])
		}
	}
	// collected newest-first; reverse to render oldest-first
	for i, j := 0, len(tail)-1; i < j; i, j = i+1, j-1 {
		tail[i], tail[j] = tail[j], tail[i]
	}
	return tail
}

// renderFleetDetail is the master-detail panel: the full field set for the
// cursor row in a bordered box, including the parent/land-readiness, paused
// resume hint, and the worktree's recent change events that the compact table
// cannot fit.
func renderFleetDetail(e fleetEntryJSON, now time.Time, tail []fleetFeedEvent) string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	row := func(k, val string) string { return dim.Render(fmt.Sprintf("%-7s", k)) + " " + val }

	title := e.Branch
	if e.Current {
		title += " *"
	}
	lines := []string{
		lipgloss.NewStyle().Bold(true).Foreground(fleetStatusColor(e.Status)).Render(title),
		row("status", e.Status+"  "+fleetDirtyLabel(e.Dirty)),
		row("sync", fleetSyncDetail(e)),
	}
	if e.Parent != "" {
		pv := e.Parent
		if e.ParentBehind > 0 {
			pv += fmt.Sprintf("  ↓%d", e.ParentBehind)
		}
		lines = append(lines, row("parent", pv))
	}
	lines = append(lines, row("active", fleetActiveDetail(e, now)))
	if e.LastChange != "" {
		lines = append(lines, row("change", clip(e.LastChange, 32)))
	}

	op := "—"
	if e.Operation != "" {
		op = e.Operation
		if e.Resume != "" {
			op += "  · " + e.Resume
		}
	}
	lines = append(lines, row("op", op))

	land := "no"
	if e.LandReady {
		land = "merged ✓ (reap-safe)"
	}
	lines = append(lines, row("land", land))
	if e.LandReady && e.Branch != "" {
		// The one next action the dashboard can suggest: the branch is fully
		// in base, so the worktree can be reaped.
		lines = append(lines, row("next", selfCmd("worktree remove "+e.Branch)))
	}
	for _, ev := range tail {
		lines = append(lines, dim.Render(ev.ts.Format("15:04:05"))+" "+ev.glyph+" "+clip(ev.path, 30))
	}
	lines = append(lines, dim.Render(clip(e.Path, 40)))

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("241")).
		Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}

// fleetDetailFeedLines is how many of the cursor worktree's change events the
// feed-mode detail panel shows — enough to read a burst of agent edits, small
// enough that the panel doesn't dwarf the table.
const fleetDetailFeedLines = 10

// renderFleetDetailFeed is the detail panel's live-feed mode: the cursor
// worktree's own slice of the merged change feed — `gk status --watch` in
// miniature. No extra git work: the events are already collected fleet-wide,
// this only filters them to one worktree.
func renderFleetDetailFeed(e fleetEntryJSON, now time.Time, tail []fleetFeedEvent) string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	title := e.Branch
	if e.Current {
		title += " *"
	}
	lines := []string{
		lipgloss.NewStyle().Bold(true).Foreground(fleetStatusColor(e.Status)).Render(title),
		dim.Render(e.Status+"  "+fleetDirtyLabel(e.Dirty)) + "  " + dim.Render("· "+fleetActiveDetail(e, now)),
	}
	if len(tail) == 0 {
		lines = append(lines, dim.Render("no changes yet — watching…"))
	}
	for _, ev := range tail {
		line := dim.Render(ev.ts.Format(changeTSFormat)) + " " + ev.glyph + " " + clip(ev.path, 30)
		if ev.symbols != "" {
			line += dim.Render(" · " + clip(ev.symbols, 26))
		}
		line += fleetFeedStatLabel(ev)
		if ev.note != "" {
			line += dim.Render(" " + ev.note)
		}
		lines = append(lines, line)
	}

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("241")).
		Padding(0, 1).
		Render(strings.Join(lines, "\n"))
}

func fleetSyncDetail(e fleetEntryJSON) string {
	lbl := fleetDiffLabel(e.Ahead, e.Behind)
	if lbl == "·" {
		return "up to date"
	}
	return lbl + " vs upstream"
}

func fleetActiveDetail(e fleetEntryJSON, now time.Time) string {
	switch lbl := fleetActiveLabel(e, now); lbl {
	case "·":
		return "unknown"
	case "now":
		return "just now"
	default:
		return lbl + " ago"
	}
}

// --- TUI ---

type fleetDataMsg struct {
	entries []fleetEntryJSON
	err     error
}

// fleetRepollMsg carries the tick generation that scheduled it: every data
// receipt bumps tickSeq and arms a fresh timer, so a stale timer from a
// superseded chain (an fsnotify-triggered poll landed in between) is ignored
// instead of doubling the poll cadence.
type fleetRepollMsg struct{ seq int }

// fleetClockMsg fires every second to refresh the header's live wall-clock and
// the per-worktree staleness ages — render-only, no git/fs work — so the UI
// visibly ticks even when no worktree changes.
type fleetClockMsg time.Time

// fleetCooldownMsg starts a poll that an fs event asked for while the rate
// limit (fsPollGap) was still closed. It carries the tick generation for the
// same reason fleetRepollMsg does: a manual refresh or a poll that landed in
// between supersedes it.
type fleetCooldownMsg struct{ seq int }

type fleetModel struct {
	ctx      context.Context
	cmd      *cobra.Command // for the embedded zoom view's head/status probes
	runner   *git.ExecRunner
	interval time.Duration
	entries  []fleetEntryJSON
	cursor   int
	now      time.Time
	width    int
	height   int
	detail   int // fleetDetailFields | fleetDetailFeed | fleetDetailOff
	lastErr  error
	quitting bool

	// multi-repo mode (set by runFleetMultiTUI). entries hold every repo's
	// worktrees; rows is the flattened group view; collapsed maps repo_root→folded.
	multi     bool
	repos     []repoIdent
	sem       chan struct{}
	collapsed map[string]bool
	rows      []fleetRow

	// change feed: prevSigs is the per-worktree signature state from the last
	// poll; feed is the merged event timeline (ring-capped); showFeed toggles
	// the pane ('e'); feedStats opts into +/- counts on events.
	prevSigs  map[string]map[string]fileSig
	feed      []fleetFeedEvent
	showFeed  bool
	feedStats bool

	// churn is the volume of work seen since startedAt — the header's Δ, and
	// the one reading a commit doesn't erase.
	churn     fleetChurn
	startedAt time.Time

	// fsnotify upgrade: ws is nil when no worktree could be watched (pure
	// polling). polling/fsPending implement the one-in-flight backpressure;
	// tickSeq invalidates superseded heartbeat timers (see fleetRepollMsg).
	//
	// deferred/lastPollStart add the RATE limit that backpressure alone does not
	// give: one-in-flight only stops two polls overlapping, so a worktree under
	// continuous churn re-triggered a fresh full poll the instant the previous
	// one landed — back-to-back polls forever (measured: 116% CPU sustained over
	// 21 worktrees, where one poll costs ~1.8 CPU-seconds). Poll STARTS are now
	// spaced by at least fsPollGap; an event arriving inside that window arms a
	// cooldown timer (deferred) instead of polling immediately.
	ws            *fleetWatchSet
	polling       bool // a poll is in flight
	deferred      bool // a rate-limited poll is armed and waiting out the gap
	fsPending     bool // an event landed mid-poll — re-poll once this one lands
	lastPollStart time.Time
	tickSeq       int

	// notify is the opt-in fleet.notify hook map — transitions detected
	// between polls fire it from the dashboard too, not just --events.
	notify map[string]string

	// zoom is the in-process drill-down ('w'): an embedded `gk status --watch`
	// model that owns the screen while non-nil. Fleet keeps gathering in the
	// background and drives the zoom's refreshes — the embedded model arms no
	// timers of its own. zoomGen invalidates in-flight frames when the target
	// switches ('[' / ']') or the zoom pops (esc).
	zoom     *changeWatchModel
	zoomPath string
	zoomGen  int

	// view controls: filter ('f' cycle: all→busy→stuck) and sort ('s' cycle:
	// default→activity→status) apply to the rendered view only — entries and
	// the JSON contract stay in gather order.
	filter   int
	sortMode int
}

// viewEntries is the filtered+sorted slice the TUI renders and the cursor
// indexes into. Entries itself is never reordered — a filter change is a view
// change, not a data change.
func (m fleetModel) viewEntries() []fleetEntryJSON {
	return fleetSortEntries(fleetFilterEntries(m.entries, m.filter, time.Now()), m.sortMode)
}

func (m fleetModel) Init() tea.Cmd {
	// First snapshot is already rendered from runFleet; schedule the data poll,
	// the once-a-second clock heartbeat, and (when available) the fs listener.
	return tea.Batch(m.tickCmd(), m.clockTickCmd(), waitFleetFSCmd(m.ws))
}

func (m fleetModel) tickCmd() tea.Cmd {
	seq := m.tickSeq
	return tea.Tick(fleetTickInterval(m.interval, m.ws), func(time.Time) tea.Msg { return fleetRepollMsg{seq} })
}

func (m fleetModel) clockTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return fleetClockMsg(t) })
}

// fsPollGap is the minimum spacing between poll STARTS when filesystem events —
// not the timer — are driving the refresh. The configured interval IS the user's
// cost budget ("re-scan this fleet no more often than every N seconds"), so fs
// events buy latency (a poll begins the moment a file lands, instead of up to N
// seconds later) without buying extra polls. Without this the fs path ignored
// the interval entirely and polled as fast as a poll could finish.
func (m fleetModel) fsPollGap() time.Duration {
	if m.interval <= 0 {
		return fsWatchDebounce
	}
	return m.interval
}

// pollNow starts a poll immediately, reserving the in-flight slot.
func (m *fleetModel) pollNow() tea.Cmd {
	m.polling = true
	m.deferred = false
	m.lastPollStart = time.Now()
	return m.pollCmd()
}

// pollOrDefer starts a poll if the rate limit allows one now, otherwise arms a
// cooldown timer for the remainder of the gap. Either way the caller has handed
// the request off: an fs event is never dropped, only delayed. deferred holds
// the in-flight slot so events arriving during the wait coalesce into the one
// poll already queued instead of each arming their own timer.
func (m *fleetModel) pollOrDefer() tea.Cmd {
	if wait := m.fsPollGap() - time.Since(m.lastPollStart); wait > 0 {
		m.deferred = true
		seq := m.tickSeq
		return tea.Tick(wait, func(time.Time) tea.Msg { return fleetCooldownMsg{seq} })
	}
	return m.pollNow()
}

func (m fleetModel) pollCmd() tea.Cmd {
	if m.multi {
		return func() tea.Msg {
			return fleetDataMsg{entries: gatherFleetMulti(m.ctx, m.repos, m.sem, fleetRepoTimeout, m.feedStats)}
		}
	}
	return func() tea.Msg {
		entries, err := gatherFleet(m.ctx, m.runner, m.feedStats)
		return fleetDataMsg{entries: entries, err: err}
	}
}

func (m fleetModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Zoom mode: the embedded watch view owns the screen and the keyboard.
	// Fleet's own machinery (poll/fs/notify/clock) keeps running below so the
	// table is fresh the moment the zoom pops.
	if m.zoom != nil {
		switch msg := msg.(type) {
		case tea.KeyMsg:
			return m.handleZoomKey(msg)
		case changeFrameMsg:
			if msg.gen != m.zoomGen {
				return m, nil // frame from a replaced/popped zoom target
			}
			_, cmd := m.zoom.Update(msg)
			return m, cmd
		case tea.WindowSizeMsg:
			m.width, m.height = msg.Width, msg.Height
			m.zoom.width, m.zoom.height = msg.Width, zoomBodyHeight(msg.Height)
			return m, nil
		case fleetClockMsg:
			// One clock drives both views: advance fleet's, mirror the zoom's.
			m.now = time.Time(msg)
			_, _ = m.zoom.Update(clockTickMsg(time.Time(msg)))
			return m, m.clockTickCmd()
		}
	}
	switch msg := msg.(type) {
	case fleetRepollMsg:
		if msg.seq != m.tickSeq || m.polling || m.deferred {
			// Stale timer from a superseded chain, or a poll is already in
			// flight (fs-triggered/manual) or queued behind the rate limit —
			// starting a second would diff the same prevSigs twice and
			// duplicate feed/notify events.
			return m, nil
		}
		return m, m.pollNow()
	case fleetCooldownMsg:
		if msg.seq != m.tickSeq || m.polling {
			return m, nil // superseded by a manual refresh or a poll that landed
		}
		return m, m.pollNow()
	case fleetFSMsg:
		// A worktree changed on disk. Two limits apply. One poll in flight at a
		// time: bursts collapse into a single queued re-poll (fsPending) — the
		// panel's backpressure requirement. And poll starts are spaced by
		// fsPollGap, so sustained churn cannot chain full fleet scans back to
		// back. Always re-arm the listener. A zoomed worktree additionally
		// refreshes its watch view immediately — the zoom's refresh is
		// independent of the fleet poll's backpressure and of the rate limit,
		// because it is a single cheap worktree, not the whole fleet.
		var zoomCmd tea.Cmd
		if m.zoom != nil && msg.path == m.zoomPath && !m.zoom.paused {
			zoomCmd = m.zoom.refreshCmd()
		}
		if m.polling {
			m.fsPending = true
			return m, tea.Batch(waitFleetFSCmd(m.ws), zoomCmd)
		}
		if m.deferred {
			return m, tea.Batch(waitFleetFSCmd(m.ws), zoomCmd) // already queued
		}
		return m, tea.Batch(m.pollOrDefer(), waitFleetFSCmd(m.ws), zoomCmd)
	case fleetClockMsg:
		// Render-only heartbeat: advance the clock and re-arm. No git/fs work.
		m.now = time.Time(msg)
		return m, m.clockTickCmd()
	case fleetDataMsg:
		m.polling = false
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.lastErr = nil
			if len(m.notify) > 0 {
				for _, ev := range fleetTransitions(m.entries, msg.entries, m.now) {
					fireFleetNotify(m.ctx, m.notify, ev)
				}
			}
			m.entries = msg.entries
			// Churn reads the signature state this poll is about to replace,
			// so it has to run before applyFeedDiff swaps it out.
			m.churn.accumulate(m.prevSigs, m.entries)
			m.feed, m.prevSigs = applyFeedDiff(m.prevSigs, m.entries, m.feed, m.now)
			if m.multi {
				m.rebuildRows()
			} else if n := len(m.viewEntries()); m.cursor >= n {
				m.cursor = max(0, n-1)
			}
		}
		m.tickSeq++ // invalidate any timer armed before this receipt
		cmds := []tea.Cmd{fleetSyncWatchersCmd(m.ctx, m.ws, m.entries, m.zoomPath)}
		if m.fsPending {
			// Events landed while this poll ran. Honour them — but through the
			// rate limit, not immediately: an unconditional re-poll here is what
			// let one busy worktree pin the fleet at back-to-back scans.
			m.fsPending = false
			cmds = append(cmds, m.pollOrDefer())
		} else {
			cmds = append(cmds, m.tickCmd())
		}
		// The fleet poll doubles as the zoom's heartbeat: refresh the zoomed
		// view on every data receipt, and pop it if its worktree vanished
		// (e.g. reaped mid-watch) rather than watching a dead path forever.
		// The live indicator re-reads the watcher set each round — the
		// activity-based allocation may have granted (or freed) this
		// worktree's watcher since the zoom opened.
		if m.zoom != nil {
			if !m.hasEntry(m.zoomPath) {
				m.closeZoom()
			} else {
				m.zoom.fsLive = m.ws.hasWatcher(m.zoomPath)
				if !m.zoom.paused {
					cmds = append(cmds, m.zoom.refreshCmd())
				}
			}
		}
		return m, tea.Batch(cmds...)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			m.ws.Close()
			return m, tea.Quit
		case "j", "down":
			limit := len(m.viewEntries())
			if m.multi {
				limit = len(m.rows)
			}
			if m.cursor < limit-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case " ":
			if m.multi {
				m.toggleCursorRepo()
			}
		case "w":
			// Zoom into the live change feed for the cursor's worktree —
			// single-repo picks the cursor entry directly; multi resolves
			// through the grouped rows (headers → the repo's current worktree).
			var path string
			if m.multi {
				path = m.cursorWatchTarget()
			} else if view := m.viewEntries(); m.cursor >= 0 && m.cursor < len(view) && view[m.cursor].Status != "error" {
				path = view[m.cursor].Path
			}
			if path != "" {
				return m.openZoom(path)
			}
		case "enter", "tab":
			// Both modes: cycle the cursor panel (fields → live feed → off).
			// Multi-repo folding stays on space, so the two gestures don't
			// share a key anymore.
			m.detail = (m.detail + 1) % fleetDetailModes
		case "e":
			m.showFeed = !m.showFeed
		case "f":
			m.filter = (m.filter + 1) % fleetFilterModes
			m.cursor = 0
			if m.multi {
				m.rebuildRows()
			}
		case "s":
			m.sortMode = (m.sortMode + 1) % fleetSortModes
			m.cursor = 0
			if m.multi {
				m.rebuildRows()
			}
		case "r":
			if m.polling {
				return m, nil // refresh already under way
			}
			// A manual refresh overrides the rate limit — the user asked for it,
			// and a keypress is not the runaway loop fsPollGap exists to stop.
			// tickSeq++ retires the armed cooldown timer so it cannot fire a
			// second, redundant poll on top of this one.
			m.tickSeq++
			return m, m.pollNow() // manual refresh now
		}
	}
	return m, nil
}

func (m fleetModel) View() string {
	if m.quitting {
		return ""
	}
	// Zoom mode: breadcrumb (where in the fleet + how to get back), then the
	// embedded watch view.
	if m.zoom != nil {
		return m.zoomBreadcrumb() + "\n" + m.zoom.View()
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	footer := "j/k move · enter panel · w zoom · e feed · f/s view · r refresh · q quit · %s"
	var dash string
	if m.multi {
		dash = renderFleetGrouped(m.rows, m.cursor, m.now, m.width, m.detail, m.feed,
			m.totalRepoCount(), len(m.entries), m.churn, m.uptime())
		footer = "j/k move · space fold · enter panel · w zoom · e feed · f/s view · r refresh · q quit · %s"
	} else {
		dash = renderFleet(fleetView{
			entries: m.viewEntries(),
			cursor:  m.cursor,
			now:     m.now,
			width:   m.width,
			detail:  m.detail,
			feed:    m.feed,
			churn:   m.churn,
			since:   m.uptime(),
		})
	}

	var b strings.Builder
	b.WriteString(dash)
	if m.showFeed {
		// Size the feed against what the dashboard actually drew — the detail
		// panel can be taller than the table, and a row count would miss that.
		if pane := renderFleetFeed(m.feed, m.width, m.feedPaneLines(lipgloss.Height(dash)), m.multi); pane != "" {
			b.WriteString("\n" + pane)
		}
	}
	body := b.String()

	var tail strings.Builder
	if m.lastErr != nil {
		tail.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("203")).
			Render("refresh failed: " + m.lastErr.Error()))
		tail.WriteString("\n")
	}
	cadence := fleetWatchHealthLabel(m.ws.snapshotHealth(), m.interval)
	if m.filter != fleetFilterAll || m.sortMode != fleetSortDefault {
		cadence += fmt.Sprintf(" · filter:%s sort:%s", fleetFilterName(m.filter), fleetSortName(m.sortMode))
	}
	tail.WriteString(dim.Render(fmt.Sprintf(footer, cadence)))

	// Pin the keybar to the bottom of the alt screen: the dashboard owns the
	// whole terminal, so a footer floating under a short table (with dead space
	// below it) reads as a truncated frame. Padding only — never clipping.
	pad := "\n"
	if m.height > 0 {
		if gap := m.height - lipgloss.Height(body) - lipgloss.Height(tail.String()); gap > 0 {
			pad = strings.Repeat("\n", gap+1)
		}
	}
	return body + pad + tail.String()
}

// fleetWatchHealthLabel keeps watcher allocation visible without making the
// footer read mutable watcher state itself. A watcher shortage is explicit:
// those eligible worktrees still refresh on the regular polling fallback.
func fleetWatchHealthLabel(health fleetWatchHealth, interval time.Duration) string {
	label := fmt.Sprintf("polling every %s", interval)
	if health.Watched > 0 {
		heartbeat := fsHeartbeatInterval
		if interval > heartbeat {
			heartbeat = interval
		}
		label = fmt.Sprintf("fs events · heartbeat %s", heartbeat)
	}
	label += fmt.Sprintf(" · watch %d/%d", health.Watched, health.Eligible)
	if health.PollingFallback {
		label += " · polling fallback"
	}
	label += fmt.Sprintf(" · budget %d/%d", health.Watched*health.DirCap, health.Budget)
	if health.Saturated {
		label += " · saturated"
	}
	return label
}

// uptime is how long watch has been running — the Δ reading's denominator.
func (m fleetModel) uptime() time.Duration {
	if m.startedAt.IsZero() || m.now.IsZero() {
		return 0
	}
	return m.now.Sub(m.startedAt)
}

// feedPaneLines sizes the feed pane: it fills whatever height the dashboard
// (dashH: header + rule + the taller of table/detail panel) leaves, since the
// alt screen is ours either way and a longer tail is strictly more history.
// Height 0 (unknown) gets a static default; too little room drops the pane
// entirely rather than showing a one-line stub.
func (m fleetModel) feedPaneLines(dashH int) int {
	const want = 8
	if m.height <= 0 {
		return want
	}
	free := m.height - dashH - 3 // the pane's own rule + keybar + slack
	if free < 2 {
		return 0
	}
	return free
}

func runFleetTUI(ctx context.Context, cmd *cobra.Command, runner *git.ExecRunner, initial []fleetEntryJSON, interval time.Duration, feedStats bool, filter int) error {
	m := fleetModel{
		ctx:       ctx,
		cmd:       cmd,
		runner:    runner,
		filter:    filter,
		interval:  interval,
		entries:   initial,
		now:       time.Now(),
		startedAt: time.Now(),
		detail:    fleetDetailFields,
		showFeed:  true,
		feedStats: feedStats,
		prevSigs:  map[string]map[string]fileSig{},
		// Synchronous setup: the walk is bounded by the per-worktree dir
		// budget and fleets are small; nil (nothing watchable) → pure polling.
		ws:     newFleetWatchSet(ctx, initial),
		notify: fleetNotifyConfig(),
	}
	defer m.ws.Close()
	m.feed, m.prevSigs = applyFeedDiff(m.prevSigs, initial, nil, m.now)
	prog := tea.NewProgram(
		m,
		tea.WithContext(ctx),
		tea.WithOutput(os.Stderr),
		tea.WithInputTTY(),
		tea.WithAltScreen(),
	)
	if _, err := prog.Run(); err != nil {
		if ctx.Err() != nil {
			return nil // cancelled — clean exit
		}
		return fmt.Errorf("fleet tui: %w", err)
	}
	return nil
}
