package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

var statusVisFlags []string
var statusNoFetch bool
var statusTopN int
var statusLegend bool

// effectiveVis holds the resolved visualization set for the current runStatus
// invocation. Populated at the top of runStatus from flag > config > default
// and read by statusVisEnabled. A nil value means "no viz" (e.g., --vis none).
var effectiveVis []string

// statusFetchTimeout is the hard ceiling on the optional upstream fetch at
// the top of runStatus. On slow or flaky networks, status still returns
// within this budget by falling back to the locally cached ahead/behind.
const statusFetchTimeout = 3 * time.Second

// statusFetchDebounce skips the auto-fetch when we already fetched within
// this window. The TTL is deliberately short — long enough to absorb a
// burst of `st` in the same second, short enough that a user actively
// watching for remote changes still sees them promptly.
const statusFetchDebounce = 3 * time.Second

func init() {
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"st"},
		Short:   "Show concise working tree status",
		RunE:    runStatus,
	}
	cmd.Flags().StringSliceVar(&statusVisFlags, "vis", nil, "visualizations (repeatable or comma-list): gauge,bar,progress,types,staleness,tree,conflict,churn,risk,base,since-push,stash,heatmap,glyphs; pass 'none' to disable the configured default")
	cmd.Flags().BoolVar(&statusNoFetch, "no-fetch", false, "skip the quiet upstream fetch (same as GK_NO_FETCH=1 or status.auto_fetch: false)")
	cmd.Flags().IntVar(&statusTopN, "top", 0, "limit the entry list to the first N rows; 0 = unlimited. A footer shows the hidden remainder")
	cmd.Flags().BoolVar(&statusLegend, "legend", false, "print a one-time key for every glyph and color in the current output and exit")
	rootCmd.AddCommand(cmd)
}

// resolveStatusVis picks the active viz set for this invocation:
//   - if --vis is not passed, use config's status.vis (default gauge,bar,progress)
//   - if --vis is passed with "none", disable all viz layers
//   - otherwise, the explicit flag value wins
func resolveStatusVis(cmd *cobra.Command, cfg *config.Config) []string {
	if cmd.Flags().Changed("vis") {
		if len(statusVisFlags) == 1 && statusVisFlags[0] == "none" {
			return nil
		}
		return statusVisFlags
	}
	if cfg != nil {
		return cfg.Status.Vis
	}
	return nil
}

func statusVisEnabled(name string) bool {
	for _, v := range effectiveVis {
		if v == name {
			return true
		}
	}
	return false
}

// shouldAutoFetch folds together every opt-out signal that disables the
// quiet upstream fetch. Precedence: CLI flag > env var > config.
func shouldAutoFetch(cmd *cobra.Command, cfg *config.Config) bool {
	if statusNoFetch {
		return false
	}
	if v := strings.TrimSpace(os.Getenv("GK_NO_FETCH")); v != "" && v != "0" && strings.ToLower(v) != "false" {
		return false
	}
	if cfg == nil {
		return true
	}
	return cfg.Status.AutoFetch
}

// maybeFetchUpstream does a best-effort, strictly-bounded fetch of the
// current branch's upstream ref. Scope is intentionally minimal:
//
//   - Only the configured upstream remote + branch; no --all, no --tags,
//     no submodule recursion, no FETCH_HEAD write (so we never contend
//     with a parallel `gk pull`).
//   - Hard 3s timeout via context — slow/offline networks never block
//     status beyond that budget.
//   - GIT_TERMINAL_PROMPT=0 + SSH_ASKPASS= empty so a stale credential
//     cannot pop an interactive prompt mid-workflow.
//   - stderr discarded so "remote: ..." chatter never interleaves with
//     the status output.
//   - Debounced: a marker under $GIT_COMMON_DIR/gk/last-fetch records
//     the last successful fetch. A burst of `st` calls inside
//     statusFetchDebounce only fires the network once; the fast path
//     stat()'s the default `.git/gk/last-fetch` path so we can skip
//     every git spawn on warm calls in the common (non-worktree) layout.
//   - Returns silently on every error path — status always renders with
//     whatever is already in refs/remotes/*, even offline.
func maybeFetchUpstream(parent context.Context, repoDir string) {
	// Fast path: in a regular repo (non-worktree), .git is the common
	// dir and we can check the debounce marker without any git spawn.
	// On worktrees .git is a file (not a dir), this fast path misses
	// and we fall through to the careful rev-parse below.
	if fastPathDebounced(repoDir) {
		return
	}

	remote, branch, gitDir, ok := resolveUpstreamAndGitDir(parent, repoDir)
	if !ok {
		return
	}
	if gitDir != "" && recentlyFetched(gitDir) {
		return
	}

	ctx, cancel := context.WithTimeout(parent, statusFetchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git",
		"-c", "submodule.recurse=false",
		"fetch",
		"--quiet",
		"--no-tags",
		"--no-write-fetch-head",
		"--no-recurse-submodules",
		remote, branch,
	)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"SSH_ASKPASS=",
		"GCM_INTERACTIVE=never",
	)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	stopSpinner := startFetchSpinner("fetching " + remote + "...")
	err := cmd.Run()
	stopSpinner()

	if err != nil {
		// Fetch failed (offline, auth, timeout). Touching the marker
		// on failure would mask a transient issue — leave the marker
		// alone so the next `st` retries immediately.
		return
	}
	if gitDir != "" {
		markFetch(gitDir)
	}
}

// spinnerFrames is a small braille-dot animation that's readable in
// every modern monospace font and plays well with a short fetch window.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerStartDelay — quick fetches (e.g., local filesystem remote, or a
// debounced skip that still reached here through an edge case) finish
// faster than the first draw, so no spinner ever appears. This avoids
// a flash of animation that would immediately clear.
const spinnerStartDelay = 150 * time.Millisecond

