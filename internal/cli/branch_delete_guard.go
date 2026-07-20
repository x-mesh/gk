package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// errBranchProtected marks a refusal driven by the configured protected
// list, so a caller that has its own confirmation flow can recognise the
// case and offer one instead of dead-ending.
var errBranchProtected = errors.New("branch is protected")

// branchDeleteOpts tunes deleteBranchGuarded.
type branchDeleteOpts struct {
	// Force selects `git branch -D`. It overrides git's own merged check
	// only — never the protected list, which is policy the user wrote
	// down rather than a safety git happens to enforce.
	Force bool
	// AllowProtected lifts the protected refusal. Set it only after the
	// user has confirmed this specific branch; never wire it to a
	// default or to a blanket --force.
	AllowProtected bool
	// SelfCreated marks a branch this very operation created moments ago
	// — a failed `worktree add` rolling back, or `worktree run
	// --cleanup` reclaiming what it made. Such a branch cannot be one the
	// user meant to keep, and it may legitimately share a protected name
	// only by coincidence, so every policy check is skipped.
	SelfCreated bool
}

// deleteBranchGuarded is the single choke point for removing a local
// branch. Every `git branch -d/-D` in this package goes through it so the
// protected-branch policy lives in one place: the previous arrangement
// repeated the checks at each call site, and the `worktree add` orphan
// path was added without them — leaving a protected branch deletable
// whenever no worktree happened to hold it.
func deleteBranchGuarded(ctx context.Context, runner git.Runner, cfg *config.Config, name string, opts branchDeleteOpts) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("no branch to delete")
	}
	if !opts.SelfCreated {
		if err := guardBranchDeletable(ctx, runner, cfg, name, opts.AllowProtected); err != nil {
			return err
		}
	}
	flag := "-d"
	if opts.Force {
		flag = "-D"
	}
	if _, stderr, err := runner.Run(ctx, "branch", flag, name); err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("branch %s %s: %s", flag, name, msg)
	}
	return nil
}

// guardBranchDeletable reports why name must not be deleted, or nil when
// it may go. Worktree occupancy is checked first because git refuses that
// case anyway — surfacing it here just names the holder instead of
// letting a raw git error explain it.
func guardBranchDeletable(ctx context.Context, runner git.Runner, cfg *config.Config, name string, allowProtected bool) error {
	if path := branchWorktreePath(ctx, runner, name); path != "" {
		return WithHint(
			fmt.Errorf("branch %q is checked out in worktree %s", name, path),
			"remove that worktree first, or switch it to another branch")
	}
	if allowProtected {
		return nil
	}
	if isProtectedBranchName(name, protectedBranchNames(cfg)) {
		return WithHint(
			fmt.Errorf("refusing to delete protected branch %q: %w", name, errBranchProtected),
			"listed under branch.protected in your gk config — delete it deliberately with `git branch -D "+name+"`")
	}
	return nil
}

// protectedBranchNames merges the configured protected list with the
// resolved base branch. The base is included because losing it breaks
// every ahead/behind and merge-target computation gk makes, which is the
// same reason main/develop are on the list to begin with — leaving it out
// made protection depend on whether the user remembered to name it twice.
func protectedBranchNames(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	out := append([]string(nil), cfg.Branch.Protected...)
	if b := strings.TrimSpace(cfg.BaseBranch); b != "" && !isProtectedBranchName(b, out) {
		out = append(out, b)
	}
	return out
}

// branchWorktreePath returns the path of the worktree holding name, or ""
// when none does. It reports the holder where branchInUse only answers
// yes/no, so a refusal can point at the worktree to deal with.
func branchWorktreePath(ctx context.Context, runner git.Runner, name string) string {
	out, _, err := runner.Run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	for _, e := range parseWorktreePorcelain(string(out)) {
		if e.Branch == name {
			return e.Path
		}
	}
	return ""
}
