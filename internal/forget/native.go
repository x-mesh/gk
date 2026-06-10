package forget

// native.go — gk's built-in history rewrite engine for the path-removal
// slice of git filter-repo (`--invert-paths --path X`). The pipeline is
//
//   git fast-export --no-data ... | nativeRewriter | git fast-import --force
//
// with all judgment in the Go middle stage: drop filechanges under the
// target paths, prune commits that become empty, splice children onto the
// nearest kept ancestor, and collapse merges whose parents become
// duplicate or redundant.
//
// Why pruning by splicing is sound with delta streams: fast-export emits a
// commit's filechanges as a delta against its FIRST parent. A commit is
// pruned only when its FILTERED delta is empty — i.e. its filtered tree
// equals its first parent's filtered tree. Children spliced onto the
// grandparent therefore keep valid deltas, because the tree they were
// expressed against is identical after filtering.
//
// Scope guarantees compared to the delegated engine:
//   - only refs/heads/* and refs/tags/* are rewritten; refs/gk/* backups,
//     refs/remotes/* and the stash stay untouched
//   - no gc/prune afterwards — pre-rewrite objects stay reachable through
//     the backup refs, so the printed rollback actually works
//   - v1 does not rewrite SHA references inside commit messages and keeps
//     merges whose parents remain ≥2 distinct non-redundant commits
//
// Inputs the engine cannot handle (shallow clones, replace refs, renames
// in the stream, foreign commands) abort before any ref moves.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// NativeResult summarizes a completed native rewrite.
type NativeResult struct {
	CommitsSeen      int
	CommitsPruned    int
	MergesSimplified int
	RefsUpdated      int
	RefsDeleted      []string
	// CommitMap maps original commit SHAs to their rewritten SHAs. Pruned
	// commits map to the SHA of their surviving ancestor ("" when the
	// entire history of that commit vanished).
	CommitMap map[string]string
}

// nativeRewriter implements feHandler over the export→import pipe.
type nativeRewriter struct {
	matches func(path string) bool
	out     *bufio.Writer

	// replacement chases pruned marks to their surviving ancestor mark;
	// 0 means "no surviving ancestor" (the history vanished entirely).
	replacement map[int]int
	kept        map[int]bool

	// kept-graph topology for redundant-parent detection on merges.
	parents map[int][]int
	gen     map[int]int

	// markOID joins export marks with original SHAs for the commit map.
	markOID map[int]string

	// refTip tracks each ref's ORIGINAL tip mark, fed by both commit
	// blocks and reset blocks; final ref placement is emitted at done-time
	// from this table, never from stream position.
	refTip   map[string]int
	refOrder []string

	// tags are buffered (they are cheap) and re-targeted at done-time so
	// a tag arriving before its commit's fate is decided cannot mis-point.
	tags []*feTag

	headRef string

	stats NativeResult
	// vanishedRefs collects refs whose entire history was pruned; they are
	// deleted after a successful import.
	vanishedRefs []string
}

func newNativeRewriter(targets []string, headRef string, out *bufio.Writer) *nativeRewriter {
	norm := make([]string, 0, len(targets))
	for _, t := range targets {
		t = strings.TrimPrefix(t, "./")
		t = strings.TrimSuffix(t, "/")
		if t != "" {
			norm = append(norm, t)
		}
	}
	return &nativeRewriter{
		matches: func(p string) bool {
			for _, t := range norm {
				if p == t || strings.HasPrefix(p, t+"/") {
					return true
				}
			}
			return false
		},
		out:         out,
		replacement: map[int]int{},
		kept:        map[int]bool{},
		parents:     map[int][]int{},
		gen:         map[int]int{},
		markOID:     map[int]string{},
		refTip:      map[string]int{},
		headRef:     headRef,
	}
}

// resolve chases a mark through prunes to its surviving ancestor (0 = none).
func (n *nativeRewriter) resolve(mark int) int {
	for mark != 0 && !n.kept[mark] {
		next, ok := n.replacement[mark]
		if !ok {
			return 0
		}
		mark = next
	}
	return mark
}