// startFetchSpinner draws a stderr-bound braille spinner until stop() is
// called. Non-TTY stderr (pipes, CI, `2>file`) makes it a no-op so status
// output streams stay clean. The first frame is delayed so sub-150ms
// fetches never draw anything to clear.
func startFetchSpinner(msg string) (stop func()) {
	if !ui.IsStderrTerminal() {
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-done:
			return // fetch finished before we ever drew
		case <-time.After(spinnerStartDelay):
		}
		t := time.NewTicker(80 * time.Millisecond)
		defer t.Stop()
		i := 0
		for {
			fmt.Fprintf(os.Stderr, "\r%s %s", spinnerFrames[i], msg)
			select {
			case <-done:
				// Clear with BOTH strategies so legacy terminals that
				// don't parse `\x1b[2K` (serial consoles, some CI log
				// viewers, `script(1)` transcripts) still end clean:
				// first overwrite with spaces, then emit the erase.
				pad := strings.Repeat(" ", len(msg)+4)
				fmt.Fprint(os.Stderr, "\r"+pad+"\r\x1b[2K")
				return
			case <-t.C:
				i = (i + 1) % len(spinnerFrames)
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

// fileKindGlyph returns a single-cell geometric glyph + color function for
// the file at path, classifying it by purpose rather than by git state.
// Chosen for semantic density: a reviewer can see at a glance whether a
// dirty entry is production source, test, config, docs, or generated —
// information the two-letter XY porcelain code cannot express.
//
//	●  source code          green
//	◐  test file            magenta
//	◆  config               yellow
//	¶  docs / README        blue
//	▣  binary / asset       faint
//	↻  generated / vendored faint
//	⊙  lockfile             faint
//	·  fallback / unknown   faint
//
// Detection is cheap: pure path matching, no file I/O or git calls.
// Precedence matches user intuition: a file is a test *before* it is
// "Go source", a lockfile *before* it is "JSON", a generated path
// *before* it is classified by extension.
func fileKindGlyph(path string) (glyph string, paint func(format string, a ...interface{}) string) {
	base := filepath.Base(path)
	lower := strings.ToLower(base)
	dir := strings.ToLower(path)

	// Lockfiles (exact-name match so a pnpm-lock.yaml isn't "YAML config").
	if lockfileBasenames[base] {
		return "⊙", color.New(color.Faint).Sprintf
	}
	// Generated / vendored paths (prefix match on lowercase path).
	generatedPrefixes := []string{"dist/", "build/", "vendor/", "node_modules/", ".next/", ".nuxt/", "target/", "out/"}
	for _, p := range generatedPrefixes {
		if strings.HasPrefix(dir, p) || strings.Contains(dir, "/"+p) {
			return "↻", color.New(color.Faint).Sprintf
		}
	}
	if strings.HasSuffix(lower, ".pb.go") || strings.HasSuffix(lower, "_gen.go") ||
		strings.HasSuffix(lower, ".min.js") || strings.HasSuffix(lower, ".min.css") {
		return "↻", color.New(color.Faint).Sprintf
	}

	// Tests (suffix/prefix heuristics across common languages).
	if strings.HasSuffix(lower, "_test.go") ||
		strings.HasSuffix(lower, ".test.ts") || strings.HasSuffix(lower, ".test.tsx") ||
		strings.HasSuffix(lower, ".test.js") || strings.HasSuffix(lower, ".test.jsx") ||
		strings.HasSuffix(lower, ".spec.ts") || strings.HasSuffix(lower, ".spec.js") ||
		strings.HasPrefix(lower, "test_") ||
		strings.HasSuffix(lower, "_spec.rb") ||
		strings.Contains(dir, "/tests/") || strings.Contains(dir, "/testdata/") {
		return "◐", color.MagentaString
	}

	// Docs (README/LICENSE/*.md/.rst/.txt/.adoc).
	if lower == "readme" || lower == "readme.md" || lower == "license" ||
		lower == "changelog.md" || lower == "contributing.md" {
		return "¶", color.BlueString
	}
	ext := filepath.Ext(lower)
	switch ext {
	case ".md", ".rst", ".txt", ".adoc", ".org":
		return "¶", color.BlueString
	}

	// Config (common formats + .env + dotfile configs at any depth).
	switch ext {
	case ".yml", ".yaml", ".toml", ".json", ".ini", ".conf", ".cfg", ".env":
		return "◆", color.YellowString
	}
	if strings.HasPrefix(lower, ".env") || lower == "dockerfile" || lower == "makefile" || lower == ".gitignore" || lower == ".editorconfig" {
		return "◆", color.YellowString
	}

	// Binary / asset.
	binaryExts := map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true, ".ico": true,
		".pdf": true, ".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".7z": true, ".rar": true,
		".mp3": true, ".mp4": true, ".mov": true, ".wav": true, ".woff": true, ".woff2": true, ".ttf": true, ".otf": true,
		".so": true, ".dylib": true, ".dll": true, ".exe": true, ".a": true, ".o": true,
	}
	if binaryExts[ext] {
		return "▣", color.New(color.Faint).Sprintf
	}

	// Source code (broad net for known languages).
	sourceExts := map[string]bool{
		".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true,
		".py": true, ".rb": true, ".rs": true, ".c": true, ".cc": true, ".cpp": true, ".h": true, ".hpp": true,
		".java": true, ".kt": true, ".scala": true, ".swift": true, ".m": true, ".mm": true,
		".php": true, ".lua": true, ".ex": true, ".exs": true, ".erl": true, ".clj": true, ".cljs": true,
		".hs": true, ".ml": true, ".elm": true, ".zig": true, ".nim": true, ".dart": true,
		".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".ps1": true,
		".sql": true, ".graphql": true, ".proto": true,
	}
	if sourceExts[ext] {
		return "●", color.GreenString
	}

	return "·", color.New(color.Faint).Sprintf
}

// glyphPrefix returns the colored glyph + trailing space to prepend to an
// entry line when `--vis glyphs` is enabled; empty string otherwise. We
// intentionally do NOT reserve the 2-cell column when disabled — wasting
// horizontal space on every invocation to preserve muscle memory for the
// rare user who A/B toggles the flag would penalize the common case where
// glyphs is either on (column always present) or off (column never).
func glyphPrefix(path string, enabled bool) string {
	if !enabled {
		return ""
	}
	g, paint := fileKindGlyph(path)
	return paint(g) + " "
}

// renderStatusHeatmap produces a 2D density grid: rows = top-level
// directory (or root), columns = status kind (C/S/M/?). Each cell's
// glyph encodes the count — `·` for zero, then `░▒▓█` scaled to the
// grid's peak. Designed to stay useful on 100+ dirty-file states where
// the flat/tree listing overflows a screen: the user can see in a
// glance which subtree has the most entries and of which kind.
//
// Output (NO_COLOR-safe, single-cell glyphs):
//
//	              C    S    M    ?
//	src/api/      ·    ·    ▓    ░
//	src/ui/       ·    ▒    ▓▓   ▓
//	tests/        ·    ·    ▒    ·
//	node_modules  ·    ·    ·    ▓▓▓
func renderStatusHeatmap(entries []git.StatusEntry) []string {
	if len(entries) == 0 {
		return nil
	}
	type key struct {
		dir  string
		kind int // 0=conflict, 1=staged, 2=modified, 3=untracked
	}
	counts := map[key]int{}
	dirOrder := []string{}
	dirSeen := map[string]bool{}
	peak := 0

	classify := func(e git.StatusEntry) int {
		if e.Kind == git.KindUnmerged {
			return 0
		}
		if e.Kind == git.KindUntracked {
			return 3
		}
		if len(e.XY) >= 2 && e.XY[0] != '.' && e.XY[0] != ' ' && (e.XY[1] == '.' || e.XY[1] == ' ') {
			return 1
		}
		return 2
	}

	for _, e := range entries {
		top := topDir(e.Path)
		if !dirSeen[top] {
			dirSeen[top] = true
			dirOrder = append(dirOrder, top)
		}
		k := key{dir: top, kind: classify(e)}
		counts[k]++
		if counts[k] > peak {
			peak = counts[k]
		}
	}

	// Alphabetical row order for deterministic output; peak-dense dirs
	// would be a nicer sort, but stable ordering matters more for
	// regression testing and user muscle memory.
	sort.Strings(dirOrder)

	// Compute directory name column width (capped to keep layout sane).
	nameW := 4
	for _, d := range dirOrder {
		if len(d) > nameW {
			nameW = len(d)
		}
	}
	if nameW > 24 {
		nameW = 24
	}

	heat := []rune{' ', '░', '▒', '▓', '█'}
	glyphOf := func(n int) string {
		if n == 0 {
			return "·"
		}
		if peak <= 1 {
			return string(heat[4])
		}
		idx := 1 + (n-1)*(len(heat)-1)/maxIntStatus(peak-1, 1)
		if idx >= len(heat) {
			idx = len(heat) - 1
		}
		return string(heat[idx])
	}

	faint := color.New(color.Faint).SprintFunc()
	colColors := []func(string, ...interface{}) string{
		color.RedString, color.GreenString, color.YellowString,
		color.New(color.Faint).Sprintf,
	}
	colLabels := []string{"C", "S", "M", "?"}

	// Header row.
	var header strings.Builder
	fmt.Fprintf(&header, "%s %-*s", faint("heatmap:"), nameW, "")
	for _, lbl := range colLabels {
		fmt.Fprintf(&header, "  %s", faint(lbl))
	}

	lines := []string{header.String()}
	for _, d := range dirOrder {
		displayName := d
		if len(displayName) > nameW {
			displayName = displayName[:nameW-1] + "…"
		}
		var row strings.Builder
		fmt.Fprintf(&row, "%s %-*s", faint(strings.Repeat(" ", len("heatmap:"))), nameW, displayName)
		for kind := 0; kind < 4; kind++ {
			n := counts[key{dir: d, kind: kind}]
			cell := glyphOf(n)
			if n > 0 {
				cell = colColors[kind](cell)
			} else {
				cell = faint(cell)
			}
			fmt.Fprintf(&row, "  %s", cell)
		}
		lines = append(lines, row.String())
	}
	return lines
}

// topDir returns the first path segment of p, or "." for root-level
// files. Used by renderStatusHeatmap to bucket entries into rows.
func topDir(p string) string {
	if p == "" {
		return "."
	}
	if i := strings.IndexByte(p, '/'); i > 0 {
		return p[:i] + "/"
	}
	return "."
}

// visibleWidth approximates the terminal cell width of s, subtracting ANSI
// CSI SGR escape sequences (`\x1b[...m`) and counting runes (close enough
// for the BMP glyphs gk uses; CJK-wide runes that exist in some branch
// names will undercount, which only triggers a false-negative drop — we
// keep the suffix we could have omitted, never wrap when we shouldn't).
func visibleWidth(s string) int {
	w := 0
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// Skip until final byte (alpha) of CSI sequence.
			j := i + 2
			for j < len(s) && !((s[j] >= '@' && s[j] <= '~')) {
				j++
			}
			i = j + 1
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		w++
		i += size
	}
	return w
}

// stripControlChars removes ASCII control characters (bytes < 0x20, including
// ESC) from s before it is written to the terminal. Git itself rejects branch
// names that contain control characters, so this is defence-in-depth for any
// value that passes through external sources (e.g. commit messages, stash refs).
func stripControlChars(s string) string {
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		if s[i] >= 0x20 || s[i] == '\t' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// compactBranch returns the branch name truncated with a middle ellipsis
// if it exceeds maxWidth. Branch names carry signal at both ends (scope
// prefix + feature tail), so `feature/api-v2-auth-refactor` → `feat…efactor`
// preserves both anchors rather than hard-cutting to a head-only prefix.
// For names within the budget, returns as-is.
func compactBranch(name string, maxWidth int) string {
	runes := []rune(name)
	if maxWidth <= 0 || len(runes) <= maxWidth {
		return name
	}
	// Leave room for the ellipsis character (1 cell).
	keep := maxWidth - 1
	head := keep / 2
	tail := keep - head
	return string(runes[:head]) + "…" + string(runes[len(runes)-tail:])
}

// compactUpstreamSuffix renders the trailing "  → <remote-or-path>"
// fragment for the gauge-head layout. Dedup rule:
//
//	local == upstream-branch  →  "  → origin"            (remote short only)
//	local != upstream-branch  →  "  → origin/release"    (full remote/branch)
//	upstream empty            →  ""                       (caller handles)
//
// Saves 15–30 characters on the common case where the branch name
// exactly matches its upstream, which is the overwhelming majority of
// real-world branches.
func compactUpstreamSuffix(branch, upstream string, cyan func(format string, a ...interface{}) string, faint func(a ...interface{}) string) string {
	if upstream == "" {
		return ""
	}
	slash := strings.IndexByte(upstream, '/')
	if slash <= 0 || slash >= len(upstream)-1 {
		return " " + faint("→") + " " + cyan("%s", upstream)
	}
	remote := upstream[:slash]
	upstreamBranch := upstream[slash+1:]
	target := upstream
	if upstreamBranch == branch {
		target = remote
	}
	// Single-space around `→` so the arrow binds visually to the pair it
	// connects (branch→remote reads as one unit) while the outer double-
	// space before the suffix still separates the branch-identity block
	// from the subsequent age ribbons.
	return " " + faint("→") + " " + cyan("%s", target)
}

// detachedShortSHA returns the abbreviated commit id for the current HEAD
// when the branch name is "(detached)". Empty string on any error.
func detachedShortSHA(ctx context.Context, runner *git.ExecRunner) string {
	out, _, err := runner.Run(ctx, "rev-parse", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sincePushSuffix returns a compact "since push Xh (Nc)" fragment meant
// to be appended to the branch line. Three return paths:
//   - non-empty string: there are unpushed commits, render them
//   - "up to date" (faint): @{u} resolves AND no unpushed commits exist
//   - empty string: unresolvable (no upstream configured, offline where
//     refs/remotes isn't populated, or rev-list failed)
//
// The caller currently only renders when non-empty. Distinguishing "up
// to date" from "can't tell" is tracked for a follow-up that threads a
// (string, ok bool) signal; for now the zero/error cases still collapse.
// sincePushSuffix now returns (suffix, ok). ok=false means the value
// cannot be determined (no upstream, rev-list failed, offline with no
// cached remote ref). Callers should render a dim `?` marker in that
// case rather than silently pretending "up to date" — that was the bug
// the error-vs-zero refine surfaces.
//
//	ok=false              → unknown        → caller: "· since push ?"
//	ok=true, suffix==""   → known up-to-date → caller: silent (no chip)
//	ok=true, suffix!=""   → unpushed exists → caller: "· <suffix>"
func sincePushSuffix(ctx context.Context, runner *git.ExecRunner) (string, bool) {
	out, _, err := runner.Run(ctx, "rev-list", "@{u}..HEAD", "--format=%ct")
	if err != nil {
		return "", false
	}
	// Empty output = `@{u}` resolved but no unpushed commits = known up-to-date.
	if len(strings.TrimSpace(string(out))) == 0 {
		return "", true
	}
	oldest := int64(0)
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "commit ") {
			continue
		}
		ts, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			continue
		}
		count++
		if oldest == 0 || ts < oldest {
			oldest = ts
		}
	}
	if count == 0 || oldest == 0 {
		// Non-empty rev-list output but no parseable timestamps — treat as
		// unknown rather than silently claiming up-to-date.
		return "", false
	}
	age := shortAge(time.Unix(oldest, 0))
	if age == "" {
		age = "now"
	}
	if count == 1 {
		return fmt.Sprintf("since push %s", age), true
	}
	return fmt.Sprintf("since push %s (%dc)", age, count), true
}

// renderStashSummary returns a compact one-liner describing the stash
// list — count, newest/oldest age, and a warning when the stash top
// touches any currently-dirty file (classic pop-conflict footgun).
// Empty string when there are no stashes.
//
// Cost: one `git stash list --format=...` call (~3ms). Overlap check is
// an additional `git stash show --name-only stash@{0}` only — we only
// check the TOP stash because that's the one users pop by default.
func renderStashSummary(ctx context.Context, runner *git.ExecRunner) string {
	out, _, err := runner.Run(ctx, "stash", "list", "--format=%gd%x00%ct%x00%s")
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return ""
	}
	type stashEntry struct {
		ref  string
		ts   int64
		subj string
	}
	var stashes []stashEntry
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\x00", 3)
		if len(f) != 3 {
			continue
		}
		ts, _ := strconv.ParseInt(f[1], 10, 64)
		stashes = append(stashes, stashEntry{ref: f[0], ts: ts, subj: f[2]})
	}
	if len(stashes) == 0 {
		return ""
	}

	// stash list is newest-first; oldest = last entry.
	newest := stashes[0]
	oldest := stashes[len(stashes)-1]

	faint := color.New(color.Faint).SprintFunc()
	parts := []string{
		faint("stash:"),
		fmt.Sprintf("%d %s", len(stashes), pluralize(len(stashes), "entry", "entries")),
		faint("· newest ") + shortAge(time.Unix(newest.ts, 0)),
	}
	if len(stashes) > 1 {
		parts = append(parts, faint("· oldest ")+shortAge(time.Unix(oldest.ts, 0)))
	}

	// Overlap check — only for the top stash, since that's `git stash pop`'s
	// implicit target and the most likely collision.
	if overlap := topStashOverlap(ctx, runner); overlap > 0 {
		parts = append(parts, color.YellowString("⚠ %d overlap with dirty", overlap))
	}
	return "  " + strings.Join(parts, "  ")
}

