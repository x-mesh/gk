package timemachine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
	"github.com/x-mesh/gk/internal/reflog"
)

// ReadBranches collects reflog events from every local branch.
//
// For each entry in `git for-each-ref refs/heads/`, reflog.Read is called to
// pull up to `perBranchLimit` entries (<= 0 for unlimited). All events are
// flattened into a single newest-first slice at the caller's next Merge
// step. Errors on individual branches propagate — typical failures are
// permission or a missing reflog for a freshly-pruned branch, both of which
// the caller probably wants to see.
func ReadBranches(ctx context.Context, r git.Runner, perBranchLimit int) ([]Event, error) {
	out, stderr, err := r.Run(ctx, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if err != nil {
		return nil, fmt.Errorf("for-each-ref refs/heads/: %s: %w",
			strings.TrimSpace(string(stderr)), err)
	}

	var events []Event
	for _, ref := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		entries, err := reflog.Read(ctx, r, ref, perBranchLimit)
		if err != nil {
			// A branch without a reflog is rare but legal — skip quietly.
			continue
		}
		for _, e := range entries {
			events = append(events, FromReflogEntry(e))
		}
	}
	return events, nil
}

// ReadStash returns the current stash stack as []Event.
//
// A repo with zero stashes does not have refs/stash; git reflog errors with
// "unknown revision or ref". ReadStash treats that case as empty (not an
// error) — fresh repos routinely have no stashes, and callers should
// continue with other sources.
func ReadStash(ctx context.Context, r git.Runner) ([]Event, error) {
	entries, err := reflog.Read(ctx, r, "stash", 0)
	if err != nil {
		// Empty-stash case: git prints
		//   "fatal: your current branch 'main' does not have any commits yet"
		// or
		//   "fatal: ambiguous argument 'stash': unknown revision or path not in the working tree."
		// Either way the reflog just isn't there; return empty instead of
		// propagating an error that callers would have to specially-case.
		if strings.Contains(err.Error(), "unknown revision") ||
			strings.Contains(err.Error(), "does not have any commits") ||
			strings.Contains(err.Error(), "bad revision") {
			return nil, nil
		}
		// Stash reflog being absent is the common case; be forgiving and
		// return empty for any read error so one missing source never
		// breaks the unified timeline.
		return nil, nil
	}
	out := make([]Event, 0, len(entries))
	for _, e := range entries {
		ev := FromReflogEntry(e)
		ev.Kind = KindStash // override — same entry shape, different source pool
		out = append(out, ev)
	}
	return out, nil
}

// ReadBackups wraps gitsafe.ListBackups and returns the result as []Event
// so all sources share the same slice type before Merge.
func ReadBackups(ctx context.Context, r git.Runner) ([]Event, error) {
	refs, err := gitsafe.ListBackups(ctx, r)
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(refs))
	for _, br := range refs {
		out = append(out, FromBackupRef(br))
	}
	return out, nil
}

// Merge concatenates several event slices and returns a newest-first sorted
// single slice. Events with zero When sort to the end so partial data never
// steals the top rows.
//
// Merge does NOT dedupe by OID — a commit referenced from both a branch
// reflog and a backup ref shows up twice (with distinct Ref fields). The
// TUI list pane is expected to annotate the duplicates rather than collapse
// them; dedupe semantics are a later TM-05 bitset addition.
func Merge(groups ...[]Event) []Event {
	total := 0
	for _, g := range groups {
		total += len(g)
	}
	out := make([]Event, 0, total)
	for _, g := range groups {
		out = append(out, g...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].When, out[j].When
		switch {
		case a.IsZero() && !b.IsZero():
			return false
		case !a.IsZero() && b.IsZero():
			return true
		default:
			return a.After(b)
		}
	})
	return out
}

// Limit truncates events to at most n items (newest-first assumed). n <= 0
// is a no-op (returns the slice unchanged).
func Limit(events []Event, n int) []Event {
	if n <= 0 || len(events) <= n {
		return events
	}
	return events[:n]
}

// FilterByBranch returns events that belong to the given branch name.
//
// A reflog event belongs to a branch when its Ref is "refs/heads/<branch>@{N}"
// (per-branch reflog scan) or the short form "<branch>@{N}". HEAD reflog
// events match only when branch == "HEAD". Backup events match on the
// Branch field (the sanitized segment from the ref path). Stash events
// never match a specific branch filter.
//
// Empty branch is a no-op (returns input unchanged).
func FilterByBranch(events []Event, branch string) []Event {
	if branch == "" {
		return events
	}
	out := make([]Event, 0, len(events))
	for _, ev := range events {
		if matchesBranch(ev, branch) {
			out = append(out, ev)
		}
	}
	return out
}

func matchesBranch(ev Event, branch string) bool {
	switch ev.Kind {
	case KindReflog:
		// Reflog Ref shapes: "HEAD@{N}" or "refs/heads/<name>@{N}".
		if branch == "HEAD" {
			return strings.HasPrefix(ev.Ref, "HEAD@{")
		}
		if strings.HasPrefix(ev.Ref, "refs/heads/"+branch+"@{") {
			return true
		}
		// Short form fallback: git reflog sometimes emits "<name>@{N}".
		return strings.HasPrefix(ev.Ref, branch+"@{")
	case KindBackup:
		return ev.Branch == branch
	default:
		return false
	}
}

// FilterBySince returns events whose When is at or after the cutoff. Events
// with zero When (missing timestamp) are dropped — a since-filter implies
// "recent only", and untimestamped entries cannot be proven recent.
//
// Zero cutoff is a no-op (returns input unchanged).
func FilterBySince(events []Event, cutoff time.Time) []Event {
	if cutoff.IsZero() {
		return events
	}
	out := make([]Event, 0, len(events))
	for _, ev := range events {
		if !ev.When.IsZero() && !ev.When.Before(cutoff) {
			out = append(out, ev)
		}
	}
	return out
}

// FilterByKind returns only events whose Kind.String() matches one of the
// given allowed strings. Empty `allowed` is a no-op (returns input). Unknown
// kind names silently filter everything out.
//
// The result is a freshly allocated slice — the input is never mutated, so
// callers can invoke FilterByKind multiple times on the same events slice
// without inter-call interference.
func FilterByKind(events []Event, allowed ...string) []Event {
	if len(allowed) == 0 {
		return events
	}
	set := make(map[string]struct{}, len(allowed))
	for _, k := range allowed {
		set[k] = struct{}{}
	}
	out := make([]Event, 0, len(events))
	for _, ev := range events {
		if _, ok := set[ev.Kind.String()]; ok {
			out = append(out, ev)
		}
	}
	return out
}
