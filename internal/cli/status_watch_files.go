package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/diff"
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
	// symbols is the display-joined list of function contexts the path's
	// hunks touch ("openZoom, closeZoom"). It participates in the equality
	// comparison, which is safe: it derives from the same diff as the +/-
	// counts, so it never flaps independently of a real change.
	symbols string
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
	symbols string // display-joined changed-function names ("" when unknown)
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
	stats := changeDiffProfile(ctx, runner)
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
		if xy == "??" {
			// Untracked files never appear in `git diff` — profile them from
			// the content so new files still carry +N and a symbol.
			if up, ok := untrackedChangeProfile(root, path); ok {
				ds = up
			}
		}
		var mtime int64
		if root != "" {
			if fi, serr := os.Stat(filepath.Join(root, path)); serr == nil {
				mtime = fi.ModTime().UnixNano()
			}
		}
		sigs[path] = fileSig{xy: xy, added: ds.added, removed: ds.removed,
			symbols: strings.Join(ds.symbols, ", "), mtime: mtime}
	}
	return sigs
}

// changeProfile is one path's +/- line counts plus the function contexts its
// hunks touch — what the numstat pair used to answer, upgraded with "which
// function", from the same two git calls.
type changeProfile struct {
	added   int
	removed int
	symbols []string // extracted names, deduped, first-seen order, capped
}

// changeProfileSymbolCap bounds how many distinct function names one path
// carries into the feed — an event line must stay glanceable, and past a few
// names the honest summary is "lots of this file changed" anyway.
const changeProfileSymbolCap = 3

// changeDiffProfile fetches per-path +/- counts AND changed-function names
// for the feed, replacing the former two `git diff --numstat` runs with two
// `git diff -U0` parses (staged + unstaged merged, keyed by the NEW path so
// renames match the porcelain snapshot). Same subprocess count, one upgrade:
// unified hunk headers carry git's own function-context detection — no
// .gitattributes or external tooling required — so feed events can name the
// function, not just the file. --no-optional-locks for the same reason as
// changeSnapshot (never contend with the agent's own `git add` on
// index.lock); -U0 keeps the payload to changed lines only.
func changeDiffProfile(ctx context.Context, runner *git.ExecRunner) map[string]changeProfile {
	merged := map[string]changeProfile{}
	for _, args := range [][]string{
		{"--no-optional-locks", "diff", "-U0"},
		{"--no-optional-locks", "diff", "--cached", "-U0"},
	} {
		out, _, err := runner.Run(ctx, args...)
		if err != nil {
			continue
		}
		res, perr := diff.ParseUnifiedDiff(bytes.NewReader(out))
		if perr != nil {
			continue
		}
		for _, f := range res.Files {
			path := f.NewPath
			if path == "" {
				path = f.OldPath
			}
			if path == "" || f.IsBinary {
				continue
			}
			p := merged[path]
			p.added += f.AddedLines
			p.removed += f.DeletedLines
			for _, h := range f.Hunks {
				name := funcContextName(h.FuncName)
				if name == "" {
					// git's funcname is the ENCLOSING context above the hunk,
					// so it is empty exactly where the interesting name is
					// inside the hunk itself: a new file, a change at the top
					// of a file, or a language the default heuristic can't
					// read (CSS selectors start with '.'). Scan the changed
					// lines for a definition instead.
					name = definitionNameFromHunk(path, h)
				}
				p.symbols = appendSymbol(p.symbols, name)
			}
			merged[path] = p
		}
	}
	return merged
}

// appendSymbol adds one extracted name to the capped, deduped symbol list.
func appendSymbol(into []string, name string) []string {
	if name == "" || len(into) >= changeProfileSymbolCap {
		return into
	}
	for _, s := range into {
		if s == name {
			return into
		}
	}
	return append(into, name)
}

