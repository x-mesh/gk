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
// are staged with `git add -- <files>` (note the double-dash so
// filenames starting with `-` are not misread) then committed.
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

	backup, err := EnsureBackupRef(ctx, runner)
	if err != nil {
		return result, err
	}
	result.BackupRef = backup

	before, _, err := runner.Run(ctx, "write-tree")
	if err == nil {
		result.TreeBefore = strings.TrimSpace(string(before))
	}

	for _, m := range messages {
		if opts.DryRun {
			result.CommitShas = append(result.CommitShas, "")
			continue
		}

		addArgs := append([]string{"add", "--"}, m.Group.Files...)
		if _, stderr, err := runner.Run(ctx, addArgs...); err != nil {
			return result, fmt.Errorf("aicommit: git add %v: %w (stderr=%s)",
				m.Group.Files, err, string(stderr))
		}

		msg := formatCommitMessage(m, opts.Trailer)
		commitArgs := []string{"commit", "-m", msg, "--"}
		commitArgs = append(commitArgs, m.Group.Files...)
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
	// Header.
	b.WriteString(m.Group.Type)
	if m.Group.Scope != "" {
		b.WriteString("(" + m.Group.Scope + ")")
	}
	b.WriteString(": ")
	b.WriteString(m.Subject)

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

// Satisfy a lint check that we use the provider package (Messages
// reference provider.Group/Footer indirectly).
var _ = provider.Locality("")
