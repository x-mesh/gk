package gitsafe

import (
	"context"
	"errors"
	"fmt"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

// Report captures the result of a Preflight check. Callers typically branch
// on report.OK() or report.Err() — the latter produces the canonical error
// with a hint string that matches gk's existing UX.
//
// Callers that intentionally proceed on a dirty tree (e.g. `gk wipe`) should
// call report.AllowDirty().Err() to suppress the dirty-tree signal while
// still rejecting an in-progress rebase/merge/cherry-pick.
type Report struct {
	InProgress gitstate.StateKind
	Dirty      bool
}

// OK reports whether the working tree is safe for a HEAD-moving operation.
func (r Report) OK() bool {
	return r.InProgress == gitstate.StateNone && !r.Dirty
}

// Err returns the canonical error for the detected state, preserving the
// exact message strings that gk v0.x surfaces so existing test fixtures and
// user muscle memory remain stable.
func (r Report) Err() error {
	if r.InProgress != gitstate.StateNone {
		return fmt.Errorf("in-progress %s; run `gk continue` or `gk abort` first", r.InProgress)
	}
	if r.Dirty {
		return errors.New("working tree has uncommitted changes; commit or stash first")
	}
	return nil
}

// AllowDirty returns a copy of the report with the dirty-tree signal cleared.
// Used by destructive commands (`gk wipe`) that intentionally discard local
// changes: the in-progress guard still fires, but dirty tree is tolerated.
func (r Report) AllowDirty() Report {
	r.Dirty = false
	return r
}

type checkOpts struct {
	workDir string
}

// Option customizes Check's behavior.
type Option func(*checkOpts)

// WithWorkDir pins the working directory used for filesystem-based git-state
// detection. Required for tests that use a FakeRunner, and any caller whose
// runner is not an *git.ExecRunner. Without this option, Check uses the
// process cwd — which is almost always wrong for callers that construct a
// runner with an explicit Dir.
func WithWorkDir(dir string) Option {
	return func(o *checkOpts) { o.workDir = dir }
}

// Check runs the standard gk preflight: gitstate in-progress detection plus
// dirty-tree detection. Returns a Report the caller can interpret. Any
// underlying failure (filesystem error reading .git state, or git-status
// failure) is returned as the second value.
func Check(ctx context.Context, r git.Runner, opts ...Option) (Report, error) {
	o := checkOpts{}
	for _, opt := range opts {
		opt(&o)
	}

	var rep Report

	state, err := gitstate.Detect(ctx, o.workDir)
	if err != nil {
		return rep, err
	}
	if state != nil {
		rep.InProgress = state.Kind
	}

	dirty, err := git.NewClient(r).IsDirty(ctx)
	if err != nil {
		return rep, fmt.Errorf("status: %w", err)
	}
	rep.Dirty = dirty

	return rep, nil
}