// renderStatusLegend prints the glyph + color vocabulary currently in
// scope for `gk status`. Only layers present in effectiveVis are shown
// so the output stays relevant to what the user will actually see.
func renderStatusLegend(w io.Writer, vis []string) {
	faint := color.New(color.Faint).SprintFunc()
	bold := color.New(color.Bold).SprintFunc()
	fmt.Fprintln(w, bold("gk status vocabulary"))

	enabled := func(name string) bool {
		for _, v := range vis {
			if v == name {
				return true
			}
		}
		return false
	}

	sec := func(label string) {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, faint("— "+label+" —"))
	}

	sec("porcelain XY codes")
	fmt.Fprintf(w, "  %s  staged change   (index side)\n", color.GreenString("A./M./R./D.."))
	fmt.Fprintf(w, "  %s  worktree change (unstaged)\n", color.YellowString(".M/.D/.R./.T"))
	fmt.Fprintf(w, "  %s  merge conflict\n", color.RedString("UU/AA/DD/AU/UA"))
	fmt.Fprintf(w, "  %s  untracked\n", color.New(color.FgHiBlack).Sprint("??"))

	if enabled("gauge") {
		sec("--vis gauge — ahead/behind divergence")
		fmt.Fprintf(w, "  [%s%s%s]  left=ahead (darker), right=behind (lighter), │ is the upstream anchor\n",
			color.GreenString("▓▓"), color.New(color.Faint).Sprint("│"), color.RedString("▒▒"))
	}
	if enabled("bar") {
		sec("--vis bar — composition")
		fmt.Fprintf(w, "  %s conflict   %s staged   %s modified   %s untracked\n",
			color.RedString("▓"), color.GreenString("█"), color.YellowString("▒"), color.New(color.Faint).Sprint("░"))
	}
	if enabled("progress") {
		sec("--vis progress — path to clean")
		fmt.Fprintln(w, "  filled% = staged / (total dirty)")
		fmt.Fprintln(w, "  verbs: resolve, stage, commit, add  — each action clears its bucket")
	}
	if enabled("tree") {
		sec("--vis tree")
		fmt.Fprintln(w, "  ├─└─│  box-drawing branches; (N) badge = subtree file count")
	}
	if enabled("glyphs") {
		sec("--vis glyphs — file-kind column")
		fmt.Fprintf(w, "  %s source   %s test   %s config   %s docs   %s asset   %s generated   %s lockfile   %s unknown\n",
			color.GreenString("●"), color.MagentaString("◐"), color.YellowString("◆"),
			color.BlueString("¶"), color.New(color.Faint).Sprint("▣"),
			color.New(color.Faint).Sprint("↻"), color.New(color.Faint).Sprint("⊙"),
			color.New(color.Faint).Sprint("·"))
	}
	if enabled("staleness") {
		sec("--vis staleness")
		fmt.Fprintln(w, "  · last commit Nd  — only shown when HEAD is ≥1 day old")
		fmt.Fprintln(w, "  (Nd old)          — per-untracked mtime, only shown when ≥1 day old")
	}
	if enabled("heatmap") {
		sec("--vis heatmap — 2-D density (rows=dir, cols=C/S/M/?)")
		fmt.Fprintln(w, "  · (zero)  ░ ▒ ▓ █ (ascending density, scaled to the peak cell)")
	}
	if enabled("since-push") || enabled("stash") || enabled("base") || enabled("conflict") || enabled("churn") || enabled("risk") || enabled("types") {
		sec("other active layers")
		if enabled("since-push") {
			fmt.Fprintln(w, "  · since push Xh — age of oldest unpushed commit (suppressed when no upstream)")
		}
		if enabled("stash") {
			fmt.Fprintln(w, "  stash: N entries — summary line; ⚠ warns if top stash overlaps a dirty file")
		}
		if enabled("base") {
			fmt.Fprintln(w, "  from <trunk> [gauge] — divergence vs base_branch / refs/remotes/<r>/HEAD")
		}
		if enabled("conflict") {
			fmt.Fprintln(w, "  [N hunks · both modified] — appended to conflicts section rows")
		}
		if enabled("churn") {
			fmt.Fprintln(w, "  ▁▂▃▄▅▆▇█ — per-file 8-cell sparkline of last commits' add+del totals")
		}
		if enabled("risk") {
			fmt.Fprintf(w, "  %s — high-risk marker (diff LOC + author diversity over 30d)\n",
				color.New(color.FgRed, color.Bold).Sprint("⚠"))
		}
		if enabled("types") {
			fmt.Fprintln(w, "  types: .ext×N chip — extension histogram over dirty entries")
		}
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, faint("For the log vocabulary: gk log --legend  (planned)"))
}

