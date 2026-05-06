package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// stashIfChanged runs `git stash push <args>` and reports whether the
// command actually created a new stash entry, defending against the
// dirty-but-not-stashable trap where:
//
//   - `git status --porcelain -uno` reports dirty (e.g. a submodule
//     pointer changed, or only a file mode-bit flipped),
//   - `git stash push` exits 0 but prints "No local changes to save"
//     because it skips submodule and mode-only diffs by default.
//
// Without this guard, callers that pop unconditionally hit the canonical
// "No stash entries found" failure several seconds later, after fetch
// and integration have already run.
//
// Detection strategy: capture refs/stash before and after the push and
// compare. A new entry advances refs/stash; a no-op leaves it untouched
// (or absent on both sides if the stack was empty to begin with).
//
// stashArgs should be everything after the literal "stash" subcommand,
// e.g. ("push", "--include-untracked", "-m", "gk pull autostash").
func stashIfChanged(ctx context.Context, r git.Runner, stashArgs ...string) (bool, error) {
	before := stashTip(ctx, r)

	args := append([]string{"stash"}, stashArgs...)
	if _, errOut, err := r.Run(ctx, args...); err != nil {
		return false, fmt.Errorf("stash push: %s: %w", strings.TrimSpace(string(errOut)), err)
	}

	after := stashTip(ctx, r)
	return before != after, nil
}

// stashTip returns the SHA of refs/stash, or "" when no stash exists.
// `git rev-parse --quiet --verify refs/stash` is the cheapest way to
// distinguish "no stash" from "stash present" in a single round-trip.
// Errors are swallowed because the only expected error is the absent-
// ref case, which we represent as the empty string.
func stashTip(ctx context.Context, r git.Runner) string {
	out, _, _ := r.Run(ctx, "rev-parse", "--quiet", "--verify", "refs/stash")
	return strings.TrimSpace(string(out))
}

// describeDirtyButNotStashed returns a short hint identifying why a
// dirty tree did not produce a stash entry. Empty string when no
// distinctive cause is found, in which case callers should fall back to
// a generic message.
//
// Two common causes are surfaced explicitly because they are the bulk
// of real reports:
//
//   - submodule pointer mismatch — `git diff --submodule` would show
//     the change but `git stash push` ignores submodules unless
//     `--recurse-submodules` is set;
//   - mode-bit-only diff — `git diff` shows `old mode / new mode` lines
//     and stash treats these as no-op.
func describeDirtyButNotStashed(ctx context.Context, r git.Runner) string {
	if subOut, _, err := r.Run(ctx, "submodule", "status"); err == nil {
		for _, line := range strings.Split(string(subOut), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// Lines starting with '+' or '-' indicate the working tree
			// differs from the recorded submodule SHA, which is the
			// state that git stash silently skips.
			if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
				return "submodule pointer differs from the recorded commit; git stash ignores this without --recurse-submodules. inspect: git submodule status / git diff --submodule"
			}
		}
	}
	// Mode-bit diff detection — `git diff --raw HEAD` covers staged and
	// unstaged in one query. Lines look like:
	//
	//   :100644 100755 abcd... efgh... M  path     (staged mode change)
	//   :100644 100755 abcd... 0000... M  path     (unstaged mode change)
	//
	// Field 0 is the old mode, field 1 the new mode. When they differ we
	// flag the mode hint regardless of whether the new-side blob hash is
	// also different — even mixed content+mode diffs are worth pointing
	// at, since core.filemode mismatches across machines are a frequent
	// "stash silently dropped my changes" report.
	if rawOut, _, err := r.Run(ctx, "diff", "--raw", "--no-renames", "HEAD"); err == nil {
		for _, line := range strings.Split(string(rawOut), "\n") {
			fields := strings.Fields(strings.TrimPrefix(line, ":"))
			if len(fields) < 4 {
				continue
			}
			if fields[0] != fields[1] {
				return "file-mode bits differ between index and working tree; git stash treats mode-only diffs as no-op. inspect: git diff (look for `old mode/new mode` lines) — chmod to align or commit the mode change"
			}
		}
	}
	return ""
}
