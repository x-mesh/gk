package cli

import (
	"fmt"
	"hash/fnv"
	"os"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ui"
)

// runStatusWatch dispatches the --watch loop. When stdout is a TTY we run a
// bubbletea program against the alt-screen — full-frame buffered redraws
// eliminate the clear-then-paint flicker that the legacy line-by-line
// renderer produced. Non-TTY stdout (pipes, CI, redirection) falls back to
// the simple "blank line + reprint" loop because bubbletea on a non-TTY
// either no-ops or emits raw control sequences into the log.
func runStatusWatch(cmd *cobra.Command) error {
	interval := statusWatchInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	interval = clampWatchInterval(interval)

	if _, ok := ui.TTYWidth(); !ok || NoColorFlag() {
		return runStatusWatchPlain(cmd, interval)
	}
	return runStatusWatchTea(cmd, interval)
}

// runStatusWatchPlain is the fallback path for non-TTY callers. Preserves
// pre-bubbletea behaviour so `gk status --watch | tee log` still produces a
// readable scroll. No alt screen, no key handling — Ctrl+C via signal is
// the only way out.
func runStatusWatchPlain(cmd *cobra.Command, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for first := true; ; first = false {
		if !first {
			fmt.Fprintln(cmd.OutOrStdout())
		}
		if _, err := runStatusOnce(cmd); err != nil {
			return err
		}
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-ticker.C:
		}
	}
}

// runStatusWatchTea drives the flicker-free TTY watch loop.
func runStatusWatchTea(cmd *cobra.Command, interval time.Duration) error {
	model := &watchModel{cmd: cmd, interval: interval}
	prog := tea.NewProgram(model,
		tea.WithAltScreen(),
		tea.WithContext(cmd.Context()),
	)
	_, err := prog.Run()
	return err
}

// Interval bounds. The floor protects against runaway refresh loops when a
// user holds `-`; the ceiling keeps the displayed "every Ns" readable. The
// default 2s sits comfortably between them.
const (
	watchMinInterval = 250 * time.Millisecond
	watchMaxInterval = 60 * time.Second
	// watchPulseDuration is how long the "● just changed" accent stays
	// visible after a content change. Long enough that a glance catches
	// it; short enough that quiet seconds feel quiet.
	watchPulseDuration = 1500 * time.Millisecond
)

func clampWatchInterval(d time.Duration) time.Duration {
	if d < watchMinInterval {
		return watchMinInterval
	}
	if d > watchMaxInterval {
		return watchMaxInterval
	}
	return d
}

type watchTickMsg time.Time

// watchPulseEndMsg fires after watchPulseDuration so the View redraws once
// when the "just changed" accent should drop. Without this, the accent
// would linger until the next refresh tick obscured it.
type watchPulseEndMsg struct{}

type watchFrameMsg struct {
	text     string // raw frame text (ANSI included) for display
	stripped string // ANSI-stripped + line-trimmed text for diff/hash
	ts       time.Time
	err      error
	hash     uint64
}

type watchModel struct {
	cmd      *cobra.Command
	interval time.Duration

	frame      string
	lastUpdate time.Time
	lastChange time.Time // most recent ts at which the frame actually differed
	err        error

	paused     bool
	refreshing bool

	width, height int

	prevHash   uint64
	suppressed bool // last refresh produced an identical frame

	// prevStripped is the ANSI-stripped, line-trimmed *previous* frame —
	// used to compute which lines are new in the current frame. Indices in
	// markedLines align with the splits of the *current* frame.text.
	prevStripped string
	markedLines  []bool

	now func() time.Time
}

// nowFn returns m.now if injected (tests), else time.Now. Lets us drive the
// pulse-end logic deterministically from unit tests without sleeping.
func (m *watchModel) nowFn() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m *watchModel) Init() tea.Cmd {
	// Render the first frame immediately rather than waiting `interval`
	// seconds, then arm the ticker for subsequent refreshes.
	return tea.Batch(m.refreshCmd(), m.tickCmd())
}

func (m *watchModel) tickCmd() tea.Cmd {
	return tea.Tick(m.interval, func(t time.Time) tea.Msg { return watchTickMsg(t) })
}

// refreshCmd renders one frame off the bubbletea event loop. Returns nil when
// a refresh is already in flight so a fast user holding `r` (or a tight tick
// interval) can't pile up overlapping git invocations against the same cmd.
func (m *watchModel) refreshCmd() tea.Cmd {
	if m.refreshing {
		return nil
	}
	m.refreshing = true
	cmd := m.cmd
	return func() tea.Msg {
		var buf strings.Builder
		oldOut := cmd.OutOrStdout()
		cmd.SetOut(&buf)
		_, runErr := runStatusOnce(cmd)
		cmd.SetOut(oldOut)
		text := buf.String()
		stripped := normalizeFrame(text)
		h := fnv.New64a()
		_, _ = h.Write([]byte(stripped))
		hash := h.Sum64()
		if os.Getenv("GK_WATCH_DEBUG") == "1" {
			writeWatchDebugFrame(stripped, hash)
		}
		return watchFrameMsg{text: text, stripped: stripped, ts: time.Now(), err: runErr, hash: hash}
	}
}

