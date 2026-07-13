package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/x-mesh/gk/internal/git"
)

// --- consolidated worktree change scan ---------------------------------------
//
// fleet used to pay two `git status --porcelain` runs per worktree per poll:
// one for the dirty counts (countContextDirty) and one for the newest-change
// mtime (worktreeNewestChangeMtime). This scan replaces both with a single
// `--no-optional-locks status --porcelain -z` pass that yields the counts,
// the per-path change signatures (the feed's diff input), and the most
// recently touched file — so adding file-level visibility made fleet cheaper,
// not more expensive. --no-optional-locks matters here: fleet polls while
// agents commit in those very worktrees, and an optional-lock status would
// contend on their .git/index.lock.

// worktreeScan is one worktree's change state at a poll tick.
type worktreeScan struct {
	dirty contextDirtyJSON
	// sigs maps changed path → signature (porcelain XY + on-disk mtime).
	// Diff stats are zero here — the feed fills them in only when the
	// opt-in stats mode pays for the extra numstat calls.
	sigs map[string]fileSig
	// newestPath/newestMtime identify the most recently modified changed
	// file — the "what did the agent just touch" signal.
	newestPath  string
	newestMtime time.Time
}

// scanWorktreeChanges runs the consolidated scan for one worktree. withStats
// additionally pays two `git diff -U0` runs to fill per-path +/- counts and
// changed-function names (the feed-stats opt-in). Best-effort: any git
// failure returns a zero scan (clean), matching the degrade-to-nil convention
// of the probes it replaces.
func scanWorktreeChanges(ctx context.Context, runner *git.ExecRunner, root string, withStats bool) worktreeScan {
	out, _, err := runner.Run(ctx, "--no-optional-locks", "status", "--porcelain", "-z")
	if err != nil {
		return worktreeScan{}
	}
	s := parseWorktreeScan(string(out), root)
	if withStats && len(s.sigs) > 0 {
		for p, ds := range changeDiffProfile(ctx, runner) {
			if sig, ok := s.sigs[p]; ok {
				sig.added, sig.removed = ds.added, ds.removed
				sig.symbols = strings.Join(ds.symbols, ", ")
				s.sigs[p] = sig
			}
		}
		// Untracked files never appear in `git diff` — profile them from the
		// content so a brand-new file still carries +N and a symbol.
		for p, sig := range s.sigs {
			if sig.xy != "??" {
				continue
			}
			if up, ok := untrackedChangeProfile(root, p); ok {
				sig.added = up.added
				sig.symbols = strings.Join(up.symbols, ", ")
				s.sigs[p] = sig
			}
		}
	}
	return s
}

// parseWorktreeScan derives counts, signatures, and the newest change from raw
// `status --porcelain -z` output. Split from scanWorktreeChanges so the parse
// is unit-testable against fixed porcelain output.
func parseWorktreeScan(raw, root string) worktreeScan {
	s := worktreeScan{sigs: map[string]fileSig{}}
	toks := splitPorcelainZ(raw)
	for _, t := range toks {
		xy, path := t.xy, t.path
		x, y := xy[0], xy[1]

		// Same tally rules as countContextDirty — one classification source
		// would be better, but that parser is line-based (non -z); keep the
		// rules in sync (see countContextDirty in context.go).
		switch {
		case x == '?' && y == '?':
			s.dirty.Untracked++
		case x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D'):
			s.dirty.Conflicts++
		default:
			if x != ' ' {
				s.dirty.Staged++
			}
			if y != ' ' {
				s.dirty.Unstaged++
			}
		}

		// mtime 0 = not on disk (e.g. a staged delete): it gets a signature —
		// deletions are changes — but never wins the newest-change slot.
		// (time.Time{}.UnixNano() is a large negative sentinel, not 0.)
		var mtimeNS int64
		if root != "" {
			if fi, serr := os.Stat(filepath.Join(root, path)); serr == nil {
				mt := fi.ModTime()
				mtimeNS = mt.UnixNano()
				if mt.After(s.newestMtime) {
					s.newestMtime = mt
					s.newestPath = path
				}
			}
		}
		s.sigs[path] = fileSig{xy: xy, mtime: mtimeNS}
	}
	return s
}

