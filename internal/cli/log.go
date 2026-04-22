package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:     "log [revisions] [-- <path>...]",
		Aliases: []string{"slog"},
		Short:   "Show short, colorful commit log",
		RunE:    runLog,
	}
	cmd.Flags().String("since", "", "show commits since this time (e.g. 1w, 2d, \"last monday\")")
	cmd.Flags().String("format", "", "git pretty-format string (overrides config)")
	cmd.Flags().Bool("graph", false, "include topology graph")
	cmd.Flags().IntP("limit", "n", 0, "max number of commits (0 = unlimited)")
	cmd.Flags().Bool("pulse", false, "print commit-rhythm sparkline above the log")
	cmd.Flags().Bool("calendar", false, "print week-by-weekday heatmap above the log")
	cmd.Flags().Bool("tags-rule", false, "insert a separator line before each tagged commit")
	cmd.Flags().Bool("impact", false, "append an eighths-bar scaled to |+add -del| per commit")
	cmd.Flags().Bool("cc", false, "prepend a Conventional-Commits glyph + append type tally")
	cmd.Flags().Bool("safety", false, "prefix each commit with a rebase-safety marker (◆/◇/✎/!)")
	cmd.Flags().Bool("hotspots", false, "mark commits that touch the repo's most-churned files")
	cmd.Flags().Bool("trailers", false, "append Co-authored-by/Reviewed-by trailer roll-up")
	cmd.Flags().Bool("lanes", false, "render author swim-lanes (replaces the commit list)")
	cmd.Flags().StringSlice("vis", nil, "visualization set (overrides config default; pass 'none' to disable): pulse,calendar,tags-rule,impact,cc,safety,hotspots,trailers,lanes")
	rootCmd.AddCommand(cmd)
}

