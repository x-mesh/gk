package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// cleanupSelectRow is one worktree the cleanup checkbox list offers.
type cleanupSelectRow struct {
	entry     worktreeCleanupEntry
	candidate bool
	detached  bool
	head      string
	info      cleanupRowInfo
}

// cleanupSelectExcluded names the kept reasons whose worktrees the list
// leaves out: git cannot remove the current or a bare worktree, and a
// running process still holds a live lock.
var cleanupSelectExcluded = map[string]bool{
	"current":     true,
	"bare":        true,
	"locked-live": true,
}

// cleanupSelectRows lists the worktrees the user may remove, in git's
// worktree order: cleanup's candidates and the worktrees it kept, each with
// its verdict. The main worktree is left out because git refuses to remove it.
func cleanupSelectRows(report worktreeCleanupJSON, entries []WorktreeEntry, mainPath string) []cleanupSelectRow {
	verdicts := make(map[string]cleanupSelectRow, len(report.Candidates)+len(report.Skipped))
	for _, c := range report.Candidates {
		verdicts[c.Path] = cleanupSelectRow{entry: c, candidate: true}
	}
	for _, s := range report.Skipped {
		if !cleanupSelectExcluded[cleanupPrimaryReason(s)] {
			verdicts[s.Path] = cleanupSelectRow{entry: s}
		}
	}
	rows := make([]cleanupSelectRow, 0, len(verdicts))
	for _, e := range entries {
		r, ok := verdicts[e.Path]
		if !ok || (mainPath != "" && sameDir(mainPath, e.Path)) {
			continue
		}
		// cleanup returns before its lock check for detached and protected
		// worktrees, so their verdict does not carry the lock.
		r.detached, r.head = e.Detached, e.Head
		r.entry.Locked = r.entry.Locked || e.Locked
		rows = append(rows, r)
	}
	return rows
}

// cleanupRowInfo is what the list shows besides the verdict of cleanup, so
// the user can judge a worktree that cleanup keeps.
type cleanupRowInfo struct {
	commitAt  time.Time
	subject   string
	createdAt time.Time // zero when the reflog no longer holds the creation
	target    string
	ahead     int // commits on the branch that target lacks
	behind    int
	upstream  string
	unpushed  int
	gone      bool
}

// loadCleanupRowInfo fills the info of every row, statusScanWorkers rows at
// a time, and the uncommitted changes of rows cleanup kept before it looked
// at them. A git call that fails leaves its field empty.
func loadCleanupRowInfo(ctx context.Context, runner *git.ExecRunner, cfg *config.Config, rows []cleanupSelectRow) {
	branches := map[string]branchInfo{}
	if list, err := listLocalBranches(ctx, runner); err == nil {
		for _, b := range list {
			branches[b.Name] = b
		}
	}
	subjects := map[string]string{}
	if out, _, err := runner.Run(ctx, "for-each-ref", "--format=%(refname:short)%00%(contents:subject)", "refs/heads"); err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			if name, subject, ok := strings.Cut(line, "\x00"); ok {
				subjects[name] = subject
			}
		}
	}
	base := finishBaseBranch(ctx, runner, cfg)
	sem := make(chan struct{}, min(runtime.NumCPU(), statusScanWorkers))
	var wg sync.WaitGroup
	for i := range rows {
		wg.Add(1)
		go func(r *cleanupSelectRow) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r.info = cleanupRowInfoFor(ctx, runner, *r, branches[r.entry.Branch], subjects[r.entry.Branch], base)
			if !r.candidate && r.entry.Dirty == nil && !cleanupHasReason(r.entry, "clean") && cleanupPrimaryReason(r.entry) != "prunable" {
				r.entry.Dirty = worktreeDirtyAt(ctx, r.entry.Path)
				if r.entry.Dirty != nil {
					r.entry.Untracked, _ = worktreeUntrackedAt(ctx, r.entry.Path)
				}
			}
		}(&rows[i])
	}
	wg.Wait()
}