// porcelainZRecord is one parsed `status --porcelain -z` record.
type porcelainZRecord struct {
	xy   string
	path string
}

// splitPorcelainZ tokenizes porcelain v1 -z output into (XY, path) records,
// consuming the extra source-path token a rename/copy record carries.
func splitPorcelainZ(raw string) []porcelainZRecord {
	toks := splitNulTokens(raw)
	var recs []porcelainZRecord
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if len(t) < 4 { // "XY p" minimum
			continue
		}
		x, y := t[0], t[1]
		recs = append(recs, porcelainZRecord{xy: t[:2], path: t[3:]})
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			i++ // consume the rename/copy source path token
		}
	}
	return recs
}

func splitNulTokens(raw string) []string {
	var toks []string
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\x00' {
			toks = append(toks, raw[start:i])
			start = i + 1
		}
	}
	if start < len(raw) {
		toks = append(toks, raw[start:])
	}
	return toks
}

// scanTotals rolls a worktree's per-file signatures up into a diffstat: how
// many files are in flight and how many lines they add/remove against HEAD.
// The line counts are zero outside feed-stats mode (no diff runs, no counts);
// the file count is free either way.
func scanTotals(sigs map[string]fileSig) (files, added, removed int) {
	for _, s := range sigs {
		files++
		added += s.added
		removed += s.removed
	}
	return files, added, removed
}

// fleetTotals is the same roll-up across a set of worktrees — the number the
// header and the repo group lines report.
func fleetTotals(entries []fleetEntryJSON) (files, added, removed int) {
	for _, e := range entries {
		files += e.Files
		added += e.Added
		removed += e.Removed
	}
	return files, added, removed
}

// --- session churn ---------------------------------------------------------
//
// The diffstat above answers "how much is uncommitted right now" — and it
// drops to zero the moment an agent commits. Churn answers the other half:
// how much work went by while watch was up. Signatures carry each file's
// counts against HEAD, so a poll's churn is the POSITIVE movement of those
// counts; a shrink means a commit (or a revert) reset the baseline, not
// negative work, and contributes nothing. A worktree seen for the first time
// is a silent baseline, exactly as it is for the feed: whatever was already
// dirty when watch started is not something watch saw happen.
//
// This is why the feed lines cannot simply be summed: their +/− are cumulative
// against HEAD, so a file re-touched five times would be counted five times.

type fleetChurn struct {
	files          map[string]bool // "worktree\x00path" — distinct files touched
	added, removed int
}

func (c fleetChurn) touched() int { return len(c.files) }

func (c fleetChurn) any() bool { return c.added > 0 || c.removed > 0 || len(c.files) > 0 }

// accumulate folds one poll's entries into the running churn, diffing them
// against the signature state that produced the previous frame (so it must run
// before applyFeedDiff replaces that state).
func (c *fleetChurn) accumulate(prevSigs map[string]map[string]fileSig, entries []fleetEntryJSON) {
	for _, e := range entries {
		prev, seen := prevSigs[e.Path]
		if e.sigs == nil || !seen {
			continue // an error entry carries no scan; an unseen worktree is a baseline
		}
		for p, cur := range e.sigs {
			old, existed := prev[p] // zero value when the file is new this poll
			if existed && old == cur {
				continue // untouched since the last poll
			}
			if c.files == nil {
				c.files = map[string]bool{}
			}
			c.files[e.Path+"\x00"+p] = true
			if d := cur.added - old.added; d > 0 {
				c.added += d
			}
			if d := cur.removed - old.removed; d > 0 {
				c.removed += d
			}
		}
	}
}

// --- cross-worktree change feed -----------------------------------------------
//
// The feed is the fleet-wide counterpart of `gk status --watch`: a merged
// timeline of which files changed in WHICH worktree. Poll N produces each
// worktree's signature set; diffing it against poll N-1 yields events. The
// first scan of a worktree is a silent baseline — fleet reports what changes
// from now on, not a dump of everything already dirty at startup.

