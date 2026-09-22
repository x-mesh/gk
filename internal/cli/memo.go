package cli

import (
	"strings"
	"sync"
)

// --- scope-bounded memoisation -----------------------------------------------
//
// These caches all answer one shape of question: "for this worktree or
// repository, what does git say about state X?" Keying an entry by the state
// itself makes a hit exact — a moved commit tip or a rewritten config file is a
// different key, never a wrong answer — but it also leaves the previous key
// behind on every move, and `gk watch` is built to stay up for days.
//
// A scoped memo keeps the exactness and drops the accumulation. The SCOPE (a
// worktree, a repository, a pair of refs) is the map key and holds exactly one
// entry, tagged with the fingerprint that produced it. A fingerprint change
// REPLACES that entry instead of adding one beside it, which loses nothing: the
// superseded answer can never be asked for again, because the inputs that would
// key it have already moved.
//
// What that bounds is growth per COMMIT, which is what ran away before: a scope
// holds one entry no matter how far its refs travel. Scopes themselves are
// named after refs, so a branch that existed and was then deleted leaves its
// one entry behind — the standing cost is (worktrees × branches ever seen)
// rather than (branches × commits). Far flatter, and small enough to leave
// alone, but not a constant.

type memoEntry[V any] struct {
	fingerprint string
	value       V
}

// scopedMemo holds one entry per scope. The zero value is ready to use.
type scopedMemo[V any] struct{ m sync.Map }

// load returns the value memoised for scope while its fingerprint still
// matches. An empty scope or fingerprint means "not memoisable" and never hits.
func (c *scopedMemo[V]) load(scope, fingerprint string) (V, bool) {
	var zero V
	if scope == "" || fingerprint == "" {
		return zero, false
	}
	v, ok := c.m.Load(scope)
	if !ok {
		return zero, false
	}
	e, valid := v.(memoEntry[V])
	if !valid || e.fingerprint != fingerprint {
		return zero, false
	}
	return e.value, true
}

// store replaces whatever this scope held. A scope or fingerprint that is empty
// is not memoisable, so the call is a no-op.
func (c *scopedMemo[V]) store(scope, fingerprint string, value V) {
	if scope == "" || fingerprint == "" {
		return
	}
	c.m.Store(scope, memoEntry[V]{fingerprint: fingerprint, value: value})
}

// do returns the memoised value for scope, calling compute when the scope is
// unseen or its fingerprint has moved. Callers that must decide whether to
// compute before spawning work — a fan-out that would otherwise start a
// goroutine per cache hit — use load and store directly instead.
//
// compute reports whether its answer may be kept. A probe that failed for a
// reason the fingerprint cannot see — a timeout, an unusable ref — reports
// false, or that failure freezes in place until the inputs happen to move.
func (c *scopedMemo[V]) do(scope, fingerprint string, compute func() (V, bool)) V {
	if v, ok := c.load(scope, fingerprint); ok {
		return v
	}
	value, keep := compute()
	if keep {
		c.store(scope, fingerprint, value)
	}
	return value
}

// twoRefScope names a comparison between two refs in one working directory.
// The directory belongs in it because the tips these scopes carry are SHORT
// hashes: 7 hex chars collide across repositories far too easily to key a
// process-wide cache on alone.
//
// An EMPTY directory is a valid scope, not a missing one. --repo defaults to
// empty, so `gk switch`, `gk worktree` and a single-repo `gk watch` all build
// their runner with Dir: "" — which means the process's own working directory,
// one repository that does not change while it runs. Rejecting it turned these
// memos off on exactly the paths they were written for.
func twoRefScope(dir, a, b string) string {
	if a == "" || b == "" {
		return ""
	}
	return strings.Join([]string{dir, a, b}, "\x00")
}

// twoTipFingerprint is the state a two-ref comparison depends on. It is empty
// when either tip is unknown: a hit would then answer for commits that may
// since have moved, so the caller measures every time instead.
func twoTipFingerprint(aTip, bTip string) string {
	if aTip == "" || bTip == "" {
		return ""
	}
	return aTip + "\x00" + bTip
}