func runLog(cmd *cobra.Command, args []string) error {
	cfg, _ := config.Load(cmd.Flags())

	since, _ := cmd.Flags().GetString("since")
	format, _ := cmd.Flags().GetString("format")
	graph, _ := cmd.Flags().GetBool("graph")
	limit, _ := cmd.Flags().GetInt("limit")

	// Resolve the effective viz set: any explicit flag (individual boolean or
	// --vis) overrides the config default. --vis none disables everything.
	effectiveLogVis := resolveLogVis(cmd, cfg)
	pulse := containsVis(effectiveLogVis, "pulse")
	calendar := containsVis(effectiveLogVis, "calendar")
	tagsRule := containsVis(effectiveLogVis, "tags-rule")
	viz := logVizFlags{
		impact:   containsVis(effectiveLogVis, "impact"),
		cc:       containsVis(effectiveLogVis, "cc"),
		safety:   containsVis(effectiveLogVis, "safety"),
		hotspots: containsVis(effectiveLogVis, "hotspots"),
		trailers: containsVis(effectiveLogVis, "trailers"),
	}

	if format == "" {
		format = cfg.Log.Format
	}
	if format == "" {
		format = defaultLogFormat
	}
	if limit == 0 {
		limit = cfg.Log.Limit
	}
	if !graph {
		graph = cfg.Log.Graph
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}

	if containsVis(effectiveLogVis, "lanes") && !JSONOut() {
		return renderLanes(cmd, runner, since, limit, args)
	}

	// When any per-commit visualization is enabled, we take over rendering
	// instead of passing the user's pretty-format through to git log. The
	// non-viz fast path still uses git's native output.
	if viz.any() && !JSONOut() {
		if pulse || calendar {
			dates := fetchCommitDates(cmd.Context(), runner, since, args)
			if pulse {
				if line := pulseLine(dates, since); line != "" {
					fmt.Fprintln(cmd.OutOrStdout(), line)
				}
			}
			if calendar {
				for _, line := range calendarLines(dates) {
					fmt.Fprintln(cmd.OutOrStdout(), line)
				}
			}
		}
		return renderVizLog(cmd, runner, since, limit, args, viz, tagsRule)
	}

	gitArgs := []string{"log"}
	if JSONOut() {
		// JSON 모드는 필드를 NUL로 나눠 안전 파싱
		gitArgs = append(gitArgs, "-z", "--pretty=format:"+jsonLogFormat, "--date=iso-strict", "--color=never")
	} else {
		// git은 stdout이 파이프일 때 %C(...) 포맷 코드를 스트립한다.
		// 우리는 버퍼로 캡처하므로 최종 출력 대상이 TTY이면 명시적으로 색상을 강제한다.
		if logUseColor() {
			gitArgs = append(gitArgs, "--color=always")
		} else {
			gitArgs = append(gitArgs, "--color=never")
		}
		gitArgs = append(gitArgs, "--pretty=format:"+format)
		if graph {
			gitArgs = append(gitArgs, "--graph", "--decorate", "--topo-order", "--abbrev-commit")
		} else {
			gitArgs = append(gitArgs, "--decorate", "--abbrev-commit")
		}
	}
	if limit > 0 {
		gitArgs = append(gitArgs, "-n", strconv.Itoa(limit))
	}
	if since != "" {
		sinceNorm := normalizeSince(since)
		gitArgs = append(gitArgs, "--since="+sinceNorm)
	}
	gitArgs = append(gitArgs, args...)

	stdout, stderr, err := runner.Run(cmd.Context(), gitArgs...)
	if err != nil {
		return fmt.Errorf("git log failed: %s: %w", strings.TrimSpace(string(stderr)), err)
	}

	if JSONOut() {
		return writeJSONLog(cmd.OutOrStdout(), stdout)
	}
	if (pulse || calendar) && !JSONOut() {
		dates := fetchCommitDates(cmd.Context(), runner, since, args)
		if pulse {
			if line := pulseLine(dates, since); line != "" {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
		}
		if calendar {
			for _, line := range calendarLines(dates) {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
		}
	}
	if tagsRule {
		stdout = injectTagRules(cmd.Context(), runner, stdout)
	}
	_, _ = cmd.OutOrStdout().Write(stdout)
	if len(stdout) > 0 && !strings.HasSuffix(string(stdout), "\n") {
		fmt.Fprintln(cmd.OutOrStdout())
	}
	return nil
}

// renderLanes prints one horizontal row per author, marking each commit
// with `●` on a shared time axis. When too many authors would crowd the
// view, the tail is collapsed into a synthetic "others" lane so the top
// contributors stay readable.
func renderLanes(cmd *cobra.Command, runner *git.ExecRunner, since string, limit int, pathArgs []string) error {
	ctx := cmd.Context()
	args := []string{"log", "--format=%an%x00%cI"}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit))
	}
	if since != "" {
		args = append(args, "--since="+normalizeSince(since))
	}
	args = append(args, pathArgs...)
	out, _, err := runner.Run(ctx, args...)
	if err != nil {
		return fmt.Errorf("git log failed: %w", err)
	}

	type ent struct {
		author string
		t      time.Time
	}
	var entries []ent
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		t, err := time.Parse(time.RFC3339, parts[1])
		if err != nil {
			continue
		}
		entries = append(entries, ent{parts[0], t})
	}
	if len(entries) == 0 {
		return nil
	}
	minT, maxT := entries[0].t, entries[0].t
	for _, e := range entries {
		if e.t.Before(minT) {
			minT = e.t
		}
		if e.t.After(maxT) {
			maxT = e.t
		}
	}

	order := []string{}
	byAuthor := map[string][]time.Time{}
	for _, e := range entries {
		if _, ok := byAuthor[e.author]; !ok {
			order = append(order, e.author)
		}
		byAuthor[e.author] = append(byAuthor[e.author], e.t)
	}
	sort.SliceStable(order, func(i, j int) bool {
		return len(byAuthor[order[i]]) > len(byAuthor[order[j]])
	})
	const maxLanes = 6
	if len(order) > maxLanes {
		others := []time.Time{}
		for _, a := range order[maxLanes-1:] {
			others = append(others, byAuthor[a]...)
		}
		order = append(order[:maxLanes-1], "others")
		byAuthor["others"] = others
	}

	width, _ := ui.TTYWidth()
	if width <= 0 {
		width = 80
	}
	nameWidth := 0
	for _, a := range order {
		if len(a) > nameWidth {
			nameWidth = len(a)
		}
	}
	if nameWidth > 15 {
		nameWidth = 15
	}
	cols := width - nameWidth - 2
	if cols < 10 {
		cols = 10
	}

	total := maxT.Sub(minT)
	if total <= 0 {
		total = 1
	}
	w := cmd.OutOrStdout()
	faint := color.New(color.Faint).SprintFunc()
	for _, a := range order {
		row := []rune(strings.Repeat("─", cols))
		for _, t := range byAuthor[a] {
			col := int(t.Sub(minT)) * cols / int(total)
			if col >= cols {
				col = cols - 1
			}
			if col < 0 {
				col = 0
			}
			row[col] = '●'
		}
		name := a
		if len(name) > nameWidth {
			name = name[:nameWidth]
		}
		fmt.Fprintf(w, "%-*s %s\n", nameWidth, name, color.CyanString(string(row)))
	}
	axis := fmt.Sprintf("%-*s %s", nameWidth, "", faint(fmt.Sprintf("└── %s ago → now", formatAge(time.Since(minT)))))
	fmt.Fprintln(w, axis)
	return nil
}