func cleanupRowInfoFor(ctx context.Context, runner *git.ExecRunner, r cleanupSelectRow, b branchInfo, subject, base string) cleanupRowInfo {
	var info cleanupRowInfo
	if r.detached || r.entry.Branch == "" {
		if r.head == "" {
			return info
		}
		if out, _, err := runner.Run(ctx, "log", "-1", "--format=%ct%x00%s", r.head); err == nil {
			ts, subj, _ := strings.Cut(strings.TrimSpace(string(out)), "\x00")
			if secs, err := strconv.ParseInt(ts, 10, 64); err == nil {
				info.commitAt = time.Unix(secs, 0)
			}
			info.subject = subj
		}
		return info
	}
	branch := r.entry.Branch
	info.commitAt, info.subject = b.LastCommit, subject
	info.upstream, info.unpushed, info.gone = b.Upstream, b.Ahead, b.Gone
	info.createdAt, _ = branchCreatedAt(ctx, runner, branch)
	info.target = r.entry.Target
	if info.target == "" {
		info.target = cleanupMergeTarget(ctx, runner, base, branch)
	}
	if info.target != "" && info.target != branch {
		if out, _, err := runner.Run(ctx, "rev-list", "--left-right", "--count", info.target+"..."+branch); err == nil {
			if f := strings.Fields(string(out)); len(f) == 2 {
				info.behind, _ = strconv.Atoi(f[0])
				info.ahead, _ = strconv.Atoi(f[1])
			}
		}
	}
	return info
}

func cleanupHasReason(e worktreeCleanupEntry, reason string) bool {
	for _, r := range e.Reasons {
		if r == reason {
			return true
		}
	}
	return false
}

func cleanupAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return shortAge(t)
}

func cleanupAgo(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	if a := shortAge(t); a != "now" {
		return a + " ago"
	}
	return "just now"
}

// cleanupPushState says whether the branch lives on the remote, which is
// what survives once the worktree and the local branch are gone.
func cleanupPushState(r cleanupSelectRow) string {
	switch {
	case r.detached || r.entry.Branch == "":
		return ""
	case r.info.gone:
		return "upstream gone"
	case r.info.upstream == "":
		return "not pushed"
	case r.info.unpushed > 0:
		return fmt.Sprintf("↑%d unpushed", r.info.unpushed)
	default:
		return "pushed"
	}
}

// cleanupSelectState is the STATE column: the verdict with the numbers that
// decide it, uncommitted changes, and the push state.
func cleanupSelectState(r cleanupSelectRow) string {
	reason := cleanupPrimaryReason(r.entry)
	var parts []string
	switch {
	case reason == "prunable":
		return "prunable: directory gone"
	case r.candidate:
		verdict := reason
		if cleanupHasReason(r.entry, "squash-merged") {
			verdict = "squash-merged"
		} else if cleanupHasReason(r.entry, "merged") {
			verdict = "merged"
		}
		parts = append(parts, verdict)
		if len(r.entry.Disposable) > 0 {
			parts = append(parts, "lockfiles only")
		}
	case reason == "unmerged":
		parts = append(parts, fmt.Sprintf("unmerged ↑%d", r.info.ahead))
	case reason == "unstarted":
		parts = append(parts, "unstarted, created "+cleanupAgo(r.info.createdAt))
	case reason != "dirty":
		parts = append(parts, reason)
	}
	if r.entry.Dirty != nil && len(r.entry.Disposable) == 0 {
		parts = append(parts, "dirty: "+formatDirtyCounts(*r.entry.Dirty, nil))
	}
	if push := cleanupPushState(r); push != "" {
		parts = append(parts, push)
	}
	return strings.Join(parts, " · ")
}

// cleanupSelectDetail describes the cursor row in three lines: where it is,
// when it was worked on, and what removing it leaves behind.
func cleanupSelectDetail(r cleanupSelectRow) string {
	name := r.entry.Branch
	if r.detached || name == "" {
		name = "(detached " + shortWorktreeHead(r.head) + ")"
	}
	when := "last commit " + cleanupAgo(r.info.commitAt)
	if r.info.subject != "" {
		subject := r.info.subject
		if runeLen(subject) > 72 {
			subject = string([]rune(subject)[:71]) + "…"
		}
		when += fmt.Sprintf(" %q", subject)
	}
	if !r.detached && r.entry.Branch != "" {
		when += " · created " + cleanupAgo(r.info.createdAt)
	}
	var state []string
	reason := cleanupPrimaryReason(r.entry)
	switch {
	case reason == "prunable":
		state = append(state, "directory is gone; removing drops only the registration")
	case r.info.target != "" && r.info.target != r.entry.Branch:
		state = append(state, fmt.Sprintf("vs %s: ↑%d ↓%d", r.info.target, r.info.ahead, r.info.behind))
	case !r.detached && r.entry.Branch != "":
		state = append(state, "no parent/base to compare with")
	}
	if push := cleanupPushState(r); push != "" {
		state = append(state, push)
	}
	switch {
	case reason == "prunable":
	case r.entry.Dirty != nil:
		state = append(state, "uncommitted: "+formatDirtyCounts(*r.entry.Dirty, r.entry.Untracked))
	default:
		state = append(state, "no uncommitted changes")
	}
	if r.entry.Locked {
		state = append(state, "locked")
	}
	return name + "  " + r.entry.Path + "\n" + when + "\n" + strings.Join(state, " · ")
}

