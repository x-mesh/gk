package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"
)

// fleetStatusRank orders statuses worst-first for the repo roll-up. It matches
// the single-worktree precedence in fleetStatus (paused outranks conflict): a
// repo's headline is its most urgent worktree. "error" — a repo that could not
// be gathered — sits above everything so it is never hidden.
var fleetStatusRank = map[string]int{
	"error":    8,
	"paused":   7,
	"conflict": 6,
	"dirty":    5,
	"diverged": 4,
	"ahead":    3,
	"behind":   2,
	"clean":    1,
}

func fleetGroupRollup(group []fleetEntryJSON) string {
	worst := "clean"
	for _, e := range group {
		if fleetStatusRank[e.Status] > fleetStatusRank[worst] {
			worst = e.Status
		}
	}
	return worst
}

// fleetRow is one visible line in the grouped multi-repo view: a repo header or
// a worktree row beneath it.
type fleetRow struct {
	header    bool
	repoRoot  string
	label     string // header: repo basename
	rollup    string // header: worst-wins status
	count     int    // header: worktree count
	collapsed bool   // header: folded?
	entry     fleetEntryJSON
}

// buildFleetRows flattens repo-grouped entries into renderable rows honoring the
// collapsed set. entries must already be sorted by repo_root (gatherFleetMulti
// does this), so a single linear pass groups them.
func buildFleetRows(entries []fleetEntryJSON, collapsed map[string]bool) []fleetRow {
	var rows []fleetRow
	for i := 0; i < len(entries); {
		root := entries[i].RepoRoot
		j := i
		for j < len(entries) && entries[j].RepoRoot == root {
			j++
		}
		group := entries[i:j]
		folded := collapsed[root]
		rows = append(rows, fleetRow{
			header:    true,
			repoRoot:  root,
			label:     group[0].Repo,
			rollup:    fleetGroupRollup(group),
			count:     len(group),
			collapsed: folded,
		})
		if !folded {
			for _, e := range group {
				rows = append(rows, fleetRow{repoRoot: root, entry: e})
			}
		}
		i = j
	}
	return rows
}

// initialCollapsed folds repos whose roll-up is clean at startup: on a wide
// scan (20+ repos in the reference layout) the interesting rows are the ones
// with work in flight, and a folded clean repo is still one glance — dot +
// roll-up — away; space unfolds. Startup-only policy: a repo that turns
// dirty (or clean) later keeps whatever fold state the user left it in, so
// the table never reshuffles itself mid-session.
func initialCollapsed(entries []fleetEntryJSON) map[string]bool {
	collapsed := map[string]bool{}
	for i := 0; i < len(entries); {
		root := entries[i].RepoRoot
		j := i
		for j < len(entries) && entries[j].RepoRoot == root {
			j++
		}
		if fleetGroupRollup(entries[i:j]) == "clean" {
			collapsed[root] = true
		}
		i = j
	}
	return collapsed
}

// fleetRowKey identifies a row across polls so the cursor stays put when the row
// list is rebuilt. Header rows carry an empty path.
type fleetRowKey struct {
	repoRoot string
	path     string
}

func fleetRowKeyOf(r fleetRow) fleetRowKey {
	if r.header {
		return fleetRowKey{repoRoot: r.repoRoot}
	}
	return fleetRowKey{repoRoot: r.repoRoot, path: r.entry.Path}
}

// renderFleetGrouped draws the multi-repo dashboard: a count+clock header, then
// one line per repo group (▼/▶ fold arrow, roll-up dot, label, worktree count)
// with each repo's worktrees indented beneath when expanded. detail joins the
// cursor row's master-detail panel beside the table (worktree rows only —
// a header row has no single entry to detail); feed feeds its event tail.
func renderFleetGrouped(rows []fleetRow, cursor int, now time.Time, width int, detail int, feed []fleetFeedEvent, totalRepos, totalWts int) string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	if width <= 0 || width > 120 {
		width = 80
	}
	repos, wts := 0, 0
	for _, r := range rows {
		if r.header {
			repos++
			wts += r.count
		}
	}

	var b strings.Builder
	// A filtered view says so in the header — "5/21 repos" reads as "16 are
	// hidden", where a bare "5 repos" would read as "that's everything".
	repoN, wtN := fmt.Sprintf("%d", repos), fmt.Sprintf("%d", wts)
	if totalRepos > repos {
		repoN = fmt.Sprintf("%d/%d", repos, totalRepos)
	}
	if totalWts > wts {
		wtN = fmt.Sprintf("%d/%d", wts, totalWts)
	}
	count := fmt.Sprintf("%s %s · %s %s",
		repoN, pluralize(totalRepos, "repo", "repos"),
		wtN, pluralize(totalWts, "worktree", "worktrees"))
	left := lipgloss.NewStyle().Bold(true).Render("gk watch") + "  " + dim.Render(count)
	header := left
	if !now.IsZero() {
		clockText := now.Format("15:04:05")
		clock := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true).Render("●") + dim.Render(" "+clockText)
		gap := width - runewidth.StringWidth("gk watch  "+count) - runewidth.StringWidth("● "+clockText)
		if gap < 1 {
			gap = 1
		}
		header = left + strings.Repeat(" ", gap) + clock
	}
	b.WriteString(header + "\n" + dim.Render(strings.Repeat("─", width)) + "\n")

	if len(rows) == 0 {
		if totalWts > 0 {
			b.WriteString(dim.Render("  (nothing matches the filter — press f to widen)"))
		} else {
			b.WriteString(dim.Render("  (no repos)"))
		}
		return b.String()
	}

	table := renderFleetGroupedTable(rows, cursor, now)
	if detail != fleetDetailOff && cursor >= 0 && cursor < len(rows) && width >= 64 {
		if r := rows[cursor]; !r.header && r.entry.Status != "error" {
			var panel string
			if detail == fleetDetailFeed {
				panel = renderFleetDetailFeed(r.entry, now, fleetEventTail(feed, r.entry.Path, fleetDetailFeedLines))
			} else {
				panel = renderFleetDetail(r.entry, now, fleetEventTail(feed, r.entry.Path, 3))
			}
			b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, table, "   ", panel))
			return b.String()
		}
	}
	b.WriteString(table)
	return b.String()
}

