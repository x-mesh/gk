package aicommit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
)

// ApplyOptions configures ApplyMessages.
//
// Trailer, when non-empty, is appended as "Trailer: <value>" to each
// commit (provider + model attribution — opt-in via ai.commit.trailer).
// DryRun prints the plan to stdout but skips `git add`/`commit`.
// SkipGpgKeyCheck is a test hook; real runs should keep false.
type ApplyOptions struct {
	Trailer         string
	DryRun          bool
	SkipGpgKeyCheck bool
	// PrecapturedBackupRef, when non-empty, replaces the backup ref
	// that ApplyMessages would otherwise create at entry. Callers that
	// rewrite history before calling Apply (e.g. WIP chain unwrap) use
	// this so the recorded backup points at the ORIGINAL pre-rewrite
	// HEAD rather than the post-rewrite ancestor.
	PrecapturedBackupRef string
}

// ApplyResult is what the caller receives after ApplyMessages runs.
// CommitShas[i] corresponds to Messages[i]; an empty entry means that
// group failed (DryRun also leaves entries empty).
type ApplyResult struct {
	BackupRef  string
	CommitShas []string
	TreeBefore string
	TreeAfter  string
}

// EnsureBackupRef creates a refs/gk/ai-commit-backup/<branch>/<unix>
// ref pointing at HEAD and returns its full path. Safe on detached
// HEAD — BackupRefName renders the segment as "detached". Returns the
// ref path even when HEAD is unborn (no commits yet); in that case no
// ref is actually created and callers get an empty string.
func EnsureBackupRef(ctx context.Context, runner git.Runner) (string, error) {
	branch, err := currentBranch(ctx, runner)
	if err != nil {
		return "", err
	}
	head, _, err := runner.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		// Unborn branch — nothing to back up yet. Return empty so the
		// caller can still proceed and print a friendly "no backup
		// needed" hint.
		return "", nil
	}
	sha := strings.TrimSpace(string(head))
	if sha == "" {
		return "", nil
	}
	refName := gitsafe.BackupRefName("ai-commit", branch, time.Now())
	if _, _, err := runner.Run(ctx, "update-ref", refName, sha); err != nil {
		return "", fmt.Errorf("aicommit: create backup ref: %w", err)
	}
	return refName, nil
}