// logVizNames lists every known viz token alongside its flag name so
// resolveLogVis can map between them.
var logVizNames = []string{
	"pulse", "calendar", "tags-rule",
	"impact", "cc", "safety", "hotspots", "trailers",
	"lanes",
}

// resolveLogVis determines the effective viz set for this invocation.
// Precedence mirrors `gk status --vis`:
//   - If any individual viz flag (--cc, --impact, ...) OR --vis is set,
//     the user's explicit set wins (union across forms).
//   - --vis none returns an empty set, disabling every viz layer.
//   - Otherwise the config default (cfg.Log.Vis) applies.
//
// This keeps one-off overrides ergonomic (`gk log --impact`) while still
// letting projects lean on the configured default for bare `gk log`.
func resolveLogVis(cmd *cobra.Command, cfg *config.Config) []string {
	var explicit []string
	anyExplicit := false

	for _, name := range logVizNames {
		if !cmd.Flags().Changed(name) {
			continue
		}
		anyExplicit = true
		if v, _ := cmd.Flags().GetBool(name); v {
			explicit = appendUnique(explicit, name)
		}
	}
	if cmd.Flags().Changed("vis") {
		anyExplicit = true
		slice, _ := cmd.Flags().GetStringSlice("vis")
		if len(slice) == 1 && slice[0] == "none" {
			return nil
		}
		for _, v := range slice {
			explicit = appendUnique(explicit, v)
		}
	}

	if anyExplicit {
		return explicit
	}
	// When the caller explicitly sets --format, they are asking for the raw
	// git pretty-format output; the configured viz default would ignore it,
	// so suppress the default so --format keeps control of rendering.
	if cmd.Flags().Changed("format") {
		return nil
	}
	if cfg != nil {
		return cfg.Log.Vis
	}
	return nil
}

func containsVis(set []string, name string) bool {
	for _, v := range set {
		if v == name {
			return true
		}
	}
	return false
}

func appendUnique(xs []string, x string) []string {
	for _, v := range xs {
		if v == x {
			return xs
		}
	}
	return append(xs, x)
}

// logVizFlags captures the five per-commit visualizations that require
// gk to take over log rendering from git's pretty-format.
type logVizFlags struct {
	impact, cc, safety, hotspots, trailers bool
}

func (v logVizFlags) any() bool {
	return v.impact || v.cc || v.safety || v.hotspots || v.trailers
}

// must unwraps a cobra flag getter that cannot fail because we registered
// the flag with the expected type earlier.
func must[T any](v T, err error) T {
	if err != nil {
		var zero T
		return zero
	}
	return v
}

// commitRecord holds the per-commit fields we need for viz rendering. Records
// are parsed from a single `git log` invocation with a known multi-field
// format delimited by NUL + RS.
type commitRecord struct {
	sha, short, subject, author, relDate, body string
}

// parseCommitRecords splits a `%H%00%h%00%s%00%an%00%ar%00%b%1e`-formatted
// stream into structured records. Bodies may contain embedded newlines so
// the subject/author/date fields are pulled from the first line of each
// record; anything after the subject-line's trailing %b survives as body.
func parseCommitRecords(raw []byte) []commitRecord {
	records := strings.Split(string(raw), "\x1e")
	out := make([]commitRecord, 0, len(records))
	for _, rec := range records {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		f := strings.SplitN(rec, "\x00", 6)
		if len(f) < 6 {
			continue
		}
		out = append(out, commitRecord{
			sha:     f[0],
			short:   f[1],
			subject: f[2],
			author:  f[3],
			relDate: f[4],
			body:    f[5],
		})
	}
	return out
}

// fetchNumstats runs a separate `git log --format=%H --numstat` pass over the
// same revision scope and returns per-sha change counts. Split from the main
// pretty-format pass because interleaving numstat into a NUL-delimited
// record stream is fragile.
func fetchNumstats(ctx context.Context, runner *git.ExecRunner, since string, limit int, pathArgs []string) map[string]numstat {
	args := []string{"log", "--format=%H", "--numstat"}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit))
	}
	if since != "" {
		args = append(args, "--since="+normalizeSince(since))
	}
	args = append(args, pathArgs...)
	out, _, err := runner.Run(ctx, args...)
	if err != nil {
		return map[string]numstat{}
	}
	m := map[string]numstat{}
	var cur string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if len(line) >= 40 && isHex(line[:40]) {
			cur = line
			if _, ok := m[cur]; !ok {
				m[cur] = numstat{}
			}
			continue
		}
		if cur == "" {
			continue
		}
		cols := strings.SplitN(line, "\t", 3)
		if len(cols) != 3 {
			continue
		}
		a, _ := strconv.Atoi(cols[0])
		d, _ := strconv.Atoi(cols[1])
		n := m[cur]
		n.adds += a
		n.dels += d
		n.files = append(n.files, cols[2])
		m[cur] = n
	}
	return m
}

