package resolve

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
)

// enrichWithBase reconstructs diff3 (base) information for a conflict whose
// worktree markers carry none. git's default merge.conflictStyle omits the
// base block, which silently disables the mechanical tier's two strongest
// rules (one-side-unchanged-from-base and union additivity) in most repos.
// The three index stages (:1 base, :2 ours, :3 theirs) still hold everything
// needed, so the conflict is re-merged IN MEMORY with `git merge-file
// --diff3` and re-parsed from that — the worktree file is never rewritten.
//
// Safety: the enriched parse is used only when the current worktree content
// byte-matches a pristine plain-style re-merge — any hand-edited conflict is
// left exactly as the user shaped it.
func (r *Resolver) enrichWithBase(ctx context.Context, cf ConflictFile, sp stagePresence) ConflictFile {
	for _, seg := range cf.Segments {
		if seg.Hunk != nil && seg.Hunk.Base != nil {
			return cf // already has diff3 info
		}
	}
	if !(sp.Base && sp.Ours && sp.Theirs) {
		return cf // degenerate shapes are handled elsewhere
	}
	labelOurs, labelTheirs := conflictLabels(cf)
	ours := r.stageContent(ctx, 2, cf.Path)
	base := r.stageContent(ctx, 1, cf.Path)
	theirs := r.stageContent(ctx, 3, cf.Path)
	if ours == nil || base == nil || theirs == nil {
		return cf
	}

	plain, ok := r.mergeFile(ctx, ours, base, theirs, labelOurs, labelTheirs, false)
	if !ok {
		return cf
	}
	current, err := r.readFile(r.absPath(cf.Path))
	if err != nil || !bytes.Equal(bytes.TrimRight(current, "\n"), bytes.TrimRight(plain, "\n")) {
		return cf // hand-edited (or style mismatch) — never second-guess the user
	}

	d3, ok := r.mergeFile(ctx, ours, base, theirs, labelOurs, labelTheirs, true)
	if !ok {
		return cf
	}
	enriched, perr := Parse(cf.Path, d3)
	if perr != nil {
		return cf
	}
	return enriched
}

// conflictLabels returns the marker labels of the first hunk so the
// re-merge reproduces the worktree bytes exactly (labels are part of the
// pristine-content check above).
func conflictLabels(cf ConflictFile) (ours, theirs string) {
	for _, seg := range cf.Segments {
		if seg.Hunk != nil {
			return seg.Hunk.OursLabel, seg.Hunk.TheirsLabel
		}
	}
	return "", ""
}

// mergeFile runs `git merge-file -p [--diff3]` over the three stage contents
// via temp files and returns the merged bytes. merge-file exits with the
// conflict count, so a non-zero exit with output is the EXPECTED case here —
// only empty output counts as failure.
func (r *Resolver) mergeFile(ctx context.Context, ours, base, theirs []byte, labelOurs, labelTheirs string, diff3 bool) ([]byte, bool) {
	dir, err := os.MkdirTemp("", "gk-resolve-merge-")
	if err != nil {
		return nil, false
	}
	defer os.RemoveAll(dir)

	paths := [3]string{filepath.Join(dir, "ours"), filepath.Join(dir, "base"), filepath.Join(dir, "theirs")}
	for i, b := range [][]byte{ours, base, theirs} {
		if err := os.WriteFile(paths[i], b, 0o600); err != nil {
			return nil, false
		}
	}
	args := []string{"merge-file", "-p"}
	if diff3 {
		args = append(args, "--diff3")
	}
	args = append(args,
		"-L", labelOurs,
		"-L", "base",
		"-L", labelTheirs,
		paths[0], paths[1], paths[2],
	)
	out, _, _ := r.Runner.Run(ctx, args...)
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}
