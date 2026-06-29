package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Live multi-worktree supervision dashboard",
		Long: `Watch every worktree at once: branch, ahead/behind, dirty/conflict state,
last activity, any paused operation, and which one is current. Built for
supervising parallel work (e.g. several AI agents each in their own worktree) —
answers "who is dirty / stuck / stale / ready to land" without a per-worktree
status probe.

The TUI polls 'git worktree list' on an interval; a live wall-clock in the
header (and a per-worktree "active N ago") ticks every second so you can tell
the dashboard is alive between polls. j/k move, enter toggles the detail panel,
r refreshes, q quits. Under --json (or GK_AGENT) it instead emits a one-shot
machine-readable snapshot — the same data the TUI renders — so an agent or
script can poll it directly.`,
		Args: cobra.NoArgs,
		RunE: runFleet,
	}
	cmd.Flags().Int("interval", 2, "poll interval in seconds (TUI mode)")
	cmd.Flags().StringSlice("repos", nil, "explicit repo paths to watch (multi-repo)")
	cmd.Flags().StringSlice("scan", nil, "directory roots to scan for git repos (multi-repo)")
	cmd.Flags().Bool("all", false, "watch sibling repos of the current repo (multi-repo)")
	cmd.Flags().Int("depth", 2, "max scan recursion depth for --scan")
	rootCmd.AddCommand(cmd)
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
}

const fleetRepoTimeout = 3 * time.Second

func runFleet(cmd *cobra.Command, _ []string) error {
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
	if multi {
		sem := newFleetLimiter(fleetConcurrency())
		entries := gatherFleetMulti(ctx, ids, sem, fleetRepoTimeout)
		if JSONOut() {
			return emitAgentResult(cmd.OutOrStdout(), entries)
		}
		// No TTY (pipe/redirect/CI): static grouped snapshot, all repos expanded.
		if !ui.IsTerminal() {
			fmt.Fprintln(cmd.OutOrStdout(), renderFleetGrouped(buildFleetRows(entries, nil), -1, time.Time{}, 0))
			return nil
		}
		interval := resolveFleetInterval(cmd)
		return runFleetMultiTUI(ctx, ids, sem, entries, time.Duration(interval)*time.Second)
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}

	entries, err := gatherFleet(ctx, runner)
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

	interval := resolveFleetInterval(cmd)
	return runFleetTUI(ctx, runner, entries, time.Duration(interval)*time.Second)
}

// gatherFleet builds the fleet snapshot, reusing the same worktree enrichment
// `gk worktree list` uses (porcelain parse + branch ahead/behind + per-path
// dirty probe) plus the supervision fields. The bare worktree is skipped — it
// holds no working state. Per-worktree enrichment runs concurrently: each entry
// is independent, so a handful of worktrees stay snappy under a 2s poll.
func gatherFleet(ctx context.Context, runner *git.ExecRunner) ([]fleetEntryJSON, error) {
	root := runner.Dir
	if root == "" {
		root, _ = os.Getwd()
	}
	if r, _, ok := repoRootAndCommonDir(ctx, root); ok {
		root = r
	}
	sem := newFleetLimiter(fleetConcurrency())
	return gatherFleetRepo(ctx, runner, filepath.Base(root), root, sem)
}