// ansiSeqRe matches CSI escape sequences (color, cursor moves, etc.). Hash
// equality must reflect *user-visible* content, not byte-for-byte rendering
// — otherwise a stylistic ANSI reordering or per-call cursor positioning
// would falsely mark a frame as "changed" and pulse the header for nothing.
var ansiSeqRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// normalizeFrame returns the ANSI-stripped, line-right-trimmed canonical
// form of a rendered frame. Used for both hash equality (skip-unchanged) and
// per-line diff (gutter marker). Two frames are visually equal when their
// normalized forms are byte-equal.
func normalizeFrame(text string) string {
	stripped := ansiSeqRe.ReplaceAllString(text, "")
	// Per-line right-trim: cheap and removes incidental trailing spaces
	// that lipgloss sometimes emits when padding boxes to their target
	// width. Doesn't need to be UTF-8-aware — ASCII space/tab only.
	if !strings.ContainsAny(stripped, " \t\n") {
		return stripped
	}
	var b strings.Builder
	b.Grow(len(stripped))
	for i, line := range strings.Split(stripped, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimRight(line, " \t"))
	}
	return b.String()
}

// hashFrame is retained as a thin wrapper for tests that exercise hash
// equality directly. Production code uses normalizeFrame + fnv.
func hashFrame(text string) uint64 {
	stripped := normalizeFrame(text)
	h := fnv.New64a()
	_, _ = h.Write([]byte(stripped))
	return h.Sum64()
}

// markChangedLines returns a per-line bool slice marking lines in `curr`
// that did not appear in `prev`. Both inputs must be the normalized
// (ANSI-stripped + RTrimmed) form so styling differences don't trigger
// false marks. Multiset semantics: a line that appears N times in curr but
// only K<N times in prev marks the trailing N-K occurrences as new.
func markChangedLines(prev, curr string) []bool {
	currLines := strings.Split(curr, "\n")
	if prev == "" {
		// First frame ever — nothing to diff against. Returning an
		// all-false slice (vs nil) keeps the caller branch-free.
		return make([]bool, len(currLines))
	}
	prevSet := map[string]int{}
	for _, l := range strings.Split(prev, "\n") {
		prevSet[l]++
	}
	marked := make([]bool, len(currLines))
	for i, l := range currLines {
		if prevSet[l] > 0 {
			prevSet[l]--
			continue
		}
		marked[i] = true
	}
	return marked
}

// decorateFrame prepends a 2-column left gutter to every line of the raw
// (ANSI-included) frame. During the pulse window, lines flagged in marked
// receive a cyan `▎ ` marker; all other lines get two spaces. The gutter
// is reserved unconditionally so pulse start/end never shifts the body
// horizontally — visually stable is more important than the extra columns.
func decorateFrame(text string, marked []bool, pulseActive bool) string {
	lines := strings.Split(text, "\n")
	mark := lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true).Render("▎") + " "
	plain := "  "
	var b strings.Builder
	b.Grow(len(text) + 2*len(lines))
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		if pulseActive && i < len(marked) && marked[i] && strings.TrimSpace(stripFrameANSI(line)) != "" {
			b.WriteString(mark)
		} else {
			b.WriteString(plain)
		}
		b.WriteString(line)
	}
	return b.String()
}

// stripFrameANSI is a small helper that lets decorateFrame skip marking
// purely-blank lines (which only differ as filler and don't warrant a
// reader's eye). Reuses the package regex.
func stripFrameANSI(s string) string { return ansiSeqRe.ReplaceAllString(s, "") }

var watchDebugCounter uint64

func writeWatchDebugFrame(text string, hash uint64) {
	watchDebugCounter++
	path := fmt.Sprintf("/tmp/gk-watch-frame-%d.txt", watchDebugCounter%4)
	header := fmt.Sprintf("# hash=%016x ts=%s\n", hash, time.Now().Format(time.RFC3339Nano))
	_ = os.WriteFile(path, []byte(header+text), 0o600)
}

