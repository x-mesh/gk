package cli

import (
	"context"
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

	// lastActive is the absolute timestamp behind ActiveAgoS. Unexported (never
	// serialized) so the live TUI re-derives the relative age every clock tick
	// instead of freezing it at poll time.
	lastActive time.Time
}

func runFleet(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
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

	interval, _ := cmd.Flags().GetInt("interval")
	if interval < 1 {
		interval = 2
	}
	return runFleetTUI(ctx, runner, entries, time.Duration(interval)*time.Second)
}

// gatherFleet builds the fleet snapshot, reusing the same worktree enrichment
// `gk worktree list` uses (porcelain parse + branch ahead/behind + per-path
// dirty probe) plus the supervision fields. The bare worktree is skipped — it
// holds no working state. Per-worktree enrichment runs concurrently: each entry
// is independent, so a handful of worktrees stay snappy under a 2s poll.
func gatherFleet(ctx context.Context, runner *git.ExecRunner) ([]fleetEntryJSON, error) {
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
			out[i] = enrichFleetEntry(ctx, live[i], meta, current, base, now)
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
			if m.cursor >= len(m.entries) {
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
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "enter", "tab":
			m.detail = !m.detail
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
	b.WriteString(renderFleet(fleetView{
		entries: m.entries,
		cursor:  m.cursor,
		now:     m.now,
		width:   m.width,
		detail:  m.detail,
	}))
	b.WriteString("\n")
	if m.lastErr != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("203")).
			Render("refresh failed: " + m.lastErr.Error()))
		b.WriteString("\n")
	}
	b.WriteString(dim.Render(fmt.Sprintf("j/k move · enter detail · r refresh · q quit · polling every %s", m.interval)))
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