// ApplyMessages creates one commit per Message. Files from each group
// are staged with `git add -A -- <files>` — `-A` so unstaged deletions
// and renames stage alongside additions/modifications (plain `git add`
// skips removed paths and fails with "pathspec did not match any
// files"); the trailing `--` keeps filenames starting with `-` from
// being misread.
//
// Files whose deletion is already fully staged (porcelain "D ": gone
// from working tree AND from index, only present in HEAD) are excluded
// from the `git add` invocation — `git add -A` on them still fails the
// pathspec check because nothing matches. The follow-up `git commit --
// <files>` picks the deletion up via the staged HEAD diff.
//
// Rename pair expansion in commit pathspec: staged rename pairs
// (new→orig) are recomputed for each group (a prior group's commit can
// change what is staged). For each group the commit pathspec is expanded
// via expandRenamePairs so that the orig (deletion) side is included
// alongside the new path — preventing dangling staged deletions when the
// grouper only emits the new path. Fully-staged deletions are likewise
// recomputed per group so a deletion committed by group i doesn't make
// group i+1's `git add -A` fail the pathspec check on a path that is no
// longer in the index.
//
// Tree-OID guard: ApplyMessages records `git write-tree` before any
// commit and again after all commits. A mismatch between TreeBefore
// and the ORIGINAL tree (captured upstream) signals drift between
// classification and application — callers should compare before they
// call ApplyMessages, but TreeBefore is returned for the audit trail.
//
// On the first error the function returns the partial result (commits
// already made remain in history; caller can use BackupRef + `gk
// commit --abort` to restore).
func ApplyMessages(ctx context.Context, runner git.Runner, messages []Message, opts ApplyOptions) (ApplyResult, error) {
	result := ApplyResult{}

	if opts.PrecapturedBackupRef != "" {
		// Caller already snapshotted the original HEAD (e.g. before
		// WIP chain unwrap). Don't overwrite — the pre-rewrite ref is
		// the one --abort needs to roll back to.
		result.BackupRef = opts.PrecapturedBackupRef
	} else if !opts.DryRun {
		// No backup ref on dry-run: a preview must not mutate refs, and
		// the written ref would become the LATEST backup — after a
		// partial apply, a follow-up dry-run would silently retarget
		// `gk commit --abort` from the real restore point to the
		// partially-applied HEAD.
		backup, err := EnsureBackupRef(ctx, runner)
		if err != nil {
			return result, err
		}
		result.BackupRef = backup
	}

	before, _, err := runner.Run(ctx, "write-tree")
	if err == nil {
		result.TreeBefore = strings.TrimSpace(string(before))
	}

	for _, m := range messages {
		if opts.DryRun {
			result.CommitShas = append(result.CommitShas, "")
			continue
		}

		// An empty group with AllowEmpty produces a `--allow-empty`
		// commit and touches no pathspec — skip staging entirely so we
		// never stage the whole repo via a zero-pathspec `git add`.
		// But a pathspec-less commit consumes the INDEX, not nothing:
		// anything the user staged outside the plan would ride into this
		// "empty" commit unscanned and unreviewed. Refuse unless the
		// index is clean — the uncovered-files-stay-dirty guarantee and
		// the plan-scoped secret scan both depend on it.
		if len(m.Group.Files) == 0 && m.AllowEmpty {
			staged, _, serr := runner.Run(ctx, "diff", "--cached", "--name-only")
			if serr != nil {
				return result, fmt.Errorf("aicommit: inspect index before --allow-empty: %w", serr)
			}
			if s := strings.TrimSpace(string(staged)); s != "" {
				return result, fmt.Errorf(
					"aicommit: refusing allow_empty commit %q: the index has staged changes outside the plan (%s) — an empty commit would swallow them; commit or unstage them first",
					m.Subject, strings.Join(strings.Split(s, "\n"), ", "))
			}
			msg := formatCommitMessage(m, opts.Trailer)
			stdout, stderr, err := runner.Run(ctx, "commit", "--allow-empty", "-m", msg)
			if err != nil {
				return result, fmt.Errorf("aicommit: git commit --allow-empty: %w (stderr=%s stdout=%s)",
					err, string(stderr), string(stdout))
			}
			result.CommitShas = append(result.CommitShas, parseCommitSha(string(stdout)))
			continue
		}

		// Recompute staged deletions and rename pairs PER GROUP: a
		// previous group's commit can delete or rename files, which
		// shifts what `git add -A` and the commit pathspec must do for
		// later groups. Capturing once before the loop would feed stale
		// data into groups 2..N. Group counts are small (≤10 typically)
		// so two extra status calls per group is negligible.
		stagedDeletes, err := stagedDeletedPaths(ctx, runner)
		if err != nil {
			return result, err
		}
		renamePairs, err := stagedRenamePairs(ctx, runner)
		if err != nil {
			return result, err
		}

		toAdd := filterStaged(m.Group.Files, stagedDeletes)
		if len(toAdd) > 0 {
			// A file git already tracks but that sits inside a gitignored
			// directory (e.g. a config force-added under an ignored data/
			// dir) makes a plain `git add -A -- <path>` fail with "paths are
			// ignored, use -f" — even though the file is tracked. Stage such
			// paths with `git add -u`, which updates tracked entries only and
			// never consults the ignore rules; it stages a tracked file's
			// modification or deletion exactly like -A did. Genuinely new
			// (untracked) paths keep the ignore-respecting `git add -A` so a
			// plan can't smuggle in a truly ignored file.
			tracked, err := trackedPaths(ctx, runner, toAdd)
			if err != nil {
				return result, err
			}
			var update, create []string
			for _, f := range toAdd {
				if _, ok := tracked[f]; ok {
					update = append(update, f)
				} else {
					create = append(create, f)
				}
			}
			for _, step := range []struct {
				flag  string
				files []string
			}{{"-u", update}, {"-A", create}} {
				if len(step.files) == 0 {
					continue
				}
				addArgs := append([]string{"add", step.flag, "--"}, step.files...)
				if _, stderr, err := runner.Run(ctx, addArgs...); err != nil {
					return result, fmt.Errorf("aicommit: git add %s %v: %w (stderr=%s)",
						step.flag, step.files, err, string(stderr))
				}
			}
		}

		msg := formatCommitMessage(m, opts.Trailer)
		commitArgs := []string{"commit", "-m", msg, "--"}
		commitArgs = append(commitArgs, expandRenamePairs(m.Group.Files, renamePairs)...)
		stdout, stderr, err := runner.Run(ctx, commitArgs...)
		if err != nil {
			return result, fmt.Errorf("aicommit: git commit: %w (stderr=%s stdout=%s)",
				err, string(stderr), string(stdout))
		}

		// Extract the new SHA so the caller can print / audit it.
		sha := parseCommitSha(string(stdout))
		result.CommitShas = append(result.CommitShas, sha)
	}

	after, _, err := runner.Run(ctx, "write-tree")
	if err == nil {
		result.TreeAfter = strings.TrimSpace(string(after))
	}

	return result, nil
}

