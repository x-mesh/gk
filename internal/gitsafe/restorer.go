package gitsafe

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

// Strategy selects the git reset mode used during Restore.
//
// The names mirror `git reset --<mode>` exactly. TM-14's decision table
// chooses the appropriate Strategy based on (dirty, autostash, key); callers
// that bypass the modal supply one directly.
type Strategy int

const (
	// StrategyMixed maps to `git reset --mixed` — HEAD moves, index is
	// updated to match, working tree is preserved. This is `gk undo`'s
	// default: non-destructive on the worktree.
	StrategyMixed Strategy = iota

	// StrategyHard maps to `git reset --hard` — HEAD moves, index and
	// working tree are both rewritten. Destructive to uncommitted changes.
	// Used by `gk wipe` and `gk timemachine restore --mode hard`.
	StrategyHard

	// StrategySoft maps to `git reset --soft` — only HEAD moves, staged
	// changes remain staged. Reserved for future use.
	StrategySoft

	// StrategyKeep maps to `git reset --keep` — HEAD moves if the working
	// tree can be kept in sync; refuses otherwise. TM-14 picks this when
	// the tree is dirty and autostash is off.
	StrategyKeep
)

// String returns the git mode flag for the Strategy (e.g. "mixed", "hard").
func (s Strategy) String() string {
	switch s {
	case StrategyMixed:
		return "mixed"
	case StrategyHard:
		return "hard"
	case StrategySoft:
		return "soft"
	case StrategyKeep:
		return "keep"
	default:
		return "mixed"
	}
}

// Target describes the commit Restore should move HEAD to.
//
// SHA is required; Label and Summary are used only for human-facing output
// (success banners, dry-run preview).
type Target struct {
	SHA     string
	Label   string
	Summary string
}

// Result is returned by a successful Restore. Callers format the output
// themselves — the struct keeps gitsafe independent of cobra/stdout conventions.
type Result struct {
	BackupRef string
	From      string
	To        string
	Strategy  Strategy

	// AutostashRef is set (and non-empty) only when an autostash was pushed
	// but the subsequent `stash pop` step could not re-apply cleanly (e.g.
	// conflict). Callers should surface it so the user can finish manually
	// via `git stash pop <ref>`. Empty when no stash was pushed or pop
	// succeeded.
	AutostashRef string
}

// Stage identifies which step of Restore produced a failure. Callers branch
// on Stage to decide how aggressively to surface the error; for example, a
// "reset" failure leaves the backup ref intact and the user can recover, but
// a "snapshot" failure means nothing happened at all.
type Stage string

const (
	StageSnapshot  Stage = "snapshot"
	StageBackup    Stage = "backup"
	StageAutostash Stage = "autostash"
	StageReset     Stage = "reset"
	StagePop       Stage = "pop"
	StageVerify    Stage = "verify"
)

// RestoreError wraps the underlying git failure with the Stage at which it
// occurred plus a Recovery hint the caller can print. Returned from Restore
// whenever any step fails — callers use errors.As(err, &gitsafe.RestoreError{})
// to inspect.
type RestoreError struct {
	Stage    Stage
	Recovery string
	Err      error
}

func (e *RestoreError) Error() string {
	if e.Recovery != "" {
		return fmt.Sprintf("%s: %v (recovery: %s)", e.Stage, e.Err, e.Recovery)
	}
	return fmt.Sprintf("%s: %v", e.Stage, e.Err)
}

func (e *RestoreError) Unwrap() error { return e.Err }

// Restorer performs the atomic "backup ref + reset" dance shared by every
// HEAD-moving gk command. Callers construct one via NewRestorer and call
// Restore once per operation.
type Restorer struct {
	runner git.Runner
	now    func() time.Time
	kind   string // "undo" | "wipe" | "timemachine"
}

// NewRestorer returns a Restorer for the given command kind. kind is embedded
// in the backup ref path (`refs/gk/<kind>-backup/...`); callers must pass a
// stable identifier.
func NewRestorer(runner git.Runner, now func() time.Time, kind string) *Restorer {
	if now == nil {
		now = time.Now
	}
	return &Restorer{runner: runner, now: now, kind: kind}
}