// funcContextName reduces a hunk's full function context ("func (m
// fleetModel) openZoom(path string) (tea.Model, tea.Cmd)") to the bare name
// ("openZoom") for the feed line. The heuristic: drop a Go method receiver
// group, then take the last identifier before the argument list's "(". A
// context with no "(" (e.g. "type fleetModel struct") is used as-is — better
// an odd label than a dropped signal. The result is comma-free (a defensive
// cut at the first comma), so display strings joined with ", " stay
// unambiguous.
func funcContextName(raw string) string {
	name := strings.TrimSpace(raw)
	if name == "" {
		return ""
	}
	// Go method receiver: "func (recv T) name(args...)" — cut the receiver
	// group so the first "(" below is the argument list, not the receiver.
	if rest, ok := strings.CutPrefix(name, "func ("); ok {
		if end := strings.Index(rest, ")"); end >= 0 {
			name = "func " + strings.TrimSpace(rest[end+1:])
		}
	}
	if paren := strings.Index(name, "("); paren >= 0 {
		name = strings.TrimSpace(name[:paren])
	}
	if fields := strings.Fields(name); len(fields) > 0 {
		name = fields[len(fields)-1]
	}
	name = strings.TrimSpace(strings.SplitN(name, ",", 2)[0])
	// A paren-less context keeps the definition line's trailing punctuation
	// ("class TestScanHandler:" → "TestScanHandler:") — strip it so Python
	// classes and friends read as bare names.
	name = strings.TrimRight(name, ":;{")
	return clip(name, 40)
}

// --- definition scan: the fallback when git offers no function context ------

// definitionPatterns maps a language family to the definition-line shapes its
// changed lines may carry. Keyed by extension so a brace in Go ("if x {")
// can never be misread as a CSS selector — each family only ever sees its own
// shapes. The generic set covers the keyword-led definitions that are
// unambiguous in any language.
var (
	rePyDef      = regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_][A-Za-z0-9_]*)`)
	rePyClass    = regexp.MustCompile(`^\s*class\s+([A-Za-z_][A-Za-z0-9_.]*)`)
	reGoFunc     = regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)`)
	reGoType     = regexp.MustCompile(`^type\s+([A-Za-z_][A-Za-z0-9_]*)`)
	reRustFn     = regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?fn\s+([A-Za-z_][A-Za-z0-9_]*)`)
	reJSFunction = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][A-Za-z0-9_$]*)`)
	reJSArrow    = regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:async\s*)?(?:\(|function\b)`)
	reJSClass    = regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?class\s+([A-Za-z_$][A-Za-z0-9_$]*)`)
	reRubyDef    = regexp.MustCompile(`^\s*def\s+([A-Za-z_][A-Za-z0-9_?!.]*)`)
	reCSSRule    = regexp.MustCompile(`^\s*([.#]?[A-Za-z][A-Za-z0-9_-]*(?:\s*[.#:][A-Za-z0-9_-]+)*)\s*[^{;]*\{`)
)

var definitionPatterns = map[string][]*regexp.Regexp{
	".py":   {rePyDef, rePyClass},
	".go":   {reGoFunc, reGoType},
	".rs":   {reRustFn},
	".js":   {reJSFunction, reJSArrow, reJSClass},
	".jsx":  {reJSFunction, reJSArrow, reJSClass},
	".ts":   {reJSFunction, reJSArrow, reJSClass},
	".tsx":  {reJSFunction, reJSArrow, reJSClass},
	".mjs":  {reJSFunction, reJSArrow, reJSClass},
	".cjs":  {reJSFunction, reJSArrow, reJSClass},
	".rb":   {reRubyDef},
	".css":  {reCSSRule},
	".scss": {reCSSRule},
	".less": {reCSSRule},
}

// genericDefinitionPatterns are the keyword-led shapes safe to try on any
// other extension — a line starting with def/class/func/fn/function is a
// definition in whatever language it appears.
var genericDefinitionPatterns = []*regexp.Regexp{rePyDef, rePyClass, reGoFunc, reRustFn, reJSFunction}