func (m *watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case watchTickMsg:
		// Always re-arm the ticker so interval changes take effect on the
		// *next* tick. Suppress the actual refresh when paused.
		if m.paused {
			return m, m.tickCmd()
		}
		return m, tea.Batch(m.refreshCmd(), m.tickCmd())

	case watchFrameMsg:
		m.refreshing = false
		m.err = msg.err
		m.lastUpdate = msg.ts
		// Skip-unchanged: when the new frame hashes identically to the
		// previous one we keep the rendered frame and only mark the
		// header so the user knows refresh fired without churn. This
		// keeps the display stable on slow terminals (SSH, tmux nested)
		// where even a flicker-free repaint costs visible bytes.
		hadPrev := m.prevHash != 0
		m.suppressed = hadPrev && msg.hash == m.prevHash
		var pulseCmd tea.Cmd
		if !m.suppressed {
			m.frame = msg.text
			// Compute per-line marks against the *previous* normalized
			// frame, then advance prevStripped to the current one so
			// the next transition compares against this baseline.
			if hadPrev {
				m.markedLines = markChangedLines(m.prevStripped, msg.stripped)
				m.lastChange = msg.ts
				// Schedule a pulse-end redraw so the gutter clears
				// promptly even when the next tick is far away
				// (e.g. interval is 60s).
				pulseCmd = tea.Tick(watchPulseDuration, func(time.Time) tea.Msg {
					return watchPulseEndMsg{}
				})
			} else {
				// First frame: no diff baseline yet, so no marks.
				m.markedLines = nil
			}
			m.prevStripped = msg.stripped
		}
		// Suppressed branch: keep markedLines + prevStripped as-is —
		// the displayed frame text didn't change so any in-flight pulse
		// from the prior transition still aligns with the current view.
		m.prevHash = msg.hash
		return m, pulseCmd

	case watchPulseEndMsg:
		// No state to update — we just need a re-View so the
		// "● just changed" accent disappears. bubbletea redraws after
		// every Update return.
		return m, nil
	}
	return m, nil
}

func (m *watchModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit
	case "r":
		return m, m.refreshCmd()
	case "p", " ":
		m.paused = !m.paused
		return m, nil
	case "+", "=":
		// Doubling/halving feels snappier than fixed-step nudges and
		// mirrors what `top -d` users expect. Re-arming the ticker
		// makes the change visible on the next tick.
		m.interval = clampWatchInterval(m.interval * 2)
		return m, m.tickCmd()
	case "-", "_":
		m.interval = clampWatchInterval(m.interval / 2)
		return m, m.tickCmd()
	}
	return m, nil
}

// View styles. Kept package-private and lazily-initialised in functions
// rather than as package-level vars so we don't fight init order with the
// fatih/color global state used elsewhere in cli/.
func watchHeaderLine(m *watchModel) string {
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	dim := lipgloss.NewStyle().Faint(true)
	pause := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	errSty := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	pulseSty := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)

	var bits []string
	bits = append(bits, accent.Render("gk watch"))
	if m.paused {
		bits = append(bits, pause.Render("⏸ paused"))
	} else {
		bits = append(bits, fmt.Sprintf("every %s", m.interval))
	}
	if !m.lastUpdate.IsZero() {
		ts := m.lastUpdate.Format("15:04:05")
		if m.suppressed {
			bits = append(bits, dim.Render(fmt.Sprintf("last %s (no change)", ts)))
		} else {
			bits = append(bits, fmt.Sprintf("last %s", ts))
		}
	}
	// Change cue: pulse for ~watchPulseDuration after a transition, then
	// drop to a quiet "changed HH:MM:SS" so the user can still see when
	// the most recent change happened. Suppressed across the very first
	// frame (no prior state to diff against).
	if !m.lastChange.IsZero() {
		if m.nowFn().Sub(m.lastChange) < watchPulseDuration {
			bits = append(bits, pulseSty.Render("● just changed"))
		} else {
			bits = append(bits, dim.Render("changed "+m.lastChange.Format("15:04:05")))
		}
	}
	if m.err != nil {
		bits = append(bits, errSty.Render("⚠ "+truncateForHeader(m.err.Error(), 60)))
	}
	bits = append(bits, dim.Render("[r] refresh  [p] pause  [+/-] interval  [q] quit"))

	line := strings.Join(bits, dim.Render(" · "))
	if m.width > 0 {
		line = lipgloss.NewStyle().MaxWidth(m.width).Render(line)
	}
	return line
}

func truncateForHeader(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return s[:max-1] + "…"
}

func (m *watchModel) View() string {
	var sb strings.Builder
	sb.WriteString(watchHeaderLine(m))
	sb.WriteString("\n\n")
	pulseActive := !m.lastChange.IsZero() && m.nowFn().Sub(m.lastChange) < watchPulseDuration
	sb.WriteString(decorateFrame(m.frame, m.markedLines, pulseActive))
	return sb.String()
}