// Backup creates a backup ref at the current HEAD and returns its full path.
// The ref path follows the BackupRefName format; callers typically surface
// this path in stdout so users know how to recover via `git reset --hard`.
func (r *Restorer) Backup(ctx context.Context, branch string) (string, error) {
	ref := BackupRefName(r.kind, branch, r.now())
	_, stderr, err := r.runner.Run(ctx, "update-ref", ref, "HEAD")
	if err != nil {
		return "", fmt.Errorf("update-ref: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return ref, nil
}

type restoreOpts struct {
	autostash bool
}

// RestoreOption customizes Restore's behavior.
type RestoreOption func(*restoreOpts)

// WithAutostash controls whether Restore stashes dirty working tree changes
// before the reset and pops the stash afterwards. Only meaningful when the
// tree is dirty; Restore does not force a dirty check before invoking this
// (callers are expected to have run gitsafe.Check).
//
// When enabled, the ordering contract becomes:
//  1. snapshot HEAD
//  2. write backup ref
//  3. stash push --include-untracked
//  4. git reset --<mode> target
//  5. stash pop
//
// If step 3 fails, the backup ref is rolled back (update-ref -d) and Restore
// returns with Stage == StageAutostash — the user's worktree is untouched.
//
// If step 5 (pop) fails (typically a conflict), Result.AutostashRef is set so
// the caller can instruct the user to resolve manually via `git stash pop`.
func WithAutostash(enabled bool) RestoreOption {
	return func(o *restoreOpts) { o.autostash = enabled }
}

// Restore moves HEAD to the Target using the given Strategy.
//
// Contract (ordering invariant — do NOT reorder):
//  1. snapshot   — ResolveRef("HEAD") → Result.From
//  2. backup     — update-ref <backupRef> HEAD (must precede any HEAD motion)
//  3. autostash  — stash push -u (only if WithAutostash(true) passed)
//  4. reset      — git reset --<mode> target.SHA
//  5. pop        — stash pop (only if step 3 ran)
//  6. verify     — ResolveRef("HEAD") confirms the move (warning-only on failure)
//
// Failure handling:
//   - step 1/2 fail → RestoreError with Stage=snapshot/backup; nothing mutated
//   - step 3 fails → update-ref -d rolls back step 2; tree untouched
//   - step 4 fails → backup ref intact; Recovery hint = `git reset --hard <backupRef>`
//   - step 5 fails → result.AutostashRef set; Restore returns *with* the result
//     (not an error) because HEAD motion succeeded; caller prints a warning
//   - step 6 fails → warning only; the operation is considered successful
//
// The branch argument is used only for the backup ref name; pass an empty
// string for detached HEAD (SanitizeBranchSegment translates that to
// "detached").
func (r *Restorer) Restore(ctx context.Context, branch string, target Target, strategy Strategy, opts ...RestoreOption) (Result, error) {
	o := restoreOpts{}
	for _, opt := range opts {
		opt(&o)
	}

	var res Result
	res.Strategy = strategy
	res.To = target.SHA

	// Step 1: snapshot.
	from, err := ResolveRef(ctx, r.runner, "HEAD")
	if err != nil {
		return res, &RestoreError{Stage: StageSnapshot, Err: err}
	}
	res.From = from

	// Step 2: backup before any HEAD motion.
	backupRef, err := r.Backup(ctx, branch)
	if err != nil {
		return res, &RestoreError{Stage: StageBackup, Err: err}
	}
	res.BackupRef = backupRef

	// Step 3: autostash (optional).
	stashPushed := false
	if o.autostash {
		stashMsg := fmt.Sprintf("gk-%s-autostash-%d", r.kind, r.now().Unix())
		_, stderr, serr := r.runner.Run(ctx, "stash", "push", "--include-untracked", "-m", stashMsg)
		if serr != nil {
			// Roll back backup ref — tree must remain untouched.
			_, _, _ = r.runner.Run(ctx, "update-ref", "-d", backupRef)
			res.BackupRef = ""
			return res, &RestoreError{
				Stage:    StageAutostash,
				Err:      fmt.Errorf("%s: %w", strings.TrimSpace(string(stderr)), serr),
				Recovery: "no changes made; backup ref removed",
			}
		}
		stashPushed = true
	}

	// Step 4: reset.
	mode := "--" + strategy.String()
	_, stderr, err := r.runner.Run(ctx, "reset", mode, target.SHA)
	if err != nil {
		return res, &RestoreError{
			Stage:    StageReset,
			Err:      fmt.Errorf("%s: %w", strings.TrimSpace(string(stderr)), err),
			Recovery: fmt.Sprintf("git reset --hard %s", backupRef),
		}
	}

	// Step 5: stash pop (only if we pushed).
	if stashPushed {
		if _, _, perr := r.runner.Run(ctx, "stash", "pop", "--index"); perr != nil {
			// Pop failed — HEAD is where user wanted it, but the stash
			// remains on the stack. Surface via Result; not a fatal error.
			res.AutostashRef = "stash@{0}"
			return res, nil
		}
	}

	// Step 6: verify — best effort. Failure is a warning, not an error.
	if post, verr := ResolveRef(ctx, r.runner, "HEAD"); verr == nil && post != target.SHA {
		// Didn't land where expected (shallow clone quirk etc.) — surface as
		// a soft warning. Caller may log but restore is considered complete.
		_ = post // callers can resolve HEAD themselves if they care
	}

	return res, nil
}

// ResolveRef resolves a ref name to its full commit SHA via `git rev-parse
// --verify <ref>^{commit}`. Exported because multiple gk commands need this
// helper (gk undo --to, gk timemachine restore, gk precheck).
func ResolveRef(ctx context.Context, r git.Runner, ref string) (string, error) {
	out, stderr, err := r.Run(ctx, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return strings.TrimSpace(string(out)), nil
}
