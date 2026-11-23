package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	ghapi "github.com/x-mesh/gk/internal/github"
	"github.com/x-mesh/gk/internal/ui"
)

// Auto-refresh period. One refresh costs up to 7 GraphQL points, so 30 s is
// at most 840 points an hour of the 5000 a token has. The search index lags
// behind GitHub, so a shorter period would rarely show newer data. The slow
// reads (dashSlowParts) cost 1 point each for the activity counts and 13 for
// the org unreleased summary, and an auto-refresh repeats them only every
// dashSlowMaxAge: at most 180 points an hour.
const (
	dashMinInterval     = 30 * time.Second
	dashDefaultInterval = dashMinInterval
)

type dashResultMsg struct {
	seq  int
	snap dashSnapshot
	err  error
}

// dashPartMsg is the end of the read of the slow part at index part.
type dashPartMsg struct {
	part  int
	write func(*dashSnapshot)
	err   error
}

// dashTickMsg asks for an auto-refresh. gen is the watch generation it was
// scheduled under: w and r bump the generation, so a tick from before is
// dropped instead of adding a second refresh loop.
type dashTickMsg struct{ gen int }

type dashClockMsg struct{}

type dashTUI struct {
	ctx      context.Context
	cancel   context.CancelFunc
	fetch    func(context.Context) (dashSnapshot, error)
	now      func() time.Time
	interval time.Duration

	watch     bool
	gen       int
	scheduled bool      // a tick of the current generation is pending
	nextAt    time.Time // when that tick fires
	inFlight  bool
	seq       int
	lastFetch time.Time // when the last read finished

	// The slow reads run apart from the main read, so the screen shows the
	// PRs and issues first and fills each block when its read ends.
	parts []*dashPart
	// slowLimitUntil is the rate limit a slow read hit. It is kept apart from
	// limitUntil because the main read, started with the slow reads, can
	// succeed after one of them and clears only its own limit.
	slowLimitUntil time.Time

	snap       *dashSnapshot
	lastOK     time.Time
	err        error
	limitUntil time.Time

	vp       viewport.Model
	width    int
	sections []int // content lines that start a section, for tab
}

func newDashTUI(ctx context.Context, fetch func(context.Context) (dashSnapshot, error), interval time.Duration, watch bool) *dashTUI {
	ctx, cancel := context.WithCancel(ctx)
	return &dashTUI{ctx: ctx, cancel: cancel, fetch: fetch, now: time.Now, interval: interval, watch: watch, vp: viewport.New(80, 20), width: 80}
}

func (m *dashTUI) Init() tea.Cmd {
	return tea.Batch(m.startFetch(), m.startParts(true), dashClock())
}

// startParts starts the slow reads that are due: every one with force, and
// otherwise one that has not been read, whose last read is older than
// dashSlowMaxAge, or whose last read was on another day, because the
// activity counts are by day. A read in flight is not started again, and none
// starts during a rate limit.
func (m *dashTUI) startParts(force bool) tea.Cmd {
	if m.limited() {
		return nil
	}
	var cmds []tea.Cmd
	for i, p := range m.parts {
		fresh := !p.at.IsZero() && m.now().Sub(p.at) < dashSlowMaxAge && p.at.YearDay() == m.now().YearDay()
		if p.inFlight || (!force && fresh) {
			continue
		}
		p.inFlight = true
		ctx := m.ctx
		cmds = append(cmds, func() tea.Msg {
			write, err := p.fetchWithin(ctx)
			return dashPartMsg{part: i, write: write, err: err}
		})
	}
	return tea.Batch(cmds...)
}

func (m *dashTUI) partInFlight() bool {
	for _, p := range m.parts {
		if p.inFlight {
			return true
		}
	}
	return false
}

func dashClock() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return dashClockMsg{} })
}

// startFetch starts one read. Callers have checked that none is in flight.
func (m *dashTUI) startFetch() tea.Cmd {
	m.seq++
	m.inFlight = true
	seq, ctx, fetch := m.seq, m.ctx, m.fetch
	return func() tea.Msg {
		snap, err := fetch(ctx)
		return dashResultMsg{seq: seq, snap: snap, err: err}
	}
}

// schedule arms one tick of the current generation after d, or at the end of
// a rate limit when that is later.
func (m *dashTUI) schedule(d time.Duration) tea.Cmd {
	at := m.now().Add(d)
	if end := m.limitEnd(); end.After(at) {
		at = end
	}
	m.scheduled, m.nextAt = true, at
	gen := m.gen
	return tea.Tick(at.Sub(m.now()), func(time.Time) tea.Msg { return dashTickMsg{gen: gen} })
}

func (m *dashTUI) limited() bool { return m.now().Before(m.limitEnd()) }

// limitEnd is the later end of the main read's and the slow reads' rate
// limits.
func (m *dashTUI) limitEnd() time.Time {
	if m.slowLimitUntil.After(m.limitUntil) {
		return m.slowLimitUntil
	}
	return m.limitUntil
}