type numstat struct {
	adds, dels int
	files      []string
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// ccType holds the single-cell geometric glyph + color treatment for one
// Conventional-Commits type. Glyphs are chosen from BMP Unicode shapes that
// render as single cells in modern monospace fonts and that avoid collision
// with the `--safety` column's ◆/◇/✎/! markers. The colorize function is
// applied to the matching portion of the subject line for inline highlight.
type ccType struct {
	name, glyph string
	colorize    func(string, ...interface{}) string
}

// ccTypes intentionally avoids emoji — gitmoji-style glyphs feel AI-ish,
// vary in width across fonts, and clash with the project's otherwise
// technical aesthetic. Each entry pairs a geometric glyph with a subject
// color so even in --no-color mode the prefix remains legible.
var ccTypes = []ccType{
	{"feat", "▲", color.GreenString},
	{"fix", "✕", color.RedString},
	{"refactor", "↻", color.YellowString},
	{"docs", "¶", color.BlueString},
	{"chore", "·", color.New(color.Faint).Sprintf},
	{"test", "◎", color.MagentaString},
	{"perf", "↑", color.CyanString},
	{"ci", "⊙", color.New(color.Faint).Sprintf},
	{"build", "▣", color.CyanString},
	{"revert", "←", color.RedString},
	{"style", "✧", color.New(color.Faint).Sprintf},
}

var ccHeaderRE = regexp.MustCompile(`^([a-z]+)(?:\([^)]+\))?!?:\s*`)

// ccClassify returns the type keyword and its glyph for a commit subject,
// or ("", "") if the subject does not match Conventional Commits. The
// glyph is what gets prepended to the log row.
func ccClassify(subject string) (string, string) {
	m := ccHeaderRE.FindStringSubmatch(subject)
	if m == nil {
		return "", ""
	}
	t := m[1]
	for _, entry := range ccTypes {
		if entry.name == t {
			return t, entry.glyph
		}
	}
	return t, "◦"
}

// ccColorize returns the subject with its leading `type` token inline-
// highlighted in the type's signature color. Called alongside the glyph
// prefix so the type is visible both from the margin and within the line
// (and readable when color is available — the plain subject is unchanged
// under `--no-color` because the color funcs no-op when NoColor is set).
func ccColorize(subject, typeName string) string {
	if typeName == "" || !strings.HasPrefix(subject, typeName) {
		return subject
	}
	for _, entry := range ccTypes {
		if entry.name == typeName {
			return entry.colorize(typeName) + subject[len(typeName):]
		}
	}
	return subject
}

// renderImpactBar returns an eighths-bar whose width scales with |adds+dels|
// relative to the overall peak of the record set.
func renderImpactBar(adds, dels, peak int) string {
	total := adds + dels
	if total == 0 {
		return ""
	}
	const maxWidth = 10
	cells := 0
	if peak > 0 {
		cells = (total * maxWidth * 8) / peak
	}
	full := cells / 8
	rem := cells % 8
	bar := strings.Repeat("█", full)
	if rem > 0 {
		eighths := []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
		bar += eighths[rem]
	}
	if bar == "" {
		bar = "▏"
	}
	return color.New(color.Faint).Sprint(bar)
}

// rebaseSafety classifies a commit by its relation to the current upstream.
// The "already pushed" case — which is the overwhelming majority on an
// active branch — returns a blank space on purpose: marking every pushed
// commit with a glyph would flood the log column and drown out the cases
// that actually deserve attention.
//
//	(space) already pushed (in @{u}) — default, silent state
//	◇       unpushed (ahead of @{u}) — you still need to push this
//	✎       recently amended (reflog says HEAD was rewritten within an hour)
func rebaseSafety(ctx context.Context, runner *git.ExecRunner, sha string, pushed map[string]bool, amended map[string]bool) rune {
	if amended[sha] {
		return '✎'
	}
	if pushed[sha] {
		return ' '
	}
	return '◇'
}

// collectPushedShas enumerates SHAs reachable from @{upstream} so we can mark
// commits as already-pushed without one git call per commit. Returns empty
// map when there is no configured upstream.
func collectPushedShas(ctx context.Context, runner *git.ExecRunner) map[string]bool {
	out, _, err := runner.Run(ctx, "rev-list", "@{upstream}")
	if err != nil {
		return map[string]bool{}
	}
	m := make(map[string]bool, 256)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			m[line] = true
		}
	}
	return m
}

