package branchclean

import (
	"bufio"
	"bytes"
	"context"
	"strconv"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// patchIDBaseLimit caps how many base commits past the merge base are hashed
// for one branch. A branch whose squash lies further back is not matched,
// which keeps it listed as unmerged: the safe direction. A variable so a test
// can reach the limit with a few commits.
var patchIDBaseLimit = 1000

// patchIDDiffArgs fixes the diff options on both sides, so a diff algorithm,
// external diff, or textconv in the user's config cannot make two equal
// changes hash differently.
var patchIDDiffArgs = []string{"--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--diff-algorithm=myers", "-U3"}

// patchIDMerged reports whether base holds a commit whose patch-id equals the
// branch's whole change since its merge base: the GitHub "Squash & Merge"
// shape, which merge-tree reports as a conflict once the base changes the
// same lines again. It writes no objects. An empty change matches nothing,
// and a runner that cannot stream stdin gives false, so the check only ever
// adds branches it has proven merged.
//
// The base commits are read only for the paths the branch changed: a squash
// of the branch changes exactly those paths, so its diff is complete, and the
// log stays small on a long-lived base.
//
// Like git cherry, patch-id hashes the changed lines with their context, so a
// squash whose context lines differ from the branch's is not matched.
func patchIDMerged(ctx context.Context, r git.Runner, base, branch string) (bool, error) {
	st, ok := r.(git.Streamer)
	if !ok {
		return false, nil
	}
	ch, err := branchChange(ctx, r, st, base, branch)
	if err != nil || ch.id == "" {
		return false, err
	}

	args := append([]string{"log", "-p", "--no-merges", "--format=commit %H"}, patchIDDiffArgs...)
	args = append(args, "--max-count="+strconv.Itoa(patchIDBaseLimit), ch.mergeBase+".."+base, "--")
	args = append(args, pathspecLiterals(ch.paths)...)
	log, _, err := r.Run(ctx, args...)
	if err != nil {
		return false, err
	}
	baseIDs, err := patchIDs(ctx, st, log)
	if err != nil {
		return false, err
	}
	for _, e := range baseIDs {
		if e.id == ch.id {
			return true, nil
		}
	}
	return false, nil
}

// branchPatch is a branch's whole change since its merge base with base. An
// empty id means the change is empty and matches nothing.
type branchPatch struct {
	mergeBase string
	paths     []string
	id        string
}

func branchChange(ctx context.Context, r git.Runner, st git.Streamer, base, branch string) (branchPatch, error) {
	out, _, err := r.Run(ctx, "merge-base", base, branch)
	if err != nil {
		return branchPatch{}, err
	}
	ch := branchPatch{mergeBase: strings.TrimSpace(string(out))}

	names, _, err := r.Run(ctx, "diff", "--name-only", "--no-renames", "-z", ch.mergeBase, branch)
	if err != nil {
		return branchPatch{}, err
	}
	for _, p := range strings.Split(string(names), "\x00") {
		if p != "" {
			ch.paths = append(ch.paths, p)
		}
	}
	if len(ch.paths) == 0 {
		return ch, nil
	}

	diff, _, err := r.Run(ctx, append(append([]string{"diff"}, patchIDDiffArgs...), ch.mergeBase, branch)...)
	if err != nil {
		return branchPatch{}, err
	}
	ids, err := patchIDs(ctx, st, diff)
	if err != nil || len(ids) == 0 {
		return branchPatch{}, err
	}
	ch.id = ids[0].id
	return ch, nil
}

// patchIDMergedSet is patchIDMerged over several branches with one base log
// instead of one per branch: the path-limited log -p is most of the cost.
//
// The log covers every path the branches changed, from a common ancestor of
// their merge bases, so a hit is accepted only when the commit is not in the
// branch's own history: a change that was reverted and then re-applied on a
// later branch matches the old commit, which is not a squash of this branch.
// A base commit that also changes another branch's paths hashes differently
// than with the branch's paths alone, so it no longer matches; a squash
// changes only its branch's paths. When the log reaches patchIDBaseLimit, a
// branch it did not match is checked alone, so the reach per branch stays
// what patchIDMerged gives. A branch whose check fails is in errs, not in
// merged.
func patchIDMergedSet(ctx context.Context, r git.Runner, base string, branches []string) (merged map[string]bool, errs map[string]error) {
	merged = map[string]bool{}
	errs = map[string]error{}
	alone := func(bs []string) {
		for _, b := range bs {
			if ok, err := patchIDMerged(ctx, r, base, b); err != nil {
				errs[b] = err
			} else if ok {
				merged[b] = true
			}
		}
	}
	st, ok := r.(git.Streamer)
	if !ok {
		return merged, errs
	}
	if len(branches) < 2 {
		alone(branches)
		return merged, errs
	}

	changes := map[string]branchPatch{}
	var pending, mergeBases, paths []string
	seenPath := map[string]bool{}
	oneLinePaths := true
	for _, b := range branches {
		ch, err := branchChange(ctx, r, st, base, b)
		if err != nil {
			errs[b] = err
			continue
		}
		if ch.id == "" {
			continue
		}
		changes[b] = ch
		pending = append(pending, b)
		mergeBases = append(mergeBases, ch.mergeBase)
		for _, p := range ch.paths {
			if strings.ContainsAny(p, "\n\r") {
				oneLinePaths = false // the log reads pathspecs one per line
			}
			if !seenPath[p] {
				seenPath[p] = true
				paths = append(paths, p)
			}
		}
	}
	if len(pending) < 2 || !oneLinePaths {
		alone(pending)
		return merged, errs
	}

	out, _, err := r.Run(ctx, append([]string{"merge-base", "--octopus"}, mergeBases...)...)
	from := strings.TrimSpace(string(out))
	if err != nil || from == "" {
		alone(pending)
		return merged, errs
	}

	var stdin strings.Builder
	stdin.WriteString("^" + from + "\n" + base + "\n--\n")
	for _, p := range pathspecLiterals(paths) {
		stdin.WriteString(p + "\n")
	}
	args := append([]string{"log", "--stdin", "-p", "--no-merges", "--format=commit %H"}, patchIDDiffArgs...)
	args = append(args, "--max-count="+strconv.Itoa(patchIDBaseLimit))
	var log bytes.Buffer
	if err := st.Stream(ctx, strings.NewReader(stdin.String()), &log, args...); err != nil {
		alone(pending)
		return merged, errs
	}
	capped := countCommitHeaders(log.Bytes()) >= patchIDBaseLimit
	baseIDs, err := patchIDs(ctx, st, log.Bytes())
	if err != nil {
		alone(pending)
		return merged, errs
	}
	commits := map[string][]string{}
	for _, e := range baseIDs {
		commits[e.id] = append(commits[e.id], e.commit)
	}

	var unmatched []string
	for _, b := range pending {
		ok, err := forkedAfter(ctx, r, commits[changes[b].id], changes[b].mergeBase)
		switch {
		case err != nil:
			errs[b] = err
		case ok:
			merged[b] = true
		default:
			unmatched = append(unmatched, b)
		}
	}
	if capped {
		alone(unmatched)
	}
	return merged, errs
}

// forkedAfter reports whether one of commits is outside the history of
// mergeBase, that is, in mergeBase..base.
func forkedAfter(ctx context.Context, r git.Runner, commits []string, mergeBase string) (bool, error) {
	for _, c := range commits {
		_, _, err := r.Run(ctx, "merge-base", "--is-ancestor", c, mergeBase)
		if err == nil {
			continue
		}
		if git.IsExitCode(err, 1) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

// countCommitHeaders counts the "commit %H" lines of a log -p. Diff lines
// always start with a prefix character or a diff header, never with
// "commit ".
func countCommitHeaders(log []byte) int {
	n := bytes.Count(log, []byte("\ncommit "))
	if bytes.HasPrefix(log, []byte("commit ")) {
		n++
	}
	return n
}

// pathspecLiterals marks each path literal, so a name with *, ?, or a leading
// colon is not read as pathspec magic.
func pathspecLiterals(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = ":(literal)" + p
	}
	return out
}

// patchIDEntry is one line of git patch-id output: the id of a patch and the
// commit it came from (zeros for a patch without a commit header).
type patchIDEntry struct {
	id, commit string
}

// patchIDs runs git patch-id --stable over patch and returns the entries in
// order. patch-id skips empty diffs, so the entries do not line up with the
// commits of the input.
func patchIDs(ctx context.Context, st git.Streamer, patch []byte) ([]patchIDEntry, error) {
	if len(bytes.TrimSpace(patch)) == 0 {
		return nil, nil
	}
	var out bytes.Buffer
	if err := st.Stream(ctx, bytes.NewReader(patch), &out, "patch-id", "--stable"); err != nil {
		return nil, err
	}
	var ids []patchIDEntry
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		e := patchIDEntry{id: f[0]}
		if len(f) >= 2 {
			e.commit = f[1]
		}
		ids = append(ids, e)
	}
	return ids, sc.Err()
}
