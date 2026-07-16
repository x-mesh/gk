package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/branchparent"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	wt := &cobra.Command{
		Use:     "worktree",
		Aliases: []string{"wt"},
		Short:   "Worktree management helpers (interactive TUI when run without a subcommand)",
		Long: `Worktree management helpers.

With no subcommand, gk opens an interactive TUI for listing, adding,
removing, and entering worktrees. Picking a worktree spawns a new
$SHELL in its directory — type 'exit' to return to the original shell.

If you prefer the cd-in-place pattern, pass --print-path and wrap in
a shell alias so the chosen path applies to the parent shell:

    # ~/.zshrc or ~/.bashrc
    gwt() { local p="$(gk wt --print-path)"; [ -n "$p" ] && cd "$p"; }

The TUI falls back to printing this help on non-interactive stdin/stdout.`,
		RunE: runWorktreeTUI,
	}
	wt.Flags().Bool("print-path", false, "on 'cd', print the chosen path instead of entering a subshell (for `cd $(gk wt --print-path)` wrappers)")
	wt.Flags().BoolP("global", "g", false, "list every gk-managed worktree across all projects (toggle inside the TUI with 'g')")

	list := &cobra.Command{
		Use:   "list",
		Short: "List worktrees (table or --json)",
		RunE:  runWorktreeList,
	}

	add := &cobra.Command{
		Use:   "add <name|path> [branch]",
		Short: "Create a worktree checking out [branch] (or HEAD)",
		Long: `Create a worktree.

Path resolution:
  - An absolute path is used as-is.
  - A relative name/path is resolved under the managed base directory:
      <worktree.base>/<worktree.project>/<name>
    Default base is ~/.gk/worktree; project defaults to the repo's
    toplevel basename. Override both in .gk.yaml.

Branch logic:
  - Without --new, [branch] must already exist (local or remote-tracking).
  - With --new (-b), a new branch named [branch] is created from
    --from (default HEAD).
  - A newly created branch records its fork parent
    (branch.<name>.gk-parent = the base branch) so gk status, the
    SOURCE column, and gk land --promote know where it came from.
    Skipped when the base is not a local branch (detached HEAD, remote
    ref, raw SHA). Undo with: gk branch unset-parent.

Examples:
  gk worktree add ai-commit           # ~/.gk/worktree/<project>/ai-commit
  gk worktree add feat-x -b feat/x    # new branch, managed path
  gk worktree add /tmp/exp -b hotfix  # absolute path wins, branch still created
`,
		Args: cobra.RangeArgs(1, 2),
		RunE: runWorktreeAdd,
	}
	add.Flags().BoolP("new", "b", false, "create a new branch named [branch] at --from")
	add.Flags().String("from", "", "base ref for the new branch (default: HEAD)")
	add.Flags().Bool("detach", false, "detach HEAD in the worktree instead of tracking a branch")
	add.Flags().Bool("init", false, "run worktree init (link/copy/run from .gk.yaml) after creating; skips the interactive prompt")
	add.Flags().Bool("no-init", false, "skip worktree init entirely, even when worktree.init is configured")

	rm := &cobra.Command{
		Use:   "remove <path>",
		Short: "Remove a worktree",
		Args:  cobra.ExactArgs(1),
		RunE:  runWorktreeRemove,
	}
	rm.Flags().BoolP("force", "f", false, "force remove a dirty worktree, and unlock+remove a worktree whose lock holder is no longer running")
	rm.Flags().Bool("force-locked", false, "remove even when the lock holder is still running (dangerous: may be in active use)")

	prune := &cobra.Command{
		Use:   "prune",
		Short: "Prune worktree administrative records",
		RunE:  runWorktreePrune,
	}

	initc := &cobra.Command{
		Use:   "init [path]",
		Short: "Bootstrap a worktree's gitignored state (link/copy/run from .gk.yaml)",
		Long: `Reconstitute the gitignored, per-checkout state a fresh worktree lacks:
secrets (.env), dependency trees (node_modules), virtualenvs (.venv).

Reads worktree.init from .gk.yaml and applies it to the target worktree
(default: the current one):

  link:  symlink each path from the main worktree   (secrets, shared config)
  copy:  copy each path from the main worktree       (per-worktree editable)
  run:   execute each shell command in the worktree  (npm ci, uv sync, …)

The operation is idempotent — re-running fixes only what's missing, so it
doubles as a "retry the failed setup step" command.

When worktree.init is absent, gk detects the project's package manifests
(package-lock.json, pnpm-lock.yaml, uv.lock, requirements.txt, go.mod, …)
and proposes a worktree.init block you can save into .gk.yaml.

Examples:
  gk worktree init                 # bootstrap the current worktree
  gk worktree init ~/.gk/worktree/gk/feat-x
  gk worktree init --save          # also write the detected block to .gk.yaml
`,
		Args: cobra.RangeArgs(0, 1),
		RunE: runWorktreeInit,
	}
	initc.Flags().Bool("save", false, "write the detected worktree.init block to .gk.yaml (only when none is configured)")
	// --dry-run is the inherited persistent flag (root.go) — reused here to
	// preview link/copy/run without performing them.

	wt.AddCommand(list, add, rm, prune, initc,
		newWorktreeAcquireCmd(),
		newWorktreeRunCmd(),
		newWorktreeFinishCmd(),
		newWorktreeCleanupCmd(),
		newWorktreeRenameCmd(),
	)
	rootCmd.AddCommand(wt)
}

// WorktreeEntry represents a single row in `gk worktree list --json`.
type WorktreeEntry struct {
	Path     string `json:"path"`
	Head     string `json:"head"`
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached"`
	Bare     bool   `json:"bare"`
	Locked   bool   `json:"locked"`
	Prunable bool   `json:"prunable"`
}

// worktreeAddJSON is the machine-readable result of `gk worktree add`: the
// plan under --dry-run, the outcome otherwise. Agent mode wraps it in the
// standard envelope so callers read result.path instead of scraping the
// human success line.
type worktreeAddJSON struct {
	Path     string `json:"path"`
	Branch   string `json:"branch,omitempty"`
	Parent   string `json:"parent,omitempty"`
	From     string `json:"from,omitempty"`
	Created  bool   `json:"created"`
	Detached bool   `json:"detached,omitempty"`
	Managed  bool   `json:"managed"`
	DryRun   bool   `json:"dry_run,omitempty"`
	Init     string `json:"init,omitempty"` // done | skipped (JSON mode only)
}

// worktreeListEntryJSON enriches the raw porcelain record with the same
// where-it-diverged / has-it-uncommitted-work signals the human table shows,
// so `gk worktree list --json` answers "which worktree holds unfinished
// work?" in one call instead of forcing a per-path status probe.
type worktreeListEntryJSON struct {
	Path     string            `json:"path"`
	Head     string            `json:"head,omitempty"`
	Branch   string            `json:"branch,omitempty"`
	Detached bool              `json:"detached,omitempty"`
	Bare     bool              `json:"bare,omitempty"`
	Locked   bool              `json:"locked,omitempty"`
	Prunable bool              `json:"prunable,omitempty"`
	Current  bool              `json:"current,omitempty"`
	Upstream string            `json:"upstream,omitempty"`
	Parent   string            `json:"parent,omitempty"`
	Ahead    int               `json:"ahead,omitempty"`
	Behind   int               `json:"behind,omitempty"`
	Dirty    *contextDirtyJSON `json:"dirty,omitempty"`
}