// collectRecentlyAmended returns the set of SHAs that the local reflog shows
// as having been rewritten (commit --amend / rebase) within the last hour.
func collectRecentlyAmended(ctx context.Context, runner *git.ExecRunner) map[string]bool {
	m := map[string]bool{}
	out, _, err := runner.Run(ctx, "reflog", "HEAD", "--format=%H %gs", "--since=1.hours.ago")
	if err != nil {
		return m
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "amend") || strings.Contains(line, "rebase") {
			if f := strings.Fields(line); len(f) > 0 {
				m[f[0]] = true
			}
		}
	}
	return m
}

// collectHotspots returns the top-10 most-touched files in the last 90 days.
// Cheap enough to compute on demand — no cache file yet.
func collectHotspots(ctx context.Context, runner *git.ExecRunner) map[string]bool {
	out, _, err := runner.Run(ctx, "log", "--since=90.days.ago", "--name-only", "--format=")
	if err != nil {
		return map[string]bool{}
	}
	counts := map[string]int{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		counts[line]++
	}
	type kv struct {
		k string
		v int
	}
	list := make([]kv, 0, len(counts))
	for k, v := range counts {
		list = append(list, kv{k, v})
	}
	// Partial sort via full sort — repos are small-ish.
	// Keep only files with ≥5 touches to avoid marking trivial rename counts.
	result := map[string]bool{}
	// Sort desc by count.
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].v > list[i].v {
				list[i], list[j] = list[j], list[i]
			}
		}
		if i >= 10 {
			break
		}
		if list[i].v < 5 {
			break
		}
		result[list[i].k] = true
	}
	return result
}

// parseTrailers extracts Co-authored-by and review trailers from the commit
// body. Returns a compact roll-up string, or empty if none.
var trailerRE = regexp.MustCompile(`(?mi)^(Co-authored-by|Reviewed-by|Signed-off-by): ([^<\n]+?)\s*(?:<[^>]+>)?$`)

func parseTrailers(body string) string {
	authors := map[string]bool{}
	reviewers := map[string]bool{}
	for _, m := range trailerRE.FindAllStringSubmatch(body, -1) {
		kind := strings.ToLower(m[1])
		name := strings.TrimSpace(m[2])
		switch kind {
		case "co-authored-by":
			authors[name] = true
		case "reviewed-by":
			reviewers[name] = true
		}
	}
	if len(authors) == 0 && len(reviewers) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[")
	for a := range authors {
		b.WriteString("+" + a + " ")
	}
	if len(reviewers) > 0 {
		b.WriteString("review:")
		first := true
		for r := range reviewers {
			if !first {
				b.WriteString("+")
			}
			b.WriteString(r)
			first = false
		}
	}
	out := strings.TrimSpace(b.String()) + "]"
	return out
}

// renderVizLog drives the custom log-rendering pipeline used when any of
// --impact/--cc/--safety/--hotspots/--trailers are active. It performs a
// single `git log` with a fixed multi-field format + numstat, parses records,
// and emits one augmented line per commit.
func renderVizLog(cmd *cobra.Command, runner *git.ExecRunner, since string, limit int, pathArgs []string, v logVizFlags, tagsRule bool) error {
	ctx := cmd.Context()
	args := []string{
		"log",
		"--date=iso-strict",
		"--format=" + vizRecordFormat,
	}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit))
	}
	if since != "" {
		args = append(args, "--since="+normalizeSince(since))
	}
	args = append(args, pathArgs...)

	stdout, stderr, err := runner.Run(ctx, args...)
	if err != nil {
		return fmt.Errorf("git log failed: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	records := parseCommitRecords(stdout)

	var numstats map[string]numstat
	if v.impact || v.hotspots {
		numstats = fetchNumstats(ctx, runner, since, limit, pathArgs)
	}

	var pushed, amended, hotspots map[string]bool
	if v.safety {
		pushed = collectPushedShas(ctx, runner)
		amended = collectRecentlyAmended(ctx, runner)
	}
	if v.hotspots {
		hotspots = collectHotspots(ctx, runner)
	}

	peak := 0
	if v.impact {
		for _, r := range records {
			n := numstats[r.sha]
			if t := n.adds + n.dels; t > peak {
				peak = t
			}
		}
	}

	tags := map[string]tagInfo{}
	if tagsRule {
		tags = fetchTags(ctx, runner)
	}
	width, _ := ui.TTYWidth()
	if width <= 0 {
		width = 72
	}

	w := cmd.OutOrStdout()
	typeCounts := map[string]int{}
	for _, r := range records {
		if tagsRule {
			if t, ok := tags[r.short]; ok {
				fmt.Fprintln(w, renderTagRule(t, width))
			}
		}

		var prefix, suffix strings.Builder
		if v.safety {
			if mark := rebaseSafety(ctx, runner, r.sha, pushed, amended); mark != ' ' {
				prefix.WriteRune(mark)
				prefix.WriteByte(' ')
			}
		}
		subject := r.subject
		if v.cc {
			if ccType, glyph := ccClassify(r.subject); ccType != "" {
				typeCounts[ccType]++
				prefix.WriteString(glyph + " ")
				subject = ccColorize(r.subject, ccType)
			} else {
				prefix.WriteString("  ")
			}
		}

		line := fmt.Sprintf("%s (%s) <%s> %s",
			color.YellowString(r.short),
			color.GreenString(r.relDate),
			color.New(color.FgBlue, color.Bold).Sprint(r.author),
			subject,
		)
		n := numstats[r.sha]
		if v.hotspots {
			if commitHitsHotspot(n.files, hotspots) {
				suffix.WriteString("  " + color.RedString("🔥"))
			}
		}
		if v.impact {
			if bar := renderImpactBar(n.adds, n.dels, peak); bar != "" {
				suffix.WriteString("  " + bar + " " + color.New(color.Faint).Sprintf("+%d −%d", n.adds, n.dels))
			}
		}
		if v.trailers {
			if t := parseTrailers(r.body); t != "" {
				suffix.WriteString("  " + color.New(color.Faint).Sprint(t))
			}
		}

		fmt.Fprintln(w, prefix.String()+line+suffix.String())
	}

	if v.cc && len(typeCounts) > 0 {
		parts := make([]string, 0, len(typeCounts))
		keys := make([]string, 0, len(typeCounts))
		for k := range typeCounts {
			keys = append(keys, k)
		}
		// deterministic ordering
		for i := 0; i < len(keys); i++ {
			for j := i + 1; j < len(keys); j++ {
				if typeCounts[keys[j]] > typeCounts[keys[i]] {
					keys[i], keys[j] = keys[j], keys[i]
				}
			}
		}
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, typeCounts[k]))
		}
		fmt.Fprintln(w, color.New(color.Faint).Sprint("         types: "+strings.Join(parts, " ")))
	}
	return nil
}