// isAncestor reports whether a is an ancestor of (or equal to) b in the
// kept graph. The search walks parents from b and prunes on generation
// numbers, so it touches only commits that could possibly be above a.
func (n *nativeRewriter) isAncestor(a, b int) bool {
	if a == b {
		return true
	}
	if n.gen[a] >= n.gen[b] {
		return false
	}
	seen := map[int]bool{b: true}
	queue := []int{b}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, p := range n.parents[cur] {
			if p == a {
				return true
			}
			if seen[p] || n.gen[p] <= n.gen[a] {
				continue
			}
			seen[p] = true
			queue = append(queue, p)
		}
	}
	return false
}

func (n *nativeRewriter) trackRef(ref string, tip int) {
	if _, ok := n.refTip[ref]; !ok {
		n.refOrder = append(n.refOrder, ref)
	}
	n.refTip[ref] = tip
}

func (n *nativeRewriter) OnReset(r *feReset) error {
	// In-stream resets only matter as ref-tip records; placement is
	// re-derived at done-time. A reset without `from` (fast-export's
	// "clear branch before reusing the name for a root") carries no tip.
	if r.HasFrom {
		n.trackRef(r.Ref, r.FromMark)
	}
	return nil
}

func (n *nativeRewriter) OnCommit(c *feCommit) error {
	n.stats.CommitsSeen++
	if c.Mark == 0 {
		return fmt.Errorf("commit %s without mark", c.Ref)
	}
	if c.OriginalOID != "" {
		n.markOID[c.Mark] = c.OriginalOID
	}
	n.trackRef(c.Ref, c.Mark)

	// Original parents, in order.
	var origParents []int
	if c.HasFrom {
		origParents = append(origParents, c.FromMark)
	}
	origParents = append(origParents, c.MergeMarks...)

	// Map through prunes and drop vanished parents. Duplicates are kept
	// deliberately: filter-repo emits a merge whose parents collapsed onto
	// the same commit with the duplicate edge intact, and matching it
	// byte-for-byte is what keeps the engines SHA-identical.
	newParents := make([]int, 0, len(origParents))
	for _, p := range origParents {
		if q := n.resolve(p); q != 0 {
			newParents = append(newParents, q)
		}
	}

	// Filter the delta.
	filtered := make([]feChange, 0, len(c.Changes))
	for _, ch := range c.Changes {
		if !n.matches(ch.Path) {
			filtered = append(filtered, ch)
		}
	}

	parentsChanged := len(newParents) != len(origParents)
	if !parentsChanged {
		for i := range newParents {
			if newParents[i] != origParents[i] {
				parentsChanged = true
				break
			}
		}
	}
	changesFiltered := len(filtered) != len(c.Changes)
	touched := parentsChanged || changesFiltered

	// Dedupe + ancestry reduction — but ONLY to decide prunability,
	// mirroring filter-repo: a merge that keeps content keeps ALL its
	// (remapped) parents, duplicate and redundant edges included; a merge
	// whose delta is empty and whose extra parents all became (duplicates
	// or) ancestors of the first parent has stopped merging anything and
	// can be pruned.
	effective := newParents
	if touched && len(newParents) > 1 {
		distinct := make([]int, 0, len(newParents))
		for _, p := range newParents {
			dup := false
			for _, e := range distinct {
				if e == p {
					dup = true
					break
				}
			}
			if !dup {
				distinct = append(distinct, p)
			}
		}
		reduced := make([]int, 0, len(distinct))
		for i, p := range distinct {
			redundant := false
			for j, q := range distinct {
				if i == j {
					continue
				}
				if n.isAncestor(p, q) {
					redundant = true
					break
				}
			}
			if !redundant {
				reduced = append(reduced, p)
			}
		}
		effective = reduced
	}

	// Prune decision. A commit is removable only when the rewrite touched
	// it, its filtered delta is empty, it no longer (effectively) merges
	// anything, and it was not a deliberately-empty commit in the original
	// history. deleteall blocks are absolute snapshots, not deltas — never
	// pruned.
	//
	// The replacement must be the FIRST parent: an empty delta proves the
	// commit's filtered tree equals its first parent's, so only a splice
	// onto that parent keeps the children's deltas valid. A merge whose
	// reduction would survive through a NON-first parent (e.g. `merge -s
	// ours` topologies) is kept instead — degenerate but content-correct.
	wasMeaningful := len(c.Changes) > 0 || len(origParents) > 1
	repValid := len(effective) == 0 || (len(newParents) > 0 && effective[0] == newParents[0])
	if touched && !c.DeleteAll && len(filtered) == 0 && len(effective) <= 1 && wasMeaningful && repValid {
		rep := 0
		if len(effective) == 1 {
			rep = effective[0]
		}
		n.replacement[c.Mark] = rep
		n.stats.CommitsPruned++
		if len(origParents) > 1 {
			n.stats.MergesSimplified++
		}
		return nil
	}

	n.kept[c.Mark] = true
	n.parents[c.Mark] = newParents
	g := 0
	for _, p := range newParents {
		if n.gen[p] >= g {
			g = n.gen[p] + 1
		}
	}
	n.gen[c.Mark] = g

	if len(newParents) == 0 {
		// The commit became (or was) a root; clear the branch name first
		// so fast-import does not graft it onto the ref's current tip.
		writeReset(n.out, c.Ref, 0)
	}
	writeCommit(n.out, c, newParents, filtered)
	return nil
}

