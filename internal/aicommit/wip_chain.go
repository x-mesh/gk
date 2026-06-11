package aicommit

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// defaultWIPPatterns are the baked-in subject regexes that mark a
// commit as "WIP-like" — eligible for the gk commit chain unwrap.
//
// Why these specifically:
//   - --wip--    : git stash autosaves use this prefix
//   - wip / WIP  : the most common manual save-point convention
//   - tmp / temp : "I'll fix this later" stub commits
//   - save       : checkpoint commits
//   - checkpoint : same idea, more explicit
//   - fixup!     : git commit --fixup output
//   - squash!    : git commit --squash output
var defaultWIPPatterns = []string{
	`^--wip--`,
	`^[Ww][Ii][Pp]\b`,
	`^[Tt]mp\b`,
	`^[Tt]emp\b`,
	`^[Ss]ave\b`,
	`^[Cc]heckpoint\b`,
	`^fixup!`,
	`^squash!`,
}

const (
	// wipPatternMaxLen caps an individual user pattern length, blocking
	// pathological config from blowing up regexp compile time. RE2
	// already prevents catastrophic match-time backtracking; this just
	// guards compile-side cost.
	wipPatternMaxLen = 1024
	// wipPatternMaxCount caps total custom patterns. Defaults aren't
	// counted.
	wipPatternMaxCount = 100
)