const vizRecordFormat = "%H%x00%h%x00%s%x00%an%x00%ar%x00%b%x1e"

func commitHitsHotspot(files []string, hotspots map[string]bool) bool {
	for _, f := range files {
		if hotspots[f] {
			return true
		}
	}
	return false
}

// tagInfo captures the info needed to render a tag-rule separator.
type tagInfo struct {
	name string
	ago  string
}

// fetchTags returns a map from commit short-sha → tagInfo for all tags, keyed
// by the first 7 characters so it matches the default `%h` width.
func fetchTags(ctx context.Context, runner *git.ExecRunner) map[string]tagInfo {
	out, _, err := runner.Run(ctx, "for-each-ref", "refs/tags",
		"--format=%(refname:short)%00%(objectname:short)%00%(*objectname:short)%00%(creatordate:relative)",
		"--sort=-creatordate")
	if err != nil {
		return nil
	}
	m := make(map[string]tagInfo)
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\x00", 4)
		if len(f) != 4 {
			continue
		}
		// For annotated tags, *objectname is the commit the tag points at.
		// Lightweight tags leave it blank; the tag object itself is the commit.
		commit := f[2]
		if commit == "" {
			commit = f[1]
		}
		m[commit] = tagInfo{name: f[0], ago: f[3]}
	}
	return m
}

// logShaRE extracts the first short SHA that appears on a log line, skipping
// leading whitespace, graph chars (│├└─|/\\ ), and ANSI escapes.
var logShaRE = regexp.MustCompile(`(?m)^(?:[│├└─|\/\\ *]*(?:\x1b\[[0-9;]*m)?)*([0-9a-f]{7,40})`)

// injectTagRules walks the log stdout line-by-line, inserting a separator row
// just before any commit whose short-sha matches a known tag.
func injectTagRules(ctx context.Context, runner *git.ExecRunner, stdout []byte) []byte {
	tags := fetchTags(ctx, runner)
	if len(tags) == 0 {
		return stdout
	}
	width, _ := ui.TTYWidth()
	if width <= 0 {
		width = 72
	}
	var out bytes.Buffer
	for _, line := range strings.SplitAfter(string(stdout), "\n") {
		if m := logShaRE.FindStringSubmatch(line); m != nil {
			sha := m[1]
			key := sha
			if len(key) > 7 {
				key = key[:7]
			}
			if t, ok := tags[key]; ok {
				out.WriteString(renderTagRule(t, width))
				out.WriteByte('\n')
			}
		}
		out.WriteString(line)
	}
	return out.Bytes()
}

