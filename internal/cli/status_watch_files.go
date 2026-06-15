package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// --- Change-feed watch (`gk status --watch`) ---------------------------------
//
// A live timeline of *which files change*, for watching an AI agent (or any
// fast editor) edit a tree in real time. This is the polling prototype of
// proposal "A안": each tick snapshots the working tree, diffs it against the
// previous snapshot, and appends timeline events (new / re-touched / cleared).
//
// Polling can only see the NET change between ticks — three quick edits to one
// file between snapshots surface as a single "re-touched" event. That is the
// known trade-off of the polling version; an fsnotify trigger would catch each
// write. The render deliberately stays light (no full status re-render) so the
// loop is cheap enough to poll sub-second.

// fileSig is one path's dirty signature at a snapshot: the porcelain XY code
// plus its accumulated +/- line counts. Two snapshots are compared field by
// field to decide whether a path produced a timeline event.
type fileSig struct {
	xy      string
	added   int
	removed int
	// mtime is the file's on-disk modification time (UnixNano). It lets the
	// diff catch a re-save that leaves the porcelain code and +/- counts
	// unchanged (e.g. swapping a line for one of equal length) — without it
	// those edits would be silently dropped from the live feed.
	mtime int64
}

// changeEvent is one entry in the timeline feed.
type changeEvent struct {
	ts      time.Time
	path    string
	label   string // xyLabel(xy), or "cleared" when the file left the dirty set
	added   int
	removed int
	note    string // "new", "re-touched", "" (baseline / cleared)
	cleared bool
}

// changeSnapshot reads the current dirty set as path→fileSig. It uses
// `--no-optional-locks` so polling never contends with the agent's own
// `git add`/commit (which would otherwise race on .git/index.lock), and
// porcelain v1 -z so the parse stays a trivial NUL split.
func changeSnapshot(ctx context.Context, runner *git.ExecRunner, root string) map[string]fileSig {
	sigs := map[string]fileSig{}
	out, _, err := runner.Run(ctx, "--no-optional-locks", "status", "--porcelain", "-z")
	if err != nil {
		return sigs
	}
	stats := changeDiffStats(ctx, runner)
	tokens := strings.Split(string(out), "\x00")
	for i := 0; i < len(tokens); i++ {
		tok := tokens[i]
		if len(tok) < 4 { // "XY p" minimum
			continue
		}
		xy := tok[:2]
		path := tok[3:]
		// Rename/copy entries carry the original path as the next NUL token;
		// consume it so it isn't mistaken for a second changed file.
		if xy[0] == 'R' || xy[0] == 'C' {
			i++
		}
		ds := stats[path]
		var mtime int64
		if root != "" {
			if fi, serr := os.Stat(filepath.Join(root, path)); serr == nil {
				mtime = fi.ModTime().UnixNano()
			}
		}
		sigs[path] = fileSig{xy: xy, added: ds.added, removed: ds.removed, mtime: mtime}
	}
	return sigs
}

// changeDiffStats fetches +/- line counts for the feed. Unlike the shared
// fetchDiffStats it (1) passes --no-optional-locks so a polling tick never
// blocks on .git/index.lock while the agent runs `git add`, and (2) uses
// `--numstat -z`, whose rename records carry the old and new paths as
// separate NUL fields — so the stat is keyed by the NEW path the porcelain
// snapshot uses, instead of the "old => new" string the non-z parse produces.
// Staged + unstaged are merged.
func changeDiffStats(ctx context.Context, runner *git.ExecRunner) map[string]diffStat {
	merged := map[string]diffStat{}
	for _, args := range [][]string{
		{"--no-optional-locks", "diff", "--numstat", "-z"},
		{"--no-optional-locks", "diff", "--cached", "--numstat", "-z"},
	} {
		out, _, err := runner.Run(ctx, args...)
		if err != nil {
			continue
		}
		for p, s := range parseNumstatZ(out) {
			ex := merged[p]
			merged[p] = diffStat{added: ex.added + s.added, removed: ex.removed + s.removed}
		}
	}
	return merged
}