// renderFleetGroupedTable draws the grouped rows themselves — split out so the
// detail panel can be joined beside just the table, not the header.
func renderFleetGroupedTable(rows []fleetRow, cursor int, now time.Time) string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	var b strings.Builder
	for i, r := range rows {
		caret := "  "
		if i == cursor {
			caret = "› "
		}
		var line string
		switch {
		case r.header:
			arrow := "▼"
			if r.collapsed {
				arrow = "▶"
			}
			dot := lipgloss.NewStyle().Foreground(fleetStatusColor(r.rollup)).Render("●")
			label := lipgloss.NewStyle().Bold(true).Render(clip(r.label, 24))
			line = fmt.Sprintf("%s%s %s %s %s  %s",
				caret, arrow, dot, label,
				dim.Render(fmt.Sprintf("(%d)", r.count)),
				dim.Render(r.rollup))
		case r.entry.Status == "error":
			dot := lipgloss.NewStyle().Foreground(fleetStatusColor("error")).Render("●")
			line = fmt.Sprintf("%s    %s %s", caret, dot, dim.Render("unreachable: "+r.entry.Error))
		default:
			e := r.entry
			dot := lipgloss.NewStyle().Foreground(fleetStatusColor(e.Status)).Render("●")
			branch := clip(e.Branch, 18)
			if e.Current {
				branch += "*"
			}
			if e.Operation != "" {
				branch += " ⏸"
			}
			line = fmt.Sprintf("%s    %s %-21s  %-8s  %-11s  %-14s  %s",
				caret, dot, branch,
				fleetDiffLabel(e.Ahead, e.Behind),
				fleetDirtyLabel(e.Dirty),
				fleetLastChangeLabel(e.LastChange),
				fleetActiveStyled(e, now))
		}
		if i == cursor {
			line = lipgloss.NewStyle().Bold(true).Render(line)
		}
		b.WriteString(line)
		if i < len(rows)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// rebuildRows regenerates the flattened row list from entries+collapsed and
// restores the cursor onto the same logical row (repo/worktree) it sat on, so a
// poll or a fold/unfold does not make the selection jump.
func (m *fleetModel) rebuildRows() {
	var key fleetRowKey
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		key = fleetRowKeyOf(m.rows[m.cursor])
	}
	m.rows = buildFleetRows(m.viewEntries(), m.collapsed)
	if key != (fleetRowKey{}) {
		for i, r := range m.rows {
			if fleetRowKeyOf(r) == key {
				m.cursor = i
				break
			}
		}
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// cursorWatchTarget returns the worktree path to drill into for the current
// cursor: the worktree under the cursor, or — when the cursor is on a header —
// that repo's current worktree (else its first). It returns "" when nothing is
// watchable (an unreachable repo, or an empty/out-of-range cursor).
func (m fleetModel) cursorWatchTarget() string {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return ""
	}
	r := m.rows[m.cursor]
	if !r.header {
		if r.entry.Status == "error" {
			return ""
		}
		return r.entry.Path
	}
	var first string
	for _, e := range m.entries {
		if e.RepoRoot != r.repoRoot || e.Status == "error" {
			continue
		}
		if e.Current {
			return e.Path
		}
		if first == "" {
			first = e.Path
		}
	}
	return first
}

// toggleCursorRepo folds/unfolds the repo the cursor is on (works whether the
// cursor sits on the header or a worktree row) and rebuilds the row list.
func (m *fleetModel) toggleCursorRepo() {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return
	}
	root := m.rows[m.cursor].repoRoot
	m.collapsed[root] = !m.collapsed[root]
	m.rebuildRows()
}

// runFleetMultiTUI runs the grouped multi-repo dashboard. It reuses fleetModel
// with multi=true; polling calls gatherFleetMulti over the discovered repo set.
func runFleetMultiTUI(ctx context.Context, cmd *cobra.Command, repos []repoIdent, sem chan struct{}, initial []fleetEntryJSON, interval time.Duration, feedStats bool, filter int) error {
	m := fleetModel{
		ctx:       ctx,
		cmd:       cmd,
		interval:  interval,
		filter:    filter,
		entries:   initial,
		now:       time.Now(),
		multi:     true,
		repos:     repos,
		sem:       sem,
		collapsed: initialCollapsed(initial),
		detail:    fleetDetailFields,
		showFeed:  true,
		feedStats: feedStats,
		prevSigs:  map[string]map[string]fileSig{},
		ws:        newFleetWatchSet(ctx, initial),
		notify:    fleetNotifyConfig(),
	}
	defer m.ws.Close()
	m.feed, m.prevSigs = applyFeedDiff(m.prevSigs, initial, nil, m.now)
	m.rebuildRows()
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