// gatherFleetRepo enriches one repo's worktrees, tagging every entry with the
// repo label/root. sem bounds the per-worktree enrich goroutines — the bulk of
// the git subprocesses — across every repo, so multi-repo mode does not spawn
// repo×worktree enrichers at once. The repo-level fan-out and each repo's
// one-shot `worktree list`/meta queries run outside sem; gating the repo level
// on the same semaphore would deadlock (repos holding every slot while their
// worktrees wait for one).
func gatherFleetRepo(ctx context.Context, runner *git.ExecRunner, label, root string, sem chan struct{}) ([]fleetEntryJSON, error) {
	stdout, stderr, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("fleet: worktree list: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	entries := parseWorktreePorcelain(string(stdout))
	meta := loadWorktreeBranchMeta(ctx, runner)
	current := currentWorktreePath(ctx, runner)
	base := resolveDefaultBranchForWorktree(ctx, runner)
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
			e := enrichFleetEntry(ctx, live[i], meta, current, base, now)
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
		// No multi flags: config opts in ONLY when not inside a repo, so a bare
		// `gk fleet` in a repo stays single-repo (use --all to override).
		if (len(fc.Repos) > 0 || len(fc.Scan) > 0) && currentRepoRoot(ctx) == "" {
			repos, scan = fc.Repos, fc.Scan
		} else {
			return nil, false, nil
		}
	}

	ids, err := discoverRepos(ctx, repos, scan, fc.Exclude, depth)
	if err != nil {
		return nil, true, err
	}
	if len(ids) == 0 {
		return nil, true, fmt.Errorf("fleet: no git repositories found; pass --scan <dir> or --repos <path>")
	}
	return ids, true, nil
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

// resolveFleetInterval is the TUI poll interval: the --interval flag when the
// user set it, else fleet.interval from config, else the 2s default.
func resolveFleetInterval(cmd *cobra.Command) int {
	if cmd.Flags().Changed("interval") {
		if n, _ := cmd.Flags().GetInt("interval"); n >= 1 {
			return n
		}
		return 1
	}
	if cfg, err := config.Load(nil); err == nil && cfg != nil && cfg.Fleet.Interval > 0 {
		return cfg.Fleet.Interval
	}
	if n, _ := cmd.Flags().GetInt("interval"); n >= 1 {
		return n
	}
	return 2
}

// gatherFleetMulti gathers several repos concurrently into one flat, sorted entry
// list. Each repo is isolated by a timeout; a failed or timed-out repo becomes a
// single synthetic status:"error" entry so it never silently vanishes from the
// snapshot. GIT_OPTIONAL_LOCKS=0 keeps fleet's read-only probes from contending
// on index.lock with the agents editing in those repos.
func gatherFleetMulti(ctx context.Context, repos []repoIdent, sem chan struct{}, perRepo time.Duration) []fleetEntryJSON {
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
			entries, err := gatherFleetRepo(rctx, runner, id.Label, id.Root, sem)
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
func enrichFleetEntry(ctx context.Context, e WorktreeEntry, meta map[string]worktreeBranchMeta, current, base string, now time.Time) fleetEntryJSON {
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
	f.Dirty = worktreeDirtyAt(ctx, e.Path)

	wr := &git.ExecRunner{Dir: e.Path}

	// Paused operation — the "who is stuck" signal a dirty count alone misses.
	if st, derr := gitstate.Detect(ctx, e.Path); derr == nil && st != nil {
		if op := fleetOperationLabel(st); op != "" {
			f.Operation = op
			f.Resume = selfCmd("continue")
		}
	}

	// Activity: HEAD commit time, advanced to the newest changed-file mtime so
	// an agent mid-edit (no commit yet) still reads "now". Statting changed
	// files is only paid when the worktree is dirty.
	active := m.LastCommit // zero when meta missing
	if f.Dirty != nil {
		if mt := worktreeNewestChangeMtime(ctx, wr, e.Path); mt.After(active) {
			active = mt
		}
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
	default:
		return ""
	}
}

// worktreeNewestChangeMtime returns the newest filesystem mtime among the
// worktree's changed (tracked + untracked) files — the freshest "an agent
// touched this" signal. Best-effort: an unscannable path yields the zero time.
func worktreeNewestChangeMtime(ctx context.Context, runner *git.ExecRunner, path string) time.Time {
	out, _, err := runner.Run(ctx, "status", "--porcelain", "-z")
	if err != nil {
		return time.Time{}
	}
	var newest time.Time
	for _, name := range parsePorcelainZPaths(string(out)) {
		if info, e := os.Stat(filepath.Join(path, name)); e == nil {
			if mt := info.ModTime(); mt.After(newest) {
				newest = mt
			}
		}
	}
	return newest
}

// parsePorcelainZPaths extracts the changed paths from `git status --porcelain
// -z` output. Records are NUL-separated "XY <path>"; a rename/copy record is
// followed by its source path as the next NUL token, which we skip.
func parsePorcelainZPaths(raw string) []string {
	toks := strings.Split(raw, "\x00")
	var paths []string
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if len(t) < 4 { // "XY p" minimum; trailing empty token after final NUL
			continue
		}
		x, y := t[0], t[1]
		paths = append(paths, t[3:])
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			i++ // consume the rename/copy source path token
		}
	}
	return paths
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

// fleetView carries everything renderFleet needs. A zero `now` suppresses the
// live wall-clock (static snapshot); a negative cursor renders no selection
// marker; detail draws the master-detail panel for the cursor row.
type fleetView struct {
	entries []fleetEntryJSON
	cursor  int
	now     time.Time
	width   int
	detail  bool
}

// renderFleet draws the worktree dashboard: a full-width header (count + live
// clock), then the glanceable table, optionally joined with a detail panel for
// the cursor row.
func renderFleet(v fleetView) string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	width := v.width
	if width <= 0 || width > 120 {
		width = 80
	}

	var b strings.Builder
	b.WriteString(renderFleetHeader(v.entries, v.now, width, dim))
	b.WriteString("\n")

	if len(v.entries) == 0 {
		b.WriteString(dim.Render("  (no worktrees)"))
		return b.String()
	}

	table := renderFleetTable(v.entries, v.cursor, v.now)

	// Master-detail: place the detail panel beside the table when there's room.
	// Below ~64 cols the panel is dropped so the table stays readable.
	if v.detail && v.cursor >= 0 && v.cursor < len(v.entries) && width >= 64 {
		panel := renderFleetDetail(v.entries[v.cursor], v.now)
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, table, "   ", panel))
		return b.String()
	}
	b.WriteString(table)
	return b.String()
}

