package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
)

// Worktree state of a local branch. Unknown means git status failed in the
// worktree: it is not reported as clean.
const (
	dashTreeNone    = ""
	dashTreeClean   = "clean"
	dashTreeDirty   = "dirty"
	dashTreeUnknown = "unknown"
)

// dashLocalBranch is one local branch and the worktree that has it checked
// out, if any.
type dashLocalBranch struct {
	Name     string
	Tip      string
	Upstream string // short form, e.g. origin/fix/a; "" when unset
	Worktree string // path; "" when no worktree has the branch
	Current  bool   // the worktree is the one gk dash runs in
	Tree     string // dashTree* for the worktree; dashTreeNone without one
}

// dashBaseBehind is how far a local base branch trails its remote branch.
type dashBaseBehind struct {
	Base   string
	Behind int
}

type dashLocal struct {
	Remote   string
	Branches []dashLocalBranch
	Behind   []dashBaseBehind
}

// collectDashLocal reads the local branches, their worktrees and worktree
// state, and how far each base in bases trails <remote>/<base>. It reads only:
// no fetch, no ref or index change.
func collectDashLocal(ctx context.Context, runner git.Runner, remote string, bases []string) (dashLocal, error) {
	loc := dashLocal{Remote: remote}
	out, _, err := runner.Run(ctx, "for-each-ref", "--format=%(refname:short)%00%(objectname)%00%(upstream:short)", "refs/heads")
	if err != nil {
		return loc, fmt.Errorf("list local branches: %w", err)
	}
	byName := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\x00")
		if len(f) != 3 || f[0] == "" {
			continue
		}
		byName[f[0]] = len(loc.Branches)
		loc.Branches = append(loc.Branches, dashLocalBranch{Name: f[0], Tip: f[1], Upstream: f[2]})
	}

	wtRaw, _, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return loc, fmt.Errorf("list worktrees: %w", err)
	}
	var entries []dashWorktree
	for _, e := range parseWorktreePorcelain(string(wtRaw)) {
		if i, ok := byName[e.Branch]; ok && !e.Detached && !e.Bare {
			entries = append(entries, dashWorktree{entry: e, branch: i})
		}
	}
	plain := make([]WorktreeEntry, len(entries))
	for i, e := range entries {
		plain[i] = e.entry
	}
	statuses := scanWorktreeListStatuses(ctx, plain)
	top := dashToplevel(ctx, runner)
	for i, e := range entries {
		b := &loc.Branches[e.branch]
		b.Worktree = e.entry.Path
		b.Current = top != "" && samePath(e.entry.Path, top)
		switch st := statuses[i]; {
		case st.err != nil:
			b.Tree = dashTreeUnknown
		case st.dirty != nil:
			b.Tree = dashTreeDirty
		default:
			b.Tree = dashTreeClean
		}
	}

	for _, base := range bases {
		local, remoteRef := "refs/heads/"+base, "refs/remotes/"+remote+"/"+base
		if !refExists(ctx, runner, local) || !refExists(ctx, runner, remoteRef) {
			continue
		}
		_, behind := countAheadBehind(ctx, runner, local, remoteRef)
		loc.Behind = append(loc.Behind, dashBaseBehind{Base: base, Behind: behind})
	}
	return loc, nil
}

type dashWorktree struct {
	entry  WorktreeEntry
	branch int
}

func dashToplevel(ctx context.Context, runner git.Runner) string {
	out, _, err := runner.Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// samePath compares two paths after resolving symlinks: on macOS a temp dir
// is /var/... in one listing and /private/var/... in another.
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// linkedBranches returns the local branches of pr. A same-repository PR
// matches a branch that tracks <remote>/<head>, or one with no upstream and
// the head's name. A fork PR matches only a branch at the PR's head commit:
// its head name can collide with an unrelated local branch.
func (loc dashLocal) linkedBranches(pr ghapi.DashPR) []dashLocalBranch {
	var out []dashLocalBranch
	for _, b := range loc.Branches {
		var ok bool
		if pr.CrossRepo {
			ok = pr.HeadOID != "" && b.Tip == pr.HeadOID
		} else {
			ok = b.Upstream == loc.Remote+"/"+pr.HeadRef || (b.Upstream == "" && b.Name == pr.HeadRef)
		}
		if ok {
			out = append(out, b)
		}
	}
	return out
}