// parseNumstatZ parses `git diff --numstat -z`. A normal record is the single
// NUL token "<add>\t<del>\t<path>". A rename/copy record has an empty path
// field after the two tabs and is followed by two more NUL tokens — the old
// then the new path; it is keyed by the NEW path. Binary files ("-") are
// skipped (but their path tokens are still consumed so the cursor stays aligned).
func parseNumstatZ(out []byte) map[string]diffStat {
	m := map[string]diffStat{}
	toks := strings.Split(string(out), "\x00")
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t == "" {
			continue
		}
		cols := strings.SplitN(t, "\t", 3)
		if len(cols) != 3 {
			continue
		}
		path := cols[2]
		if path == "" { // rename/copy header → old, new follow as NUL tokens
			if i+2 >= len(toks) {
				break
			}
			path = toks[i+2]
			i += 2
		}
		a, e1 := strconv.Atoi(cols[0])
		d, e2 := strconv.Atoi(cols[1])
		if e1 != nil || e2 != nil { // binary "-" or malformed counts
			continue
		}
		m[path] = diffStat{added: a, removed: d}
	}
	return m
}

// diffChangeSnapshots returns the timeline events produced by the transition
// prev→curr at time ts. A path that is new emits "new"; one whose signature
// changed emits "re-touched"; one that left the dirty set emits a "cleared"
// event. Events are sorted by path so a single tick's batch renders stably.
// On the first tick (prev == nil) the current dirty set seeds the feed as
// baseline entries (empty note) so the watcher opens with context.
func diffChangeSnapshots(prev, curr map[string]fileSig, ts time.Time) []changeEvent {
	var evs []changeEvent
	baseline := prev == nil
	for path, sig := range curr {
		old, existed := prev[path]
		if existed && old == sig {
			continue
		}
		note := ""
		switch {
		case baseline:
			note = ""
		case existed:
			note = "re-touched"
		default:
			note = "new"
		}
		evs = append(evs, changeEvent{
			ts: ts, path: path, label: xyLabel(sig.xy),
			added: sig.added, removed: sig.removed, note: note,
		})
	}
	for path := range prev {
		if _, still := curr[path]; !still {
			evs = append(evs, changeEvent{ts: ts, path: path, label: "cleared", cleared: true})
		}
	}
	sort.Slice(evs, func(i, j int) bool { return evs[i].path < evs[j].path })
	return evs
}

// changeGlyph maps an event to its single-cell leading marker.
func changeGlyph(e changeEvent) string {
	if e.cleared {
		return "✓"
	}
	switch e.label {
	case "new", "added": // untracked, or staged-add (xyLabel "A ") — both are "+"
		return "+"
	case "deleted", "del":
		return "−"
	case "conflict":
		return "⚔"
	case "renamed", "ren":
		return "→"
	default:
		return "~"
	}
}

// runChangeWatch dispatches the change-feed loop: a bubbletea TUI on a TTY,
// an append-only stream otherwise (so `gk st --watch | tee feed.log`
// keeps working — the genesis of the machine-readable stream idea).
func runChangeWatch(cmd *cobra.Command) error {
	interval := statusWatchInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	interval = clampWatchInterval(interval)

	runner := &git.ExecRunner{Dir: RepoFlag()}
	// Resolve the worktree root once. A failure here means "not a git repo (or
	// the index is unreadable)" — surface it instead of looping forever showing
	// a misleading "working tree clean" (snapshots swallow git errors so the
	// feed stays responsive to transient hiccups; the persistent case is caught
	// right here). root also anchors the per-file mtime stat in changeSnapshot.
	root := repoToplevel(cmd.Context(), runner)
	if root == "" {
		return WithHint(
			fmt.Errorf("gk status --watch: not a git repository (or its index is unreadable)"),
			"run it from inside a git repository",
		)
	}

	// fsnotify is the primary trigger when available; polling is the fallback.
	fs, _ := newFSWatcher(cmd.Context(), runner, fsWatchDebounce)
	if fs != nil {
		defer fs.Close()
	}

	if _, ok := ui.TTYWidth(); !ok || NoColorFlag() {
		return runChangeWatchPlain(cmd, runner, interval, fs, root)
	}
	// lipgloss's default renderer lazily probes the terminal (OSC 11 background
	// + DSR cursor query) the first time it styles a string. Inside a bubbletea
	// session that probe races bubbletea's own stdin reader, and the terminal's
	// response can be left unconsumed — leaking into the shell on exit as a
	// garbled / shifted prompt. Force the detection HERE, before bubbletea owns
	// stdin, so the session renders from cache and never queries mid-run.
	_ = lipgloss.ColorProfile()
	_ = lipgloss.HasDarkBackground()

	model := newChangeWatchModel(cmd, interval)
	model.runner = runner
	model.root = root
	model.fs = fs
	prog := tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(cmd.Context()))
	_, err := prog.Run()
	return err
}