// CompileWIPPatterns merges the baked-in defaults with the caller's
// custom list and returns compiled regexes. Caller patterns ADD to
// the defaults; they don't replace — so users can extend without
// losing well-known cases like git-stash's `--wip--`.
func CompileWIPPatterns(custom []string) ([]*regexp.Regexp, error) {
	if len(custom) > wipPatternMaxCount {
		return nil, fmt.Errorf("aicommit: too many wip patterns (%d > %d)", len(custom), wipPatternMaxCount)
	}
	all := append([]string(nil), defaultWIPPatterns...)
	all = append(all, custom...)
	out := make([]*regexp.Regexp, 0, len(all))
	for _, p := range all {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if len(p) > wipPatternMaxLen {
			return nil, fmt.Errorf("aicommit: wip pattern too long (%d > %d)", len(p), wipPatternMaxLen)
		}
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("aicommit: bad wip pattern %q: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// IsWIPSubject returns true when subject matches any compiled pattern.
//
// Subjects are first stripped of a leading noise prefix (runs of
// backticks and whitespace) before matching. Past LLM runs occasionally
// leaked fenced-code-block markers into the subject line — without this
// normalization a polluted subject like "``` WIP(...)" would slip past
// the WIP-anchored patterns and break chain unwrap.
func IsWIPSubject(subject string, patterns []*regexp.Regexp) bool {
	normalized := stripNoisePrefix(subject)
	for _, re := range patterns {
		if re.MatchString(normalized) {
			return true
		}
	}
	return false
}

// stripNoisePrefix removes leading backticks and Unicode whitespace.
// Intentionally conservative — only the characters we have observed
// contaminating subjects are trimmed, so unrelated punctuation in a
// genuine subject still anchors `^`-based patterns where the user
// expects.
func stripNoisePrefix(s string) string {
	return strings.TrimLeft(s, "` \t\r\n ")
}

// WIPCommit is one entry in the detected unwrap chain.
type WIPCommit struct {
	SHA     string
	Subject string
	Files   []FileChange
}

// DetectWIPChainOptions tunes DetectWIPChain.
type DetectWIPChainOptions struct {
	// MaxChain caps how many commits we'll consider unwrapping. 0
	// falls back to defaultMaxChain (10). Prevents runaway when a
	// user's branch is entirely save-point commits.
	MaxChain int
	// Patterns is the compiled regex list. Use CompileWIPPatterns to
	// build, which already merges defaults + custom.
	Patterns []*regexp.Regexp
	// AllowPushed disables the per-commit `branch -r --contains` gate
	// that stops the walk at the first commit already on a remote. Set
	// by `gk commit --force-wip` when the user explicitly accepts that
	// unwrapping will rewrite already-pushed history (force-push
	// required afterward).
	AllowPushed bool
}

// StopReason is the *why* attached to the end of a DetectWIPChain walk.
// It is reported regardless of whether the returned chain is empty or
// non-empty, so the CLI can explain "we found 2 then stopped at a
// pushed commit" or "we found nothing because HEAD isn't WIP".
type StopReason string

const (
	StopReasonNone           StopReason = ""              // shouldn't happen in practice — kept for zero value
	StopReasonNonWIPSubject  StopReason = "non-wip"       // hit a normal commit (usual stop condition)
	StopReasonDetachedHEAD   StopReason = "detached-head" // refused outright; no branch to recover from
	StopReasonPushed         StopReason = "pushed"        // hit a commit already on a remote
	StopReasonMergeCommit    StopReason = "merge-commit"  // multi-parent — unwrap is risky
	StopReasonRootCommit     StopReason = "root-commit"   // reached repo root (parentless WIP)
	StopReasonMaxChain       StopReason = "max-chain"     // walked MaxChain entries and they were all WIP
	StopReasonShallowHistory StopReason = "shallow"       // HEAD~i missing (shallow clone, etc.)
)

const defaultMaxChain = 10

// DetectWIPChain walks HEAD backward as long as each commit's subject
// matches a WIP pattern, returning the chain in HEAD-first order
// (chain[0] is HEAD, chain[N-1] is the oldest WIP) along with a
// StopReason explaining why the walk ended.
//
// Safety stops (each maps to a StopReason; chain is whatever was
// successfully collected up to that point):
//   - Detached HEAD (`rev-parse --abbrev-ref HEAD` returns "HEAD")
//     → return (nil, StopReasonDetachedHEAD). detached-HEAD reset
//     rewinds the pointer with no branch to recover from.
//   - Commit reachable from any remote tracking branch → stop. Uses
//     `git branch -r --contains <sha>` rather than relying on
//     `@{upstream}` so manually-pushed commits (`git push origin HEAD`
//     without `-u`) are still caught. Bypass with AllowPushed.
//   - Merge commit → stop (multi-parent unwrap is risky).
//   - MaxChain reached → stop, take what we have.
//   - Root commit → include it (diff-tree --root handles file list)
//     but stop walking (no parent left).
//   - First commit that doesn't match a WIP pattern → stop. This is
//     the normal terminator and the most common StopReason.
//
// Note: protected branch names are NOT a stop condition here. The
// per-commit push gate is sufficient — branch-name guarding caused
// the entire feature to refuse silently on `develop`/`main` even when
// every WIP commit was purely local. Callers that need extra restraint
// can set AllowPushed=false (the default) which already keeps pushed
// history untouched.
//
// Any unexpected git error returns (chain-so-far, "", err) — the
// caller should treat that as "skip the unwrap step" rather than
// aborting commit.
func DetectWIPChain(ctx context.Context, runner git.Runner, opts DetectWIPChainOptions) ([]WIPCommit, StopReason, error) {
	max := opts.MaxChain
	if max <= 0 {
		max = defaultMaxChain
	}

	cur, _, err := runner.Run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil {
		curName := strings.TrimSpace(string(cur))
		// Detached HEAD: rev-parse --abbrev-ref returns the literal
		// "HEAD". Refuse — we can't safely rewrite a detached pointer.
		if curName == "HEAD" {
			return nil, StopReasonDetachedHEAD, nil
		}
	}

	var chain []WIPCommit
	for i := 0; i < max; i++ {
		ref := fmt.Sprintf("HEAD~%d", i)
		subjOut, _, err := runner.Run(ctx, "log", "-1", "--format=%s", ref)
		if err != nil {
			// HEAD~i doesn't exist (shallow history). Stop.
			return chain, StopReasonShallowHistory, nil
		}
		subject := strings.TrimSpace(string(subjOut))
		if !IsWIPSubject(subject, opts.Patterns) {
			return chain, StopReasonNonWIPSubject, nil
		}

		shaOut, _, err := runner.Run(ctx, "rev-parse", ref)
		if err != nil {
			return chain, StopReasonShallowHistory, nil
		}
		sha := strings.TrimSpace(string(shaOut))

		parentsOut, _, _ := runner.Run(ctx, "log", "-1", "--format=%P", ref)
		parents := strings.Fields(strings.TrimSpace(string(parentsOut)))
		if len(parents) > 1 {
			return chain, StopReasonMergeCommit, nil
		}

		if !opts.AllowPushed {
			// Push detection — fail-closed against any remote tracking ref.
			// Output non-empty means at least one origin/<x> has the commit.
			remOut, _, _ := runner.Run(ctx, "branch", "-r", "--contains", sha)
			if strings.TrimSpace(string(remOut)) != "" {
				return chain, StopReasonPushed, nil
			}
		}

		files, ferr := wipChainFiles(ctx, runner, sha, len(parents) == 0)
		if ferr != nil {
			return chain, StopReasonNone, ferr
		}
		chain = append(chain, WIPCommit{
			SHA:     sha,
			Subject: subject,
			Files:   files,
		})
		if len(parents) == 0 {
			// Root commit: no parent to walk further. Stop.
			return chain, StopReasonRootCommit, nil
		}
	}
	return chain, StopReasonMaxChain, nil
}

// wipChainFiles returns the file list for a commit. Uses
// `diff-tree --root` for parentless commits so a chain that reaches
// repo root doesn't abort the whole `gk commit` flow.
//
// `-z` produces NUL-separated records and disables C-style quoting,
// so paths containing tabs / non-ASCII bytes survive intact (the
// previous tab-split parser silently dropped such files).
func wipChainFiles(ctx context.Context, runner git.Runner, sha string, rootCommit bool) ([]FileChange, error) {
	if rootCommit {
		out, _, err := runner.Run(ctx, "diff-tree", "--root", "-z", "--name-status", "--no-commit-id", "-r", sha)
		if err != nil {
			return nil, fmt.Errorf("aicommit: wip chain files for root %s: %w", sha, err)
		}
		// diff-tree --root emits each file as ADDED relative to empty
		// tree, which is the correct semantic.
		return parseWIPDiffNameStatus(string(out)), nil
	}
	out, _, err := runner.Run(ctx, "diff", "-z", "--name-status", sha+"^", sha)
	if err != nil {
		return nil, fmt.Errorf("aicommit: wip chain files for %s: %w", sha, err)
	}
	return parseWIPDiffNameStatus(string(out)), nil
}

// parseWIPDiffNameStatus parses `git diff -z --name-status` output
// into FileChange entries.
//
// With `-z`, records are NUL-terminated and there is no C-style
// quoting — paths with tabs/spaces/non-ASCII survive intact. Renames
// (`R<score>`) and copies (`C<score>`) emit THREE NUL-separated
// tokens (status, source, destination); other codes emit two
// (status, path).
//
// After the chain unwrap (`git reset HEAD~N`, mixed), files end up
// as working-tree changes — NOT staged — so Staged=false. ApplyMessages
// re-stages per-group via `git add -A -- <files>` regardless.
func parseWIPDiffNameStatus(out string) []FileChange {
	if out == "" {
		return nil
	}
	toks := strings.Split(out, "\x00")
	var files []FileChange
	i := 0
	for i < len(toks) {
		code := toks[i]
		if code == "" {
			i++
			continue
		}
		i++
		if i >= len(toks) {
			break
		}
		path := toks[i]
		i++
		// R/C codes consume an extra token (the destination); we keep
		// the destination as the canonical Path since callers care
		// about the file's current location.
		if len(code) > 0 && (code[0] == 'R' || code[0] == 'C') {
			if i < len(toks) {
				path = toks[i]
				i++
			}
		}
		if path == "" {
			continue
		}
		status := "modified"
		switch code[0] {
		case 'A':
			status = "added"
		case 'D':
			status = "deleted"
		case 'R':
			status = "renamed"
		case 'C':
			status = "copied"
		}
		files = append(files, FileChange{
			Path:   path,
			Status: status,
			Staged: false,
		})
	}
	return files
}

// ChainNetFiles returns the files that actually differ between the
// chain's base (HEAD~chainLen) and HEAD — the net diff the unwrap
// (`git reset HEAD~N`, mixed) will leave in the working tree.
//
// This replaced MergeChainFiles, a pure per-commit-status simulation:
// the simulation tracked add→delete cancellation but could not see
// CONTENT cancellation — a later WIP reverting an earlier WIP's edit
// still listed the path, the AI planned a commit for it, and apply
// died on `git commit -- <path>` finding a clean tree after the reset
// ("nothing to commit, working tree clean"). Asking git for the real
// HEAD~N→HEAD diff makes both cancellation kinds fall out for free,
// renames included.
func ChainNetFiles(ctx context.Context, runner git.Runner, chainLen int) ([]FileChange, error) {
	out, stderr, err := runner.Run(ctx, "diff", "-z", "--name-status", fmt.Sprintf("HEAD~%d", chainLen), "HEAD")
	if err != nil {
		return nil, fmt.Errorf("aicommit: chain net diff: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return parseWIPDiffNameStatus(string(out)), nil
}
