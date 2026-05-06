package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/forget"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "forget [path...]",
		Short: "Remove paths from the entire git history (rewrites SHAs)",
		Long: `Remove one or more paths from every commit in the repository, including
historical revisions, by delegating to git filter-repo.

When called with no paths, gk forget auto-detects tracked files that are
already covered by .gitignore — turning the common workflow

  echo db/ >> .gitignore
  gk forget

into a one-line cleanup for files that were committed before the
.gitignore rule existed.

DESTRUCTIVE — every commit SHA changes. After the rewrite finishes,
collaborators must re-clone or hard-reset to the new history. A backup
is written to refs/gk/forget-backup/<branch>/<unix> (visible in
gk timemachine list) plus a text manifest at .git/gk/forget-backup-<unix>.txt;
either one is enough to roll back.

Dirty working tree handling: changes inside the forget target paths are
fine — filter-repo would erase them anyway. Changes outside the targets
abort with a hint, since filter-repo's reset would silently drop them.
Pass --force-dirty when you have reviewed those outside changes and
accept losing them.

Requires git filter-repo (https://github.com/newren/git-filter-repo).
Install: brew install git-filter-repo  (or: pip install git-filter-repo)

Examples:
  gk forget                              # auto-detect tracked-but-ignored paths
  gk forget db/ secrets.json             # explicit path list
  gk forget --dry-run db/                # preview without rewriting
  gk forget --yes db/                    # skip the confirmation prompt
  gk forget --analyze db/                # exact reclaim estimate for db/
  gk forget --analyze                    # repo-wide audit (heaviest top-level dirs)
  gk forget --analyze --depth 2          # repo-wide audit, two-segment buckets
  gk forget --analyze --depth 0 --top 50 # 50 heaviest individual files
  gk forget db/ --keep "db/keep/*"       # forget db/ but keep db/keep/ entries
`,
		RunE: runForget,
	}
	cmd.Flags().BoolP("yes", "y", false, "skip the interactive confirmation prompt")
	cmd.Flags().Bool("analyze", false, "report unique blob count and total bytes per target without rewriting (implies --dry-run)")
	cmd.Flags().StringSlice("keep", nil, "filepath.Match pattern to exclude from the forget set (repeatable)")
	cmd.Flags().Bool("force-dirty", false, "proceed even when working-tree changes exist outside the forget targets (those changes will be reset by filter-repo)")
	cmd.Flags().Int("top", 20, "with --analyze and no targets, limit repo-wide audit to the N heaviest buckets")
	cmd.Flags().Int("depth", 1, "with --analyze and no targets, group buckets by the first N path segments (0 = per-file)")
	cmd.Flags().String("bar", "auto", "with --analyze, bar style: auto|filled|block|none (auto = filled on TTY, none on pipes)")
	cmd.Flags().String("sort", "size", "with --analyze, ranking: size|churn|name (churn = by unique blob count, surfaces rewrite-heavy paths)")
	cmd.Flags().BoolP("interactive", "i", false, "with --analyze, open a multi-select picker over the audit results and forget the selected paths")
	rootCmd.AddCommand(cmd)
}