// runChangeWatchPlain is the non-TTY fallback: print each new event as one
// line as it happens (tail -f style), no alt screen, no key handling. An fs
// event or the heartbeat ticker both trigger a snapshot; a nil fs channel is
// never selected, collapsing this to pure polling.
func runChangeWatchPlain(cmd *cobra.Command, runner *git.ExecRunner, interval time.Duration, fs *fsWatcher, root string) error {
	hb := interval
	if fs != nil {
		hb = fsHeartbeatInterval // events do the work; the poll is just a safety net
	}
	ticker := time.NewTicker(hb)
	defer ticker.Stop()
	w := cmd.OutOrStdout()

	var fsCh <-chan struct{}
	if fs != nil {
		fsCh = fs.events
	}

	var prev map[string]fileSig
	snap := func() {
		curr := changeSnapshot(cmd.Context(), runner, root)
		for _, e := range diffChangeSnapshots(prev, curr, time.Now()) {
			fmt.Fprintln(w, plainEventLine(e))
		}
		prev = curr
	}
	snap() // initial baseline
	for {
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-ticker.C:
			snap()
		case <-fsCh:
			snap()
		}
	}
}

func plainEventLine(e changeEvent) string {
	stat := ""
	if e.added > 0 || e.removed > 0 {
		stat = fmt.Sprintf("  +%d -%d", e.added, e.removed)
	}
	note := ""
	if e.note != "" {
		note = "  " + e.note
	}
	return fmt.Sprintf("%s  %s %s%s%s",
		e.ts.Format(changeTSFormat), changeGlyph(e), e.path, stat, note)
}

// --- bubbletea model ---------------------------------------------------------

const changeFeedCap = 1000 // ring cap; trivial memory, bounds a long session

// changeTSFormat stamps events to 1/100s. The extra precision is purely
// cosmetic — the timestamp is the snapshot moment, already captured once per
// refresh — but it makes the feed read as genuinely live (e.g. "14:25:18.11").
const changeTSFormat = "15:04:05.00"

type changeWatchModel struct {
	cmd      *cobra.Command
	runner   *git.ExecRunner
	root     string // worktree top, for per-file mtime stats
	interval time.Duration
	fs       *fsWatcher // non-nil → fsnotify drives refreshes; tick is a heartbeat

	prev       map[string]fileSig
	events     []changeEvent
	head       headInfo // compact-status header (branch/upstream/HEAD commit)
	files      int      // distinct dirty files in the latest snapshot
	added      int      // total +/- across the latest snapshot
	removed    int
	lastChange time.Time
	lastTick   time.Time

	paused     bool
	refreshing bool
	first      bool
	err        error

	// showDash swaps the feed region for the full status dashboard (the rich
	// `gk status` blocks). dashFrame holds the last captured frame.
	showDash  bool
	dashFrame string

	width, height int
	now           func() time.Time
}

func newChangeWatchModel(cmd *cobra.Command, interval time.Duration) *changeWatchModel {
	return &changeWatchModel{
		cmd:      cmd,
		runner:   &git.ExecRunner{Dir: RepoFlag()},
		interval: interval,
		first:    true,
	}
}

func (m *changeWatchModel) nowFn() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

type changeTickMsg time.Time

// clockTickMsg fires every second to refresh the divider's live wall-clock; it
// triggers a redraw only (no snapshot).
type clockTickMsg time.Time

// changeFSMsg is one debounced filesystem-change burst; changeFSClosedMsg fires
// when the watcher channel closes (the feed then leans on the heartbeat poll).
type changeFSMsg struct{}
type changeFSClosedMsg struct{}

type changeFrameMsg struct {
	curr   map[string]fileSig
	events []changeEvent
	head   headInfo
	dash   string // captured full-status frame, "" unless the dashboard is shown
	ts     time.Time
}

// headInfo is the compact orientation shown above the live feed: where you are
// (repo/branch/upstream + ahead/behind) and the latest committed state (HEAD
// short sha + subject). Cheap reads, none touch the index lock.
type headInfo struct {
	repo     string
	branch   string
	upstream string
	ahead    int
	behind   int
	sha      string // short
	subject  string
}

