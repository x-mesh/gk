package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// Only untracked files that a package manager or the OS regenerates qualify.
// Anything a human could have authored (notes, review output, scripts) must
// stay an unknown that blocks removal.
var disposableUntrackedNames = map[string]bool{
	"package-lock.json": true,
	"yarn.lock":         true,
	"pnpm-lock.yaml":    true,
	"bun.lock":          true,
	"bun.lockb":         true,
	".DS_Store":         true,
}

// worktreeUntrackedAt lists the untracked paths of one worktree and reports
// whether untracked files are its only change. Paths are repo-root relative.
// A scan failure returns (nil, false) so callers keep the worktree.
func worktreeUntrackedAt(ctx context.Context, wtPath string) (untracked []string, onlyUntracked bool) {
	out, _, err := (&git.ExecRunner{Dir: wtPath}).Run(ctx, "status", "--porcelain", "-z")
	if err != nil {
		return nil, false
	}
	return parseUntrackedPorcelainZ(string(out))
}

func parseUntrackedPorcelainZ(raw string) (untracked []string, onlyUntracked bool) {
	onlyUntracked = true
	records := strings.Split(raw, "\x00")
	for i := 0; i < len(records); i++ {
		rec := records[i]
		if len(rec) < 4 {
			continue
		}
		x, y := rec[0], rec[1]
		if x == '?' && y == '?' {
			untracked = append(untracked, rec[3:])
			continue
		}
		onlyUntracked = false
		// A rename/copy record is followed by its source path as a bare field.
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			i++
		}
	}
	return untracked, onlyUntracked
}

func isDisposableUntracked(p string) bool {
	if p == "" || strings.HasSuffix(p, "/") {
		return false
	}
	return disposableUntrackedNames[path.Base(p)]
}

// disposableUntrackedOnly returns the files to delete when untracked,
// regenerable files are the worktree's only change; nil otherwise.
func disposableUntrackedOnly(untracked []string, onlyUntracked bool) []string {
	if !onlyUntracked || len(untracked) == 0 {
		return nil
	}
	for _, p := range untracked {
		if !isDisposableUntracked(p) {
			return nil
		}
	}
	return untracked
}

// removeDisposableFiles deletes the scanned files before a plain (non-force)
// `git worktree remove`, so git itself still refuses if anything else
// appeared since the scan.
func removeDisposableFiles(root string, files []string) error {
	for _, f := range files {
		if !filepath.IsLocal(f) {
			return fmt.Errorf("refusing to delete %q outside the worktree", f)
		}
		if err := os.Remove(filepath.Join(root, f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("delete %s: %w", f, err)
		}
	}
	return nil
}