// fleetFeedCap bounds the in-memory timeline. Old events beyond the cap are
// dropped from the front — the feed is a live tail, not a log file.
const fleetFeedCap = 200

// fleetFeedEvent is one line in the merged timeline.
type fleetFeedEvent struct {
	ts      time.Time
	repo    string // repo label in multi-repo mode; "" single-repo
	branch  string
	wt      string // worktree path (the --events contract's `path`)
	path    string // changed file, repo-relative
	glyph   string // changeGlyph of the underlying event
	note    string // "new" | "re-touched" | "" (cleared)
	cleared bool
	added   int // populated only in feed-stats mode
	removed int
	symbols string // changed-function names, feed-stats mode only
}

// applyFeedDiff diffs the fresh entries against prevSigs, appends the resulting
// events to feed (ring-capped), and returns the updated feed plus the new
// signature state. A worktree absent from prevSigs is a baseline: recorded,
// no events. Worktrees that vanished from the fleet are dropped from the state.
func applyFeedDiff(prevSigs map[string]map[string]fileSig, entries []fleetEntryJSON, feed []fleetFeedEvent, now time.Time) ([]fleetFeedEvent, map[string]map[string]fileSig) {
	next := make(map[string]map[string]fileSig, len(entries))
	for _, e := range entries {
		if e.sigs == nil {
			// Error/synthetic entries carry no scan — keep prior state so a
			// transient gather failure doesn't turn into a fake baseline reset.
			if prev, ok := prevSigs[e.Path]; ok {
				next[e.Path] = prev
			}
			continue
		}
		next[e.Path] = e.sigs
		prev, seen := prevSigs[e.Path]
		if !seen {
			continue // silent baseline
		}
		for _, ev := range diffChangeSnapshots(prev, e.sigs, now) {
			feed = append(feed, fleetFeedEvent{
				ts: ev.ts, repo: e.Repo, branch: e.Branch, wt: e.Path, path: ev.path,
				glyph: changeGlyph(ev), note: ev.note, cleared: ev.cleared,
				added: ev.added, removed: ev.removed, symbols: ev.symbols,
			})
		}
	}
	if len(feed) > fleetFeedCap {
		feed = append([]fleetFeedEvent(nil), feed[len(feed)-fleetFeedCap:]...)
	}
	return feed, next
}

// --- view filtering & sorting ---------------------------------------------------

// Filter cycle ('f'): everything → worktrees someone is plausibly in right
// now → worktrees with uncommitted work → worktrees needing a human. Sort
// cycle ('s'): gather order (current-first/branch) → most recently active →
// most urgent status.
//
// `active` is the multi-repo default: a wide scan mostly finds projects
// nobody has touched in weeks, and the dashboard exists for the ones being
// worked on — everything else is one `f` (or --filter all) away.
const (
	fleetFilterAll = iota
	fleetFilterActive
	fleetFilterBusy
	fleetFilterStuck
	fleetFilterModes
)

const (
	fleetSortDefault = iota
	fleetSortActivity
	fleetSortStatus
	fleetSortModes
)

func fleetFilterName(f int) string {
	switch f {
	case fleetFilterActive:
		return "active"
	case fleetFilterBusy:
		return "busy"
	case fleetFilterStuck:
		return "stuck"
	default:
		return "all"
	}
}

// fleetFilterByName is the flag/config → mode mapping (ok=false on unknown).
func fleetFilterByName(name string) (int, bool) {
	switch name {
	case "all":
		return fleetFilterAll, true
	case "active":
		return fleetFilterActive, true
	case "busy":
		return fleetFilterBusy, true
	case "stuck":
		return fleetFilterStuck, true
	}
	return 0, false
}

func fleetSortName(s int) string {
	switch s {
	case fleetSortActivity:
		return "activity"
	case fleetSortStatus:
		return "status"
	default:
		return "default"
	}
}

