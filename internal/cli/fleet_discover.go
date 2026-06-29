package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// repoIdent identifies one repository in a multi-repo fleet: a display label and
// the canonical root (its main worktree path).
type repoIdent struct {
	Label string
	Root  string
}

// defaultFleetExclude are directory names never worth descending into while
// scanning for repos. Combined with any user-supplied excludes.
var defaultFleetExclude = []string{"node_modules", "vendor", "target", "dist", "build", ".archive"}

// fleetConcurrency caps concurrent git subprocesses across a whole fleet gather
// (shared by the per-worktree enrich goroutines). Read-only status probes, so a
// small multiple of cores is safe; clamped to keep process pressure predictable.
func fleetConcurrency() int {
	n := runtime.NumCPU()
	if n > 8 {
		n = 8
	}
	if n < 2 {
		n = 2
	}
	return n
}

// newFleetLimiter is a counting semaphore: acquire by sending, release by
// receiving. A nil limiter (not produced here) means unbounded.
func newFleetLimiter(n int) chan struct{} {
	if n < 1 {
		n = 1
	}
	return make(chan struct{}, n)
}

// repoRootAndCommonDir resolves a path to its repo top-level and git-common-dir.
// The common-dir is the dedup key: a repo reached via a symlink, or through one
// of its linked worktrees, resolves to the same common-dir and so collapses to a
// single fleet entry instead of being counted several times.
func repoRootAndCommonDir(ctx context.Context, path string) (root, common string, ok bool) {
	runner := &git.ExecRunner{Dir: path}
	stdout, _, err := runner.Run(ctx, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return "", "", false
	}
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	if len(lines) < 2 {
		return "", "", false
	}
	root = strings.TrimSpace(lines[0])
	common = strings.TrimSpace(lines[1])
	if resolved, e := filepath.EvalSymlinks(common); e == nil {
		common = resolved
	}
	return root, common, true
}

// discoverRepos resolves the multi-repo set from explicit paths and scan roots,
// deduped by git-common-dir. An explicit path that is not a git repo is an error
// (the user named it); a non-git directory found while scanning is skipped.
func discoverRepos(ctx context.Context, explicit, scanRoots, exclude []string, depth int) ([]repoIdent, error) {
	seen := map[string]repoIdent{}
	addRepo := func(path string) bool {
		root, common, ok := repoRootAndCommonDir(ctx, path)
		if !ok {
			return false
		}
		if _, dup := seen[common]; !dup {
			seen[common] = repoIdent{Label: filepath.Base(root), Root: root}
		}
		return true
	}

	for _, p := range explicit {
		p = expandHome(p)
		if !addRepo(p) {
			return nil, fmt.Errorf("fleet: not a git repository: %s", p)
		}
	}

	for _, sr := range scanRoots {
		scanForRepos(expandHome(sr), depth, exclude, addRepo)
	}

	ids := make([]repoIdent, 0, len(seen))
	for _, id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].Root < ids[j].Root })
	return ids, nil
}

// scanForRepos walks root up to maxDepth directories deep, registering each git
// repo it finds (without descending into it) and skipping excluded names.
func scanForRepos(root string, maxDepth int, exclude []string, add func(string) bool) {
	root = filepath.Clean(root)
	all := append(append([]string{}, defaultFleetExclude...), exclude...)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil //nolint:nilerr // unreadable entries are skipped, not fatal
		}
		if path != root && fleetExcluded(d.Name(), all) {
			return fs.SkipDir
		}
		if _, e := os.Stat(filepath.Join(path, ".git")); e == nil {
			add(path)
			return fs.SkipDir // a repo: do not descend further (skips submodules too)
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return fs.SkipDir
		}
		depth := 0
		if rel != "." {
			depth = strings.Count(rel, string(filepath.Separator)) + 1
		}
		if depth >= maxDepth {
			return fs.SkipDir
		}
		return nil
	})
}

func fleetExcluded(name string, patterns []string) bool {
	for _, p := range patterns {
		if name == p {
			return true
		}
		if m, _ := filepath.Match(p, name); m {
			return true
		}
	}
	return false
}