// fetchHeadInfo gathers the compact-header orientation. Every field degrades to
// its zero value on error so the header renders partial rather than failing.
func fetchHeadInfo(cmd *cobra.Command, runner *git.ExecRunner) headInfo {
	ctx := cmd.Context()
	h := headInfo{repo: detectRepoName(ctx, runner)}
	if out, _, err := runner.Run(ctx, "--no-optional-locks", "symbolic-ref", "--short", "HEAD"); err == nil {
		h.branch = strings.TrimSpace(string(out))
	}
	if out, _, err := runner.Run(ctx, "--no-optional-locks", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil {
		h.upstream = strings.TrimSpace(string(out))
		h.ahead, h.behind = detectPromptAheadBehind(ctx, runner)
	}
	if _, sha, subj := headCommitInfo(cmd, runner); sha != "" {
		h.sha = shortSHA(sha)
		h.subject = subj
	}
	return h
}

func (m *changeWatchModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.refreshCmd(), m.tickCmd(), m.clockTickCmd()}
	if m.fs != nil {
		cmds = append(cmds, m.waitFSCmd())
	}
	return tea.Batch(cmds...)
}

// clockTickCmd fires once a second purely to redraw the live wall-clock in the
// divider — render-only, no git/fs work — so the UI visibly ticks even when no
// files are changing, signalling it's alive. Cheap: one string redraw/sec.
func (m *changeWatchModel) clockTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return clockTickMsg(t) })
}

// tickInterval is the heartbeat cadence: a slow safety net while fsnotify is
// the primary trigger, or the user's full poll rate when it's the only one.
func (m *changeWatchModel) tickInterval() time.Duration {
	if m.fs != nil {
		return fsHeartbeatInterval
	}
	return m.interval
}

func (m *changeWatchModel) tickCmd() tea.Cmd {
	return tea.Tick(m.tickInterval(), func(t time.Time) tea.Msg { return changeTickMsg(t) })
}

// waitFSCmd blocks the event loop on one debounced fs signal, then yields it as
// a message; Update re-arms it so the next burst is caught.
func (m *changeWatchModel) waitFSCmd() tea.Cmd {
	if m.fs == nil {
		return nil
	}
	ch := m.fs.events
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return changeFSClosedMsg{}
		}
		return changeFSMsg{}
	}
}

// refreshCmd snapshots + diffs off the event loop. The in-flight guard keeps a
// slow git call (large tree) from piling up overlapping snapshots under a tight
// interval.
func (m *changeWatchModel) refreshCmd() tea.Cmd {
	if m.refreshing {
		return nil
	}
	m.refreshing = true
	runner := m.runner
	cmd := m.cmd
	root := m.root
	prev := m.prev
	ctx := m.cmd.Context()
	now := m.nowFn()
	showDash := m.showDash
	return func() tea.Msg {
		curr := changeSnapshot(ctx, runner, root)
		msg := changeFrameMsg{
			curr:   curr,
			events: diffChangeSnapshots(prev, curr, now),
			head:   fetchHeadInfo(cmd, runner),
			ts:     now,
		}
		// The full status dashboard is only rendered (and thus captured) when
		// the user has toggled it on — keep the common feed path cheap.
		if showDash {
			msg.dash = captureStatusFrame(cmd)
		}
		return msg
	}
}

// captureStatusFrame renders the rich `gk status` output into a string by
// temporarily redirecting the command's stdout — the dashboard the [s] toggle
// shows in place of the feed.
func captureStatusFrame(cmd *cobra.Command) string {
	var buf strings.Builder
	old := cmd.OutOrStdout()
	cmd.SetOut(&buf)
	_, _ = runStatusOnce(cmd)
	cmd.SetOut(old)
	return buf.String()
}

func (m *changeWatchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case clockTickMsg:
		// Render-only heartbeat: re-arm and let View redraw the clock. No
		// snapshot, so this stays free even at a 1s cadence.
		return m, m.clockTickCmd()
	case changeTickMsg:
		if m.paused {
			return m, m.tickCmd()
		}
		return m, tea.Batch(m.refreshCmd(), m.tickCmd())
	case changeFSMsg:
		// Re-arm the fs listener every time; refresh unless paused.
		if m.paused {
			return m, m.waitFSCmd()
		}
		return m, tea.Batch(m.refreshCmd(), m.waitFSCmd())
	case changeFSClosedMsg:
		// Watcher died — drop to heartbeat polling at the user's interval.
		m.fs = nil
		return m, m.tickCmd()
	case changeFrameMsg:
		m.refreshing = false
		m.first = false
		m.lastTick = msg.ts
		m.prev = msg.curr
		m.head = msg.head
		if msg.dash != "" {
			m.dashFrame = msg.dash
		}
		if len(msg.events) > 0 {
			m.lastChange = msg.ts
			m.events = append(m.events, msg.events...)
			if len(m.events) > changeFeedCap {
				m.events = m.events[len(m.events)-changeFeedCap:]
			}
		}
		m.files, m.added, m.removed = rollupSnapshot(msg.curr)
		return m, nil
	}
	return m, nil
}