// renderTagRule produces a centered-ish separator row marking a tag.
//
//	──┤ v0.4.0 (3 days ago) ├────────
func renderTagRule(t tagInfo, width int) string {
	head := fmt.Sprintf("──┤ %s (%s) ├", t.name, t.ago)
	// head width counts runes, not bytes.
	headRunes := 0
	for range head {
		headRunes++
	}
	tail := width - headRunes
	if tail < 2 {
		tail = 2
	}
	return color.CyanString(head) + color.New(color.Faint).Sprint(strings.Repeat("─", tail))
}

// fetchCommitDates returns committer dates for the revision scope, matching
// the same --since/path args the main log call will use. Reused by --pulse
// and --calendar to avoid running git log twice.
func fetchCommitDates(ctx context.Context, runner *git.ExecRunner, since string, pathArgs []string) []time.Time {
	args := []string{"log", "--format=%cI"}
	if since != "" {
		args = append(args, "--since="+normalizeSince(since))
	}
	args = append(args, pathArgs...)
	out, _, err := runner.Run(ctx, args...)
	if err != nil || len(out) == 0 {
		return nil
	}
	dates := make([]time.Time, 0, 128)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, line)
		if err != nil {
			continue
		}
		dates = append(dates, t)
	}
	return dates
}

// calendarLines renders a GitHub-contrib-style heatmap: 7 rows (Mon..Sun) by
// N weekly columns, bucketing each commit into its ISO-week cell using ░▒▓█
// scaled to the busiest bucket. Empty dates return nil (caller prints nothing).
//
//	    W1 W2 W3 W4
//	Mon ░  ▒  ▓  █
//	Tue ·  ▒  ▓  █
//	...
func calendarLines(dates []time.Time) []string {
	if len(dates) == 0 {
		return nil
	}
	minT, maxT := dates[0], dates[0]
	for _, d := range dates {
		if d.Before(minT) {
			minT = d
		}
		if d.After(maxT) {
			maxT = d
		}
	}
	// Anchor the grid to the Monday on or before minT.
	startDay := weekStart(time.Date(minT.Year(), minT.Month(), minT.Day(), 0, 0, 0, 0, minT.Location()))
	endDay := time.Date(maxT.Year(), maxT.Month(), maxT.Day(), 0, 0, 0, 0, maxT.Location())
	totalDays := int(endDay.Sub(startDay).Hours()/24) + 1
	if totalDays < 1 {
		totalDays = 1
	}
	weeks := (totalDays + 6) / 7
	if weeks > 26 { // cap at ~6 months for terminal sanity
		weeks = 26
	}

	// grid[row=weekday][col=week] = count
	grid := make([][]int, 7)
	for i := range grid {
		grid[i] = make([]int, weeks)
	}
	peak := 0
	for _, d := range dates {
		day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
		offset := int(day.Sub(startDay).Hours() / 24)
		week := offset / 7
		if week >= weeks {
			continue
		}
		row := weekdayRow(day.Weekday())
		grid[row][week]++
		if grid[row][week] > peak {
			peak = grid[row][week]
		}
	}

	heat := []rune{' ', '░', '▒', '▓', '█'}
	dayNames := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

	faint := color.New(color.Faint).SprintFunc()
	header := "    "
	for w := 0; w < weeks; w++ {
		header += fmt.Sprintf("W%-2d ", w+1)
	}
	lines := []string{faint(strings.TrimRight(header, " "))}
	for row := 0; row < 7; row++ {
		var b strings.Builder
		b.WriteString(dayNames[row] + " ")
		for w := 0; w < weeks; w++ {
			count := grid[row][w]
			var glyph rune
			switch {
			case count == 0:
				glyph = '·'
			case peak <= 1:
				glyph = heat[4]
			default:
				idx := (count-1)*(len(heat)-1)/maxInt(peak-1, 1) + 1
				if idx >= len(heat) {
					idx = len(heat) - 1
				}
				glyph = heat[idx]
			}
			fmt.Fprintf(&b, " %s  ", color.CyanString(string(glyph)))
		}
		lines = append(lines, b.String())
	}
	return lines
}

// weekStart returns the Monday of the week containing t (ISO weeks).
func weekStart(t time.Time) time.Time {
	wd := int(t.Weekday()) // Sun=0
	// Shift so Mon=0, Sun=6
	back := (wd + 6) % 7
	return t.AddDate(0, 0, -back)
}

func weekdayRow(w time.Weekday) int {
	// Mon=0..Sun=6
	return (int(w) + 6) % 7
}