func runWorktreeList(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	entries, err := listWorktreeEntries(cmd.Context(), runner)
	if err != nil {
		return err
	}

	// Enrich entries with branchInfo (upstream, ahead/behind, fork parent,
	// last-commit age) so both the JSON document and the table mirror `gk
	// sw` columns: "where this branch came from" + "how far diverged" +
	// "when last touched". Best-effort: a git failure collapses the
	// enrichment rather than aborting the whole list.
	branchMeta := loadWorktreeBranchMeta(cmd.Context(), runner)
	currentPath := currentWorktreePath(cmd.Context(), runner)

	if JSONOut() {
		enriched := make([]worktreeListEntryJSON, 0, len(entries))
		for _, e := range entries {
			j := worktreeListEntryJSON{
				Path: e.Path, Head: e.Head, Branch: e.Branch,
				Detached: e.Detached, Bare: e.Bare, Locked: e.Locked, Prunable: e.Prunable,
				Current: currentPath != "" && filepath.Clean(e.Path) == filepath.Clean(currentPath),
			}
			if !e.Bare {
				if m, ok := branchMeta[e.Branch]; ok {
					j.Upstream, j.Ahead, j.Behind, j.Parent = m.Upstream, m.Ahead, m.Behind, m.ForkBranch
				}
				j.Dirty = worktreeDirtyAt(cmd.Context(), e.Path)
			}
			enriched = append(enriched, j)
		}
		return emitAgentResult(cmd.OutOrStdout(), enriched)
	}

	w := cmd.OutOrStdout()

	// Build the table body and accumulate per-state counts so the
	// section's title summary can carry the magnitude (n entries · m
	// detached · k locked) without forcing the user to scan the table.
	body := make([]string, 0, len(entries))
	var detached, locked, prunable int
	rows := make([]worktreeRow, 0, len(entries))
	for _, e := range entries {
		branchLabel := e.Branch
		switch {
		case e.Bare:
			branchLabel = "(bare)"
		case e.Detached:
			branchLabel = "(detached HEAD)"
			detached++
		case branchLabel == "":
			branchLabel = "-"
		}
		marks := ""
		if e.Locked {
			marks += " [locked]"
			locked++
		}
		if e.Prunable {
			marks += " [prunable]"
			prunable++
		}
		meta := branchMeta[e.Branch]
		isCurrent := currentPath != "" && filepath.Clean(e.Path) == filepath.Clean(currentPath)
		rows = append(rows, worktreeRow{
			Current: isCurrent,
			Branch:  branchLabel,
			Source:  worktreeSourceLabel(meta),
			Diff:    formatSwitchDiff(meta.Ahead, meta.Behind),
			Age:     ifZeroTime(meta.LastCommit),
			Path:    e.Path,
			Flags:   marks,
		})
	}
	body = append(body, renderWorktreeRows(rows)...)

	summary := fmt.Sprintf("%d %s", len(entries), pluralize(len(entries), "entry", "entries"))
	if detached > 0 {
		summary += fmt.Sprintf(" · %d detached", detached)
	}
	if locked > 0 {
		summary += fmt.Sprintf(" · %d locked", locked)
	}
	if prunable > 0 {
		summary += fmt.Sprintf(" · %d prunable", prunable)
	}

	fmt.Fprint(w, ui.RenderSection("worktrees", summary, body, ui.SectionOpts{
		Layout: ui.SectionLayoutBar,
		Color:  ui.SectionInfo,
	}))
	return nil
}

// worktreeBranchMeta is the slice of branchInfo we actually consume in
// the worktree-list renderer. Keeping it narrow keeps the test fixtures
// small and lets us evolve listLocalBranches independently.
type worktreeBranchMeta struct {
	Upstream   string
	Ahead      int
	Behind     int
	ForkBranch string
	ForkPoint  string
	LastCommit time.Time
}

func loadWorktreeBranchMeta(ctx context.Context, runner *git.ExecRunner) map[string]worktreeBranchMeta {
	meta, _ := loadWorktreeBranchMetaWithBase(ctx, runner)
	return meta
}

// loadWorktreeBranchMetaWithBase also hands back the trunk it resolved. The
// probe is a `symbolic-ref` subprocess, and a caller that needs both (fleet's
// poll: the meta map for every worktree, the trunk for the land-ready check)
// used to fork it a second time — twice per repo per poll, which on a
// 17-repo fleet was 34 subprocesses where 17 would do.
func loadWorktreeBranchMetaWithBase(ctx context.Context, runner *git.ExecRunner) (map[string]worktreeBranchMeta, string) {
	// Resolved before the branch listing so a listing failure still yields the
	// trunk — callers use it independently of the meta map.
	defaultBr := resolveDefaultBranchForWorktree(ctx, runner)
	branches, err := listLocalBranches(ctx, runner)
	if err != nil {
		return nil, defaultBr
	}
	// computeForkPoints needs a default-base hint. Tolerate failures: without a
	// default we lose the fork annotation but still get upstream/diff/age.
	if defaultBr != "" {
		computeForkPoints(ctx, runner, defaultBr, branches)
	}
	out := make(map[string]worktreeBranchMeta, len(branches))
	for _, b := range branches {
		out[b.Name] = worktreeBranchMeta{
			Upstream:   b.Upstream,
			Ahead:      b.Ahead,
			Behind:     b.Behind,
			ForkBranch: b.ForkBranch,
			ForkPoint:  b.ForkPoint,
			LastCommit: b.LastCommit,
		}
	}
	return out, defaultBr
}

// resolveDefaultBranchForWorktree returns the trunk used as the
// computeForkPoints anchor. We deliberately keep this lighter than
// resolveBaseForStatus (no config layer, no provenance) because the
// worktree list is a read-only at-a-glance view — a missing trunk
// only suppresses the fork column, never breaks the table.
func resolveDefaultBranchForWorktree(ctx context.Context, runner *git.ExecRunner) string {
	out, _, err := runner.Run(ctx, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err == nil {
		s := strings.TrimSpace(string(out))
		if i := strings.Index(s, "/"); i >= 0 {
			return s[i+1:]
		}
		return s
	}
	// Fallback: probe for the conventional trunk names locally.
	for _, name := range []string{"main", "master"} {
		if _, _, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); err == nil {
			return name
		}
	}
	return ""
}

// currentWorktreePath returns the absolute path of the worktree this
// invocation runs from. Used to mark the active row with a ★. Empty
// string on failure → no row gets marked, which is harmless.
func currentWorktreePath(ctx context.Context, runner *git.ExecRunner) string {
	out, _, err := runner.Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	p := strings.TrimSpace(string(out))
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// worktreeSourceLabel collapses upstream / fork-parent / local-only into
// a single SOURCE-column string mirroring `gk sw`:
//
//	"⇄ origin/main"          — upstream tracked
//	"from main@abc1234"      — no upstream, fork point known
//	"(local)"                — neither
func worktreeSourceLabel(m worktreeBranchMeta) string {
	switch {
	case m.Upstream != "":
		return "⇄ " + m.Upstream
	case m.ForkPoint != "":
		return fmt.Sprintf("from %s@%s", m.ForkBranch, m.ForkPoint)
	default:
		return ""
	}
}

// ifZeroTime renders LastCommit as a compact age, or "" when the
// branch lookup failed entirely. shortAge handles the sub-minute case
// itself ("now"); we drop that to "" so the AGE column stays quiet
// for rows we couldn't enrich.
func ifZeroTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return shortAge(t)
}

type worktreeRow struct {
	Current bool
	Branch  string
	Source  string
	Diff    string
	Age     string
	Path    string
	Flags   string
}