func (m *changeWatchModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	case "r":
		return m, m.refreshCmd()
	case "p", " ":
		m.paused = !m.paused
		return m, nil
	case "c":
		m.events = nil
		return m, nil
	case "s":
		// Toggle the full status dashboard; refresh immediately to capture
		// (or stop capturing) it.
		m.showDash = !m.showDash
		return m, m.refreshCmd()
	case "+", "=":
		m.interval = clampWatchInterval(m.interval * 2)
		return m, m.tickCmd()
	case "-", "_":
		m.interval = clampWatchInterval(m.interval / 2)
		return m, m.tickCmd()
	}
	return m, nil
}

// rollupSnapshot returns the header totals for the current dirty set.
func rollupSnapshot(curr map[string]fileSig) (files, added, removed int) {
	for _, s := range curr {
		files++
		added += s.added
		removed += s.removed
	}
	return files, added, removed
}

// View is the split layout: a compact status header on top (where you are +
// the latest commit + the dirty rollup), a divider, then the live change feed
// filling the remaining height, and a keybar.
func (m *changeWatchModel) View() string {
	// Dashboard mode: the full `gk status` frame in place of the feed.
	if m.showDash {
		hint := lipgloss.NewStyle().Faint(true).
			Render("   [s] back to live feed  ·  [r] refresh  ·  [q] quit")
		body := m.dashFrame
		if body == "" {
			body = "   " + lipgloss.NewStyle().Faint(true).Render("loading status…")
		}
		return hint + "\n\n" + body
	}

	header := m.compactHeader()
	divider := m.divider()
	keybar := m.keyBar()
	// Rows consumed by chrome: header lines + a blank + the divider + the
	// keybar. The feed gets whatever height is left.
	budget := 0
	if m.height > 0 {
		budget = m.height - (strings.Count(header, "\n") + 1) - 3
		if budget < 1 {
			budget = 1
		}
	}
	return header + "\n\n" + divider + "\n" + m.feedBody(budget) + "\n" + keybar
}

// compactHeader renders the orientation block above the feed: line 1 is repo ·
// branch ⇄ upstream ↑A ↓B · N files +X −Y · mode; line 2 is the HEAD short sha
// + subject. Values come from m.head (fetched on refresh, not per-render).
func (m *changeWatchModel) compactHeader() string {
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	dim := lipgloss.NewStyle().Faint(true)
	bold := lipgloss.NewStyle().Bold(true)
	cyan := lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	yellow := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	pause := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	pulse := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	h := m.head

	bits := []string{accent.Render("WATCHING")}
	// Orientation: repo · branch ⇄ upstream ↑A ↓B. Skipped until the first
	// refresh fills it (avoids a misleading "(detached)" at startup).
	if h.branch != "" {
		orient := ""
		if h.repo != "" {
			orient += dim.Render(h.repo + " · ")
		}
		orient += bold.Render(h.branch)
		if h.upstream != "" {
			orient += "  " + dim.Render("⇄ ") + cyan.Render(h.upstream)
			if h.ahead != 0 || h.behind != 0 {
				orient += dim.Render(fmt.Sprintf("  ↑%d ↓%d", h.ahead, h.behind))
			}
		}
		bits = append(bits, orient)
	}
	if m.paused {
		bits = append(bits, pause.Render("⏸ paused"))
	} else if m.fs != nil {
		bits = append(bits, pulse.Render("● live")+dim.Render(" (fsnotify)"))
	} else {
		bits = append(bits, dim.Render("every "+m.interval.String()+" (poll)"))
	}
	bits = append(bits, fmt.Sprintf("%d files", m.files))
	if m.added > 0 || m.removed > 0 {
		bits = append(bits, green.Render(fmt.Sprintf("+%d", m.added))+" "+red.Render(fmt.Sprintf("−%d", m.removed)))
	}
	if !m.lastChange.IsZero() && m.nowFn().Sub(m.lastChange) < watchPulseDuration {
		bits = append(bits, pulse.Render("● just changed"))
	}
	if m.err != nil {
		bits = append(bits, red.Render("⚠ "+truncateForHeader(m.err.Error(), 50)))
	}
	line1 := accent.Render("█") + "  " + strings.Join(bits, dim.Render(" · "))
	if m.width > 0 {
		line1 = lipgloss.NewStyle().MaxWidth(m.width).Render(line1)
	}

	if h.sha == "" {
		return line1
	}
	subj := h.subject
	if m.width > 20 {
		budget := m.width - 3 - runewidth.StringWidth(h.sha) - 2
		if budget > 8 && runewidth.StringWidth(subj) > budget {
			subj = runewidth.Truncate(subj, budget, "…")
		}
	}
	line2 := "   " + yellow.Render(h.sha) + "  " + dim.Render(subj)
	return line1 + "\n" + line2
}

