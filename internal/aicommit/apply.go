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
// (new→orig) are collected once before the loop. For each group the
// commit pathspec is expanded via expandRenamePairs so that the orig
// (deletion) side is included alongside the new path — preventing
// dangling staged deletions when the grouper only emits the new path.
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
	} else {
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

	stagedDeletes, err := stagedDeletedPaths(ctx, runner)
	if err != nil {
		return result, err
	}

	renamePairs, err := stagedRenamePairs(ctx, runner)
	if err != nil {
		return result, err
	}

	for _, m := range messages {
		if opts.DryRun {
			result.CommitShas = append(result.CommitShas, "")
			continue
		}

		toAdd := filterStaged(m.Group.Files, stagedDeletes)
		if len(toAdd) > 0 {
			addArgs := append([]string{"add", "-A", "--"}, toAdd...)
			if _, stderr, err := runner.Run(ctx, addArgs...); err != nil {
				return result, fmt.Errorf("aicommit: git add %v: %w (stderr=%s)",
					toAdd, err, string(stderr))
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

// formatCommitMessage joins subject/body/footers/trailer in Conventional
// Commits order: "<type>(<scope>): <subject>\n\n<body>\n\n<footers>\n\n<trailer>".
func formatCommitMessage(m Message, trailer string) string {
	var b strings.Builder
	// Header. Strip any leading Conventional-Commits prefix the LLM
	// tucked onto Subject so we don't double up to "build: build: ...".
	subject := stripConventionalPrefix(m.Subject, m.Group.Type, m.Group.Scope)
	b.WriteString(m.Group.Type)
	if m.Group.Scope != "" {
		b.WriteString("(" + m.Group.Scope + ")")
	}
	b.WriteString(": ")
	b.WriteString(subject)

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
