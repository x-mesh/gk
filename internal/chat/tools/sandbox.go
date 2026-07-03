// Package tools implements the read-only tool surface `gk chat` exposes to
// the model: five whitelisted git subcommands and two file tools, every
// invocation passing through the Sandbox and the shared redaction pipeline
// before its result reaches a (remote) provider.
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/x-mesh/gk/internal/aicommit"
)

// Sandbox confines model-driven file access to the repository work tree.
// It is the single enforcement point — prompt-level "please stay inside
// the repo" instructions are advisory, this is not.
//
// Resolution order matters: symlinks are resolved BEFORE the containment
// check (Rel + ".." test), or a symlink inside the repo pointing outside
// would pass on its unresolved name (see internal/cli/ignore.go's
// repoRelPath for the same idiom, and macOS /var → /private/var for why
// the root itself must be canonicalized too).
type Sandbox struct {
	// Root is the EvalSymlinks-canonicalized absolute repo work-tree root.
	Root string
	// DenyGlobs are matched against the repo-relative slash path with
	// aicommit.MatchDeny — the same semantics the commit privacy gate
	// applies to diffs. The list is a UNION of the built-in defaults and
	// config layers; callers must never let a repo-local config shrink it.
	DenyGlobs []string
}

// NewSandbox canonicalizes the repo root and returns a sandbox enforcing
// the given deny globs.
func NewSandbox(repoRoot string, denyGlobs []string) (*Sandbox, error) {
	if strings.TrimSpace(repoRoot) == "" {
		return nil, fmt.Errorf("chat sandbox: empty repo root")
	}
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("chat sandbox: resolve root: %w", err)
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	return &Sandbox{Root: abs, DenyGlobs: denyGlobs}, nil
}

// Resolve validates a model-supplied path and returns its canonical
// absolute form plus the repo-relative slash path. Every rejection reason
// is deliberate:
//
//   - containment: resolved (post-symlink) path must stay under Root —
//     blocks ../ traversal AND in-repo symlinks pointing out
//   - .git: never readable through file tools (credential helpers, hooks)
//   - submodule boundary: an intermediate component owning its own .git
//     is a different repository — outside this sandbox's contract
//   - deny globs: same list, same matcher as the commit privacy gate
func (s *Sandbox) Resolve(p string) (abs, rel string, err error) {
	if strings.TrimSpace(p) == "" {
		return "", "", fmt.Errorf("chat sandbox: empty path")
	}
	joined := filepath.Clean(p)
	if !filepath.IsAbs(joined) {
		joined = filepath.Join(s.Root, joined)
	}
	resolved := canonicalizePath(joined)

	relPath, rErr := filepath.Rel(s.Root, resolved)
	if rErr != nil || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("chat sandbox: %q is outside the repository", p)
	}
	relSlash := filepath.ToSlash(relPath)

	for _, comp := range strings.Split(relSlash, "/") {
		if comp == ".git" {
			return "", "", fmt.Errorf("chat sandbox: %q reaches into .git", p)
		}
	}

	// Submodule boundary: any intermediate directory carrying its own
	// .git (file or dir) is a separate repository with separate trust.
	if dir := s.submoduleAncestor(relSlash); dir != "" {
		return "", "", fmt.Errorf("chat sandbox: %q crosses into submodule %q", p, dir)
	}

	if relSlash != "." {
		if g := aicommit.MatchDeny(relSlash, s.DenyGlobs); g != "" {
			return "", "", fmt.Errorf("chat sandbox: %q is blocked by deny_paths (%s)", p, g)
		}
	}
	return resolved, relSlash, nil
}

// submoduleAncestor returns the first ancestor directory of rel (excluding
// the repo root itself) that contains a .git entry, or "" when none does.
func (s *Sandbox) submoduleAncestor(relSlash string) string {
	if relSlash == "." {
		return ""
	}
	comps := strings.Split(relSlash, "/")
	dir := s.Root
	for i := 0; i < len(comps)-1; i++ {
		dir = filepath.Join(dir, comps[i])
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return filepath.ToSlash(filepath.Join(comps[:i+1]...))
		}
	}
	return ""
}

// canonicalizePath resolves symlinks over the longest existing prefix,
// preserving any non-existent tail. A fully existing path resolves
// directly; a missing leaf resolves its parent chain so a symlinked
// ancestor still cannot smuggle the path outside the root.
func canonicalizePath(abs string) string {
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	dir, base := filepath.Split(filepath.Clean(abs))
	dir = filepath.Clean(dir)
	if dir == abs { // filesystem root
		return abs
	}
	return filepath.Join(canonicalizePath(dir), base)
}