// fleetFilterEntries keeps the entries matching the filter mode. active =
// someone is plausibly in it right now (the watcher-budget predicate: current
// checkout, dirty, paused op, or moved within the last hour — errors kept so
// a broken repo can't hide); busy = has uncommitted work or needs attention;
// stuck = blocked on a human (paused op, conflicts, unreachable repo).
func fleetFilterEntries(entries []fleetEntryJSON, mode int, now time.Time) []fleetEntryJSON {
	if mode == fleetFilterAll {
		return entries
	}
	var out []fleetEntryJSON
	for _, e := range entries {
		switch mode {
		case fleetFilterActive:
			if fleetEntryActive(e, now) || e.Status == "error" {
				out = append(out, e)
			}
		case fleetFilterBusy:
			switch e.Status {
			case "dirty", "conflict", "paused", "error":
				out = append(out, e)
			}
		case fleetFilterStuck:
			switch e.Status {
			case "conflict", "paused", "error":
				out = append(out, e)
			}
		}
	}
	return out
}

// fleetSortEntries orders a copy of entries by the sort mode. RepoRoot stays
// the primary key so multi-repo grouping (a linear pass) survives any mode.
func fleetSortEntries(entries []fleetEntryJSON, mode int) []fleetEntryJSON {
	if mode == fleetSortDefault {
		return entries
	}
	out := make([]fleetEntryJSON, len(entries))
	copy(out, entries)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RepoRoot != out[j].RepoRoot {
			return out[i].RepoRoot < out[j].RepoRoot
		}
		switch mode {
		case fleetSortActivity:
			return out[i].lastActive.After(out[j].lastActive)
		case fleetSortStatus:
			return fleetStatusRank[out[i].Status] > fleetStatusRank[out[j].Status]
		}
		return false
	})
	return out
}

// fleetFeedStatLabel renders an event's ± counts in the same green/red form
// `gk status --watch` uses, so the two feeds read identically.
func fleetFeedStatLabel(ev fleetFeedEvent) string {
	if ev.cleared || (ev.added == 0 && ev.removed == 0) {
		return ""
	}
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	var parts []string
	if ev.added > 0 {
		parts = append(parts, green.Render(fmt.Sprintf("+%d", ev.added)))
	}
	if ev.removed > 0 {
		parts = append(parts, red.Render(fmt.Sprintf("−%d", ev.removed)))
	}
	return "  " + strings.Join(parts, " ")
}

// renderFleetFeed draws the newest `lines` events, oldest first, under a rule —
// the fleet-wide file timeline. multi controls whether the repo label is shown.
func renderFleetFeed(feed []fleetFeedEvent, width, lines int, multi bool) string {
	if len(feed) == 0 || lines <= 0 {
		return ""
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	width = fleetWidth(width)
	// Elastic path column. Everything around it is bounded — the head
	// (timestamp · glyph · [who]) and the tail (symbols, ± stats, note) — so the
	// columns a wide terminal adds go to the path, the field that was getting
	// truncated (`…/SpaceMeshApp/ContentView…`) while half the screen sat empty.
	const feedHead = 11 + 1 + 1 + 1 + 24 + 1 // ts + glyph + [who] + gutters
	const feedTail = 34 + 26                 // symbols + stats + note
	pathW := min(max(width-feedHead-feedTail, 40), 80)

	start := len(feed) - lines
	if start < 0 {
		start = 0
	}
	var b strings.Builder
	b.WriteString(dim.Render(strings.Repeat("─", width)))
	for _, ev := range feed[start:] {
		who := ev.branch
		if multi && ev.repo != "" {
			who = ev.repo + ":" + ev.branch
		}
		line := fmt.Sprintf("%s %s %s %s",
			dim.Render(ev.ts.Format(changeTSFormat)),
			ev.glyph,
			dim.Render("["+clip(who, 22)+"]"),
			clipLeft(ev.path, pathW),
		)
		if ev.symbols != "" {
			line += dim.Render(" · " + clip(ev.symbols, 34))
		}
		line += fleetFeedStatLabel(ev)
		if ev.note != "" {
			line += dim.Render("  " + ev.note)
		}
		b.WriteString("\n" + line)
	}
	return b.String()
}
