package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Live multi-worktree supervision dashboard",
		Long: `Watch every worktree at once: branch, ahead/behind, dirty/conflict state,
and which one is current. Built for supervising parallel work (e.g. several AI
agents each in their own worktree) — answers "who is dirty / stuck / behind"
without a per-worktree status probe.

The TUI polls 'git worktree list' on an interval; q quits, j/k move, r refreshes.
Under --json (or GK_AGENT) it instead emits a one-shot machine-readable snapshot
— the same data the TUI renders — so an agent or script can poll it directly.`,
		Args: cobra.NoArgs,
		RunE: runFleet,
	}
	cmd.Flags().Int("interval", 2, "poll interval in seconds (TUI mode)")
	rootCmd.AddCommand(cmd)
}

// fleetEntryJSON is the per-worktree fleet record — the contract `gk fleet
// --json` emits and the TUI consumes. Status is a derived at-a-glance roll-up
// so a reader does not have to interpret the dirty counts themselves.
type fleetEntryJSON struct {
	Path    string            `json:"path"`
	Branch  string            `json:"branch,omitempty"`
	Current bool              `json:"current,omitempty"`
	Ahead   int               `json:"ahead,omitempty"`
	Behind  int               `json:"behind,omitempty"`
	Dirty   *contextDirtyJSON `json:"dirty,omitempty"`
	Status  string            `json:"status"` // clean | dirty | conflict | ahead | behind | diverged
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
	// an interactive program that cannot read input.
	if !ui.IsTerminal() {
		fmt.Fprint(cmd.OutOrStdout(), renderFleet(entries, -1))
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
// dirty probe). The bare worktree is skipped — it holds no working state.
func gatherFleet(ctx context.Context, runner *git.ExecRunner) ([]fleetEntryJSON, error) {
	stdout, stderr, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("fleet: worktree list: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	entries := parseWorktreePorcelain(string(stdout))
	meta := loadWorktreeBranchMeta(ctx, runner)
	current := currentWorktreePath(ctx, runner)

	out := make([]fleetEntryJSON, 0, len(entries))
	for _, e := range entries {
		if e.Bare {
			continue
		}
		f := fleetEntryJSON{
			Path:    e.Path,
			Branch:  e.Branch,
			Current: current != "" && filepath.Clean(e.Path) == filepath.Clean(current),
		}
		if e.Detached {
			f.Branch = "(detached)"
		}
		if m, ok := meta[e.Branch]; ok {
			f.Ahead, f.Behind = m.Ahead, m.Behind
		}
		f.Dirty = worktreeDirtyAt(ctx, e.Path)
		f.Status = fleetStatus(f)
		out = append(out, f)
	}

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

func fleetStatus(f fleetEntryJSON) string {
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

// renderFleet draws the worktree table. cursor < 0 renders no selection marker
// (static snapshot); otherwise the cursor row is marked and highlighted.
func renderFleet(entries []fleetEntryJSON, cursor int) string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	var b strings.Builder

	count := fmt.Sprintf("%d %s", len(entries), pluralize(len(entries), "worktree", "worktrees"))
	b.WriteString(lipgloss.NewStyle().Bold(true).Render("gk fleet"))
	b.WriteString("  ")
	b.WriteString(dim.Render(count))
	b.WriteString("\n\n")

	if len(entries) == 0 {
		b.WriteString(dim.Render("  (no worktrees)\n"))
		return b.String()
	}

	const branchW = 22
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
		row := fmt.Sprintf("%s %s %-*s  %-8s  %-12s  %s",
			caret, dot, branchW+1, branch,
			fleetDiffLabel(e.Ahead, e.Behind),
			fleetDirtyLabel(e.Dirty),
			dim.Render(clip(e.Path, 48)),
		)
		if i == cursor {
			row = lipgloss.NewStyle().Bold(true).Render(row)
		}
		b.WriteString(row)
		b.WriteString("\n")
	}
	return b.String()
}

// --- TUI ---

type fleetDataMsg struct {
	entries []fleetEntryJSON
	err     error
}

type fleetRepollMsg struct{}

type fleetModel struct {
	ctx      context.Context
	runner   *git.ExecRunner
	interval time.Duration
	entries  []fleetEntryJSON
	cursor   int
	lastErr  error
	quitting bool
}

func (m fleetModel) Init() tea.Cmd {
	// First snapshot is already rendered from runFleet; schedule the next poll.
	return m.tickCmd()
}

func (m fleetModel) tickCmd() tea.Cmd {
	return tea.Tick(m.interval, func(time.Time) tea.Msg { return fleetRepollMsg{} })
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
	b.WriteString(renderFleet(m.entries, m.cursor))
	b.WriteString("\n")
	if m.lastErr != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("203")).
			Render("refresh failed: " + m.lastErr.Error()))
		b.WriteString("\n")
	}
	b.WriteString(dim.Render(fmt.Sprintf("j/k move · r refresh · q quit · polling every %s", m.interval)))
	return b.String()
}

func runFleetTUI(ctx context.Context, runner *git.ExecRunner, initial []fleetEntryJSON, interval time.Duration) error {
	m := fleetModel{ctx: ctx, runner: runner, interval: interval, entries: initial}
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