func runForget(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()
	yes, _ := cmd.Flags().GetBool("yes")
	analyze, _ := cmd.Flags().GetBool("analyze")
	keep, _ := cmd.Flags().GetStringSlice("keep")
	forceDirty, _ := cmd.Flags().GetBool("force-dirty")
	top, _ := cmd.Flags().GetInt("top")
	depth, _ := cmd.Flags().GetInt("depth")
	barStr, _ := cmd.Flags().GetString("bar")
	sortStr, _ := cmd.Flags().GetString("sort")
	interactive, _ := cmd.Flags().GetBool("interactive")
	// --analyze is read-only by definition; treat it as a dry-run so the
	// preview-only path runs without a separate code branch.
	dryRun := DryRun() || analyze

	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)

	// Preflight: filter-repo must be installed before we go any further,
	// except for --analyze which is read-only and just walks objects.
	// Done first so a missing binary is the first error a user sees, not
	// a cryptic mid-run failure.
	if !analyze {
		if err := forget.EnsureFilterRepo(); err != nil {
			return WithHint(err, "run `gk doctor` to see the full preflight result")
		}
	}

	// In-progress rebase/merge/cherry-pick is non-negotiable — filter-repo
	// would corrupt the half-applied state. AllowDirty masks the dirty
	// signal so the gate fires only on those structural conditions; we
	// inspect dirty separately after the targets are known.
	rep, err := gitsafe.Check(ctx, runner, gitsafe.WithWorkDir(RepoFlag()))
	if err != nil {
		return err
	}
	if err := rep.AllowDirty().Err(); err != nil {
		return err
	}

	paths, err := resolveTargets(ctx, runner, args)
	if err != nil {
		return err
	}
	if len(keep) > 0 {
		paths, err = forget.FilterKept(paths, keep)
		if err != nil {
			return err
		}
	}
	if len(paths) == 0 {
		// --analyze with no targets switches into repo-wide audit mode.
		// This is the "I don't know what's heavy yet, show me the
		// landscape" entry point. We do not abort with a hint; we
		// surface the heaviest buckets and trust the user to pick
		// targets from the output.
		if analyze {
			return runAudit(cmd, runner, depth, top, barStr, sortStr, interactive)
		}
		return WithHint(
			fmt.Errorf("no paths to forget"),
			"add paths to .gitignore and rerun, or pass paths explicitly: gk forget <path>...",
		)
	}

	// Dirty-vs-target reconciliation. The classic forget workflow is:
	// "this directory was committed by mistake, it's been changing on
	// disk for ages, now I want it gone from history". Refusing to run
	// because the live directory is mid-write would block exactly that
	// case. So we accept dirty entries that lie under any forget target
	// (filter-repo will erase them anyway) but still reject changes
	// outside, where the user would silently lose uncommitted work.
	outside, err := dirtyOutsideTargets(ctx, runner, paths)
	if err != nil {
		return err
	}
	if len(outside) > 0 && !forceDirty {
		preview := outside
		if len(preview) > 5 {
			preview = preview[:5]
		}
		return WithHint(
			fmt.Errorf("working-tree changes outside forget targets (%d path(s)); filter-repo would reset them"+
				"\n  example: %s", len(outside), strings.Join(preview, ", ")),
			"commit/stash those changes, narrow the forget targets, or pass --force-dirty if you accept the loss",
		)
	}

	commits, err := forget.CountTouchingCommits(ctx, runner, paths)
	if err != nil {
		return err
	}

	branch, _ := client.CurrentBranch(ctx)

	fmt.Fprintln(w, "gk forget — history rewrite preview")
	fmt.Fprintf(w, "  branch:  %s\n", displayBranch(branch))
	fmt.Fprintf(w, "  paths:   %d\n", len(paths))
	for _, p := range paths {
		fmt.Fprintf(w, "    - %s\n", p)
	}
	fmt.Fprintf(w, "  commits affected: %d (every commit SHA in the repo will change)\n", commits)
	fmt.Fprintln(w, "  backup:  refs/gk/forget-backup/<branch>/<unix>  +  .git/gk/forget-backup-<unix>.txt")

	if analyze {
		fmt.Fprintln(w, "analyzing blob sizes (may scan all objects)...")
		entries, aerr := forget.Analyze(ctx, runner, RepoFlag(), paths)
		if aerr != nil {
			return aerr
		}
		var grandTotal int64
		var grandBlobs int
		for _, e := range entries {
			fmt.Fprintf(w, "  %-40s  %4d unique blobs  total %10s  largest %10s\n",
				e.Path, e.UniqueBlobs, forget.HumanBytes(e.TotalBytes), forget.HumanBytes(e.LargestBytes))
			grandTotal += e.TotalBytes
			grandBlobs += e.UniqueBlobs
		}
		fmt.Fprintf(w, "  %-40s  %4d unique blobs  total %10s\n",
			"total", grandBlobs, forget.HumanBytes(grandTotal))
		return nil
	}

	if dryRun {
		fmt.Fprintln(w, "[dry-run] not rewriting history")
		return nil
	}

	if !yes {
		if !ui.IsTerminal() {
			return WithHint(fmt.Errorf("refusing to rewrite history without confirmation"),
				"pass --yes to proceed non-interactively")
		}
		ok, cerr := ui.Confirm(
			fmt.Sprintf("Rewrite %d commits to remove %d path(s)?", commits, len(paths)),
			false,
		)
		if cerr != nil {
			return cerr
		}
		if !ok {
			return fmt.Errorf("aborted")
		}
	}

	gitDir, err := repoGitDir(ctx, runner)
	if err != nil {
		return fmt.Errorf("locate .git dir: %w", err)
	}

	backup, err := forget.CreateBackup(ctx, runner, gitDir, time.Now())
	if err != nil {
		return fmt.Errorf("create backup: %w", err)
	}
	fmt.Fprintf(w, "backup written: %s\n", backup.Manifest)

	captured, err := forget.CaptureOrigin(ctx, runner)
	if err != nil {
		return err
	}

	if err := forget.RunFilterRepo(ctx, RepoFlag(), paths); err != nil {
		return fmt.Errorf("filter-repo: %w (backup at %s)", err, backup.Manifest)
	}

	if err := forget.RestoreOrigin(ctx, runner, captured); err != nil {
		// Non-fatal — the rewrite succeeded; surface as a warning so the
		// user can re-add origin themselves rather than rolling back.
		fmt.Fprintf(cmd.ErrOrStderr(), "warn: could not restore origin remote: %v\n", err)
	}

	fmt.Fprintln(w, "history rewritten.")
	if captured != nil && captured.URL != "" && branch != "" {
		fmt.Fprintln(w, "next:")
		fmt.Fprintf(w, "  git push --force-with-lease %s %s\n", captured.Name, branch)
		fmt.Fprintln(w, "  # collaborators must re-clone or run:")
		fmt.Fprintf(w, "  #   git fetch %s && git reset --hard %s/%s\n", captured.Name, captured.Name, branch)
	}
	fmt.Fprintf(w, "rollback: git update-ref --stdin < %s\n", backup.Manifest)
	return nil
}

