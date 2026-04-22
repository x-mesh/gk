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

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

var statusVisFlags []string
var statusNoFetch bool

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
	cmd.Flags().StringSliceVar(&statusVisFlags, "vis", nil, "visualizations (repeatable or comma-list): gauge,bar,progress,types,staleness,tree,conflict,churn,risk; pass 'none' to disable the configured default")
	cmd.Flags().BoolVar(&statusNoFetch, "no-fetch", false, "skip the quiet upstream fetch (same as GK_NO_FETCH=1 or status.auto_fetch: false)")
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
//   - Debounced: a marker under $GIT_COMMON_DIR/gk/ records the last
//     successful fetch per-remote so a burst of `st` calls only fires
//     the network once.
//   - Returns silently on every error path — status always renders with
//     whatever is already in refs/remotes/*, even offline.
func maybeFetchUpstream(parent context.Context, repoDir string) {
	remote, branch, ok := currentUpstream(parent, repoDir)
	if !ok {
		return
	}

	gitDir := gitCommonDir(parent, repoDir)
	if gitDir != "" && recentlyFetched(gitDir, remote) {
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
	if err := cmd.Run(); err != nil {
		// Fetch failed (offline, auth, timeout). Touching the marker
		// on failure would mask a transient issue — leave the marker
		// alone so the next `st` retries immediately.
		return
	}
	if gitDir != "" {
		markFetch(gitDir, remote)
	}
}

// currentUpstream resolves the current branch's upstream into
// (remote, short-branch) with a single `git rev-parse --abbrev-ref HEAD@{u}`
// call. Previously this cost three separate git invocations; one process
// spawn is ~5–15 ms on macOS so collapsing them shaves off a measurable
// fraction of the pre-fetch overhead.
//
// Returns ok=false when there is no upstream configured (detached HEAD,
// brand-new local branch, etc.) — the caller should skip fetching.
func currentUpstream(ctx context.Context, repoDir string) (string, string, bool) {
	c := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD@{u}")
	c.Dir = repoDir
	// Suppress the "no upstream" error message; we detect via exit code.
	c.Stderr = io.Discard
	out, err := c.Output()
	if err != nil {
		return "", "", false
	}
	full := strings.TrimSpace(string(out))
	if full == "" {
		return "", "", false
	}
	// Upstream is "<remote>/<branch>"; branches may contain "/" so split
	// on the first separator only.
	parts := strings.SplitN(full, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// gitCommonDir returns `git rev-parse --git-common-dir`, resolved to an
// absolute path. Worktrees share this directory so the debounce marker
// is effective across them. Returns "" on any failure.
func gitCommonDir(ctx context.Context, repoDir string) string {
	c := exec.CommandContext(ctx, "git", "rev-parse", "--git-common-dir")
	c.Dir = repoDir
	out, err := c.Output()
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return ""
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(repoDir, dir)
	}
	return dir
}

// fetchMarkerPath returns the per-remote marker file used to debounce
// maybeFetchUpstream. Remote names cannot contain path separators by git
// convention so the name is safe to use verbatim.
func fetchMarkerPath(gitDir, remote string) string {
	return filepath.Join(gitDir, "gk", "last-fetch-"+remote)
}

// recentlyFetched reports whether the marker for this remote was touched
// within statusFetchDebounce. Missing marker → not recent → do fetch.
func recentlyFetched(gitDir, remote string) bool {
	info, err := os.Stat(fetchMarkerPath(gitDir, remote))
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) < statusFetchDebounce
}

// markFetch touches the per-remote marker so subsequent calls within the
// debounce window skip the network round-trip. Failures are swallowed —
// the worst case is that we fetch again on the next invocation.
func markFetch(gitDir, remote string) {
	path := fetchMarkerPath(gitDir, remote)
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

	// branch line
	line := fmt.Sprintf("%s %s", faint("branch:"), bold(st.Branch))
	if st.Upstream != "" {
		line += fmt.Sprintf("  %s %s", faint("⇄"), cyan(st.Upstream))
	}
	if statusVisEnabled("gauge") && st.Upstream != "" {
		line += "  " + renderDivergenceGauge(st.Ahead, st.Behind)
	} else if st.Ahead != 0 || st.Behind != 0 {
		line += fmt.Sprintf("  ↑%d ↓%d", st.Ahead, st.Behind)
	}
	if statusVisEnabled("staleness") {
		if ago := lastCommitAgo(cmd, runner); ago != "" {
			line += "  " + faint("· last commit "+ago)
		}
	}
	fmt.Fprintln(w, line)

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
	if statusVisEnabled("tree") && len(st.Entries) > 0 {
		renderStatusTree(w, st.Entries)
	} else {
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
				fmt.Fprintf(w, "  %s %s%s\n", color.RedString(e.XY), e.Path, suffix)
			}
		}
		if len(grouped.Staged) > 0 {
			fmt.Fprintln(w, color.GreenString("staged:"))
			for _, e := range grouped.Staged {
				fmt.Fprintf(w, "  %s %s\n", color.GreenString(e.XY), displayPath(e))
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
				parts := []string{fmt.Sprintf("  %s %s", color.YellowString(e.XY), displayPath(e))}
				if showRisk {
					if marker := riskMarker(cmd.Context(), runner, e); marker != "" {
						parts[0] = fmt.Sprintf("  %s %s %s", color.YellowString(e.XY), marker, displayPath(e))
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
				fmt.Fprintf(w, "  %s %s%s\n", color.New(color.FgHiBlack).Sprint("??"), e.Path, suffix)
			}
		}
	}
	if len(st.Entries) == 0 {
		fmt.Fprintln(w, faint("working tree clean"))
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

// sortByRisk re-orders modified entries so high-risk files rise to the top
// of the section.
func sortByRisk(ctx context.Context, runner *git.ExecRunner, entries []git.StatusEntry) []git.StatusEntry {
	type scored struct {
		entry git.StatusEntry
		score int
	}
	list := make([]scored, len(entries))
	for i, e := range entries {
		list[i] = scored{e, fileRiskScore(ctx, runner, e)}
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
// carry the two-letter XY porcelain code colored by category.
func renderStatusTree(w io.Writer, entries []git.StatusEntry) {
	root := buildStatusTree(entries)
	faint := color.New(color.Faint).SprintFunc()
	writeChildren(w, root, "", faint)
}

func writeChildren(w io.Writer, n *treeNode, prefix string, faint func(...interface{}) string) {
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
			fmt.Fprintf(w, "%s%s%s  %s\n", prefix, faint(branch), colorXY(child.entry.XY), displayTreeName(child))
		} else {
			fmt.Fprintf(w, "%s%s%s/  %s\n", prefix, faint(branch),
				color.New(color.Bold).Sprint(child.name),
				faint(fmt.Sprintf("(%d)", subtreeCount(child))),
			)
			writeChildren(w, child, prefix+faint(indent), faint)
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

// lastCommitAgo returns a short relative age ("11d", "4h", "32m") of HEAD's
// committer date, or empty string when there is no HEAD (fresh repo), the age
// is under 1 minute, or git fails. It calls through the runner from the
// current command context.
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
	return formatAge(time.Since(time.Unix(secs, 0)))
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
var lockfileBasenames = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"Gemfile.lock": true, "Cargo.lock": true, "composer.lock": true,
	"poetry.lock": true, "Pipfile.lock": true,
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
//	clean: [███░░░░░░░] 30%  stage 5 · commit 3 · resolve 1 · discard-or-track 1
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
		parts = append(parts, fmt.Sprintf("discard-or-track %d", untracked))
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
	left := strings.Repeat("·", perSide-aFill) + strings.Repeat("▓", aFill)
	right := strings.Repeat("▓", bFill) + strings.Repeat("·", perSide-bFill)

	var suffix string
	switch {
	case ahead == 0 && behind == 0:
		suffix = "  " + color.New(color.Faint).Sprint("in sync")
	default:
		parts := make([]string, 0, 2)
		if ahead > 0 {
			parts = append(parts, fmt.Sprintf("↑%d", ahead))
		}
		if behind > 0 {
			parts = append(parts, fmt.Sprintf("↓%d", behind))
		}
		if narrow {
			suffix = "  " + strings.Join(parts, " ")
		} else {
			suffix = "  (" + strings.Join(parts, " ") + ")"
		}
	}
	return fmt.Sprintf("[%s%s%s]%s",
		color.GreenString(left),
		color.New(color.Faint).Sprint("│"),
		color.RedString(right),
		suffix,
	)
}