// AbortRestore resets HEAD back to the given backup ref with a hard
// reset. Intended for `gk commit --abort` after partial failure.
// Empty backupRef is a no-op returning nil — callers should branch on
// that case to print a friendly "nothing to abort" message.
func AbortRestore(ctx context.Context, runner git.Runner, backupRef string) error {
	if backupRef == "" {
		return nil
	}
	if _, stderr, err := runner.Run(ctx, "reset", "--hard", backupRef); err != nil {
		return fmt.Errorf("aicommit: git reset --hard %s: %w (stderr=%s)",
			backupRef, err, string(stderr))
	}
	return nil
}

// currentBranch returns the short branch name or empty on detached HEAD.
func currentBranch(ctx context.Context, runner git.Runner) (string, error) {
	out, _, err := runner.Run(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		// Detached HEAD — git exits non-zero. That's fine; downstream
		// uses empty branch segment which BackupRefName renders as
		// "detached".
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

// Header returns the Conventional-Commits header line, "<type>(<scope>):
// <subject>". Any leading prefix the LLM duplicated onto Subject is stripped
// so the line never doubles up to "build: build: ...". This is the single
// source of truth for rendering a message's header — formatCommitMessage (the
// committed message), the plan summary, and the interactive picker all route
// through it, so the preview always matches what actually gets committed.
func (m Message) Header() string {
	var b strings.Builder
	b.WriteString(m.Group.Type)
	if m.Group.Scope != "" {
		b.WriteString("(" + m.Group.Scope + ")")
	}
	if m.Breaking {
		// "!" sits after the optional scope and before the colon —
		// "feat(x)!: ..." — per Conventional Commits. stripConventionalPrefix
		// already tolerates a "!" on the subject, so a duplicated prefix is
		// still collapsed correctly.
		b.WriteString("!")
	}
	b.WriteString(": ")
	b.WriteString(stripConventionalPrefix(m.Subject, m.Group.Type, m.Group.Scope))
	return b.String()
}

// formatCommitMessage joins subject/body/footers/trailer in Conventional
// Commits order: "<type>(<scope>): <subject>\n\n<body>\n\n<footers>\n\n<trailer>".
func formatCommitMessage(m Message, trailer string) string {
	var b strings.Builder
	b.WriteString(m.Header())

	if m.Body != "" {
		b.WriteString("\n\n")
		b.WriteString(strings.TrimSpace(m.Body))
	}

	if len(m.Footers) > 0 || trailer != "" {
		b.WriteString("\n\n")
		for _, f := range m.Footers {
			b.WriteString(f.Token)
			b.WriteString(": ")
			b.WriteString(f.Value)
			b.WriteByte('\n')
		}
		if trailer != "" {
			b.WriteString("AI-Assisted-By: ")
			b.WriteString(trailer)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// stripConventionalPrefix removes a leading "<type>(<scope>)?(!)?: " from
// subject when it duplicates the (type, scope) we're about to prepend.
// Match is case-insensitive on type and tolerates a missing or different
// scope to catch the LLM's most common variants. Only strips a single
// occurrence so legitimate ":" inside the subject is preserved.
func stripConventionalPrefix(subject, gType, gScope string) string {
	s := strings.TrimLeft(subject, " \t")
	if gType == "" {
		return s
	}
	lower := strings.ToLower(s)
	tlower := strings.ToLower(gType)
	if !strings.HasPrefix(lower, tlower) {
		return s
	}
	rest := s[len(gType):]
	// Optional "(scope)" — accept any scope, not just the matching one.
	if strings.HasPrefix(rest, "(") {
		if i := strings.Index(rest, ")"); i > 0 {
			rest = rest[i+1:]
		}
	}
	// Optional breaking-change "!".
	rest = strings.TrimPrefix(rest, "!")
	if !strings.HasPrefix(rest, ": ") && !strings.HasPrefix(rest, ":") {
		return s
	}
	rest = strings.TrimPrefix(rest, ":")
	rest = strings.TrimLeft(rest, " ")
	if rest == "" {
		// Subject was *only* the prefix — keep the original so we don't
		// emit an empty subject; lint will surface it as a real issue.
		return s
	}
	_ = gScope // accepted but not required to match
	return rest
}

// parseCommitSha pulls the short SHA out of `git commit` stdout.
// Output format: "[branch-name 1234567] subject". An empty result
// simply means we couldn't parse — we still succeeded.
func parseCommitSha(stdout string) string {
	line := strings.TrimSpace(stdout)
	if !strings.HasPrefix(line, "[") {
		return ""
	}
	end := strings.IndexByte(line, ']')
	if end < 0 {
		return ""
	}
	inner := line[1:end]
	parts := strings.Fields(inner)
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-1]
}

// stagedDeletedPaths returns the set of paths whose deletion is fully
// staged (HEAD has them, index does not, working tree does not). On
// these paths `git add -A -- <p>` fails with "pathspec did not match
// any files"; the caller filters them out before staging and relies on
// the subsequent `git commit -- <p>` to pick the deletion up via the
// HEAD diff.
//
// Uses `--no-renames` so a rename's "from" path shows up here as a
// deletion — staying consistent with how plain `git add` would view it
// (rename detection is a diff-time heuristic, not a working-copy fact).
func stagedDeletedPaths(ctx context.Context, runner git.Runner) (map[string]struct{}, error) {
	out, _, err := runner.Run(ctx, "diff", "--cached", "--no-renames",
		"--diff-filter=D", "--name-only", "-z")
	if err != nil {
		return nil, fmt.Errorf("aicommit: list staged deletions: %w", err)
	}
	set := map[string]struct{}{}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			set[p] = struct{}{}
		}
	}
	return set, nil
}

// trackedPaths returns the subset of files that git already tracks (i.e.
// have an index entry). It drives the staging-flag split in ApplyMessages:
// a tracked file inside a gitignored directory makes `git add -A -- <path>`
// fail with "paths are ignored, use -f", so such paths are staged with
// `git add -u` instead, which never consults the ignore rules. `git ls-files`
// lists only tracked paths, so any file it echoes back is safe to update;
// the rest are genuinely new and keep the ignore-respecting `git add -A`.
func trackedPaths(ctx context.Context, runner git.Runner, files []string) (map[string]struct{}, error) {
	if len(files) == 0 {
		return nil, nil
	}
	args := append([]string{"ls-files", "-z", "--"}, files...)
	out, _, err := runner.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("aicommit: list tracked paths: %w", err)
	}
	set := make(map[string]struct{}, len(files))
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			set[p] = struct{}{}
		}
	}
	return set, nil
}