// isFreshRepo reports whether HEAD has no resolvable commit yet (pre-
// first-commit state right after `git init`). Callers use this to skip
// commit-time / gauge / base-divergence rendering that would fail or
// mislead (e.g., "clean 100%" makes no sense when there's nothing to
// compare against).
func isFreshRepo(ctx context.Context, runner *git.ExecRunner) bool {
	_, _, err := runner.Run(ctx, "rev-parse", "--verify", "-q", "HEAD")
	return err != nil
}

// topStashOverlap returns the number of files touched by stash@{0} that
// are also present in the current working-tree index/status. Uses
// `git stash show --name-status stash@{0}` so rename entries (status `R`)
// contribute BOTH source and destination paths to the overlap set — a
// rename-only stash would otherwise appear to touch zero files and the
// pop-collision warning would silently miss it.
func topStashOverlap(ctx context.Context, runner *git.ExecRunner) int {
	stashFiles, _, err := runner.Run(ctx, "stash", "show", "--name-status", "stash@{0}")
	if err != nil {
		return 0
	}
	dirtyFiles, _, err := runner.Run(ctx, "diff", "--name-only", "HEAD")
	if err != nil {
		return 0
	}
	dirtySet := map[string]struct{}{}
	for _, line := range strings.Split(strings.TrimSpace(string(dirtyFiles)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			dirtySet[line] = struct{}{}
		}
	}
	// Parse --name-status lines: "<status>\t<path>" or for renames
	// "R<score>\t<src>\t<dst>". Add every path mentioned to the stashSet.
	stashSet := map[string]struct{}{}
	for _, line := range strings.Split(strings.TrimSpace(string(stashFiles)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		// fields[0] is the status letter (A/M/D/R/C/...), rest are paths.
		for i := 1; i < len(fields); i++ {
			p := strings.TrimSpace(fields[i])
			if p != "" {
				stashSet[p] = struct{}{}
			}
		}
	}
	n := 0
	for path := range stashSet {
		if _, ok := dirtySet[path]; ok {
			n++
		}
	}
	return n
}

func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// resolveBaseBranchForStatus picks the branch to compare the current
// branch against for divergence reporting. Priority:
//
//  1. `status.base_branch` (project config) or the top-level `base_branch`
//     — lets teams pin a canonical trunk like `develop`.
//  2. `client.DefaultBranch(remote)` — honors refs/remotes/<remote>/HEAD.
//  3. First of "main" / "master" / "develop" that exists locally.
//
// Returns empty string when nothing sensible can be resolved.
func resolveBaseBranchForStatus(ctx context.Context, runner *git.ExecRunner, client *git.Client, cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.BaseBranch) != "" {
		return strings.TrimSpace(cfg.BaseBranch)
	}
	remote := "origin"
	if cfg != nil && cfg.Remote != "" {
		remote = cfg.Remote
	}
	if name, err := client.DefaultBranch(ctx, remote); err == nil && name != "" {
		return name
	}
	for _, cand := range []string{"main", "master", "develop"} {
		if localBranchExists(ctx, runner, cand) {
			return cand
		}
	}
	return ""
}

// branchDivergence returns (ahead, behind) commit counts of head versus
// base in a single `git rev-list --left-right --count base...head` call.
// The git output is "<behind>\t<ahead>" (left-of-base, right-of-head).
func branchDivergence(ctx context.Context, runner *git.ExecRunner, base, head string) (ahead, behind int, ok bool) {
	out, _, err := runner.Run(ctx, "rev-list", "--left-right", "--count", base+"..."+head)
	if err != nil {
		return 0, 0, false
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) != 2 {
		return 0, 0, false
	}
	behind, _ = strconv.Atoi(parts[0])
	ahead, _ = strconv.Atoi(parts[1])
	return ahead, behind, true
}

// renderBaseDivergence returns a single line summarizing how the current
// branch has diverged from its base — `from main [..gauge..] (+3 −0)`.
// Suppressed when:
//   - the current branch is empty, or already is the base branch;
//   - the base cannot be resolved (e.g., fresh repo with no mainline);
//   - git rev-list fails for any reason (offline refs, pruned histories).
// One `rev-list` call; ≤10 ms on typical repos.
func renderBaseDivergence(cmd *cobra.Command, runner *git.ExecRunner, client *git.Client, cfg *config.Config, currentBranch string) string {
	if currentBranch == "" {
		return ""
	}
	base := resolveBaseBranchForStatus(cmd.Context(), runner, client, cfg)
	if base == "" || base == currentBranch {
		return ""
	}
	ahead, behind, ok := branchDivergence(cmd.Context(), runner, base, currentBranch)
	if !ok {
		return ""
	}
	// renderDivergenceGauge already prints "(↑N ↓M)" or "in sync" as its
	// suffix so no extra count string is needed here — the visual matches
	// the branch/upstream gauge on the line above, cementing the "same
	// semantics" reading.
	faint := color.New(color.Faint).SprintFunc()
	return fmt.Sprintf("  %s %s  %s",
		faint("from"),
		color.CyanString(base),
		renderDivergenceGauge(ahead, behind),
	)
}

// fastPathDebounced short-circuits maybeFetchUpstream on warm calls by
// stat()-ing the default marker path directly. It only succeeds when
// `<repoDir>/.git/gk/last-fetch` exists and is recent — i.e., a regular
// non-worktree repo that was fetched by a prior `gk status`. Worktrees
// (where .git is a file pointing elsewhere) intentionally miss this
// path and fall through to the rev-parse-based resolution.
func fastPathDebounced(repoDir string) bool {
	if repoDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return false
		}
		repoDir = cwd
	}
	marker := filepath.Join(repoDir, ".git", "gk", "last-fetch")
	info, err := os.Stat(marker)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) < statusFetchDebounce
}

// resolveUpstreamAndGitDir collapses the two pieces of metadata we need
// (upstream branch + git common dir) into a single `git rev-parse` spawn.
// Output is two lines: "<remote>/<branch>\n<path-to-common-dir>".
//
// Returns ok=false when the branch has no upstream configured; callers
// skip fetching in that case. gitDir may still be non-empty for marker
// persistence in future flows.
func resolveUpstreamAndGitDir(ctx context.Context, repoDir string) (remote, branch, gitDir string, ok bool) {
	c := exec.CommandContext(ctx, "git", "rev-parse",
		"--abbrev-ref", "HEAD@{u}",
		"--git-common-dir",
	)
	c.Dir = repoDir
	c.Stderr = io.Discard
	out, err := c.Output()
	if err != nil {
		return "", "", "", false
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 2 {
		return "", "", "", false
	}
	upstream := strings.TrimSpace(lines[0])
	gitDir = strings.TrimSpace(lines[1])
	if gitDir != "" && !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repoDir, gitDir)
	}
	if upstream == "" {
		// Upstream unresolved — skip fetch, still return gitDir so
		// callers can persist markers if they want to.
		return "", "", gitDir, false
	}
	parts := strings.SplitN(upstream, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", gitDir, false
	}
	return parts[0], parts[1], gitDir, true
}

// fetchMarkerPath returns the single marker file used to debounce
// maybeFetchUpstream. One marker per repo (rather than per-remote) keeps
// the fast path trivial; the 99% case of a single `origin` upstream is
// unaffected, and multi-remote setups simply share the debounce window.
func fetchMarkerPath(gitDir string) string {
	return filepath.Join(gitDir, "gk", "last-fetch")
}

// recentlyFetched reports whether the fetch marker was touched within
// statusFetchDebounce. Missing marker → not recent → do fetch.
func recentlyFetched(gitDir string) bool {
	info, err := os.Stat(fetchMarkerPath(gitDir))
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) < statusFetchDebounce
}