// resolveTargets returns the path list to feed filter-repo. When the user
// passes explicit paths, those win as-is. Otherwise we auto-detect
// tracked-but-ignored entries — the canonical "I added .gitignore after
// the fact" workflow.
func resolveTargets(ctx context.Context, r git.Runner, args []string) ([]string, error) {
	if len(args) > 0 {
		// Pass-through; filter-repo accepts both files and directories.
		// We don't validate existence on disk because the whole point of
		// this command is to remove paths that may not exist anymore.
		out := make([]string, 0, len(args))
		for _, a := range args {
			a = strings.TrimSpace(a)
			if a != "" {
				out = append(out, a)
			}
		}
		return out, nil
	}
	auto, err := forget.AutoDetectIgnored(ctx, r)
	if err != nil {
		return nil, fmt.Errorf("auto-detect tracked-but-ignored paths: %w", err)
	}
	// Filter to paths that actually appear in history; an entry could be
	// staged-but-not-committed and would be a no-op for filter-repo.
	return forget.PathInHistory(ctx, r, auto)
}

// runAudit prints a repo-wide history audit grouped by `depth`-deep
// path prefix and capped at `top` entries. Triggered by `gk forget
// --analyze` with no positional targets and no .gitignore-derived
// auto-detect hits — i.e. the "explore what's heavy" entry point.
//
// Output marks history-only buckets with `(history)`. Those are paths
// no longer present in HEAD but still inflating clones — the highest
// leverage forget targets, since users can erase them without
// affecting current work.
func runAudit(cmd *cobra.Command, runner *git.ExecRunner, depth, top int, barStr, sortStr string, interactive bool) error {
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	barMode, err := forget.ParseBarMode(barStr)
	if err != nil {
		return err
	}
	sortMode, err := forget.ParseSortMode(sortStr)
	if err != nil {
		return err
	}

	if !JSONOut() {
		fmt.Fprintf(w, "gk forget --analyze (repo-wide, depth=%d, top=%d, sort=%s)\n", depth, top, sortStr)
		fmt.Fprintln(w, "scanning all objects on every ref — may take a while on large repos...")
		fmt.Fprintln(w)
	}

	entries, err := forget.Audit(ctx, runner, RepoFlag(), depth, top, sortMode)
	if err != nil {
		return err
	}

	// --json: machine-readable output for CI / dashboards. Skip the
	// human header/footer and emit a single JSON document with the
	// entries plus aggregate totals so consumers do not need to
	// re-derive them.
	if JSONOut() {
		return emitAuditJSON(w, entries, depth, top, sortStr)
	}

	fmt.Fprint(w, forget.RenderAudit(entries, forget.RenderOpts{
		Mode:    barMode,
		NoColor: NoColorFlag(),
	}))

	if len(entries) > 0 {
		var visible int64
		var historyOnly int64
		for _, e := range entries {
			visible += e.TotalBytes
			if !e.InHEAD {
				historyOnly += e.TotalBytes
			}
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "shown: %s across %d bucket(s)", forget.HumanBytes(visible), len(entries))
		if historyOnly > 0 {
			fmt.Fprintf(w, "  ·  history-only: %s", forget.HumanBytes(historyOnly))
		}
		fmt.Fprintln(w)
	}

	if interactive {
		return runAuditInteractive(cmd, runner, entries)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "next:")
	fmt.Fprintln(w, "  gk forget --analyze <path>             # narrow to one path for an exact reclaim estimate")
	fmt.Fprintln(w, "  gk forget --analyze -i                 # multi-select picker → forget chosen paths")
	fmt.Fprintln(w, "  echo <path>/ >> .gitignore && gk forget   # forget a path from history")
	fmt.Fprintln(w, "  gk forget --analyze --sort churn       # rank by rewrite count (lock files etc.)")
	fmt.Fprintln(w, "  gk forget --analyze --depth 2          # finer-grained directory grouping")
	fmt.Fprintln(w, "  gk forget --analyze --json             # machine-readable for CI/dashboards")
	return nil
}