// renderFleetHeader is the title line (count) with the live wall-clock pushed
// to the right edge, followed by a horizontal rule. The clock — green dot +
// HH:MM:SS — ticks every second so the dashboard visibly stays alive even when
// no worktree changes (mirrors `gk status --watch`).
func renderFleetHeader(entries []fleetEntryJSON, now time.Time, width int, dim lipgloss.Style) string {
	count := fmt.Sprintf("%d %s", len(entries), pluralize(len(entries), "worktree", "worktrees"))
	left := lipgloss.NewStyle().Bold(true).Render("gk fleet") + "  " + dim.Render(count)
	header := left

	if !now.IsZero() {
		clockText := now.Format("15:04:05")
		clock := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true).Render("●") +
			dim.Render(" "+clockText)
		gap := width - runewidth.StringWidth("gk fleet  "+count) - runewidth.StringWidth("● "+clockText)
		if gap < 1 {
			gap = 1
		}
		header = left + strings.Repeat(" ", gap) + clock
	}
	return header + "\n" + dim.Render(strings.Repeat("─", width))
}

// renderFleetTable draws one line per worktree: status dot, branch (with a `*`
// current marker and a `⏸` paused marker), sync, dirty, and the staleness age.
// The cursor row is marked and bolded.
func renderFleetTable(entries []fleetEntryJSON, cursor int, now time.Time) string {
	const branchW = 20
	var b strings.Builder
	for i, e := range entries {
		caret := "  "
		if i == cursor {
			caret = "› "
		}
		dot := lipgloss.NewStyle().Foreground(fleetStatusColor(e.Status)).Render("●")
		branch := clip(e.Branch, branchW)
		if e.Current {
			branch += "*"
		}
		if e.Operation != "" {
			branch += " ⏸"
		}
		row := fmt.Sprintf("%s%s %-*s  %-8s  %-11s  %s",
			caret, dot, branchW+3, branch,
			fleetDiffLabel(e.Ahead, e.Behind),
			fleetDirtyLabel(e.Dirty),
			fleetActiveLabel(e, now),
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

// renderFleetDetail is the master-detail panel: the full field set for the
// cursor row in a bordered box, including the parent/land-readiness and paused
// resume hint that the compact table cannot fit.
func renderFleetDetail(e fleetEntryJSON, now time.Time) string {
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
	lines = append(lines, row("land", land), dim.Render(clip(e.Path, 40)))

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

type fleetRepollMsg struct{}

// fleetClockMsg fires every second to refresh the header's live wall-clock and
// the per-worktree staleness ages — render-only, no git/fs work — so the UI
// visibly ticks even when no worktree changes.
type fleetClockMsg time.Time

type fleetModel struct {
	ctx      context.Context
	runner   *git.ExecRunner
	interval time.Duration
	entries  []fleetEntryJSON
	cursor   int
	now      time.Time
	width    int
	height   int
	detail   bool
	lastErr  error
	quitting bool

	// multi-repo mode (set by runFleetMultiTUI). entries hold every repo's
	// worktrees; rows is the flattened group view; collapsed maps repo_root→folded.
	multi     bool
	repos     []repoIdent
	sem       chan struct{}
	collapsed map[string]bool
	rows      []fleetRow
}

func (m fleetModel) Init() tea.Cmd {
	// First snapshot is already rendered from runFleet; schedule the data poll
	// and the once-a-second clock heartbeat.
	return tea.Batch(m.tickCmd(), m.clockTickCmd())
}

func (m fleetModel) tickCmd() tea.Cmd {
	return tea.Tick(m.interval, func(time.Time) tea.Msg { return fleetRepollMsg{} })
}

func (m fleetModel) clockTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return fleetClockMsg(t) })
}

func (m fleetModel) pollCmd() tea.Cmd {
	if m.multi {
		return func() tea.Msg {
			return fleetDataMsg{entries: gatherFleetMulti(m.ctx, m.repos, m.sem, fleetRepoTimeout)}
		}
	}
	return func() tea.Msg {
		entries, err := gatherFleet(m.ctx, m.runner)
		return fleetDataMsg{entries: entries, err: err}
	}
}

func (m fleetModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case fleetRepollMsg:
		return m, m.pollCmd()
	case fleetClockMsg:
		// Render-only heartbeat: advance the clock and re-arm. No git/fs work.
		m.now = time.Time(msg)
		return m, m.clockTickCmd()
	case fleetDataMsg:
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.lastErr = nil
			m.entries = msg.entries
			if m.multi {
				m.rebuildRows()
			} else if m.cursor >= len(m.entries) {
				m.cursor = max(0, len(m.entries)-1)
			}
		}
		return m, m.tickCmd()
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "j", "down":
			limit := len(m.entries)
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
			if m.multi {
				if path := m.cursorWatchTarget(); path != "" {
					return m, m.watchCmd(path)
				}
			}
		case "enter", "tab":
			if m.multi {
				// Multi-repo: enter folds/unfolds the repo (detail panel is
				// single-repo only for now).
				m.toggleCursorRepo()
			} else {
				m.detail = !m.detail
			}
		case "r":
			return m, m.pollCmd() // manual refresh now
		}
	}
	return m, nil
}

func (m fleetModel) View() string {
	if m.quitting {
		return ""
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	var b strings.Builder
	footer := "j/k move · enter detail · r refresh · q quit · polling every %s"
	if m.multi {
		b.WriteString(renderFleetGrouped(m.rows, m.cursor, m.now, m.width))
		footer = "j/k move · space fold · w watch · r refresh · q quit · polling every %s"
	} else {
		b.WriteString(renderFleet(fleetView{
			entries: m.entries,
			cursor:  m.cursor,
			now:     m.now,
			width:   m.width,
			detail:  m.detail,
		}))
	}
	b.WriteString("\n")
	if m.lastErr != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("203")).
			Render("refresh failed: " + m.lastErr.Error()))
		b.WriteString("\n")
	}
	b.WriteString(dim.Render(fmt.Sprintf(footer, m.interval)))
	return b.String()
}

func runFleetTUI(ctx context.Context, runner *git.ExecRunner, initial []fleetEntryJSON, interval time.Duration) error {
	m := fleetModel{
		ctx:      ctx,
		runner:   runner,
		interval: interval,
		entries:  initial,
		now:      time.Now(),
		detail:   true,
	}
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