func (n *nativeRewriter) OnTag(t *feTag) error {
	n.tags = append(n.tags, t)
	return nil
}

// OnDone emits final ref placements, re-targeted tags, and the done
// command. Refs whose history vanished are recorded for deletion. An
// error here aborts the stream BEFORE `done`, which makes fast-import
// (running with the done feature) fail without updating a single ref.
func (n *nativeRewriter) OnDone() error {
	for _, t := range n.tags {
		to := n.resolve(t.FromMark)
		ref := "refs/tags/" + t.Name
		if to == 0 {
			n.vanishedRefs = append(n.vanishedRefs, ref)
			continue
		}
		writeTag(n.out, t, to)
		// The tag command places the ref itself; drop the tip record so
		// the reset loop below does not overwrite the annotated tag
		// object with its target commit.
		delete(n.refTip, ref)
	}
	for _, ref := range n.refOrder {
		tip, ok := n.refTip[ref]
		if !ok {
			continue
		}
		to := n.resolve(tip)
		if to == 0 {
			if ref == n.headRef {
				return fmt.Errorf("refusing rewrite: HEAD branch %s would lose its entire history", ref)
			}
			n.vanishedRefs = append(n.vanishedRefs, ref)
			continue
		}
		writeReset(n.out, ref, to)
		n.stats.RefsUpdated++
	}
	if _, err := n.out.WriteString("done\n"); err != nil {
		return err
	}
	return n.out.Flush()
}

// ─── guards ───

// nativeGuards rejects repository states the v1 engine does not model.
func nativeGuards(ctx context.Context, runner git.Runner, gitDir string) error {
	if _, err := os.Stat(filepath.Join(gitDir, "shallow")); err == nil {
		return fmt.Errorf("native engine: shallow clones cannot be rewritten — unshallow first (git fetch --unshallow)")
	}
	out, _, err := runner.Run(ctx, "for-each-ref", "--count=1", "--format=%(refname)", "refs/replace/")
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return fmt.Errorf("native engine: refs/replace/* present — replace refs are not supported; use --engine filter-repo")
	}
	return nil
}

// ─── orchestration ───

