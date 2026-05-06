// Package forget implements `gk forget` — history-rewriting removal of
// files that should never have been committed (DBs, secrets, build
// artefacts). The package wraps `git filter-repo` and adds gk-flavour
// safety: ref backups, origin URL restoration, and a tracked-but-ignored
// auto-detection step that turns ".gitignore + gk forget" into a one-line
// recovery for the common case.
//
// The package itself does not call filter-repo synchronously — Run lives
// in runner.go and is exported separately so the cobra command can
// orchestrate preview, confirm, and execute in three independent steps.
package forget

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// filepathMatch is a thin alias around filepath.Match exposed as a var so
// tests could swap it for a doublestar matcher in the future without
// rewriting callers. Today it is just filepath.Match.
var filepathMatch = filepath.Match

// FilterKept returns the subset of `paths` whose entries do NOT match any
// of the supplied keep patterns. Patterns use filepath.Match syntax (the
// same shape gk's privacy_gate and ai-commit deny_paths use), so users
// can write `db/keep/*` without learning a new glob dialect.
//
// Each path is checked against the patterns themselves and against every
// directory prefix — so `db/keep/foo.txt` matches a pattern of `db/keep`
// or `db/keep/*`. This mirrors how `.gitignore` rules work in spirit:
// matching a directory keeps everything underneath it.
//
// Invalid patterns return an error so the user gets a clean diagnostic
// instead of silent "did not match" surprise.
func FilterKept(paths, patterns []string) ([]string, error) {
	if len(patterns) == 0 {
		return paths, nil
	}
	// Validate patterns up-front — filepath.Match returns ErrBadPattern
	// only when the pattern is actually checked against an input, but we
	// want users to see the error without waiting for a non-matching
	// path to surface it.
	for _, pat := range patterns {
		if _, err := filepathMatch(pat, ""); err != nil && err.Error() != "" {
			// filepathMatch wraps filepath.Match; the empty-input call
			// returns false,nil for a syntactically valid pattern and
			// false,ErrBadPattern for an invalid one.
			return nil, fmt.Errorf("invalid --keep pattern %q: %w", pat, err)
		}
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !anyKeepMatch(p, patterns) {
			out = append(out, p)
		}
	}
	return out, nil
}

// anyKeepMatch returns true when path matches any keep pattern, either
// directly or via a parent directory. The directory walk is what makes
// `--keep db/keep` strip `db/keep/data.sqlite` even though the literal
// path is longer than the pattern.
func anyKeepMatch(path string, patterns []string) bool {
	candidates := []string{path}
	for dir := path; ; {
		next := dirOf(dir)
		if next == dir || next == "." || next == "" {
			break
		}
		candidates = append(candidates, next)
		dir = next
	}
	for _, pat := range patterns {
		for _, c := range candidates {
			ok, _ := filepathMatch(pat, c)
			if ok {
				return true
			}
		}
	}
	return false
}

// dirOf returns the directory of p without depending on filepath, which
// would normalise separators in a way that breaks git-style forward-slash
// paths on Windows. Internal git paths are always forward-slash.
func dirOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

// AutoDetectIgnored returns the list of paths that are currently tracked
// in the index but would be ignored by .gitignore — i.e. paths the user
// added before adding the matching ignore rule. This is the natural
// target set for `.gitignore + gk forget` workflows.
//
// Implementation uses `git ls-files -i -c --exclude-standard`, which is
// the same query `git status` runs internally for its "tracked but
// ignored" warning. Output is sorted, de-duplicated, and free of empty
// lines so callers can hand it straight to filter-repo.
func AutoDetectIgnored(ctx context.Context, r git.Runner) ([]string, error) {
	stdout, _, err := r.Run(ctx, "ls-files", "-i", "-c", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("ls-files: %w", err)
	}
	return splitNullTerminated(stdout), nil
}

// PathInHistory reports, for each candidate path, whether at least one
// commit in the entire history (--all) ever touched the path. Used as a
// quick "is this even worth rewriting?" filter before invoking
// filter-repo, since filter-repo on a path that does not appear is a
// no-op that still rewrites every commit SHA.
//
// Returns the subset of `paths` that have history hits, preserving the
// caller's order. An empty input returns an empty slice with no error.
func PathInHistory(ctx context.Context, r git.Runner, paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		// `git log --all --pretty=format:%H -1 -- <p>` returns the most
		// recent SHA touching the path, or empty if none. Cheaper than
		// counting all commits.
		stdout, _, err := r.Run(ctx, "log", "--all", "--pretty=format:%H", "-1", "--", p)
		if err != nil {
			return nil, fmt.Errorf("log -- %s: %w", p, err)
		}
		if strings.TrimSpace(string(stdout)) != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// CountTouchingCommits returns the number of commits across all refs that
// modified any of the given paths. Used in the preview step so the user
// can see how invasive the rewrite will be before consenting.
//
// Counts each commit once even if it touches multiple target paths,
// matching what a user actually cares about ("how many SHAs change?").
func CountTouchingCommits(ctx context.Context, r git.Runner, paths []string) (int, error) {
	if len(paths) == 0 {
		return 0, nil
	}
	args := []string{"log", "--all", "--pretty=format:%H", "--"}
	args = append(args, paths...)
	stdout, _, err := r.Run(ctx, args...)
	if err != nil {
		return 0, fmt.Errorf("count touching commits: %w", err)
	}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			seen[line] = struct{}{}
		}
	}
	return len(seen), nil
}

// splitNullTerminated turns a NUL-separated stdout buffer into a
// deduplicated, sorted, non-empty []string. NUL is used so paths
// containing newlines or shell metacharacters do not corrupt the list —
// matches what `ls-files -z` and `for-each-ref -z` produce.
func splitNullTerminated(b []byte) []string {
	parts := strings.Split(string(b), "\x00")
	seen := map[string]struct{}{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			seen[p] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
