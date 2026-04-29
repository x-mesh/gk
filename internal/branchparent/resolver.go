package branchparent

import (
	"context"

	"github.com/x-mesh/gk/internal/git"
)

// Source describes where a resolved parent value came from. Callers display
// it for transparency (e.g., `gk status -v` showing "inferred from reflog")
// and use it to decide whether to warn — explicit values get less verbose
// fallback messaging than inferred ones.
type Source string

const (
	// SourceExplicit means the parent came from `branch.<name>.gk-parent`.
	SourceExplicit Source = "explicit"
	// SourceInferred means the parent was deduced from git history.
	// Reserved for Phase 2; the Phase 1 inferrer always returns "".
	SourceInferred Source = "inferred"
)

// Resolver is the read-path API: it knows about explicit config, inference,
// and ref existence. It is the single entry point used by status/switch/
// worktree so base-resolution logic does not fork across commands.
//
// Resolver does NOT validate writes — that's validateParent in this package.
// It only reads and decides.
type Resolver struct {
	c *git.Client
}

// NewResolver wraps a *git.Client.
func NewResolver(c *git.Client) *Resolver { return &Resolver{c: c} }

// ResolveParent attempts to find the parent of branch. Returns
// (parent, source, ok). ok=false means no parent is configured AND no
// inference succeeded — callers should fall back to their default base.
//
// Resolution order:
//  1. `branch.<branch>.gk-parent` (explicit)
//  2. inferParent (Phase 2 — currently a stub returning "")
//
// When the explicit value names a ref that does not exist locally,
// ResolveParent returns ("", "", false) so callers fall back. The caller
// is responsible for surfacing a one-line warning; we don't print here
// because Resolver has no writer.
func (r *Resolver) ResolveParent(ctx context.Context, branch string) (string, Source, bool) {
	if branch == "" {
		return "", "", false
	}
	cfg := NewConfig(r.c)
	explicit, err := cfg.GetParent(ctx, branch)
	if err == nil && explicit != "" {
		if r.parentRefExists(ctx, explicit) {
			return explicit, SourceExplicit, true
		}
		// Explicit but the ref is gone — fall through to inference / fail.
		// Caller decides whether to warn (it has the writer).
		return "", "", false
	}
	if inferred := r.inferParent(ctx, branch); inferred != "" {
		if r.parentRefExists(ctx, inferred) {
			return inferred, SourceInferred, true
		}
	}
	return "", "", false
}

// ResolveBase returns the effective comparison base for branch: the parent
// when one resolves, otherwise cfgBase. Designed as a drop-in replacement
// for callers that previously passed cfgBase directly to rev-list.
//
// cfgBase is whatever the caller would have used absent parent metadata —
// typically status's resolveBaseForStatus output. ResolveBase never
// returns "" unless the caller passes "" — it only swaps cfgBase out when
// a parent is resolvable.
func (r *Resolver) ResolveBase(ctx context.Context, branch, cfgBase string) string {
	if parent, _, ok := r.ResolveParent(ctx, branch); ok {
		return parent
	}
	return cfgBase
}

// ResolveBaseExplained is like ResolveBase but also returns the source so
// status can print verbose diagnostics ("from feat/A (parent of feat/sub)")
// without re-querying. base = parent when ok, else cfgBase + Source="".
func (r *Resolver) ResolveBaseExplained(ctx context.Context, branch, cfgBase string) (base string, source Source, parentResolved bool) {
	if parent, src, ok := r.ResolveParent(ctx, branch); ok {
		return parent, src, true
	}
	return cfgBase, "", false
}

// Issue describes a recoverable problem encountered during resolution that
// the caller should surface (typically a one-line stderr warning). The
// resolver itself never prints — it has no writer — but it tells the
// caller what to say.
type Issue struct {
	// Code is a stable machine-readable identifier ("parent-missing",
	// "parent-is-tag", etc.). Reserved for future expansion; Phase 1
	// only emits "parent-missing".
	Code string
	// Parent is the offending value (the explicit gk-parent that no
	// longer resolves to a local ref).
	Parent string
	// Message is a human-readable, single-line description suitable
	// for stderr — already including the parent name.
	Message string
}

// ResolveBaseWithIssues is the noisy variant of ResolveBase: it returns the
// effective base AND any problems that justified a fallback. Status uses
// this to print a one-line warning when the user has set a gk-parent that
// no longer exists, instead of silently using cfgBase and leaving them in
// the dark.
//
// When no explicit parent is configured, issues is empty even if inference
// would have failed — absent inference is the normal path, not a problem.
func (r *Resolver) ResolveBaseWithIssues(ctx context.Context, branch, cfgBase string) (base string, source Source, issues []Issue) {
	if branch == "" {
		return cfgBase, "", nil
	}
	cfg := NewConfig(r.c)
	explicit, err := cfg.GetParent(ctx, branch)
	if err != nil {
		// Read failure is rare (broken config); surface but don't block.
		return cfgBase, "", []Issue{{
			Code:    "parent-read-error",
			Message: "warning: could not read gk-parent: " + err.Error(),
		}}
	}
	if explicit != "" {
		if r.parentRefExists(ctx, explicit) {
			return explicit, SourceExplicit, nil
		}
		// Explicit but ref gone — record an issue and fall back.
		issues = append(issues, Issue{
			Code:    "parent-missing",
			Parent:  explicit,
			Message: "warning: parent " + explicit + " not found (deleted?); using " + cfgBase,
		})
		return cfgBase, "", issues
	}
	if inferred := r.inferParent(ctx, branch); inferred != "" {
		if r.parentRefExists(ctx, inferred) {
			return inferred, SourceInferred, nil
		}
	}
	return cfgBase, "", nil
}

// parentRefExists reports whether `refs/heads/<parent>` resolves locally.
// We deliberately reject `refs/remotes/...` and tag refs by checking the
// heads namespace specifically — parent must be a local branch (validated
// at write time, but we re-check on read because the ref could be deleted
// after set).
func (r *Resolver) parentRefExists(ctx context.Context, parent string) bool {
	return git.RefExists(ctx, r.c.Raw(), "refs/heads/"+parent)
}

// inferParent is the Phase 2 hook. Phase 1 ships a stub so the Resolver
// API is final and callers don't change shape when inference lands.
//
// When implemented, the algorithm reads the oldest reflog entry for the
// branch (its branchpoint commit), then queries `for-each-ref --contains`
// to find local branch tips that point to or descend from that commit.
// A single candidate wins; zero or multiple → ambiguous → "".
func (r *Resolver) inferParent(ctx context.Context, branch string) string {
	// Phase 1: no inference. Reserved for Phase 2.
	_ = ctx
	_ = branch
	return ""
}

// String makes Source play nicely with fmt %s.
func (s Source) String() string { return string(s) }