// RunNative performs the full native rewrite. On any error before the
// stream's done command, fast-import exits without touching refs, so the
// repository is left exactly as it was (imported loose objects aside).
func RunNative(ctx context.Context, repoDir, gitDir string, paths []string) (*NativeResult, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no paths to forget")
	}
	runner := &git.ExecRunner{Dir: repoDir}
	if err := nativeGuards(ctx, runner, gitDir); err != nil {
		return nil, err
	}

	headRef := ""
	if out, _, err := runner.Run(ctx, "symbolic-ref", "-q", "HEAD"); err == nil {
		headRef = strings.TrimSpace(string(out))
	}

	marksDir, err := os.MkdirTemp("", "gk-forget-native-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(marksDir) }()
	marksFile := filepath.Join(marksDir, "marks")

	exportCmd := exec.CommandContext(ctx, "git", "fast-export", //nolint:gosec
		"--no-data", "--show-original-ids", "--use-done-feature", "--reencode=no",
		"--signed-tags=strip", "--tag-of-filtered-object=rewrite", "--fake-missing-tagger",
		"--branches", "--tags")
	exportCmd.Dir = repoDir
	var exportErr bytes.Buffer
	exportCmd.Stderr = &exportErr
	exportOut, err := exportCmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	importCmd := exec.CommandContext(ctx, "git", "fast-import", //nolint:gosec
		"--force", "--quiet", "--export-marks="+marksFile)
	importCmd.Dir = repoDir
	var importErr bytes.Buffer
	importCmd.Stderr = &importErr
	importIn, err := importCmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	if err := exportCmd.Start(); err != nil {
		return nil, fmt.Errorf("start fast-export: %w", err)
	}
	if err := importCmd.Start(); err != nil {
		_ = exportCmd.Process.Kill()
		_ = exportCmd.Wait()
		return nil, fmt.Errorf("start fast-import: %w", err)
	}

	w := bufio.NewWriterSize(importIn, 1<<16)
	if _, werr := w.WriteString("feature done\n"); werr != nil {
		return nil, fmt.Errorf("write to fast-import: %w", werr)
	}
	rw := newNativeRewriter(paths, headRef, w)
	filterErr := parseFastExport(exportOut, rw)

	// Closing stdin without `done` (filterErr path) makes fast-import die
	// without updating refs — the abort mechanism, not a cleanup detail.
	_ = importIn.Close()
	exportWaitErr := exportCmd.Wait()
	importWaitErr := importCmd.Wait()

	if filterErr != nil {
		return nil, fmt.Errorf("native rewrite aborted (refs untouched): %w", filterErr)
	}
	if exportWaitErr != nil {
		return nil, fmt.Errorf("fast-export: %w\n%s", exportWaitErr, strings.TrimSpace(exportErr.String()))
	}
	if importWaitErr != nil {
		return nil, fmt.Errorf("fast-import: %w\n%s", importWaitErr, strings.TrimSpace(importErr.String()))
	}

	res := rw.stats
	res.CommitMap, err = buildCommitMap(marksFile, rw)
	if err != nil {
		return nil, err
	}

	// Delete refs whose entire history vanished. Annotated tags need the
	// tag-object deletion form; update-ref -d works for both.
	sort.Strings(rw.vanishedRefs)
	for _, ref := range rw.vanishedRefs {
		if _, _, derr := runner.Run(ctx, "update-ref", "-d", ref); derr != nil {
			return nil, fmt.Errorf("delete vanished ref %s: %w", ref, derr)
		}
		res.RefsDeleted = append(res.RefsDeleted, ref)
	}

	// Refresh index and working tree to the rewritten HEAD. The forget
	// flow has already enforced its dirty-tree gates.
	if _, stderr, rerr := runner.Run(ctx, "reset", "--hard"); rerr != nil {
		return nil, fmt.Errorf("reset to rewritten HEAD: %s: %w", strings.TrimSpace(string(stderr)), rerr)
	}
	return &res, nil
}

// buildCommitMap joins fast-import's mark→newSHA table with the
// rewriter's mark→originalOID table. Pruned commits map to their
// surviving ancestor's new SHA ("" when none survived).
func buildCommitMap(marksFile string, rw *nativeRewriter) (map[string]string, error) {
	data, err := os.ReadFile(marksFile)
	if err != nil {
		return nil, fmt.Errorf("read fast-import marks: %w", err)
	}
	markNew := map[int]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var mark int
		var sha string
		if _, serr := fmt.Sscanf(line, ":%d %s", &mark, &sha); serr != nil {
			continue
		}
		markNew[mark] = sha
	}
	cm := make(map[string]string, len(rw.markOID))
	for mark, oid := range rw.markOID {
		final := rw.resolve(mark)
		cm[oid] = markNew[final] // "" when final==0 or commit-only mark
	}
	return cm, nil
}

// WriteCommitMap renders the map in filter-repo's commit-map format
// (old new per line, "0000…" for vanished) for the manifest directory.
func WriteCommitMap(path string, cm map[string]string) error {
	keys := make([]string, 0, len(cm))
	for k := range cm {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		v := cm[k]
		if v == "" {
			v = strings.Repeat("0", len(k))
		}
		b.WriteString(k)
		b.WriteByte(' ')
		b.WriteString(v)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
