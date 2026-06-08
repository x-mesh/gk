package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/ui"
)

// hintError wraps an error with a short "next step" hint rendered after the
// primary error line. The hint is advisory and does not affect errors.Is /
// errors.As chains — the wrapped error is always reachable via Unwrap.
type hintError struct {
	err  error
	hint string
}

func (e *hintError) Error() string { return e.err.Error() }
func (e *hintError) Unwrap() error { return e.err }

// WithHint decorates err with a one-line remediation hint. Passing a nil err
// returns nil. An empty hint is ignored (err is returned unchanged).
func WithHint(err error, hint string) error {
	if err == nil {
		return nil
	}
	if hint = strings.TrimSpace(hint); hint == "" {
		return err
	}
	return &hintError{err: err, hint: hint}
}

// HintFrom walks the error chain and returns the first hint found, or "".
func HintFrom(err error) string {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if he, ok := e.(*hintError); ok && strings.TrimSpace(he.hint) != "" {
			return he.hint
		}
	}
	return ""
}

// FormatError returns the user-facing representation of an error raised by
// cli.Execute. Renders as:
//
//	gk: <error message>          (red)
//	  hint: <hint>                (magenta label, faint body)
//
// When Easy Mode is active and --json is not set, the error is formatted
// with emoji and beginner-friendly language via the EasyFormatter.
func FormatError(err error) string {
	if err == nil {
		return ""
	}

	// git 저장소 밖에서 실행된 명령은 git의 길고 불친절한 raw fatal
	// ("fatal: not a git repository ...")을 던진다. 명령마다 진입부 가드를
	// 다는 대신, 모든 에러가 지나는 이 단일 지점에서 표준 안내로 바꾼다.
	// 명령이 이미 자체 hint를 달아 의도적으로 처리했다면(status/diff 등)
	// 그 메시지를 존중해 건드리지 않는다.
	if HintFrom(err) == "" && isNotAGitRepoError(err) {
		err = WithHint(
			fmt.Errorf("git 저장소가 아닙니다"),
			"git init 으로 저장소를 초기화하거나, 올바른 디렉토리로 이동하세요",
		)
	}

	// Easy Mode branch: use EasyFormatter for friendlier output.
	// Skip when --json is active (Property 9: JSON Mode Bypass).
	if eng := EasyEngine(); eng != nil && eng.IsEnabled() && !JSONOut() {
		// Wire the engine's emoji mapper through so FormatError can
		// prefix ❌ / 💡. Previously this branch built the formatter
		// twice with a nil mapper, defeating the very emoji prefix
		// Easy Mode is supposed to add.
		fmtr := ui.NewEasyFormatter(eng.Emoji(), NoColorFlag())

		// Translate raw error text only. Hints come from the i18n
		// catalog already in user-language form — running them through
		// TranslateTerms a second time mangles the literal commands
		// they exist to suggest (e.g. "→ gk commit" becomes
		// "→ gk 변경사항 저장 (commit)" because \bcommit\b matches the
		// command token in the already-translated string).
		translated := eng.TranslateTerms(err.Error())
		hint := HintFrom(err)
		return fmtr.FormatError(fmt.Errorf("%s", translated), hint)
	}

	prefix := color.New(color.FgRed, color.Bold).Sprint("gk:")
	out := prefix + " " + err.Error()
	if h := HintFrom(err); h != "" {
		hintLabel := color.New(color.FgMagenta, color.Bold).Sprint("hint:")
		hintBody := color.New(color.Faint).Sprint(h)
		out += "\n  " + hintLabel + " " + hintBody
	}
	return out
}

// hintCommand is a compact helper so call sites read like:
//
//	return WithHint(err, hintCommand("gk continue"))
func hintCommand(cmd string) string { return fmt.Sprintf("try: %s", cmd) }

// inProgressOp returns the user-facing name of an in-progress git operation
// that `gk continue` / `gk abort` can resolve (rebase / merge / cherry-pick /
// revert). It returns "" for a nil state, StateNone, or StateBisect — the
// operations those two commands do not handle.
func inProgressOp(state *gitstate.State) string {
	if state == nil {
		return ""
	}
	switch state.Kind {
	case gitstate.StateRebaseMerge, gitstate.StateRebaseApply:
		return "rebase"
	case gitstate.StateMerge:
		return "merge"
	case gitstate.StateCherryPick:
		return "cherry-pick"
	case gitstate.StateRevert:
		return "revert"
	default:
		return ""
	}
}

// inProgressHint returns a remediation hint when git is mid-operation
// (rebase / merge / cherry-pick / revert) and that operation is what blocks the
// command the user just ran. It names the operation and points at the two real
// ways out — `gk continue` (finish) or `gk abort` (cancel) — instead of
// `gk switch`, which git refuses while an operation is in progress.
//
// Returns "" when there is no resolvable in-progress operation (see
// inProgressOp): callers should fall back to their default hint.
func inProgressHint(state *gitstate.State) string {
	op := inProgressOp(state)
	if op == "" {
		return ""
	}
	return fmt.Sprintf(
		"%s in progress — finish it with 'gk continue' (after resolving with 'gk resolve') or cancel with 'gk abort'",
		op,
	)
}

// isNotAGitRepoError reports whether err originates from running git outside a
// repository. We check both the wrapped message and the ExitError's stderr so
// the detection survives any chain wrapping done above the runner layer. The
// match is case-insensitive: a hard `fatal: not a git repository` and the
// softer `warning: Not a git repository` that `git diff --no-index` emits are
// both the same "you're not in a repo" condition.
func isNotAGitRepoError(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(strings.ToLower(err.Error()), "not a git repository") {
		return true
	}
	var exitErr *git.ExitError
	if errors.As(err, &exitErr) && strings.Contains(strings.ToLower(exitErr.Stderr), "not a git repository") {
		return true
	}
	return false
}

// ensureGitRepo returns a not-a-git-repo error (which FormatError renders as the
// standard "git 저장소가 아닙니다" guidance) when the working directory is not
// inside a repository. Most commands need no such guard — git's own stderr
// flows up to FormatError untouched. This exists for the few whose first git
// call swallows that stderr behind a sentinel (e.g. DefaultBranch ->
// ErrNoDefaultBranch), which would otherwise surface a misleading "no upstream
// / no base branch" message outside a repo. Call it up front in those commands.
func ensureGitRepo(ctx context.Context, r git.Runner) error {
	_, _, err := r.Run(ctx, "rev-parse", "--git-dir")
	if isNotAGitRepoError(err) {
		return err
	}
	return nil
}