// renderWorktreeRows formats rows as fixed-width columns. Widths are
// computed from the actual content so columns hug their data; long
// paths are middle-ellipsised once we exceed a generous cap so the
// table doesn't blow out narrow terminals. ANSI codes are applied
// after width computation to keep alignment intact.
func renderWorktreeRows(rows []worktreeRow) []string {
	const pathCap = 60
	wBranch, wSource, wDiff, wAge := len("BRANCH"), len("SOURCE"), len("DIFF"), len("AGE")
	for _, r := range rows {
		if w := runeLen(r.Branch); w > wBranch {
			wBranch = w
		}
		if w := runeLen(r.Source); w > wSource {
			wSource = w
		}
		if w := runeLen(r.Diff); w > wDiff {
			wDiff = w
		}
		if w := runeLen(r.Age); w > wAge {
			wAge = w
		}
	}
	faint := color.New(color.Faint).SprintFunc()
	cyan := color.CyanString
	yellow := color.YellowString
	header := fmt.Sprintf("  %s  %s  %s  %s  %s%s",
		padRight("BRANCH", wBranch),
		padRight("SOURCE", wSource),
		padRight("DIFF", wDiff),
		padRight("AGE", wAge),
		"PATH",
		"",
	)
	out := make([]string, 0, len(rows)+1)
	out = append(out, faint(header))
	for _, r := range rows {
		marker := "  "
		branchCell := r.Branch
		if r.Current {
			marker = yellow("★") + " "
			branchCell = yellow(r.Branch)
		}
		sourceCell := r.Source
		switch {
		case strings.HasPrefix(sourceCell, "⇄"):
			sourceCell = cyan(sourceCell)
		case strings.HasPrefix(sourceCell, "from "):
			sourceCell = faint(sourceCell)
		}
		pathDisplay := compactPath(r.Path, pathCap)
		flags := r.Flags
		if flags != "" {
			flags = faint(flags)
		}
		line := fmt.Sprintf("%s%s  %s  %s  %s  %s%s",
			marker,
			padRightVisible(branchCell, wBranch),
			padRightVisible(sourceCell, wSource),
			padRightVisible(r.Diff, wDiff),
			padRightVisible(r.Age, wAge),
			pathDisplay,
			flags,
		)
		out = append(out, line)
	}
	return out
}