// definitionNameFromHunk scans a hunk's changed lines for a definition and
// returns the first name found. Added lines are scanned before deleted ones:
// for a new function the definition line IS an addition, and for a removed
// one it survives only among the deletions.
func definitionNameFromHunk(path string, h diff.Hunk) string {
	pats := definitionPatternsFor(path)
	for _, kind := range []diff.LineKind{diff.LineAdded, diff.LineDeleted} {
		for _, ln := range h.Lines {
			if ln.Kind != kind {
				continue
			}
			if name := matchDefinition(pats, ln.Content); name != "" {
				return name
			}
		}
	}
	return ""
}

func definitionPatternsFor(path string) []*regexp.Regexp {
	if pats, ok := definitionPatterns[strings.ToLower(filepath.Ext(path))]; ok {
		return pats
	}
	return genericDefinitionPatterns
}

func matchDefinition(pats []*regexp.Regexp, line string) string {
	for _, re := range pats {
		if m := re.FindStringSubmatch(line); m != nil {
			return clip(strings.TrimSpace(m[1]), 40)
		}
	}
	return ""
}

// untrackedProfileMaxBytes caps how much of an untracked file the profile
// reads. Past this, no numbers beat approximate ones — a giant artifact must
// never stall a poll tick.
const untrackedProfileMaxBytes = 256 * 1024

// untrackedChangeProfile derives a profile for a path `git diff` cannot see
// (untracked): every line counts as an addition and definitions come from
// scanning the content directly — otherwise a brand-new file shows neither
// +/- nor a symbol on the feed, exactly where "what is the agent writing?"
// matters most. ok is false for unreadable, directory, binary-looking
// (NUL byte — git's own text sniff), or oversized files.
func untrackedChangeProfile(root, rel string) (changeProfile, bool) {
	if root == "" {
		return changeProfile{}, false
	}
	full := filepath.Join(root, rel)
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() || fi.Size() > untrackedProfileMaxBytes {
		return changeProfile{}, false
	}
	data, err := os.ReadFile(full)
	if err != nil || len(data) == 0 {
		return changeProfile{}, false
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return changeProfile{}, false
	}

	p := changeProfile{added: strings.Count(string(data), "\n")}
	if data[len(data)-1] != '\n' {
		p.added++ // final line without a trailing newline still counts
	}
	pats := definitionPatternsFor(rel)
	for _, line := range strings.Split(string(data), "\n") {
		if len(p.symbols) >= changeProfileSymbolCap {
			break
		}
		p.symbols = appendSymbol(p.symbols, matchDefinition(pats, line))
	}
	return p, true
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
			added: sig.added, removed: sig.removed, symbols: sig.symbols, note: note,
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
	fs, _ := newFSWatcher(cmd.Context(), runner, fsWatchDebounce, fsWatchCostBudget())
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
		next, trigger, err := nextPlainWatchTrigger(cmd.Context(), ticker.C, fsCh)
		fsCh = next
		if err != nil {
			return err
		}
		if trigger {
			snap()
		}
	}
}

// nextPlainWatchTrigger waits for one plain-watch wake-up. A closed fsnotify
// channel is retired by returning nil, because receiving from it forever would
// otherwise make the outer select spin after a watcher falls back to polling.
func nextPlainWatchTrigger(ctx context.Context, tick <-chan time.Time, fsCh <-chan struct{}) (<-chan struct{}, bool, error) {
	select {
	case <-ctx.Done():
		return fsCh, false, ctx.Err()
	case <-tick:
		return fsCh, true, nil
	case _, ok := <-fsCh:
		if !ok {
			return nil, false, nil
		}
		return fsCh, true, nil
	}
}