// cleanupSortModes are the orders s cycles through. Each sort is stable, so
// ties keep the worktree order of git.
var cleanupSortModes = []struct {
	name string
	less func(a, b cleanupSelectRow, sizes map[string]int64) bool
}{
	{"safe first", func(a, b cleanupSelectRow, _ map[string]int64) bool { return cleanupRisk(a) < cleanupRisk(b) }},
	{"oldest commit", func(a, b cleanupSelectRow, _ map[string]int64) bool {
		if a.info.commitAt.IsZero() || b.info.commitAt.IsZero() {
			return !a.info.commitAt.IsZero() && b.info.commitAt.IsZero()
		}
		return a.info.commitAt.Before(b.info.commitAt)
	}},
	{"largest", func(a, b cleanupSelectRow, sizes map[string]int64) bool {
		return sizes[a.entry.Path] > sizes[b.entry.Path]
	}},
	{"name", func(a, b cleanupSelectRow, _ map[string]int64) bool {
		return cleanupSelectBranch(a, math.MaxInt) < cleanupSelectBranch(b, math.MaxInt)
	}},
}

// cleanupRisk ranks rows for "safe first": cleanup's candidates, then rows
// whose removal keeps every commit, then rows with uncommitted changes.
func cleanupRisk(r cleanupSelectRow) int {
	switch {
	case r.candidate:
		return 0
	case r.entry.Dirty != nil:
		return 2
	default:
		return 1
	}
}

func sortCleanupRows(rows []cleanupSelectRow, mode int, sizes map[string]int64) []cleanupSelectRow {
	out := slices.Clone(rows)
	less := cleanupSortModes[mode].less
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j], sizes) })
	return out
}

const cleanupBranchMax = 32

func cleanupBranchWidth(rows []cleanupSelectRow) int {
	w := len("BRANCH")
	for _, r := range rows {
		w = max(w, runeLen(cleanupSelectBranch(r, cleanupBranchMax)))
	}
	return w
}

// cleanupSelectHeader names the columns of cleanupSelectItems.
func cleanupSelectHeader(branchW int) string {
	return fmt.Sprintf("     %-*s  %7s  %6s  %s", branchW, "BRANCH", "SIZE", "COMMIT", "STATE")
}

// mainWorktreeOf names the main worktree, which git lists first. A bare main
// has no checkout and is excluded as "bare" already, so no linked worktree
// stands in for it.
func mainWorktreeOf(entries []WorktreeEntry) string {
	if len(entries) == 0 || entries[0].Bare {
		return ""
	}
	return entries[0].Path
}

// cleanupSelectItems renders rows as checkbox items keyed by path, with
// cleanup's candidates checked.
func cleanupSelectItems(rows []cleanupSelectRow, sizes map[string]int64, branchW int) ([]ui.MultiSelectItem, map[string]bool) {
	items := make([]ui.MultiSelectItem, 0, len(rows))
	preselect := map[string]bool{}
	for _, r := range rows {
		branch := cleanupSelectBranch(r, cleanupBranchMax)
		size := "-"
		if n, ok := sizes[r.entry.Path]; ok {
			size = humanSize(n)
		}
		items = append(items, ui.MultiSelectItem{
			Key: r.entry.Path,
			Display: fmt.Sprintf("%s%s  %7s  %6s  %s",
				branch, strings.Repeat(" ", max(branchW-runeLen(branch), 0)), size,
				cleanupAge(r.info.commitAt), cleanupSelectState(r)),
		})
		if r.candidate {
			preselect[r.entry.Path] = true
		}
	}
	return items, preselect
}

func cleanupSelectBranch(r cleanupSelectRow, maxLen int) string {
	b := r.entry.Branch
	if r.detached || b == "" {
		b = "(detached)"
	}
	if runeLen(b) > maxLen {
		b = string([]rune(b)[:maxLen-1]) + "…"
	}
	return b
}