// markFetch touches the debounce marker so subsequent calls within the
// window skip the network round-trip. Failures are swallowed — the worst
// case is that we fetch again on the next invocation.
func markFetch(gitDir string) {
	path := fetchMarkerPath(gitDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return
	}
	_ = f.Close()
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

func runStatus(cmd *cobra.Command, args []string) error {
	if NoColorFlag() {
		color.NoColor = true
	}
	cfg, _ := config.Load(cmd.Flags())
	effectiveVis = resolveStatusVis(cmd, cfg)

	// --legend short-circuits everything: print the glyph/color key for
	// the currently-active viz set and return. Useful for first-run users
	// or anyone wondering "what does ⊛ mean on this row?".
	if statusLegend {
		renderStatusLegend(cmd.OutOrStdout(), effectiveVis)
		return nil
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}
	if shouldAutoFetch(cmd, cfg) {
		maybeFetchUpstream(cmd.Context(), RepoFlag())
	}
	client := git.NewClient(runner)
	st, err := client.Status(cmd.Context())
	if err != nil {
		return err
	}

	w := cmd.OutOrStdout()
	bold := color.New(color.Bold).SprintFunc()
	cyan := color.CyanString
	faint := color.New(color.Faint).SprintFunc()

	// Sanitize branch/upstream at the display boundary so that any
	// control characters in external data cannot inject ANSI sequences.
	displayBranch := stripControlChars(st.Branch)
	displayUpstream := stripControlChars(st.Upstream)

	// Fresh repo (pre-first-commit): HEAD resolves to nothing, so commit-
	// based viz (gauge, since-push, base, staleness) all silently fail.
	// Print a one-line affirmative instead and skip the rest.
	if isFreshRepo(cmd.Context(), runner) {
		fmt.Fprintf(w, "%s %s  %s\n",
			faint("branch:"),
			bold(displayBranch),
			faint("· no commits yet  (git add . && git commit)"),
		)
		if len(st.Entries) == 0 {
			fmt.Fprintln(w, faint("working tree clean"))
		}
		return nil
	}

	// Detached HEAD: `git status --porcelain=v2` emits `branch.head (detached)`
	// which, when passed to rev-list downstream, produces "ambiguous argument"
	// errors that get swallowed silently. We render a dedicated warning line
	// and skip every upstream-dependent viz (gauge, since-push, base) so the
	// user sees the failure mode explicitly rather than missing rows.
	detached := st.Branch == "" || st.Branch == "(detached)"
	// branch line
	var line string
	switch {
	case detached:
		short := detachedShortSHA(cmd.Context(), runner)
		if short == "" {
			short = "?"
		}
		line = fmt.Sprintf("%s %s %s  %s",
			faint("branch:"),
			color.New(color.Faint).Sprint("HEAD @"),
			color.YellowString(short),
			color.New(color.FgYellow, color.Bold).Sprint("⚠ detached"),
		)
	default:
		// L1 refinement (adopted from .xm/op/refine-2026-04-22-gk-status-
		// branch-line-layout.json): when gauge is active on a branch that
		// has an upstream, hoist it to line-head so the single most
		// action-critical signal is the first thing the eye hits.
		//
		//   [··▓▓│▒▒▒▒··]  (↑2 ↓4)  feature/api-v2 → origin  · 3d · push 2h
		//
		// Fallback to branch-head layout when gauge is off or no upstream
		// exists — preserves muscle memory + the `branch:` label that
		// users relied on since v0.1.
		if statusVisEnabled("gauge") && st.Upstream != "" {
			line = renderDivergenceGauge(st.Ahead, st.Behind) +
				"  " + bold(compactBranch(displayBranch, 32)) +
				compactUpstreamSuffix(displayBranch, displayUpstream, cyan, faint)
		} else {
			line = fmt.Sprintf("%s %s", faint("branch:"), bold(displayBranch))
			if st.Upstream != "" {
				line += fmt.Sprintf("  %s %s", faint("⇄"), cyan(displayUpstream))
			}
			if st.Ahead != 0 || st.Behind != 0 {
				line += fmt.Sprintf("  ↑%d ↓%d", st.Ahead, st.Behind)
			}
		}
	}
	// C3 polish: staleness and since-push are informational tails — if
	// appending them would overflow the TTY, drop them rather than wrap
	// the line and destroy the gauge-head anchor. Wrapping a single-line
	// branch header is strictly worse than dropping a secondary age.
	ttyW, haveTTY := ui.TTYWidth()
	wouldOverflow := func(extra string) bool {
		if !haveTTY || ttyW <= 0 {
			return false
		}
		// len() is byte-count — Unicode glyphs inflate past cell count,
		// but the gauge/glyph glyphs we use are single-cell so treating
		// this as an upper bound (pessimistic) is fine.
		return visibleWidth(line)+visibleWidth(extra) > ttyW
	}
	if statusVisEnabled("staleness") {
		if ago := lastCommitAgo(cmd, runner); ago != "" {
			extra := "  " + faint("· last commit "+ago)
			if !wouldOverflow(extra) {
				line += extra
			}
		}
	}
	if statusVisEnabled("since-push") && !detached {
		// Three-state rendering (error-vs-zero refine):
		//   ok=false           → "· since push ?"   (dim, unknown state)
		//   ok=true, suffix==""→ silent             (known up-to-date)
		//   ok=true, suffix!="" → "· <suffix>"       (known unpushed)
		unpushed, ok := sincePushSuffix(cmd.Context(), runner)
		var extra string
		if !ok {
			extra = "  " + faint("· since push ?")
		} else if unpushed != "" {
			// R6: drop the "(Nc)" count when the gauge is already showing it.
			if statusVisEnabled("gauge") {
				if i := strings.Index(unpushed, " ("); i > 0 {
					unpushed = unpushed[:i]
				}
			}
			extra = "  " + faint("· "+unpushed)
		}
		if extra != "" && !wouldOverflow(extra) {
			line += extra
		}
	}
	fmt.Fprintln(w, line)

	if statusVisEnabled("stash") {
		if stashLine := renderStashSummary(cmd.Context(), runner); stashLine != "" {
			fmt.Fprintln(w, stashLine)
		}
	}

	if statusVisEnabled("base") && !detached {
		if baseLine := renderBaseDivergence(cmd, runner, client, cfg, st.Branch); baseLine != "" {
			fmt.Fprintln(w, baseLine)
		}
	}

	// --top N applies globally to the entry list. Sections/tree below
	// operate on the truncated slice; a footer at the end surfaces the
	// hidden remainder so the cut is obvious, not silently missing data.
	hiddenByTop := 0
	totalEntries := len(st.Entries)
	if statusTopN > 0 && len(st.Entries) > statusTopN {
		sortedEntries := make([]git.StatusEntry, len(st.Entries))
		copy(sortedEntries, st.Entries)
		sort.SliceStable(sortedEntries, func(i, j int) bool { return sortedEntries[i].Path < sortedEntries[j].Path })
		hiddenByTop = len(sortedEntries) - statusTopN
		st.Entries = sortedEntries[:statusTopN]
	}

	// group entries by Kind
	grouped := groupEntries(st.Entries)
	if statusVisEnabled("bar") {
		if line := renderDensityBar(grouped); line != "" {
			fmt.Fprintln(w, line)
		}
	}
	if statusVisEnabled("progress") {
		if line := renderProgressMeter(grouped); line != "" {
			fmt.Fprintln(w, line)
		}
	}
	if statusVisEnabled("types") && len(st.Entries) > 0 {
		if line := renderTypesChip(st.Entries); line != "" {
			fmt.Fprintln(w, line)
		}
	}
	if statusVisEnabled("heatmap") && len(st.Entries) > 0 {
		for _, line := range renderStatusHeatmap(st.Entries) {
			fmt.Fprintln(w, line)
		}
	}
	if statusVisEnabled("tree") && len(st.Entries) > 0 {
		stats := fetchDiffStats(cmd.Context(), runner)
		renderStatusTree(w, st.Entries, stats)
	} else {
		useGlyphs := statusVisEnabled("glyphs")
		if len(grouped.Unmerged) > 0 {
			fmt.Fprintln(w, color.New(color.FgRed, color.Bold).Sprint("conflicts:"))
			showAnatomy := statusVisEnabled("conflict")
			for _, e := range grouped.Unmerged {
				suffix := ""
				if showAnatomy {
					if s := conflictAnatomy(RepoFlag(), e); s != "" {
						suffix = "  " + faint(s)
					}
				}
				fmt.Fprintf(w, "  %s%s %s%s\n", glyphPrefix(e.Path, useGlyphs), color.RedString(e.XY), e.Path, suffix)
			}
		}
		if len(grouped.Staged) > 0 {
			fmt.Fprintln(w, color.GreenString("staged:"))
			for _, e := range grouped.Staged {
				fmt.Fprintf(w, "  %s%s %s\n", glyphPrefix(e.Path, useGlyphs), color.GreenString(e.XY), displayPath(e))
			}
		}
		if len(grouped.Modified) > 0 {
			fmt.Fprintln(w, color.YellowString("modified:"))
			modified := grouped.Modified
			if statusVisEnabled("risk") {
				modified = sortByRisk(cmd.Context(), runner, modified)
			}
			showChurn := statusVisEnabled("churn") && len(st.Entries) <= 50
			showRisk := statusVisEnabled("risk")
			for _, e := range modified {
				parts := []string{fmt.Sprintf("  %s%s %s", glyphPrefix(e.Path, useGlyphs), color.YellowString(e.XY), displayPath(e))}
				if showRisk {
					if marker := riskMarker(cmd.Context(), runner, e); marker != "" {
						parts[0] = fmt.Sprintf("  %s%s %s %s", glyphPrefix(e.Path, useGlyphs), color.YellowString(e.XY), marker, displayPath(e))
					}
				}
				if showChurn {
					if sl := fileChurnSparkline(cmd.Context(), runner, e.Path, 8); sl != "" {
						parts = append(parts, faint(sl))
					}
				}
				fmt.Fprintln(w, strings.Join(parts, "  "))
			}
		}
		if len(grouped.Untracked) > 0 {
			fmt.Fprintln(w, color.New(color.FgHiBlack).Sprint("untracked:"))
			showAge := statusVisEnabled("staleness")
			for _, e := range grouped.Untracked {
				suffix := ""
				if showAge {
					if age := untrackedAge(RepoFlag(), e.Path); age != "" {
						suffix = "  " + faint("("+age+" old)")
					}
				}
				fmt.Fprintf(w, "  %s%s %s%s\n", glyphPrefix(e.Path, useGlyphs), color.New(color.FgHiBlack).Sprint("??"), e.Path, suffix)
			}
		}
	}
	if len(st.Entries) == 0 {
		fmt.Fprintln(w, faint("working tree clean"))
	}
	if hiddenByTop > 0 {
		fmt.Fprintln(w, faint(fmt.Sprintf("… +%d more (%d total · showing top %d)", hiddenByTop, totalEntries, statusTopN)))
	}
	return nil
}

type groupedEntries struct {
	Modified, Staged, Unmerged, Untracked []git.StatusEntry
}

func groupEntries(entries []git.StatusEntry) groupedEntries {
	var g groupedEntries
	for _, e := range entries {
		switch e.Kind {
		case git.KindUnmerged:
			g.Unmerged = append(g.Unmerged, e)
		case git.KindUntracked:
			g.Untracked = append(g.Untracked, e)
		default:
			if len(e.XY) >= 2 {
				x, y := e.XY[0], e.XY[1]
				switch {
				case x != '.' && x != ' ' && (y == '.' || y == ' '):
					g.Staged = append(g.Staged, e)
				default:
					g.Modified = append(g.Modified, e)
				}
			}
		}
	}
	return g
}

func displayPath(e git.StatusEntry) string {
	if e.Orig != "" {
		return fmt.Sprintf("%s → %s", e.Orig, e.Path)
	}
	return e.Path
}

// fileChurnSparkline returns an N-cell sparkline of a file's recent commit
// activity (sum of adds+dels per commit) in chronological order (oldest left,
// newest right). Empty string when the file has no recent history.
func fileChurnSparkline(ctx context.Context, runner *git.ExecRunner, path string, cells int) string {
	out, _, err := runner.Run(ctx, "log", "-n", strconv.Itoa(cells), "--format=", "--numstat", "--", path)
	if err != nil || len(out) == 0 {
		return ""
	}
	var recent []int // newest-first from git
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cols := strings.SplitN(line, "\t", 3)
		if len(cols) != 3 {
			continue
		}
		a, _ := strconv.Atoi(cols[0])
		d, _ := strconv.Atoi(cols[1])
		recent = append(recent, a+d)
	}
	if len(recent) == 0 {
		return ""
	}
	// Reverse to oldest-first.
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}
	peak := 0
	for _, v := range recent {
		if v > peak {
			peak = v
		}
	}
	glyphs := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	var b strings.Builder
	// Left-pad with ' ' so output is always <cells> wide.
	for i := 0; i < cells-len(recent); i++ {
		b.WriteRune(' ')
	}
	for _, v := range recent {
		if v == 0 {
			b.WriteRune(' ')
			continue
		}
		idx := 0
		if peak > 1 {
			idx = (v - 1) * (len(glyphs) - 1) / maxIntStatus(peak-1, 1)
		}
		if idx >= len(glyphs) {
			idx = len(glyphs) - 1
		}
		b.WriteRune(glyphs[idx])
	}
	return b.String()
}