// divider separates the status header from the live feed and carries the live
// wall-clock on the right — it ticks every second (clockTickCmd) so the UI
// visibly stays alive even when nothing is changing.
func (m *changeWatchModel) divider() string {
	dim := lipgloss.NewStyle().Faint(true)
	livedot := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	clock := m.nowFn().Format("15:04:05")
	label := "─── live changes "
	tail := "● " + clock // visible width used for fill math
	w := m.width
	if w <= 0 || w > 80 {
		w = 80
	}
	fill := w - runewidth.StringWidth(label) - runewidth.StringWidth(tail) - 1
	if fill < 3 {
		fill = 3
	}
	return dim.Render(label+strings.Repeat("─", fill)+" ") + livedot.Render("●") + dim.Render(" "+clock)
}

func (m *changeWatchModel) feedBody(budget int) string {
	if len(m.events) == 0 {
		dim := lipgloss.NewStyle().Faint(true)
		if m.first {
			return "   " + dim.Render("scanning…")
		}
		return "   " + dim.Render("working tree clean — waiting for changes…")
	}
	// Show the tail that fits the budget — newest at the bottom.
	visible := m.events
	if budget > 0 && len(visible) > budget {
		visible = visible[len(visible)-budget:]
	}
	var b strings.Builder
	for i, e := range visible {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(m.renderEvent(e))
	}
	return b.String()
}

func (m *changeWatchModel) renderEvent(e changeEvent) string {
	dim := lipgloss.NewStyle().Faint(true)
	ts := dim.Render(e.ts.Format(changeTSFormat))
	glyph := styleGlyph(e)
	path := e.path
	// Reserve: indent(3)+ts(11)+sp(2)+glyph(1)+sp(1)+stat(~12)+note(~12).
	if m.width > 24 {
		budget := m.width - 3 - 11 - 2 - 1 - 1 - 12 - 12
		if budget > 8 && runewidth.StringWidth(path) > budget {
			path = runewidth.Truncate(path, budget, "…")
		}
	}
	stat := styleStat(e)
	note := ""
	if e.note != "" {
		note = "  " + dim.Render(e.note)
	}
	return fmt.Sprintf("   %s  %s %s%s%s", ts, glyph, path, stat, note)
}

func styleGlyph(e changeEvent) string {
	g := changeGlyph(e)
	var c lipgloss.Color
	switch g {
	case "+":
		c = "2" // green
	case "−":
		c = "1" // red
	case "⚔":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true).Render(g)
	case "✓":
		return lipgloss.NewStyle().Faint(true).Render(g)
	case "→":
		c = "6" // cyan
	default:
		c = "3" // yellow (~)
	}
	return lipgloss.NewStyle().Foreground(c).Render(g)
}

func styleStat(e changeEvent) string {
	if e.cleared || (e.added == 0 && e.removed == 0) {
		return ""
	}
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	var parts []string
	if e.added > 0 {
		parts = append(parts, green.Render(fmt.Sprintf("+%d", e.added)))
	}
	if e.removed > 0 {
		parts = append(parts, red.Render(fmt.Sprintf("−%d", e.removed)))
	}
	return "  " + strings.Join(parts, " ")
}

func (m *changeWatchModel) keyBar() string {
	return lipgloss.NewStyle().Faint(true).
		Render("   [s] status  [r] refresh  [p] pause  [c] clear  [+/-] interval  [q] quit")
}