// padRight pads s with trailing spaces to reach `width` runes. Counts
// runes, not bytes, so Unicode characters don't break alignment.
func padRight(s string, width int) string {
	pad := width - runeLen(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}

// padRightVisible is padRight that ignores ANSI escape sequences when
// measuring width — so coloured cells don't shrink the visual column.
func padRightVisible(s string, width int) string {
	visible := runeLen(stripANSIForWidth(s))
	pad := width - visible
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}

func runeLen(s string) int { return len([]rune(s)) }

// compactPath ellipsises a path's *middle* segments when it overflows
// max. The leading "/" and trailing basename are always kept so the
// reader can still tell the repo + worktree name at a glance.
func compactPath(p string, max int) string {
	if runeLen(p) <= max {
		return p
	}
	base := filepath.Base(p)
	dir := filepath.Dir(p)
	// Keep ~30 chars of dir prefix, ellipsis, full basename.
	keepDir := max - runeLen(base) - 4 // 4 = "/…/" + 1 safety
	if keepDir < 8 {
		// Too narrow to be worth compacting; truncate basename instead.
		return "…" + string([]rune(p)[runeLen(p)-max+1:])
	}
	r := []rune(dir)
	return string(r[:keepDir]) + "/…/" + base
}

// parseWorktreePorcelain parses the output of `git worktree list --porcelain`.
// Records are separated by blank lines. Each record contains key/value lines:
//
//	worktree <path>
//	HEAD <sha>
//	branch refs/heads/<name>   (or: "detached" / "bare")
//	locked [reason...]
//	prunable [reason...]
//
// worktreeDiffsFromBranches projects per-branch upstream divergence
// (Ahead/Behind already populated by listLocalBranches) onto a
// worktree-entry list, keyed by branch name. Bare/detached worktrees,
// branches not in the list, and zero-diff branches are excluded so
// the caller can quickly check `_, ok := diffs[branch]`.
func worktreeDiffsFromBranches(entries []WorktreeEntry, branches []branchInfo) map[string][2]int {
	byName := make(map[string]branchInfo, len(branches))
	for _, b := range branches {
		byName[b.Name] = b
	}
	out := map[string][2]int{}
	for _, e := range entries {
		if e.Bare || e.Detached || e.Branch == "" {
			continue
		}
		b, ok := byName[e.Branch]
		if !ok {
			continue
		}
		if b.Ahead == 0 && b.Behind == 0 {
			continue
		}
		out[e.Branch] = [2]int{b.Ahead, b.Behind}
	}
	return out
}

func parseWorktreePorcelain(raw string) []WorktreeEntry {
	var out []WorktreeEntry
	var cur *WorktreeEntry
	flush := func() {
		if cur != nil && cur.Path != "" {
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			flush()
			continue
		}
		if cur == nil {
			cur = &WorktreeEntry{}
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			cur.Path = val
		case "HEAD":
			cur.Head = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "detached":
			cur.Detached = true
		case "bare":
			cur.Bare = true
		case "locked":
			cur.Locked = true
		case "prunable":
			cur.Prunable = true
		}
	}
	flush()
	return out
}

func runWorktreeAdd(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	w := cmd.OutOrStdout()
	cfg, _ := config.Load(cmd.Flags())

	rawPath := args[0]
	branch := ""
	if len(args) == 2 {
		branch = args[1]
	}
	newBranch, _ := cmd.Flags().GetBool("new")
	from, _ := cmd.Flags().GetString("from")
	detach, _ := cmd.Flags().GetBool("detach")

	if newBranch && detach {
		return fmt.Errorf("--new and --detach are mutually exclusive")
	}
	if from != "" && !newBranch {
		return fmt.Errorf("--from requires --new")
	}

	resolvedPath, err := resolveWorktreePath(ctx, runner, cfg, rawPath)
	if err != nil {
		return err
	}
	managed := resolvedPath != rawPath
	Dbg("worktree add: raw=%q resolved=%q managed=%v", rawPath, resolvedPath, managed)

	gitArgs := []string{"worktree", "add"}
	if detach {
		gitArgs = append(gitArgs, "--detach")
	}
	if newBranch {
		if branch == "" {
			return fmt.Errorf("--new requires a branch name (e.g. gk worktree add <path> <branch> -b)")
		}
		gitArgs = append(gitArgs, "-b", branch)
	}
	gitArgs = append(gitArgs, resolvedPath)

	if newBranch {
		if from != "" {
			gitArgs = append(gitArgs, from)
		}
	} else if !detach && branch != "" {
		gitArgs = append(gitArgs, branch)
	}

	// Decide the summary shape before git runs: once `worktree add`
	// creates the auto-named branch, existence no longer distinguishes
	// "created new" from "checked out existing".
	sumBranch := branch
	sumNew := newBranch
	if !detach && branch == "" {
		sumBranch = filepath.Base(resolvedPath)
		sumNew = !branchExists(ctx, runner, sumBranch)
	}

	doInit, _ := cmd.Flags().GetBool("init")
	noInit, _ := cmd.Flags().GetBool("no-init")

	// Build the machine-readable plan up front: --dry-run, --json, and the
	// human success line all describe the same intent, so compute it once.
	res := worktreeAddJSON{
		Path:     resolvedPath,
		Branch:   sumBranch,
		Created:  sumNew,
		From:     from,
		Detached: detach,
		Managed:  managed,
	}
	if sumNew && !detach {
		res.Parent = predictWorktreeParent(ctx, runner, from)
	}

	// --dry-run must not touch anything: no mkdir, no git, no init. Report
	// the plan and stop. (Pre-fix bug: add ran git regardless of --dry-run.)
	if DryRun() {
		res.DryRun = true
		if JSONOut() {
			return emitAgentResult(w, res)
		}
		fmt.Fprintf(w, "would add worktree at %s%s\n", resolvedPath,
			worktreeAddDetail(ctx, runner, sumNew, sumBranch, from, detach))
		return nil
	}

	// Only create intermediate dirs when the path was rewritten through
	// the managed layout. An absolute path is the user's responsibility.
	if managed {
		if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); err != nil {
			return fmt.Errorf("ensure worktree base: %w", err)
		}
	}

	stdout, stderr, err := runner.Run(ctx, gitArgs...)
	if err != nil {
		return fmt.Errorf("worktree add: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	if sumNew {
		recordWorktreeParent(ctx, runner, sumBranch, from)
	}

	// JSON mode: bootstrap emits human prose (link/copy/run logs) that would
	// corrupt the envelope, so run it silently and report only the outcome.
	if JSONOut() {
		if noInit {
			res.Init = "skipped"
		} else {
			if ierr := bootstrapWorktree(ctx, io.Discard, runner, cfg, resolvedPath, worktreeInitOpts{
				explicitInit: doInit,
				prompt:       false,
				fromAdd:      true,
			}); ierr != nil {
				return ierr
			}
			if doInit {
				res.Init = "done"
			} else {
				res.Init = "skipped"
			}
		}
		return emitAgentResult(w, res)
	}

	if len(stdout) > 0 {
		_, _ = w.Write(stdout)
	}
	fmt.Fprintf(w, "added worktree at %s%s\n", resolvedPath,
		worktreeAddDetail(ctx, runner, sumNew, sumBranch, from, detach))

	if noInit {
		return nil
	}
	return bootstrapWorktree(ctx, w, runner, cfg, resolvedPath, worktreeInitOpts{
		explicitInit: doInit,
		prompt:       !doInit,
		fromAdd:      true,
	})
}

// recordWorktreeParent best-effort writes `branch.<branch>.gk-parent`
// after `worktree add` created <branch> — creation is the only moment
// the fork parent is known for certain (git itself records only
// "Created from HEAD" in the reflog), so this is where parent-aware
// surfaces (SOURCE column, gk status, gk land --promote) get their
// anchor without a manual `gk branch set-parent`.
//
// The parent is the explicit --from when it names a local branch,
// otherwise the invoking worktree's current branch. Detached HEAD,
// remote-tracking refs, raw SHAs, and anything else ValidateSet rejects
// record nothing. Failures are silent: metadata is decoration, never a
// reason to fail the add. Undo with `gk branch unset-parent`.
func recordWorktreeParent(ctx context.Context, runner git.Runner, branch, from string) {
	parent := predictWorktreeParent(ctx, runner, from)
	if parent == "" {
		return
	}
	client := git.NewClient(runner)
	if branchparent.ValidateSet(ctx, client, branch, parent) != nil {
		return
	}
	_ = branchparent.NewConfig(client).SetParent(ctx, branch, parent)
}

// predictWorktreeParent computes the gk-parent recordWorktreeParent would
// write — the explicit --from (minus refs/heads/) or, absent that, the
// invoking worktree's current branch — without persisting it. It feeds the
// --dry-run / --json preview before git runs. Best-effort: returns "" when
// no parent can be determined, and skips the ValidateSet check the real
// write applies, so a previewed parent may still be dropped at write time.
func predictWorktreeParent(ctx context.Context, runner git.Runner, from string) string {
	parent := strings.TrimPrefix(from, "refs/heads/")
	if parent != "" {
		return parent
	}
	cur, err := git.NewClient(runner).CurrentBranch(ctx)
	if err != nil {
		return ""
	}
	return cur
}

// worktreeAddDetail names the branch that landed in the new worktree
// and, for a newly created branch, the ref it was cut from — `git
// worktree add` bases new branches on HEAD, which is invisible in the
// bare success line and routinely misread as "from main". Best-effort:
// any rev-parse failure returns "" so the success message never blocks
// on decoration.
func worktreeAddDetail(ctx context.Context, runner git.Runner, newBranch bool, branch, from string, detach bool) string {
	short := func(ref string) string {
		out, _, err := runner.Run(ctx, "rev-parse", "--short", ref)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	switch {
	case detach:
		if sha := short("HEAD"); sha != "" {
			return fmt.Sprintf(" (detached @%s)", sha)
		}
	case newBranch:
		base := from
		if base == "" {
			out, _, err := runner.Run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
			if err != nil {
				return ""
			}
			base = strings.TrimSpace(string(out))
		}
		if sha := short(base); sha != "" {
			return fmt.Sprintf(" (new branch %s from %s@%s)", branch, base, sha)
		}
	default:
		if sha := short(branch); sha != "" {
			return fmt.Sprintf(" (%s@%s)", branch, sha)
		}
	}
	return ""
}

// resolveWorktreePath expands a worktree path argument into a concrete
// filesystem path using gk's managed layout:
//
//  1. absolute path → use as-is (user intent is explicit)
//  2. relative path → <base>/<project>/<input>, where:
//     base    = cfg.Worktree.Base (default "~/.gk/worktree"), ~ expanded
//     project = cfg.Worktree.Project (explicit), else basename of the
//     git toplevel directory for the current repo
//
// When the managed base cannot be resolved (empty config, no home dir,
// no git toplevel) the raw input is returned so git falls back to
// placing the worktree relative to the caller's cwd — matching the
// pre-v0.9 behaviour of `gk worktree add`.
func resolveWorktreePath(ctx context.Context, runner git.Runner, cfg *config.Config, input string) (string, error) {
	if filepath.IsAbs(input) {
		return input, nil
	}
	if cfg == nil || cfg.Worktree.Base == "" {
		return input, nil
	}
	base := expandHome(cfg.Worktree.Base)
	if base == "" {
		return input, nil
	}

	project := cfg.Worktree.Project
	if project == "" {
		slug, derr := deriveWorktreeProjectSlug(ctx, runner)
		if derr != nil || slug == "" {
			// No toplevel? Fall back to cwd behavior rather than
			// surprising the user with a failed mkdir.
			return input, nil
		}
		project = slug
	}
	if strings.ContainsAny(project, "/\\") || strings.Contains(project, "..") {
		return "", fmt.Errorf("invalid worktree.project %q: must not contain path separators or '..'", project)
	}
	return filepath.Join(base, project, input), nil
}

// deriveWorktreeProjectSlug returns basename(toplevel) so two clones at
// /Users/me/work/gk and /Users/me/personal/gk still share the layout
// unless `worktree.project` is set explicitly.
func deriveWorktreeProjectSlug(ctx context.Context, runner git.Runner) (string, error) {
	out, _, err := runner.Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", fmt.Errorf("empty toplevel")
	}
	return filepath.Base(top), nil
}

func runWorktreeRemove(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	w := cmd.OutOrStdout()
	force, _ := cmd.Flags().GetBool("force")
	forceLocked, _ := cmd.Flags().GetBool("force-locked")
	path := args[0]

	// A locked worktree needs special handling: a single `--force` does
	// NOT clear a lock (git wants `-f -f`). Decide by whether the lock
	// holder is still running — stale locks unlock under --force, live
	// ones require the explicit --force-locked to avoid yanking a worktree
	// out from under an active process (e.g. a running claude agent).
	if lock := worktreeLockInfo(ctx, runner, path); lock.Locked {
		if err := checkWorktreeLockGate(lock, force, forceLocked); err != nil {
			return err
		}
		return forceRemoveWorktree(ctx, runner, w, path)
	}

	gitArgs := []string{"worktree", "remove"}
	if force {
		gitArgs = append(gitArgs, "--force")
	}
	gitArgs = append(gitArgs, path)
	if _, stderr, err := runner.Run(ctx, gitArgs...); err != nil {
		return fmt.Errorf("worktree remove: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	fmt.Fprintf(w, "removed worktree %s\n", path)
	return nil
}

func runWorktreePrune(cmd *cobra.Command, args []string) error {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	stdout, stderr, err := runner.Run(cmd.Context(), "worktree", "prune", "-v")
	if err != nil {
		return fmt.Errorf("worktree prune: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	if len(stdout) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "nothing to prune")
		return nil
	}
	_, _ = cmd.OutOrStdout().Write(stdout)
	return nil
}

// runWorktreeTUI is the handler for bare `gk wt` / `gk worktree`. It
// drives a REPL-style loop over the worktree list and dispatches to
// add/remove/cd actions. All interactive rendering is kept on stderr so
// a caller can safely wrap the command in `$(gk wt)` and capture the
// chosen path for a cd alias.
func runWorktreeTUI(cmd *cobra.Command, args []string) error {
	if !promptAllowed() {
		return cmd.Help()
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	cfg, _ := config.Load(cmd.Flags())
	stderr := cmd.ErrOrStderr()

	startGlobal, _ := cmd.Flags().GetBool("global")

	// Menu action sentinels. Using clearly reserved keys avoids any
	// collision with a real worktree path a user might pick.
	const (
		keyAddNew = "__gk_add_new__"
		keyQuit   = "__gk_quit__"
	)

	bold := color.New(color.Bold).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()

	// Toggleable state shared between the picker call and the 'g'
	// extra-key callback: which entries are currently shown, and what
	// project they belong to (only used in global mode).
	global := startGlobal

	type rowSource struct {
		entries []WorktreeEntry
		// projectByPath surfaces the owning project slug for the global
		// mode rendering. Empty in local mode.
		projectByPath map[string]string
	}

	loadRows := func() (rowSource, error) {
		if global {
			gws, gErr := listGlobalWorktrees(ctx, cfg)
			if gErr != nil {
				return rowSource{}, gErr
			}
			rs := rowSource{
				entries:       make([]WorktreeEntry, 0, len(gws)),
				projectByPath: make(map[string]string, len(gws)),
			}
			for _, gw := range gws {
				rs.entries = append(rs.entries, gw.Entry)
				rs.projectByPath[gw.Entry.Path] = gw.Project
			}
			return rs, nil
		}
		ents, lErr := listWorktreesForTUI(ctx, runner)
		if lErr != nil {
			return rowSource{}, lErr
		}
		return rowSource{entries: ents}, nil
	}

	// loadBranchMeta reads upstream / ahead-behind / fork-parent for the
	// current repo's branches in one shot. Skipped in global mode
	// (cross-repo iteration is out of scope) — global rows still show
	// the branch but lose the SOURCE/DIFF columns. nil on failure.
	loadBranchMeta := func() map[string]worktreeBranchMeta {
		if global {
			return nil
		}
		return loadWorktreeBranchMeta(ctx, runner)
	}

	buildItems := func(rs rowSource) (items []ui.PickerItem, headers []string) {
		meta := loadBranchMeta()
		appendDiff := func(branch string) string {
			m, ok := meta[branch]
			if !ok {
				return branch
			}
			suffix := colorSwitchDiff(m.Ahead, m.Behind)
			if suffix == "" {
				return branch
			}
			return branch + "  " + suffix
		}
		sourceFor := func(branch string) string {
			m, ok := meta[branch]
			if !ok {
				return ""
			}
			return worktreeSourceLabel(m)
		}
		// ageFor reads the branch tip's commit age from the bulk-loaded meta
		// (empty for detached worktrees and in global mode, where cross-project
		// commit times aren't resolvable from this repo). hashFor takes the
		// worktree's actual HEAD sha straight off the porcelain entry, so it
		// works for detached and bare worktrees too.
		ageFor := func(branch string) string {
			return ifZeroTime(meta[branch].LastCommit)
		}
		hashFor := func(e WorktreeEntry) string {
			return shortSHA(e.Head)
		}
		if global {
			// No AGE column here: across projects the branch meta isn't loaded
			// (commit times aren't resolvable from this repo), so the column
			// would be uniformly empty — worse than absent. HASH still shows,
			// straight off each entry's HEAD.
			headers = []string{"PROJECT", "BRANCH", "HASH", "PATH", "FLAGS"}
			items = make([]ui.PickerItem, 0, len(rs.entries)+1)
			for _, e := range rs.entries {
				branch, flagsPlain := worktreeRowPartsPlain(e)
				items = append(items, ui.PickerItem{
					Display: worktreeTUILabel(e, bold, faint),
					Cells:   []string{rs.projectByPath[e.Path], appendDiff(branch), hashFor(e), e.Path, flagsPlain},
					Key:     e.Path,
				})
			}
			// global mode hides [+] add — adds belong to the current
			// repo and would surprise the user when scrolling other
			// projects' worktrees.
			items = append(items, ui.PickerItem{
				Display: faint("[q] quit"),
				Cells:   []string{"", "[q] quit", "", ""},
				Key:     keyQuit,
			})
			return items, headers
		}
		headers = []string{"BRANCH", "SOURCE", "HASH", "AGE", "PATH", "FLAGS"}
		items = make([]ui.PickerItem, 0, len(rs.entries)+2)
		for _, e := range rs.entries {
			branch, flagsPlain := worktreeRowPartsPlain(e)
			source := sourceFor(e.Branch)
			// Inside picker cells we must use cellCyan/cellFaint, not
			// fatih color helpers, because fatih emits `\x1b[0m` (full
			// reset) which clobbers bubbles/table's cursor-row purple
			// background mid-cell — leaving a torn highlight bar plus
			// poor contrast on the active row. The cell* helpers reset
			// only the foreground / bold bit they set.
			switch {
			case strings.HasPrefix(source, "⇄"):
				source = cellCyan(source)
			case strings.HasPrefix(source, "from "):
				source = cellFaint(source)
			}
			items = append(items, ui.PickerItem{
				Display: worktreeTUILabel(e, bold, faint),
				Cells:   []string{appendDiff(branch), source, hashFor(e), ageFor(e.Branch), e.Path, flagsPlain},
				Key:     e.Path,
			})
		}
		items = append(items,
			ui.PickerItem{
				Display: faint("[+] add new worktree"),
				Cells:   []string{"[+] add new worktree", "", "", ""},
				Key:     keyAddNew,
			},
			ui.PickerItem{
				Display: faint("[q] quit"),
				Cells:   []string{"[q] quit", "", "", ""},
				Key:     keyQuit,
			},
		)
		return items, headers
	}

	for {
		rs, err := loadRows()
		if err != nil {
			return err
		}

		items, headers := buildItems(rs)
		picker := &ui.TablePicker{
			Headers:        headers,
			ColumnPriority: worktreeColumnPriority(),
			Extras: []ui.TablePickerExtraKey{{
				Key:  "g",
				Help: "g toggle global",
				OnPress: func() ([]ui.PickerItem, []string, error) {
					global = !global
					rs2, gErr := loadRows()
					if gErr != nil {
						return nil, nil, gErr
					}
					rs = rs2
					its, hdrs := buildItems(rs2)
					return its, hdrs, nil
				},
			}},
		}
		picked, err := picker.Pick(ctx, "worktree", items)
		if err != nil {
			if errors.Is(err, ui.ErrPickerAborted) {
				return nil
			}
			return err
		}

		switch picked.Key {
		case keyQuit:
			return nil
		case keyAddNew:
			if err := worktreeTUIAdd(ctx, runner, cfg); err != nil {
				fmt.Fprintf(stderr, "%s %v\n", color.RedString("error:"), err)
			}
		default:
			// Chosen worktree — open the action submenu. Look up the
			// full entry so downstream actions (remove + branch drop)
			// see branch/locked/prunable state, not just the path.
			entry := findWorktreeEntry(rs.entries, picked.Key)
			if entry == nil {
				continue
			}
			done, err := worktreeTUIActOnEntry(ctx, runner, cmd, *entry, cfgProtected(cfg))
			if err != nil {
				fmt.Fprintf(stderr, "%s %v\n", color.RedString("error:"), err)
			}
			if done {
				return nil
			}
		}
	}
}

// globalWorktree pairs a parsed WorktreeEntry with the gk project slug
// it belongs to, so the global-mode picker can prefix the row.
type globalWorktree struct {
	Project string
	Entry   WorktreeEntry
}

// listGlobalWorktrees scans the gk-managed base directory (default
// ~/.gk/worktree) and returns one row per real git worktree found.
// Implementation:
//  1. read first-level dirs (= project slugs)
//  2. for each project, run `git -C <first-wt> worktree list --porcelain`
//     once and keep the entries whose paths fall under the project dir.
//
// This is one git invocation per project (not per worktree), so a few
// hundred worktrees still load in well under a second.
func listGlobalWorktrees(ctx context.Context, cfg *config.Config) ([]globalWorktree, error) {
	if cfg == nil {
		return nil, nil
	}
	base := expandHome(cfg.Worktree.Base)
	if base == "" {
		base = expandHome("~/.gk/worktree")
	}
	projects, err := os.ReadDir(base)
	if err != nil {
		// Missing base is normal on a fresh install — return an empty
		// list rather than failing the picker.
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan worktree base %q: %w", base, err)
	}

	var out []globalWorktree
	for _, proj := range projects {
		if !proj.IsDir() {
			continue
		}
		projectDir := filepath.Join(base, proj.Name())
		wts, _ := os.ReadDir(projectDir)
		var firstWT string
		for _, wt := range wts {
			if wt.IsDir() {
				firstWT = filepath.Join(projectDir, wt.Name())
				break
			}
		}
		if firstWT == "" {
			continue
		}
		r := &git.ExecRunner{Dir: firstWT}
		stdout, _, lErr := r.Run(ctx, "worktree", "list", "--porcelain")
		if lErr != nil {
			// Not a real git worktree (or stale), skip silently.
			continue
		}
		for _, e := range parseWorktreePorcelain(string(stdout)) {
			if !strings.HasPrefix(e.Path, projectDir+string(os.PathSeparator)) && e.Path != projectDir {
				continue
			}
			out = append(out, globalWorktree{Project: proj.Name(), Entry: e})
		}
	}
	return out, nil
}

// findWorktreeEntry locates the entry whose Path matches path, returning
// nil when not found (stale menu selection vs. concurrent change).
func findWorktreeEntry(entries []WorktreeEntry, path string) *WorktreeEntry {
	for i := range entries {
		if entries[i].Path == path {
			return &entries[i]
		}
	}
	return nil
}

// listWorktreesForTUI returns the current worktrees parsed from
// `git worktree list --porcelain`. Shared with the non-interactive
// `gk worktree list` command so both see identical data.
func listWorktreesForTUI(ctx context.Context, runner *git.ExecRunner) ([]WorktreeEntry, error) {
	stdout, stderr, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("worktree list: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return parseWorktreePorcelain(string(stdout)), nil
}

// worktreeTUILabel formats one row for the picker. The branch (or
// "(detached)"/"(bare)") is bolded so it scans first; path is faint.
// Flags like [locked] / [prunable] append as red-on-default suffixes.
// Used as the single-column Display fallback when the active picker
// can't render Cells (FallbackPicker).
func worktreeTUILabel(e WorktreeEntry, bold, faint func(a ...interface{}) string) string {
	branch, flags := worktreeRowParts(e)
	tail := ""
	if flags != "" {
		tail = "  " + flags
	}
	return fmt.Sprintf("%-20s  %s%s", bold(branch), faint(e.Path), tail)
}

// worktreeRowParts splits a WorktreeEntry into the branch label and the
// (ANSI-coloured) flag suffix used by the legacy single-column label
// (FallbackPicker fallback path).
func worktreeRowParts(e WorktreeEntry) (branch, flags string) {
	branch, _ = worktreeRowPartsPlain(e)
	var parts []string
	if e.Locked {
		parts = append(parts, color.RedString("[locked]"))
	}
	if e.Prunable {
		parts = append(parts, color.RedString("[prunable]"))
	}
	flags = strings.Join(parts, " ")
	return branch, flags
}

// worktreeColumnPriority maps the worktree picker's column titles to
// keep-weights for the responsive layout (see ui.TablePicker.ColumnPriority).
// BRANCH is the identity column and survives; PROJECT (global view) ranks just
// under it; AGE is the short, glanceable signal kept next, then PATH (where the
// worktree lives), SOURCE, FLAGS, and finally HASH — a bare SHA — drops first.
// Keyed by title so the one map serves both the local and global layouts, even
// across a `g` toggle that reorders the columns.
func worktreeColumnPriority() map[string]int {
	return map[string]int{
		"BRANCH":  100,
		"PROJECT": 90,
		"AGE":     80,
		"PATH":    60,
		"SOURCE":  40,
		"FLAGS":   30,
		"HASH":    10,
	}
}

// worktreeRowPartsPlain returns the branch label and the flag suffix
// without ANSI styling. TablePicker cells use this so width-based
// truncation (runewidth.Truncate) measures visible characters only.
func worktreeRowPartsPlain(e WorktreeEntry) (branch, flags string) {
	switch {
	case e.Bare:
		branch = "(bare)"
	case e.Detached:
		branch = "(detached)"
	case e.Branch != "":
		branch = e.Branch
	default:
		branch = "-"
	}
	var parts []string
	if e.Locked {
		parts = append(parts, "[locked]")
	}
	if e.Prunable {
		parts = append(parts, "[prunable]")
	}
	flags = strings.Join(parts, " ")
	return branch, flags
}

// worktreeTUIActOnEntry is the secondary picker: what to do with the
// worktree the user just selected. Returns (done, err) — done=true
// means the outer loop should exit (e.g. user picked "cd").
func worktreeTUIActOnEntry(ctx context.Context, runner *git.ExecRunner, cmd *cobra.Command, entry WorktreeEntry, protected []string) (bool, error) {
	const (
		actCD     = "cd"
		actRemove = "remove"
		actCancel = "cancel"
	)
	printPath, _ := cmd.Flags().GetBool("print-path")

	faint := color.New(color.Faint).SprintFunc()
	cdHint := faint("(enter a subshell in this worktree)")
	if printPath {
		cdHint = faint("(print path to stdout for shell alias)")
	}
	items := []ui.PickerItem{
		{Display: fmt.Sprintf("cd  %s", cdHint), Key: actCD},
		{Display: "remove", Key: actRemove},
		{Display: "cancel", Key: actCancel},
	}
	picked, err := ui.NewPicker().Pick(ctx, entry.Path, items)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return false, nil
		}
		return false, err
	}

	switch picked.Key {
	case actCD:
		if printPath {
			// Scripting path: `cd $(gk wt --print-path)`. All menu
			// rendering has been on stderr so stdout stays clean.
			fmt.Fprintln(cmd.OutOrStdout(), entry.Path)
			return true, nil
		}
		return true, enterWorktreeSubshell(cmd, entry.Path)
	case actRemove:
		return false, worktreeTUIRemove(ctx, runner, cmd.ErrOrStderr(), entry, protected)
	default: // cancel
		return false, nil
	}
}

// enterWorktreeSubshell launches $SHELL inside the worktree path and
// blocks until the user exits it. stdin/stdout/stderr are inherited so
// the subshell is fully interactive. On exit the caller's shell is
// still at its original cwd — this is the standard "tool shell" pattern
// used by nix-shell, poetry shell, etc.
//
// We expose the original PWD via GK_WT_PARENT_PWD so an advanced user
// can `cd "$GK_WT_PARENT_PWD"` from within the subshell if they want
// to peek back at the outer tree without exiting.
func enterWorktreeSubshell(cmd *cobra.Command, path string) error {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	origPWD, _ := os.Getwd()

	bold := color.New(color.Bold).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()
	fmt.Fprintf(cmd.ErrOrStderr(), "%s %s  %s\n",
		bold("▸ entered"), path, faint("(type `exit` to return)"))

	sub := exec.Command(sh)
	sub.Dir = path
	sub.Stdin = os.Stdin
	sub.Stdout = os.Stdout
	sub.Stderr = os.Stderr
	sub.Env = append(os.Environ(),
		"GK_WT_PARENT_PWD="+origPWD,
		"GK_WT="+path,
	)
	// A non-zero exit from the user's shell (Ctrl+D after a failing
	// last command, e.g.) is not our failure. Swallow *ExitError so gk
	// returns 0 when the subshell closes cleanly from the user's POV.
	if err := sub.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil
		}
		return fmt.Errorf("enter subshell: %w", err)
	}
	return nil
}

// worktreeTUIRemove removes a worktree, handling the common failure
// modes git surfaces as a terse one-line error:
//
//   - "is dirty" / "contains modified or untracked files" → offer --force
//   - "is locked" → offer --force (bypasses the lock)
//   - "is a main working tree" → refuse up front; git would anyway
//   - "not a working tree" / stale admin entry → auto-run `worktree prune`
//
// After a successful removal we also offer to delete the branch that
// was checked out, but only when no other worktree still owns it.
// Confirm defaults to yes because the user already picked "remove" in
// the action menu — a second No-by-default prompt feels redundant.
//
// runner/w are interface-typed (not *ExecRunner/*cobra.Command) so the
// flow is reusable outside the worktree TUI — `gk switch`'s d/D action
// redirects here when the targeted branch lives in another worktree.
// `protected` is forwarded to the orphan-branch offer so a protected branch
// is never casually -D'd; it is the caller's repo-correct list.
func worktreeTUIRemove(ctx context.Context, runner git.Runner, w io.Writer, entry WorktreeEntry, protected []string) error {
	stderr := w

	if entry.Bare {
		return fmt.Errorf("cannot remove the bare/main worktree: %s", entry.Path)
	}

	ok, err := ui.Confirm(fmt.Sprintf("remove %s?", entry.Path), true)
	if err != nil || !ok {
		return nil
	}

	// Check the lock state up front: git won't clear a lock with a single
	// --force, and a live lock holder means the worktree is in active use.
	if lock := worktreeLockInfo(ctx, runner, entry.Path); lock.Locked {
		if lock.Alive {
			fmt.Fprintf(stderr, "locked and still in use: %s\n", lock.Reason)
			force, _ := ui.Confirm("the lock holder is still running — force-remove anyway?", false)
			if !force {
				return nil
			}
		} else {
			fmt.Fprintf(stderr, "stale lock (holder no longer running): %s\n", lock.Reason)
			ok, _ := ui.Confirm("unlock and remove?", true)
			if !ok {
				return nil
			}
		}
		if err := forceRemoveWorktree(ctx, runner, stderr, entry.Path); err != nil {
			return err
		}
		return maybeDeleteOrphanBranch(ctx, runner, stderr, entry, protected)
	}

	// First try: plain remove.
	_, rerr, err := runner.Run(ctx, "worktree", "remove", entry.Path)
	if err != nil {
		msg := strings.ToLower(strings.TrimSpace(string(rerr)) + " " + err.Error())
		switch {
		case strings.Contains(msg, "dirty") ||
			strings.Contains(msg, "contains modified") ||
			strings.Contains(msg, "contains untracked"):
			// Dirty — surface the exact git message, then ask whether to
			// force (a single --force is enough for dirty/untracked).
			fmt.Fprintln(stderr, strings.TrimSpace(string(rerr)))
			force, _ := ui.Confirm("force-remove anyway?", false)
			if !force {
				return nil
			}
			if _, rerr2, err := runner.Run(ctx, "worktree", "remove", "--force", entry.Path); err != nil {
				return fmt.Errorf("worktree remove --force: %s: %w", strings.TrimSpace(string(rerr2)), err)
			}
		case strings.Contains(msg, "not a working tree") ||
			strings.Contains(msg, "is not a working tree") ||
			strings.Contains(msg, "already deleted"):
			// The on-disk path is gone or never was — git's admin
			// record is stale. Prune and treat as success.
			fmt.Fprintln(stderr, "worktree entry is stale — pruning admin records")
			if _, perr, err := runner.Run(ctx, "worktree", "prune", "-v"); err != nil {
				return fmt.Errorf("worktree prune: %s: %w", strings.TrimSpace(string(perr)), err)
			}
		default:
			return fmt.Errorf("worktree remove: %s: %w", strings.TrimSpace(string(rerr)), err)
		}
	}
	fmt.Fprintf(stderr, "removed %s\n", entry.Path)
	return maybeDeleteOrphanBranch(ctx, runner, stderr, entry, protected)
}

// maybeDeleteOrphanBranch offers to delete the branch a just-removed
// worktree had checked out. Guardrails: only when the branch name is
// known, it isn't detached, no other worktree still owns it, and it
// isn't a protected branch (main/master/develop by default) — removing a
// worktree must never become a casual `branch -D` of trunk that discards
// unmerged work. Intentional protected deletion stays on `gk branch`.
//
// `protected` is the caller's already-resolved protected list (which
// honors --repo and includes the built-in defaults); resolving it here via
// config.Load(nil) would read the cwd's config, not the target repo's.
func maybeDeleteOrphanBranch(ctx context.Context, runner git.Runner, stderr io.Writer, entry WorktreeEntry, protected []string) error {
	if entry.Branch == "" || entry.Detached {
		return nil
	}
	if branchInUse(ctx, runner, entry.Branch) {
		return nil
	}
	if isProtectedBranchName(entry.Branch, protected) {
		return nil
	}
	drop, _ := ui.Confirm(fmt.Sprintf("also delete branch %q?", entry.Branch), false)
	if !drop {
		return nil
	}
	if _, berr, err := runner.Run(ctx, "branch", "-D", entry.Branch); err != nil {
		fmt.Fprintf(stderr, "warn: branch -D %s: %s\n", entry.Branch, strings.TrimSpace(string(berr)))
		return nil
	}
	fmt.Fprintf(stderr, "deleted branch %s\n", entry.Branch)
	return nil
}

// worktreeTUIAdd collects a worktree name + branch choice from the user
// and dispatches to git worktree add via the managed-path resolver.
// addModel renders on stderr so stdout stays reserved for the `cd`
// action in the outer loop.
func worktreeTUIAdd(ctx context.Context, runner *git.ExecRunner, cfg *config.Config) error {
	// Resolve the current branch so the form can label its default base
	// ("blank = <branch>") instead of an opaque HEAD. Detached or failed
	// lookup → "" → the form falls back to the literal "HEAD".
	head := ""
	if br, berr := git.NewClient(runner).CurrentBranch(ctx); berr == nil {
		head = br
	}
	inputs, err := runWorktreeAddTUI(ctx, head)
	if err != nil {
		if errors.Is(err, errAddCancelled) {
			return nil
		}
		return err
	}
	name := inputs.Name
	createBranch := inputs.CreateBranch
	branchName := inputs.BranchName
	fromRef := inputs.FromRef

	resolved, err := resolveWorktreePath(ctx, runner, cfg, name)
	if err != nil {
		return err
	}
	if resolved != name {
		if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
			return fmt.Errorf("ensure worktree base: %w", err)
		}
	}

	// Pre-flight: refuse up-front when the target path is non-empty or
	// the requested new branch already exists. Without this, a
	// `git worktree add -b <br> <path>` that aborts at the path-create
	// step still creates `<br>` and leaves it orphaned — next retry
	// fails with "branch already exists" and the user sees a half-
	// completed state.
	if exists, err := nonEmptyDirExists(resolved); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("target path already exists and is non-empty: %s", resolved)
	}
	if createBranch && branchExists(ctx, runner, branchName) {
		// Conflict resolution: creating a branch that already exists
		// would fail deep inside `git worktree add -b`. Handle it here
		// so the user has real choices instead of a dead-end error.
		// A branch already in use by another worktree cannot be either
		// re-used (git refuses) or silently deleted (would strand the
		// other worktree), so we bail out cleanly in that case.
		if branchInUse(ctx, runner, branchName) {
			return fmt.Errorf("branch %q is checked out in another worktree — pick a different name or remove that worktree first", branchName)
		}
		resolution, err := promptOrphanBranchResolution(branchName, orphanBranchTip(ctx, runner, branchName))
		if err != nil {
			return err
		}
		switch resolution {
		case orphanReuse:
			// Switch modes: check out the existing branch instead of
			// recreating it. The git command below already handles
			// `worktree add <path> <branch>` when createBranch=false.
			createBranch = false
		case orphanDelete:
			if _, berr, err := runner.Run(ctx, "branch", "-D", branchName); err != nil {
				return fmt.Errorf("branch -D %s: %s: %w", branchName, strings.TrimSpace(string(berr)), err)
			}
		case orphanCancel:
			return nil
		}
	}

	gitArgs := []string{"worktree", "add"}
	if createBranch {
		gitArgs = append(gitArgs, "-b", branchName, resolved)
		if fromRef != "" {
			gitArgs = append(gitArgs, fromRef)
		}
	} else {
		gitArgs = append(gitArgs, resolved, branchName)
	}
	if _, gitErr, err := runner.Run(ctx, gitArgs...); err != nil {
		// Defensive rollback: even with the pre-flight above, a race or
		// an unexpected git failure can still leave a new branch behind.
		// If we asked git to create the branch and it was left dangling
		// (no worktree entry points at it), remove it so the user's
		// next attempt is not blocked by a phantom branch.
		if createBranch && branchExists(ctx, runner, branchName) && !branchInUse(ctx, runner, branchName) {
			_, _, _ = runner.Run(ctx, "branch", "-D", branchName)
		}
		return fmt.Errorf("worktree add: %s: %w", strings.TrimSpace(string(gitErr)), err)
	}
	if createBranch {
		recordWorktreeParent(ctx, runner, branchName, fromRef)
	}
	// Surface the resolved path on stderr so the user sees where it
	// landed without polluting stdout.
	fmt.Fprintf(os.Stderr, "added worktree at %s%s\n", resolved,
		worktreeAddDetail(ctx, runner, createBranch, branchName, fromRef, false))
	return nil
}