// riskMarker returns "⚠" when the path looks risky (large diff or touched by
// multiple authors recently). Empty string otherwise.
func riskMarker(ctx context.Context, runner *git.ExecRunner, e git.StatusEntry) string {
	score := fileRiskScore(ctx, runner, e)
	if score >= 50 {
		return color.New(color.FgRed, color.Bold).Sprint("⚠")
	}
	return ""
}

// fileRiskScore computes a lightweight risk score: current diff LOC +
// (30-day distinct-author count × 10). Files scored >= 50 are flagged.
func fileRiskScore(ctx context.Context, runner *git.ExecRunner, e git.StatusEntry) int {
	// current diff size (staged or worktree, whichever is present)
	diffSize := 0
	for _, base := range []string{"HEAD", "--cached"} {
		args := []string{"diff", "--numstat"}
		if base == "--cached" {
			args = append(args, "--cached", "--", e.Path)
		} else {
			args = append(args, "--", e.Path)
		}
		if out, _, err := runner.Run(ctx, args...); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				cols := strings.SplitN(strings.TrimSpace(line), "\t", 3)
				if len(cols) != 3 {
					continue
				}
				a, _ := strconv.Atoi(cols[0])
				d, _ := strconv.Atoi(cols[1])
				diffSize += a + d
			}
		}
	}
	// distinct authors in last 30 days
	out, _, err := runner.Run(ctx, "log", "--since=30.days.ago", "--format=%an", "--", e.Path)
	authorCount := 0
	if err == nil {
		authors := map[string]bool{}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				authors[line] = true
			}
		}
		authorCount = len(authors)
	}
	return diffSize + authorCount*10
}

// collectAuthorCounts returns path → distinct-author count over the last 30
// days for all provided paths, using a single git log call (one __AUTHOR__
// marker per commit) instead of one call per file.
func collectAuthorCounts(ctx context.Context, runner *git.ExecRunner, paths []string) map[string]int {
	if len(paths) == 0 {
		return map[string]int{}
	}
	args := []string{"log", "--since=30.days.ago", "--name-only", "--format=__AUTHOR__%an", "--"}
	args = append(args, paths...)
	out, _, err := runner.Run(ctx, args...)
	if err != nil {
		return map[string]int{}
	}
	authorsPerFile := map[string]map[string]bool{}
	var curAuthor string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "__AUTHOR__") {
			curAuthor = line[len("__AUTHOR__"):]
			continue
		}
		if curAuthor == "" {
			continue
		}
		if authorsPerFile[line] == nil {
			authorsPerFile[line] = map[string]bool{}
		}
		authorsPerFile[line][curAuthor] = true
	}
	result := make(map[string]int, len(authorsPerFile))
	for p, authors := range authorsPerFile {
		result[p] = len(authors)
	}
	return result
}

// sortByRisk re-orders modified entries so high-risk files rise to the top.
// Uses 3 git calls total (2 concurrent diffs + 1 log) rather than 3N calls.
func sortByRisk(ctx context.Context, runner *git.ExecRunner, entries []git.StatusEntry) []git.StatusEntry {
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
	}
	diffStats := fetchDiffStats(ctx, runner)
	authorCounts := collectAuthorCounts(ctx, runner, paths)

	type scored struct {
		entry git.StatusEntry
		score int
	}
	list := make([]scored, len(entries))
	for i, e := range entries {
		ds := diffStats[e.Path]
		list[i] = scored{e, ds.added + ds.removed + authorCounts[e.Path]*10}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].score > list[j].score })
	out := make([]git.StatusEntry, len(list))
	for i, s := range list {
		out[i] = s.entry
	}
	return out
}

func maxIntStatus(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// conflictKindName maps porcelain XY codes for unmerged entries to a short
// human label describing how the file conflicts.
func conflictKindName(xy string) string {
	switch xy {
	case "UU":
		return "both modified"
	case "AA":
		return "both added"
	case "DD":
		return "both deleted"
	case "AU":
		return "added by us"
	case "UA":
		return "added by them"
	case "DU":
		return "deleted by us"
	case "UD":
		return "deleted by them"
	}
	return "conflict"
}

// conflictAnatomy returns a compact "[N hunks · both modified]" summary for a
// single unmerged entry. Returns empty string if the file cannot be read
// (e.g., deleted side) — callers render an unadorned line in that case.
func conflictAnatomy(repoDir string, e git.StatusEntry) string {
	kind := conflictKindName(e.XY)
	hunks := conflictHunkCount(repoDir, e.Path)
	if hunks == 0 {
		return fmt.Sprintf("[%s]", kind)
	}
	unit := "hunks"
	if hunks == 1 {
		unit = "hunk"
	}
	return fmt.Sprintf("[%d %s · %s]", hunks, unit, kind)
}

// conflictHunkCount counts `<<<<<<<` conflict markers at line starts in the
// worktree file. Returns 0 when the file is unreadable (deleted-side conflicts).
func conflictHunkCount(repoDir, path string) int {
	p := path
	if !filepath.IsAbs(p) {
		if repoDir == "" {
			repoDir, _ = os.Getwd()
		}
		p = filepath.Join(repoDir, path)
	}
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	n, _ := f.Read(buf)
	data := buf[:n]
	// Read more if needed.
	for n == len(buf) {
		more := make([]byte, 64*1024)
		n, _ = f.Read(more)
		data = append(data, more[:n]...)
	}

	count := 0
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			line := data[start:i]
			if len(line) >= 7 && string(line[:7]) == "<<<<<<<" {
				count++
			}
			start = i + 1
		}
	}
	if start < len(data) {
		line := data[start:]
		if len(line) >= 7 && string(line[:7]) == "<<<<<<<" {
			count++
		}
	}
	return count
}

// treeNode is a lightweight path trie for renderStatusTree. Leaves hold the
// originating StatusEntry; directories hold children keyed by segment name.
type treeNode struct {
	name     string
	entry    *git.StatusEntry
	children map[string]*treeNode
}