// cleanupSelectLoss says what removing r loses beyond files git can rebuild
// from a ref, or "" when nothing is lost. It fills r.entry.Dirty for
// worktrees cleanup kept before it looked at their changes, so the removal
// knows to force them.
func cleanupSelectLoss(ctx context.Context, runner git.Runner, r *cleanupSelectRow) string {
	if r.candidate {
		// The verdict was made before the list opened. Work saved into the
		// worktree since then must not go without asking.
		if r.entry.Action == cleanupActionPruneRecord {
			return ""
		}
		dirty := worktreeDirtyAt(ctx, r.entry.Path)
		if dirty == nil {
			return ""
		}
		untracked, only := worktreeUntrackedAt(ctx, r.entry.Path)
		if disposableUntrackedOnly(untracked, only) != nil {
			return ""
		}
		r.entry.Dirty, r.entry.Untracked = dirty, untracked
		return "uncommitted changes made after the list opened (" + formatDirtyCounts(*dirty, untracked) + ")"
	}
	var losses []string
	if r.entry.Dirty == nil && cleanupPrimaryReason(r.entry) != "prunable" {
		r.entry.Dirty = worktreeDirtyAt(ctx, r.entry.Path)
		if r.entry.Dirty != nil {
			r.entry.Untracked, _ = worktreeUntrackedAt(ctx, r.entry.Path)
		}
	}
	if r.entry.Dirty != nil {
		losses = append(losses, "uncommitted changes ("+formatDirtyCounts(*r.entry.Dirty, r.entry.Untracked)+")")
	}
	if r.detached && r.head != "" {
		out, _, err := runner.Run(ctx, "for-each-ref", "--contains", r.head, "--count=1", "--format=%(refname)", "refs/heads", "refs/tags", "refs/remotes")
		if err == nil && strings.TrimSpace(string(out)) == "" {
			losses = append(losses, "detached HEAD "+shortWorktreeHead(r.head)+" is on no branch or tag")
		}
	}
	return strings.Join(losses, "; ")
}

// measureWorktreeSizes sums the disk blocks under each path, at most
// statusScanWorkers walks at a time. done is called after each path. A path
// that cannot be walked is missing from the result.
func measureWorktreeSizes(ctx context.Context, paths []string, done func()) map[string]int64 {
	sizes := make([]int64, len(paths))
	ok := make([]bool, len(paths))
	sem := make(chan struct{}, min(runtime.NumCPU(), statusScanWorkers))
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			defer done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sizes[i], ok[i] = dirDiskUsage(ctx, p)
		}(i, p)
	}
	wg.Wait()
	out := make(map[string]int64, len(paths))
	for i, p := range paths {
		if ok[i] {
			out[p] = sizes[i]
		}
	}
	return out
}

// dirDiskUsage is `du -s` in bytes: allocated blocks, so sparse files count
// what they occupy. Unreadable entries inside the tree are skipped.
func dirDiskUsage(ctx context.Context, root string) (int64, bool) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if d == nil {
				return err
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		total += diskBytes(info)
		return nil
	})
	return total, err == nil
}