func plainEventLine(e changeEvent) string {
	sym := ""
	if e.symbols != "" {
		sym = "  · " + e.symbols
	}
	stat := ""
	if e.added > 0 || e.removed > 0 {
		stat = fmt.Sprintf("  +%d -%d", e.added, e.removed)
	}
	note := ""
	if e.note != "" {
		note = "  " + e.note
	}
	return fmt.Sprintf("%s  %s %s%s%s%s",
		e.ts.Format(changeTSFormat), changeGlyph(e), e.path, sym, stat, note)
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

	// Embedded mode: `gk fleet` hosts this model as its zoom view, inside the
	// fleet program. Fleet drives every refresh (this model arms no tick or fs
	// chains of its own), so these fields only shape the render and let fleet
	// discard frames from a replaced zoom target.
	embedded bool
	fsLive   bool // fleet's fs watcher covers this worktree → header reads live
	gen      int  // stamped into changeFrameMsg; fleet drops mismatches
	// captureDash overrides the dashboard capture: the in-process capture
	// (captureStatusFrame) renders via the global --repo flag, which points at
	// the repo fleet started in — not the zoom target.
	captureDash func() string

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
	gen    int // producing model's generation (embedded mode); 0 standalone
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
	ago      string // relative age of the HEAD commit ("22m", "" when <1m)
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
	if ago, sha, subj := headCommitInfo(cmd, runner); sha != "" {
		h.sha = shortSHA(sha)
		h.ago = ago
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
	gen := m.gen
	capture := m.captureDash
	return func() tea.Msg {
		curr := changeSnapshot(ctx, runner, root)
		msg := changeFrameMsg{
			curr:   curr,
			events: diffChangeSnapshots(prev, curr, now),
			head:   fetchHeadInfo(cmd, runner),
			ts:     now,
			gen:    gen,
		}
		// The full status dashboard is only rendered (and thus captured) when
		// the user has toggled it on — keep the common feed path cheap.
		if showDash {
			if capture != nil {
				msg.dash = capture()
			} else {
				msg.dash = captureStatusFrame(cmd)
			}
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
	} else if m.fs != nil || m.fsLive {
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
	// HEAD commit age chip — "now" when committed under a minute ago, since
	// formatAge floors sub-minute spans to "". Tells the reader at a glance how
	// fresh the latest commit is: a commit landing mid-watch reads "now", then
	// ticks up ("1m", "2m", …) as later refreshes refetch the header.
	ageChip := h.ago
	if ageChip == "" {
		ageChip = "now"
	}
	if m.width > 20 {
		budget := m.width - 3 - runewidth.StringWidth(h.sha) - 2 - runewidth.StringWidth(ageChip) - 2
		if budget > 8 && runewidth.StringWidth(subj) > budget {
			subj = runewidth.Truncate(subj, budget, "…")
		}
	}
	line2 := "   " + yellow.Render(h.sha) + "  " + green.Render(ageChip) + "  " + dim.Render(subj)
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
	// The changed-function names ride between path and stats; they share the
	// path's width budget so a long path + symbols never push the +/- off
	// screen.
	sym := e.symbols
	// Reserve: indent(3)+ts(11)+sp(2)+glyph(1)+sp(1)+stat(~12)+note(~12).
	if m.width > 24 {
		budget := m.width - 3 - 11 - 2 - 1 - 1 - 12 - 12
		if budget > 8 && runewidth.StringWidth(path) > budget {
			path = runewidth.Truncate(path, budget, "…")
		}
		if sym != "" {
			symBudget := budget - runewidth.StringWidth(path) - 3
			if symBudget < 8 {
				sym = ""
			} else if runewidth.StringWidth(sym) > symBudget {
				sym = runewidth.Truncate(sym, symBudget, "…")
			}
		}
	}
	symPart := ""
	if sym != "" {
		symPart = "  " + dim.Render("· "+sym)
	}
	stat := styleStat(e)
	note := ""
	if e.note != "" {
		note = "  " + dim.Render(e.note)
	}
	return fmt.Sprintf("   %s  %s %s%s%s%s", ts, glyph, path, symPart, stat, note)
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
	// Embedded (fleet zoom): navigation keys live on the fleet breadcrumb
	// above; the interval keys are dropped because fleet drives the cadence.
	if m.embedded {
		return lipgloss.NewStyle().Faint(true).
			Render("   [s] status  [r] refresh  [p] pause  [c] clear")
	}
	return lipgloss.NewStyle().Faint(true).
		Render("   [s] status  [r] refresh  [p] pause  [c] clear  [+/-] interval  [q] quit")
}