// emitAuditJSON writes a single JSON document describing the audit.
// The shape is intentionally stable so that downstream tools can
// pin to it; new fields may be added but existing keys never change
// meaning.
func emitAuditJSON(w io.Writer, entries []forget.AuditEntry, depth, top int, sortStr string) error {
	var visible, historyOnly int64
	for _, e := range entries {
		visible += e.TotalBytes
		if !e.InHEAD {
			historyOnly += e.TotalBytes
		}
	}
	doc := struct {
		Depth            int                 `json:"depth"`
		Top              int                 `json:"top"`
		Sort             string              `json:"sort"`
		Entries          []forget.AuditEntry `json:"entries"`
		TotalBytes       int64               `json:"total_bytes"`
		HistoryOnlyBytes int64               `json:"history_only_bytes"`
	}{
		Depth:            depth,
		Top:              top,
		Sort:             sortStr,
		Entries:          entries,
		TotalBytes:       visible,
		HistoryOnlyBytes: historyOnly,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// runAuditInteractive opens a multi-select picker over the audit
// results, lets the user toggle which paths to forget, and feeds the
// chosen paths back into the standard rewrite pipeline. The forget
// half reuses the existing dirty-vs-target gate, backup ref, and
// confirmation prompt — interactive mode adds nothing destructive on
// its own; it just narrows the target list.
//
// Cancellation (esc) returns nil with no side effect, so users can
// browse audit results in the picker and back out.
func runAuditInteractive(cmd *cobra.Command, runner *git.ExecRunner, entries []forget.AuditEntry) error {
	if len(entries) == 0 {
		return nil
	}
	if !ui.IsTerminal() {
		return WithHint(
			fmt.Errorf("--interactive requires a TTY"),
			"drop -i and rerun without it, or pipe paths to a separate `gk forget <path>` invocation",
		)
	}

	items := make([]ui.MultiSelectItem, 0, len(entries))
	for _, e := range entries {
		flag := ""
		if !e.InHEAD {
			flag = "  (history-only)"
		}
		items = append(items, ui.MultiSelectItem{
			Key: e.Path,
			Display: fmt.Sprintf("%-50s  %4d blobs  %10s%s",
				truncatePathForPicker(e.Path, 50),
				e.UniqueBlobs,
				forget.HumanBytes(e.TotalBytes),
				flag,
			),
		})
	}

	chosen, err := ui.MultiSelectTUI(cmd.Context(),
		"select paths to forget (space to toggle, enter to continue, esc to cancel)",
		items, nil)
	if err != nil {
		// Cancelled or error — quiet exit either way; user did not
		// commit to a destructive action.
		return nil
	}
	if len(chosen) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no paths selected — nothing to forget.")
		return nil
	}

	// Hand the chosen paths to the same flow that `gk forget <path>...`
	// uses by re-invoking runForget with the selection as positional
	// args. We disable --analyze so the actual rewrite pipeline runs.
	if err := cmd.Flags().Set("analyze", "false"); err != nil {
		return err
	}
	return runForget(cmd, chosen)
}

// truncatePathForPicker mirrors forget.truncatePath but lives in the
// cli layer so we do not have to export the renderer's internal
// helpers. Long paths get a middle ellipsis so the picker columns
// stay aligned regardless of nesting depth.
func truncatePathForPicker(path string, width int) string {
	if len(path) <= width || width < 6 {
		if len(path) > width {
			return path[:width]
		}
		return path
	}
	const ell = "…"
	keep := width - len(ell)
	headLen := keep / 3
	tailLen := keep - headLen
	return path[:headLen] + ell + path[len(path)-tailLen:]
}

// dirtyOutsideTargets returns the working-tree-modified paths that are
// not covered by any forget target, plus any deletions in the same
// scope. Entries underneath a target are excluded because filter-repo
// will rewrite that file out of existence anyway, so a "dirty" working
// copy of a soon-to-be-forgotten file is uninteresting.
//
// Untracked files are not considered — the user typically did not
// intend to commit them, and filter-repo leaves them on disk.
func dirtyOutsideTargets(ctx context.Context, r git.Runner, targets []string) ([]string, error) {
	stdout, _, err := r.Run(ctx, "status", "--porcelain=v1", "-uno", "-z")
	if err != nil {
		return nil, fmt.Errorf("status --porcelain: %w", err)
	}
	var outside []string
	for _, entry := range strings.Split(string(stdout), "\x00") {
		if len(entry) <= 3 {
			continue
		}
		// porcelain v1 -z format: XY<space>path  (no terminator inside
		// the path because of -z). Strip the two-char status + space.
		path := entry[3:]
		if pathUnderAny(path, targets) {
			continue
		}
		outside = append(outside, path)
	}
	return outside, nil
}

// pathUnderAny reports whether path equals any target or sits under a
// target directory. Targets without a trailing slash but matching a
// directory on disk still cover their contents — filter-repo's path
// argument behaves the same way, so we mirror that here.
func pathUnderAny(path string, targets []string) bool {
	for _, t := range targets {
		t = strings.TrimRight(t, "/")
		if path == t {
			return true
		}
		if strings.HasPrefix(path, t+"/") {
			return true
		}
	}
	return false
}

func repoGitDir(ctx context.Context, r git.Runner) (string, error) {
	out, _, err := r.Run(ctx, "rev-parse", "--git-dir")
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(dir) {
		// Resolve relative to the working tree root so callers get an
		// absolute path regardless of where gk was invoked from.
		root, _, err := r.Run(ctx, "rev-parse", "--show-toplevel")
		if err == nil {
			dir = filepath.Join(strings.TrimSpace(string(root)), dir)
		}
	}
	return dir, nil
}