var pulseGlyphs = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// pulseLine is the pure-function core of --pulse: given commit timestamps and
// the --since label, it produces a single-line sparkline with a
// "(N commits, peak Weekday)" suffix. Zero-activity days render as '·'.
func pulseLine(dates []time.Time, since string) string {
	if len(dates) == 0 {
		return ""
	}
	minT, maxT := dates[0], dates[0]
	for _, d := range dates {
		if d.Before(minT) {
			minT = d
		}
		if d.After(maxT) {
			maxT = d
		}
	}
	startDay := time.Date(minT.Year(), minT.Month(), minT.Day(), 0, 0, 0, 0, minT.Location())
	endDay := time.Date(maxT.Year(), maxT.Month(), maxT.Day(), 0, 0, 0, 0, maxT.Location())
	days := int(endDay.Sub(startDay).Hours()/24) + 1
	if days < 1 {
		days = 1
	}
	if days > 180 {
		days = 180
	}

	buckets := make([]int, days)
	for _, d := range dates {
		day := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
		idx := int(day.Sub(startDay).Hours() / 24)
		if idx < 0 || idx >= days {
			continue
		}
		buckets[idx]++
	}

	peakIdx, peakVal := 0, 0
	for i, v := range buckets {
		if v > peakVal {
			peakIdx, peakVal = i, v
		}
	}

	var spark strings.Builder
	for _, v := range buckets {
		if v == 0 {
			spark.WriteRune('·')
			continue
		}
		idx := 0
		if peakVal > 0 {
			idx = (v - 1) * (len(pulseGlyphs) - 1) / maxInt(peakVal-1, 1)
		}
		if idx < 0 {
			idx = 0
		}
		if idx >= len(pulseGlyphs) {
			idx = len(pulseGlyphs) - 1
		}
		spark.WriteRune(pulseGlyphs[idx])
	}

	label := since
	if label == "" {
		label = fmt.Sprintf("%dd", days)
	}
	peakDay := startDay.Add(time.Duration(peakIdx) * 24 * time.Hour)
	faint := color.New(color.Faint).SprintFunc()
	return fmt.Sprintf("%s %s %s  %s",
		faint("pulse"),
		faint(label),
		color.CyanString(spark.String()),
		faint(fmt.Sprintf("(%d commits, peak %s)", len(dates), peakDay.Format("Mon"))),
	)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

const (
	defaultLogFormat = "%C(yellow)%h%C(reset) %C(green)(%ar)%C(reset) %C(bold blue)<%an>%C(reset) %s%C(auto)%d%C(reset)"
	// JSON 모드는 %x00 구분자로 레코드, 필드. 파이프로 디코드하기 위해 레코드는 %x1e (RS).
	jsonLogFormat = "%H%x00%h%x00%an%x00%ae%x00%aI%x00%s%x00%b%x1e"
)

var shortSinceRE = regexp.MustCompile(`^(\d+)\s*([smhdwMy])$`)

// logUseColor decides whether git log output should include ANSI color codes.
// Order: --no-color flag → NO_COLOR env → stdout TTY check.
func logUseColor() bool {
	if NoColorFlag() {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return ui.IsTerminal()
}

// normalizeSince converts short forms like "1w", "3d" into git-friendly strings.
// Everything else is passed through unchanged.
func normalizeSince(s string) string {
	m := shortSinceRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return s
	}
	n, _ := strconv.Atoi(m[1])
	switch m[2] {
	case "s":
		return fmt.Sprintf("%d.seconds.ago", n)
	case "m":
		return fmt.Sprintf("%d.minutes.ago", n)
	case "h":
		return fmt.Sprintf("%d.hours.ago", n)
	case "d":
		return fmt.Sprintf("%d.days.ago", n)
	case "w":
		return fmt.Sprintf("%d.weeks.ago", n)
	case "M":
		return fmt.Sprintf("%d.months.ago", n)
	case "y":
		return fmt.Sprintf("%d.years.ago", n)
	}
	return s
}

// LogEntry represents a single commit in JSON output mode.
type LogEntry struct {
	SHA      string `json:"sha"`
	ShortSHA string `json:"short_sha"`
	Author   string `json:"author"`
	Email    string `json:"email"`
	Date     string `json:"date"`
	Subject  string `json:"subject"`
	Body     string `json:"body,omitempty"`
}

func writeJSONLog(w io.Writer, raw []byte) error {
	entries := parseJSONLog(raw)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

// parseJSONLog splits raw output on %x1e (record sep) and %x00 (field sep).
func parseJSONLog(raw []byte) []LogEntry {
	records := strings.Split(strings.TrimRight(string(raw), "\x1e\n"), "\x1e")
	out := make([]LogEntry, 0, len(records))
	for _, rec := range records {
		if rec == "" {
			continue
		}
		fields := strings.Split(rec, "\x00")
		if len(fields) < 7 {
			continue
		}
		out = append(out, LogEntry{
			SHA:      fields[0],
			ShortSHA: fields[1],
			Author:   fields[2],
			Email:    fields[3],
			Date:     fields[4],
			Subject:  fields[5],
			Body:     fields[6],
		})
	}
	return out
}