// buildStatusTree groups entries by path segments into a trie, collapsing
// single-child directory chains into a single node ("api/v2/auth.ts") to
// avoid deep indentation for a single dangling file.
func buildStatusTree(entries []git.StatusEntry) *treeNode {
	root := &treeNode{children: map[string]*treeNode{}}
	for i := range entries {
		e := entries[i]
		path := e.Path
		if path == "" {
			continue
		}
		parts := strings.Split(path, "/")
		cur := root
		for j, part := range parts {
			if j == len(parts)-1 {
				leaf := &treeNode{name: part, entry: &entries[i]}
				cur.children[part] = leaf
			} else {
				next, ok := cur.children[part]
				if !ok {
					next = &treeNode{name: part, children: map[string]*treeNode{}}
					cur.children[part] = next
				}
				cur = next
			}
		}
	}
	collapseSingletons(root)
	return root
}

// collapseSingletons merges a directory chain whose every descendant is a
// singleton into a single node (api → api/v2 → api/v2/auth.ts becomes
// "api/v2/auth.ts" as a single leaf child of the parent).
func collapseSingletons(n *treeNode) {
	for k, c := range n.children {
		collapseSingletons(c)
		if c.entry == nil && len(c.children) == 1 {
			for gk, gc := range c.children {
				merged := &treeNode{name: c.name + "/" + gk, entry: gc.entry, children: gc.children}
				delete(n.children, k)
				n.children[merged.name] = merged
			}
		}
	}
}

// subtreeCount returns the number of entry leaves in this subtree.
func subtreeCount(n *treeNode) int {
	if n.entry != nil {
		return 1
	}
	total := 0
	for _, c := range n.children {
		total += subtreeCount(c)
	}
	return total
}

// renderStatusTree writes a hierarchical tree view of the entries to w using
// box-drawing glyphs. Directory lines carry a subtree-count badge; file lines
// carry the two-letter XY porcelain code colored by category and an optional
// "+N -N" diff-stat suffix when stats is non-nil.
func renderStatusTree(w io.Writer, entries []git.StatusEntry, stats map[string]diffStat) {
	root := buildStatusTree(entries)
	faint := color.New(color.Faint).SprintFunc()
	writeChildren(w, root, "", faint, stats)
}

func writeChildren(w io.Writer, n *treeNode, prefix string, faint func(...interface{}) string, stats map[string]diffStat) {
	keys := make([]string, 0, len(n.children))
	for k := range n.children {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := n.children[keys[i]], n.children[keys[j]]
		// dirs first, then files; each group alphabetical.
		if (a.entry == nil) != (b.entry == nil) {
			return a.entry == nil
		}
		return keys[i] < keys[j]
	})
	for i, k := range keys {
		child := n.children[k]
		last := i == len(keys)-1
		branch := "├─ "
		indent := "│  "
		if last {
			branch = "└─ "
			indent = "   "
		}
		if child.entry != nil {
			useGlyphs := statusVisEnabled("glyphs")
			stat := formatDiffStat(stats, child.entry.Path)
			fmt.Fprintf(w, "%s%s%s%s  %s%s\n", prefix, faint(branch), glyphPrefix(child.entry.Path, useGlyphs), colorXY(child.entry.XY), displayTreeName(child), stat)
		} else {
			fmt.Fprintf(w, "%s%s%s/  %s\n", prefix, faint(branch),
				color.New(color.Bold).Sprint(child.name),
				faint(fmt.Sprintf("(%d)", subtreeCount(child))),
			)
			writeChildren(w, child, prefix+faint(indent), faint, stats)
		}
	}
}

// displayTreeName returns the leaf label, appending rename origin if present.
func displayTreeName(n *treeNode) string {
	if n.entry == nil {
		return n.name
	}
	if n.entry.Orig != "" {
		return fmt.Sprintf("%s → %s", n.entry.Orig, n.name)
	}
	return n.name
}

// diffStat holds the added/removed line counts for a single file derived from
// git diff --numstat. Both fields are 0 for untracked files (not in numstat).
type diffStat struct{ added, removed int }

// parseNumstat parses the output of `git diff --numstat` into a path→diffStat
// map. Binary files (marked "-") and malformed lines are silently skipped.
func parseNumstat(out []byte) map[string]diffStat {
	m := make(map[string]diffStat)
	for _, line := range strings.Split(string(out), "\n") {
		cols := strings.SplitN(strings.TrimSpace(line), "\t", 3)
		if len(cols) != 3 || cols[2] == "" || cols[0] == "-" {
			continue
		}
		a, err1 := strconv.Atoi(cols[0])
		d, err2 := strconv.Atoi(cols[1])
		if err1 != nil || err2 != nil {
			continue
		}
		m[cols[2]] = diffStat{added: a, removed: d}
	}
	return m
}

// fetchDiffStats runs git diff --numstat (unstaged) and git diff --cached
// --numstat (staged) concurrently and merges the counts. Binary files and
// errors are silently ignored; the caller gets an empty map on total failure.
func fetchDiffStats(ctx context.Context, runner *git.ExecRunner) map[string]diffStat {
	type result struct{ m map[string]diffStat }
	ch1, ch2 := make(chan result, 1), make(chan result, 1)

	go func() {
		out, _, _ := runner.Run(ctx, "diff", "--numstat")
		ch1 <- result{m: parseNumstat(out)}
	}()
	go func() {
		out, _, _ := runner.Run(ctx, "diff", "--cached", "--numstat")
		ch2 <- result{m: parseNumstat(out)}
	}()

	r1, r2 := <-ch1, <-ch2
	merged := r1.m
	for p, s := range r2.m {
		if ex, ok := merged[p]; ok {
			merged[p] = diffStat{added: ex.added + s.added, removed: ex.removed + s.removed}
		} else {
			merged[p] = s
		}
	}
	return merged
}

// formatDiffStat returns a colored "+N -N" suffix for path, or "" when no
// stat is available (untracked files, binary files, errors).
func formatDiffStat(stats map[string]diffStat, path string) string {
	s, ok := stats[path]
	if !ok || (s.added == 0 && s.removed == 0) {
		return ""
	}
	green := color.New(color.FgGreen).SprintfFunc()
	red := color.New(color.FgRed).SprintfFunc()
	var parts []string
	if s.added > 0 {
		parts = append(parts, green("+%d", s.added))
	}
	if s.removed > 0 {
		parts = append(parts, red("-%d", s.removed))
	}
	if len(parts) == 0 {
		return ""
	}
	return "  " + strings.Join(parts, " ")
}

// colorXY picks a color for the two-letter porcelain code based on the
// entry's broad category (conflict/staged/modified/untracked).
func colorXY(xy string) string {
	switch {
	case xy == "??":
		return color.New(color.FgHiBlack).Sprint(xy)
	case strings.ContainsAny(xy, "Uu"):
		return color.RedString(xy)
	case len(xy) >= 2 && xy[0] != '.' && xy[0] != ' ' && (xy[1] == '.' || xy[1] == ' '):
		return color.GreenString(xy)
	default:
		return color.YellowString(xy)
	}
}

// lastCommitAgo returns a short relative age ("11d", "4h") of HEAD's
// committer date, or empty string when there is no HEAD (fresh repo), git
// fails, or the commit is under 1 day old. Active branches commit multiple
// times per day so annotating "last commit 2h" on every `gk status` call
// is noise — the signal only earns attention once the branch starts going
// stale, so we suppress it for <24h ages.
func lastCommitAgo(cmd *cobra.Command, runner *git.ExecRunner) string {
	out, _, err := runner.Run(cmd.Context(), "log", "-1", "--format=%ct", "HEAD")
	if err != nil {
		return ""
	}
	ts := strings.TrimSpace(string(out))
	if ts == "" {
		return ""
	}
	var secs int64
	if _, err := fmt.Sscanf(ts, "%d", &secs); err != nil {
		return ""
	}
	age := time.Since(time.Unix(secs, 0))
	if age < 24*time.Hour {
		return ""
	}
	return formatAge(age)
}

// untrackedAge returns a short relative age of an untracked file's mtime,
// suppressed under 1 day so recent scratch files don't get annotated.
func untrackedAge(repoDir, path string) string {
	p := path
	if !filepath.IsAbs(p) {
		if repoDir == "" {
			repoDir, _ = os.Getwd()
		}
		p = filepath.Join(repoDir, path)
	}
	info, err := os.Stat(p)
	if err != nil {
		return ""
	}
	age := time.Since(info.ModTime())
	if age < 24*time.Hour {
		return ""
	}
	return formatAge(age)
}

// formatAge collapses a duration into the largest unit with 1-3 significant
// digits: 45s, 12m, 3h, 11d, 6w, 4mo, 2y.
func formatAge(d time.Duration) string {
	if d < time.Minute {
		return ""
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	days := int(d.Hours() / 24)
	if days < 14 {
		return fmt.Sprintf("%dd", days)
	}
	if days < 60 {
		return fmt.Sprintf("%dw", days/7)
	}
	if days < 365 {
		return fmt.Sprintf("%dmo", days/30)
	}
	return fmt.Sprintf("%dy", days/365)
}

// dimExts lists extensions treated as binary/generated/lockfile; the types
// chip dims them so a lockfile bump doesn't look as loud as a code change.
var dimExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".ico": true, ".webp": true, ".svg": true,
	".pdf": true, ".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".7z": true, ".rar": true,
	".mp3": true, ".mp4": true, ".mov": true, ".wav": true,
	".bin": true, ".so": true, ".dylib": true, ".dll": true, ".exe": true, ".a": true, ".o": true,
	".lock": true, ".sum": true,
}