// orphanResolution enumerates the choices presented when the user asks
// for a new branch whose name already exists as an orphan (branch
// present, not checked out in any worktree — typically left behind by a
// previous failed `worktree add`).
type orphanResolution int

const (
	orphanCancel orphanResolution = iota
	orphanReuse
	orphanDelete
)

// promptOrphanBranchResolution asks the user what to do with an orphan
// branch that collides with their requested new name. The tip preview
// (e.g. "a12bc3f4 fix: something · 2h") helps decide whether it is safe
// to delete. On a non-TTY session this returns orphanCancel so callers
// surface a clear error rather than guessing.
func promptOrphanBranchResolution(name, tip string) (orphanResolution, error) {
	if !promptAllowed() {
		return orphanCancel, fmt.Errorf("branch %q already exists (orphan) — re-run interactively to resolve, or delete with `git branch -D %s`", name, name)
	}
	title := fmt.Sprintf("branch %q already exists (orphan — no worktree uses it)", name)
	desc := tip
	if desc == "" {
		desc = "choose how to proceed"
	}
	items := []ui.PickerItem{
		{Key: "reuse", Display: "check out the existing branch in the new worktree"},
		{Key: "delete", Display: fmt.Sprintf("delete %q and create a fresh branch", name)},
		{Key: "cancel", Display: "cancel"},
	}
	picker := &ui.TablePicker{Headers: []string{title + " — " + desc}}
	choice, err := picker.Pick(context.Background(), "orphan branch", items)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return orphanCancel, nil
		}
		return orphanCancel, err
	}
	switch choice.Key {
	case "reuse":
		return orphanReuse, nil
	case "delete":
		return orphanDelete, nil
	default:
		return orphanCancel, nil
	}
}