// runWorktreeCleanupSelect is `gk worktree cleanup -i` and the x key of
// `gk worktree`: a checkbox list of the removable worktrees with their size,
// cleanup's candidates checked. Worktrees cleanup kept can be checked too;
// when one holds work that removing it loses, the user confirms that once.
func runWorktreeCleanupSelect(ctx context.Context, w io.Writer, runner *git.ExecRunner, cfg *config.Config, opts worktreeCleanupOpts, current string) error {
	// The verdicts, the branch details and the sizes take seconds on a
	// repository with many worktrees; one spinner covers all three so the
	// screen never sits blank before the list opens.
	const (
		phaseVerdicts = iota
		phaseDetails
		phaseSizes
	)
	var phase, measured, toMeasure atomic.Int32
	stop := ui.StartBubbleSpinnerLive(func() string {
		switch phase.Load() {
		case phaseVerdicts:
			return "checking worktrees"
		case phaseDetails:
			return "reading branch details"
		default:
			return fmt.Sprintf("measuring worktree sizes %d/%d", measured.Load(), toMeasure.Load())
		}
	})
	defer stop()

	entries, err := listWorktreeEntries(ctx, runner)
	if err != nil {
		return err
	}
	report, err := collectWorktreeCleanup(ctx, runner, cfg, opts, "", current)
	if err != nil {
		return err
	}
	rows := cleanupSelectRows(report, entries, mainWorktreeOf(entries))
	rows = slices.DeleteFunc(rows, func(r cleanupSelectRow) bool {
		return r.entry.Locked && worktreeLockInfo(ctx, runner, r.entry.Path).Alive
	})
	if len(rows) == 0 {
		stop()
		fmt.Fprintln(w, "worktree cleanup: no worktree to remove besides the current and main ones")
		return nil
	}

	phase.Store(phaseDetails)
	loadCleanupRowInfo(ctx, runner, cfg, rows)

	paths := make([]string, 0, len(rows))
	for _, r := range rows {
		if cleanupPrimaryReason(r.entry) != "prunable" {
			paths = append(paths, r.entry.Path)
		}
	}
	toMeasure.Store(int32(len(paths)))
	phase.Store(phaseSizes)
	sizes := measureWorktreeSizes(ctx, paths, func() { measured.Add(1) })
	stop()

	branchW := cleanupBranchWidth(rows)
	sortMode := 0
	items, preselect := cleanupSelectItems(sortCleanupRows(rows, sortMode, sizes), sizes, branchW)
	byKey := make(map[string]cleanupSelectRow, len(rows))
	for _, r := range rows {
		byKey[r.entry.Path] = r
	}
	keys, err := ui.MultiSelectWith(ctx, "█ CLEANUP  choose the worktrees to remove · checked rows are safe to remove", items, preselect, ui.MultiSelectOptions{
		Header:   cleanupSelectHeader(branchW),
		BoxTitle: "details",
		Detail:   func(key string) string { return cleanupSelectDetail(byKey[key]) },
		Preview: func(selected []string) string {
			var total int64
			for _, p := range selected {
				total += sizes[p]
			}
			return fmt.Sprintf("cleanup: %d of %d checked · %s on disk · sort: %s (s) · enter removes the checked worktrees, esc cancels",
				len(selected), len(rows), humanSize(total), cleanupSortModes[sortMode].name)
		},
		Extras: []ui.MultiSelectExtraKey{{
			Key:  "s",
			Help: "s sort",
			OnPress: func() ([]ui.MultiSelectItem, map[string]bool, error) {
				sortMode = (sortMode + 1) % len(cleanupSortModes)
				// A nil preselect keeps exactly the rows the user checked.
				sorted, _ := cleanupSelectItems(sortCleanupRows(rows, sortMode, sizes), sizes, branchW)
				return sorted, nil, nil
			},
		}},
	})
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return nil
		}
		return err
	}
	if len(keys) == 0 {
		fmt.Fprintln(w, "worktree cleanup: nothing selected")
		return nil
	}

	byPath := make(map[string]int, len(rows))
	for i, r := range rows {
		byPath[r.entry.Path] = i
	}
	var safe, chosen []worktreeCleanupEntry
	var lossy []string
	lossByPath := map[string]bool{}
	for _, k := range keys {
		r := rows[byPath[k]]
		loss := cleanupSelectLoss(ctx, runner, &r)
		if loss != "" {
			lossy = append(lossy, fmt.Sprintf("%s: %s", r.entry.Path, loss))
			lossByPath[r.entry.Path] = true
		}
		if r.candidate && loss == "" {
			safe = append(safe, r.entry)
			continue
		}
		chosen = append(chosen, r.entry)
	}
	if len(lossy) > 0 {
		ok, err := ui.ConfirmTUI(ctx,
			fmt.Sprintf("remove %d worktrees that hold unsaved work?", len(lossy)),
			strings.Join(lossy, "\n")+"\nNo keeps these and removes the rest; esc removes nothing.", false)
		if errors.Is(err, ui.ErrPickerAborted) {
			return nil
		}
		if err != nil {
			return err
		}
		if !ok {
			kept := chosen[:0]
			for _, c := range chosen {
				if !lossByPath[c.Path] {
					kept = append(kept, c)
				}
			}
			chosen = kept
		}
	}

	if DryRun() {
		for _, c := range append(safe, chosen...) {
			fmt.Fprintf(w, "[dry-run] would remove worktree %s\n", c.Path)
		}
		return nil
	}
	removed, failed := applyWorktreeCleanup(ctx, w, runner, safe, opts)
	// The user picked these past cleanup's verdict and confirmed any loss, so
	// a stale lock or uncommitted changes no longer stop the removal. Their
	// branches stay: nothing proved them merged.
	r2, f2 := applyWorktreeCleanup(ctx, w, runner, chosen, worktreeCleanupOpts{forceStaleLocks: true, discardDirty: true})
	removed, failed = append(removed, r2...), append(failed, f2...)
	fmt.Fprintf(w, "worktree cleanup: removed %d, failed %d\n", len(removed), len(failed))
	for _, f := range failed {
		fmt.Fprintf(w, "  failed %s: %s\n", f.Path, f.Error)
	}
	return nil
}