// lockfileBasenames maps well-known lock-file basenames so their extension
// reports as ".lock" regardless of the underlying format (.json/.yaml).
// go.sum is included because it's functionally a lockfile even though
// its extension is .sum; go.mod is intentionally omitted (manifest, not lock).
var lockfileBasenames = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"Gemfile.lock": true, "Cargo.lock": true, "composer.lock": true,
	"poetry.lock": true, "Pipfile.lock": true, "go.sum": true,
}

// extOf returns a short human label for a path's "kind": usually `.ts` etc.,
// collapsing known lockfile names into `.lock`, and falling back to the
// basename for extensionless files (`Makefile`, `Dockerfile`).
func extOf(path string) string {
	base := filepath.Base(path)
	if lockfileBasenames[base] {
		return ".lock"
	}
	if ext := filepath.Ext(base); ext != "" {
		return ext
	}
	return base
}

// renderTypesChip returns a one-line extension histogram over the dirty
// entries. Dim-listed extensions (binaries, lockfiles) render faint. Returns
// empty string when the tree has more than 40 distinct kinds (signal lost).
//
//	types: .ts×6 .md×2 .lock×1
func renderTypesChip(entries []git.StatusEntry) string {
	counts := map[string]int{}
	for _, e := range entries {
		p := e.Path
		if p == "" && e.Orig != "" {
			p = e.Orig
		}
		counts[extOf(p)]++
	}
	if len(counts) == 0 || len(counts) > 40 {
		return ""
	}

	type kv struct {
		k string
		v int
	}
	list := make([]kv, 0, len(counts))
	for k, v := range counts {
		list = append(list, kv{k, v})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].k < list[j].k
	})
	if len(list) > 8 {
		list = list[:8]
	}

	dim := color.New(color.Faint).SprintFunc()
	parts := make([]string, 0, len(list))
	for _, item := range list {
		s := fmt.Sprintf("%s×%d", item.k, item.v)
		if dimExts[item.k] {
			s = dim(s)
		}
		parts = append(parts, s)
	}
	return fmt.Sprintf("%s %s", dim("types:"), strings.Join(parts, " "))
}

// renderProgressMeter returns a one-line progress indicator for how close the
// working tree is to clean. The filled portion represents staged files (already
// one step from committed); the verb list enumerates the remaining actions
// bucketed by the next command the user must run.
//
//	clean: [███░░░░░░░] 30%  stage 5 · commit 3 · resolve 1 · add 1
func renderProgressMeter(g groupedEntries) string {
	staged := len(g.Staged)
	modified := len(g.Modified)
	conflicts := len(g.Unmerged)
	untracked := len(g.Untracked)
	total := staged + modified + conflicts + untracked

	width := 10
	if w, ok := ui.TTYWidth(); ok && w < 80 {
		width = 5
	}

	faint := color.New(color.Faint).SprintFunc()
	if total == 0 {
		return fmt.Sprintf("%s [%s] 100%%  %s",
			faint("clean:"),
			color.GreenString(strings.Repeat("█", width)),
			faint("nothing to do"),
		)
	}
	filled := staged * width / total
	pct := staged * 100 / total

	bar := color.GreenString(strings.Repeat("█", filled)) + faint(strings.Repeat("░", width-filled))

	parts := make([]string, 0, 4)
	if conflicts > 0 {
		parts = append(parts, fmt.Sprintf("resolve %d", conflicts))
	}
	if modified > 0 {
		parts = append(parts, fmt.Sprintf("stage %d", modified))
	}
	if staged > 0 {
		parts = append(parts, fmt.Sprintf("commit %d", staged))
	}
	if untracked > 0 {
		// "add" matches git's vocabulary (`git add` both stages modified
		// files AND tracks new ones), so the verb-list stays in one
		// namespace instead of inventing the compound "discard-or-track".
		parts = append(parts, fmt.Sprintf("add %d", untracked))
	}
	return fmt.Sprintf("%s [%s] %d%%  %s",
		faint("clean:"), bar, pct,
		strings.Join(parts, faint(" · ")),
	)
}

// renderDensityBar returns a one-line stacked composition bar of the working
// tree state (conflicts / staged / modified / untracked). Each segment uses a
// distinct block glyph so it remains legible without color.
//
//	tree: [▓▒█████▒▒░░░░░░░░░░░] 1C 5S 2M 8?  (16 files)
func renderDensityBar(g groupedEntries) string {
	c := len(g.Unmerged)
	s := len(g.Staged)
	m := len(g.Modified)
	u := len(g.Untracked)
	total := c + s + m + u

	width := 20
	if w, ok := ui.TTYWidth(); ok && w < 80 {
		width = 10
	}

	if total == 0 {
		faint := color.New(color.Faint).SprintFunc()
		return fmt.Sprintf("%s [%s]  %s",
			faint("tree:"),
			faint(strings.Repeat("·", width)),
			faint("(clean)"),
		)
	}

	// Allocate cells via largest-remainder so sum == width.
	counts := []int{c, s, m, u}
	cells := make([]int, len(counts))
	frac := make([]float64, len(counts))
	used := 0
	for i, n := range counts {
		raw := float64(n) / float64(total) * float64(width)
		cells[i] = int(raw)
		frac[i] = raw - float64(cells[i])
		used += cells[i]
	}
	for used < width {
		best := -1
		bestF := -1.0
		for i, f := range frac {
			if counts[i] == 0 {
				continue
			}
			if f > bestF {
				bestF = f
				best = i
			}
		}
		if best == -1 {
			break
		}
		cells[best]++
		frac[best] = -1
		used++
	}

	glyphs := []string{"▓", "█", "▒", "░"} // conflicts, staged, modified, untracked
	colorFns := []func(string, ...interface{}) string{
		color.RedString, color.GreenString, color.YellowString,
		color.New(color.Faint).Sprintf,
	}
	var bar strings.Builder
	for i, n := range cells {
		if n == 0 {
			continue
		}
		bar.WriteString(colorFns[i](strings.Repeat(glyphs[i], n)))
	}

	parts := make([]string, 0, 4)
	if c > 0 {
		parts = append(parts, color.RedString("%dC", c))
	}
	if s > 0 {
		parts = append(parts, color.GreenString("%dS", s))
	}
	if m > 0 {
		parts = append(parts, color.YellowString("%dM", m))
	}
	if u > 0 {
		parts = append(parts, color.New(color.Faint).Sprintf("%d?", u))
	}
	faint := color.New(color.Faint).SprintFunc()
	return fmt.Sprintf("%s [%s] %s  %s",
		faint("tree:"),
		bar.String(),
		strings.Join(parts, " "),
		faint(fmt.Sprintf("(%d files)", total)),
	)
}

// renderDivergenceGauge returns a compact horizontal gauge summarizing
// ahead/behind commits relative to upstream. Wide form fills 8 slots per
// side; narrow TTYs (<80 cols) fall back to 3 slots per side.
//
//	[······▓▓│········]  (↑2)
//	[▓▓│·]  ↑2            (narrow)
func renderDivergenceGauge(ahead, behind int) string {
	width, ok := ui.TTYWidth()
	narrow := ok && width < 80

	perSide := 8
	if narrow {
		perSide = 3
	}
	aFill := ahead
	if aFill > perSide {
		aFill = perSide
	}
	bFill := behind
	if bFill > perSide {
		bFill = perSide
	}
	// Asymmetric glyphs so direction is legible without color: ahead side
	// uses the denser `▓` (you're pushing outward), behind side uses the
	// lighter `▒` (upstream is filling in from the other direction). Red-
	// green colorblind users read direction from the shape contrast, not
	// from color.
	left := strings.Repeat("·", perSide-aFill) + strings.Repeat("▓", aFill)
	right := strings.Repeat("▒", bFill) + strings.Repeat("·", perSide-bFill)

	var suffix string
	switch {
	case ahead == 0 && behind == 0:
		suffix = "  " + color.New(color.Faint).Sprint("in sync")
	default:
		// Behind leads when both are present — it's the actionable half
		// (must pull/rebase before pushing). Parens dropped (refine polish
		// Round 2): they inherited from an older rendering that needed to
		// disambiguate a mid-line suffix, but with gauge-head layout the
		// arrow glyphs already carry that role.
		parts := make([]string, 0, 2)
		if behind > 0 {
			parts = append(parts, fmt.Sprintf("↓%d", behind))
		}
		if ahead > 0 {
			parts = append(parts, fmt.Sprintf("↑%d", ahead))
		}
		suffix = "  " + strings.Join(parts, " ")
	}
	return fmt.Sprintf("[%s%s%s]%s",
		color.GreenString(left),
		color.New(color.Faint).Sprint("│"),
		color.RedString(right),
		suffix,
	)
}