func (m *dashTUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case dashResultMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.inFlight = false
		m.lastFetch = m.now()
		if msg.err != nil {
			// Keep the last snapshot; the header says what failed.
			m.err = msg.err
			var rl *ghapi.RateLimitError
			if errors.As(msg.err, &rl) {
				m.limitUntil = rl.Reset
			}
		} else {
			for _, p := range m.parts {
				p.apply(&msg.snap)
			}
			m.snap, m.lastOK, m.err, m.limitUntil = &msg.snap, m.now(), nil, time.Time{}
			m.render()
		}
		if m.watch && !m.scheduled {
			return m, m.schedule(m.interval)
		}
		return m, nil

	case dashPartMsg:
		p := m.parts[msg.part]
		p.inFlight = false
		p.at = m.now()
		p.done(msg.write, msg.err)
		// The limit ends at its reset, not when a slow read succeeds: the
		// slow reads start together, so one that ends after the limited one
		// was sent before the limit and does not show that it is over.
		var rl *ghapi.RateLimitError
		if errors.As(msg.err, &rl) {
			m.slowLimitUntil = rl.Reset
		}
		if m.snap != nil {
			p.apply(m.snap)
			m.render()
		}
		return m, nil

	case dashTickMsg:
		if msg.gen != m.gen || !m.watch {
			return m, nil
		}
		m.scheduled = false
		if m.inFlight {
			return m, nil
		}
		if m.limited() {
			return m, m.schedule(0)
		}
		return m, tea.Batch(m.startFetch(), m.startParts(false))

	case dashClockMsg:
		return m, dashClock()

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.vp.Width, m.vp.Height = msg.Width, max(msg.Height-2, 1)
		m.render()
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.cancel()
			return m, tea.Quit
		case "w":
			return m, m.toggleWatch()
		case "r":
			if m.inFlight || m.limited() {
				return m, nil
			}
			// The result schedules the next tick, so drop the pending one.
			m.gen++
			m.scheduled = false
			return m, tea.Batch(m.startFetch(), m.startParts(true))
		case "tab":
			m.jumpSection(1)
			return m, nil
		case "shift+tab":
			m.jumpSection(-1)
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}
	return m, nil
}

// toggleWatch turns auto-refresh on or off. Turning it on reads at once when
// the last read is older than the interval, else waits for the rest of it.
func (m *dashTUI) toggleWatch() tea.Cmd {
	m.watch = !m.watch
	m.gen++
	m.scheduled = false
	if !m.watch || m.inFlight {
		return nil
	}
	elapsed := m.now().Sub(m.lastFetch)
	if elapsed >= m.interval && !m.limited() {
		return tea.Batch(m.startFetch(), m.startParts(false))
	}
	return m.schedule(m.interval - elapsed)
}

func (m *dashTUI) render() {
	if m.snap == nil {
		return
	}
	var buf bytes.Buffer
	renderDash(&buf, *m.snap, m.width)
	content := strings.TrimRight(buf.String(), "\n")
	m.sections = m.sections[:0]
	for i, line := range strings.Split(content, "\n") {
		plain := ui.StripANSI(line)
		if strings.HasPrefix(plain, "PRs") || strings.HasPrefix(plain, "Issues") || strings.HasPrefix(plain, "Forecast") || strings.HasPrefix(plain, "Unreleased") {
			m.sections = append(m.sections, i)
		}
	}
	m.vp.SetContent(content)
}

func (m *dashTUI) jumpSection(dir int) {
	if len(m.sections) == 0 {
		return
	}
	cur := m.vp.YOffset
	target := m.sections[0]
	if dir > 0 {
		for _, s := range m.sections {
			if s > cur {
				target = s
				break
			}
		}
	} else {
		target = m.sections[len(m.sections)-1]
		for i := len(m.sections) - 1; i >= 0; i-- {
			if m.sections[i] < cur {
				target = m.sections[i]
				break
			}
		}
	}
	m.vp.SetYOffset(target)
}

func (m *dashTUI) status() string {
	var watch string
	switch {
	case !m.watch:
		watch = cellFaint("○ auto-refresh off (w)")
	case m.scheduled:
		watch = cellGreen("●") + fmt.Sprintf(" auto-refresh on · next in %ds", max(int(m.nextAt.Sub(m.now()).Round(time.Second)/time.Second), 0))
	default:
		watch = cellGreen("●") + " auto-refresh on"
	}
	var state string
	switch {
	case m.inFlight || m.partInFlight():
		state = cellYellow("⟳ refreshing…")
	case m.limited():
		state = cellYellow("⏸ rate limited · retry at " + m.limitEnd().Local().Format("15:04:05"))
	case m.err != nil:
		state = cellRed("⚠ refresh failed: "+m.err.Error()) + cellFaint(" · last success "+dashClockTime(m.lastOK))
	}
	if state == "" {
		return watch
	}
	return watch + "  " + state
}

func dashClockTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format("15:04:05")
}

func (m *dashTUI) View() string {
	body := cellFaint("loading…")
	if m.snap != nil {
		body = m.vp.View()
	}
	footer := cellFaint("w auto-refresh  r refresh  tab section  ↑↓ scroll  q quit")
	return m.status() + "\n" + body + "\n" + footer
}

// newDashScreen is the screen for env. Its main read leaves the slow reads
// out, and the screen sends them apart.
func newDashScreen(ctx context.Context, env dashEnv, interval time.Duration, watch bool) *dashTUI {
	env.deferSlow = true
	m := newDashTUI(ctx, func(ctx context.Context) (dashSnapshot, error) { return collectDash(ctx, env) }, interval, watch)
	m.parts = dashSlowParts(env, func() time.Time { return m.now() })
	return m
}

func runDashTUI(ctx context.Context, env dashEnv, interval time.Duration, watch bool) error {
	m := newDashScreen(ctx, env, interval, watch)
	defer m.cancel()
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