// filterStaged drops paths from files that are present in skip.
// Preserves the input order of the survivors so commit args remain
// deterministic.
func filterStaged(files []string, skip map[string]struct{}) []string {
	if len(skip) == 0 {
		return files
	}
	out := make([]string, 0, len(files))
	for _, f := range files {
		if _, gone := skip[f]; gone {
			continue
		}
		out = append(out, f)
	}
	return out
}

// stagedRenamePairs returns a map of newPath → origPath for all staged
// renames (and copies) detected by `git diff --cached --name-status -z -M`.
//
// The -z output format uses NUL as the field separator:
//   - non-rename/copy records: <status>\0<path>\0
//   - rename/copy records:     <statusScore>\0<origPath>\0<newPath>\0
//
// The returned map lets callers include the orig (deletion) side in a
// commit pathspec so that rename pairs are never split across commits.
func stagedRenamePairs(ctx context.Context, runner git.Runner) (map[string]string, error) {
	out, _, err := runner.Run(ctx, "diff", "--cached", "--name-status", "-z", "-M")
	if err != nil {
		return nil, fmt.Errorf("aicommit: list staged renames: %w", err)
	}
	pairs := map[string]string{}
	fields := strings.Split(string(out), "\x00")
	for i := 0; i < len(fields); {
		status := fields[i]
		if status == "" {
			i++
			continue
		}
		// Rename (R) and Copy (C) records start with the letter followed by
		// an optional similarity score (e.g. "R100", "C075").
		if len(status) >= 1 && (status[0] == 'R' || status[0] == 'C') {
			if i+2 >= len(fields) {
				break
			}
			origPath := fields[i+1]
			newPath := fields[i+2]
			if origPath != "" && newPath != "" {
				pairs[newPath] = origPath
			}
			i += 3
		} else {
			// Non-rename record: status + single path.
			i += 2
		}
	}
	return pairs, nil
}

// expandRenamePairs returns files with each entry that has a rename orig
// (as recorded in pairs) followed immediately by that orig path, deduped.
// If pairs is empty the input slice is returned unchanged.
// Input order is preserved; orig paths are inserted directly after their
// corresponding new path.
func expandRenamePairs(files []string, pairs map[string]string) []string {
	if len(pairs) == 0 {
		return files
	}
	seen := make(map[string]struct{}, len(files)*2)
	out := make([]string, 0, len(files)*2)
	for _, f := range files {
		if _, dup := seen[f]; !dup {
			seen[f] = struct{}{}
			out = append(out, f)
		}
		if orig, ok := pairs[f]; ok {
			if _, dup := seen[orig]; !dup {
				seen[orig] = struct{}{}
				out = append(out, orig)
			}
		}
	}
	return out
}

// Satisfy a lint check that we use the provider package (Messages
// reference provider.Group/Footer indirectly).
var _ = provider.Locality("")
