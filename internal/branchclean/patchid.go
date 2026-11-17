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
// which keeps it listed as unmerged: the safe direction.
const patchIDBaseLimit = 1000

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
	out, _, err := r.Run(ctx, "merge-base", base, branch)
	if err != nil {
		return false, err
	}
	mb := strings.TrimSpace(string(out))

	names, _, err := r.Run(ctx, "diff", "--name-only", "--no-renames", "-z", mb, branch)
	if err != nil {
		return false, err
	}
	var paths []string
	for _, p := range strings.Split(string(names), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return false, nil
	}

	diff, _, err := r.Run(ctx, append(append([]string{"diff"}, patchIDDiffArgs...), mb, branch)...)
	if err != nil {
		return false, err
	}
	ids, err := patchIDs(ctx, st, diff)
	if err != nil || len(ids) == 0 {
		return false, err
	}

	args := append([]string{"log", "-p", "--no-merges", "--format=commit %H"}, patchIDDiffArgs...)
	args = append(args, "--max-count="+strconv.Itoa(patchIDBaseLimit), mb+".."+base, "--")
	args = append(args, pathspecLiterals(paths)...)
	log, _, err := r.Run(ctx, args...)
	if err != nil {
		return false, err
	}
	baseIDs, err := patchIDs(ctx, st, log)
	if err != nil {
		return false, err
	}
	for _, id := range baseIDs {
		if id == ids[0] {
			return true, nil
		}
	}
	return false, nil
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

// patchIDs runs git patch-id --stable over patch and returns the ids in
// order.
func patchIDs(ctx context.Context, st git.Streamer, patch []byte) ([]string, error) {
	if len(bytes.TrimSpace(patch)) == 0 {
		return nil, nil
	}
	var out bytes.Buffer
	if err := st.Stream(ctx, bytes.NewReader(patch), &out, "patch-id", "--stable"); err != nil {
		return nil, err
	}
	var ids []string
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) >= 1 {
			ids = append(ids, f[0])
		}
	}
	return ids, sc.Err()
}