// orphanBranchTip returns a single-line preview of the branch's tip
// commit ("a12bc3f  fix: X  · 2h") for the orphan-branch prompt.
// Silent empty-string on failure — the prompt still works, just without
// the age cue.
func orphanBranchTip(ctx context.Context, runner git.Runner, branch string) string {
	out, _, err := runner.Run(ctx, "log", "-1",
		"--format=%h\x1f%s\x1f%ar",
		"refs/heads/"+branch,
	)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(strings.TrimRight(string(out), "\n"), "\x1f", 3)
	if len(parts) != 3 {
		return ""
	}
	return fmt.Sprintf("tip: %s  %s  · %s", parts[0], parts[1], parts[2])
}

// nonEmptyDirExists reports whether path is a directory with at least
// one entry. Returns false when the path does not exist. Used to avoid
// the confusing half-succeeded worktree add — git creates the branch
// before checking the destination, so colliding paths orphan branches.
func nonEmptyDirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return true, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return len(entries) > 0, nil
}

// branchExists reports whether refs/heads/<name> resolves. Uses
// show-ref --verify --quiet so the signal is a pure exit code without
// stderr noise that would otherwise leak into the TUI output.
func branchExists(ctx context.Context, runner git.Runner, name string) bool {
	_, _, err := runner.Run(ctx, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

// branchInUse reports whether any existing worktree currently has the
// named branch checked out. This guards the rollback step: if a parallel
// invocation already owns the branch, we must not delete it.
func branchInUse(ctx context.Context, runner git.Runner, name string) bool {
	out, _, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	needle := "branch refs/heads/" + name
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == needle {
			return true
		}
	}
	return false
}
