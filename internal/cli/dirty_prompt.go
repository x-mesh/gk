package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// promptStashDirty offers an interactive stash-and-continue when an
// otherwise-blocking dirty tree is detected. Returns:
//   - (true, nil)              user picked stash; caller should treat
//     the tree as clean from here and pop later
//   - (false, errSkipDirty)    user cancelled / esc — caller should abort
//   - (false, err)             unexpected error
//
// On non-TTY environments the call returns errSkipDirty so the caller
// can fall back to its non-interactive hint path (e.g. "use --autostash").
//
// stashLabel is included in the stash message so users can identify
// gk-created stashes in `gk stash`.
func promptStashDirty(ctx context.Context, runner git.Runner, stashLabel string) (stashed bool, err error) {
	if !ui.IsTerminal() {
		return false, errSkipDirty
	}
	statusOut, _, _ := runner.Run(ctx, "status", "--short")
	body := strings.TrimRight(string(statusOut), "\n")
	if body == "" {
		body = "(git reports a dirty tree but `git status --short` is empty)"
	}
	choice, perr := ui.ScrollSelectTUI(ctx,
		"working tree has uncommitted changes",
		body,
		[]ui.ScrollSelectOption{
			{Key: "s", Value: "stash", Display: "stash & continue (auto-pop on success)", IsDefault: true},
			{Key: "c", Value: "cancel", Display: "cancel"},
		})
	if perr != nil {
		if errors.Is(perr, ui.ErrPickerAborted) {
			return false, errSkipDirty
		}
		return false, perr
	}
	if choice != "stash" {
		return false, errSkipDirty
	}
	created, sErr := stashIfChanged(ctx, runner, "push", "--include-untracked", "-m", stashLabel)
	if sErr != nil {
		return false, WithHint(
			fmt.Errorf("stash before continue: %w", sErr),
			"git failed to write the index. run `gk doctor` to inspect (lock file? in-progress merge?).")
	}
	if !created {
		// stash push reported success but did not produce a new entry —
		// the dirty signal came from a diff git stash silently ignores
		// (submodule pointer, mode bits). Surface a hint and treat the
		// tree as effectively clean so the caller does not pop a stash
		// that does not exist.
		hint := describeDirtyButNotStashed(ctx, runner)
		if hint == "" {
			hint = "stash push reported success but produced no entry; the dirty signal is something git stash skips by default"
		}
		fmt.Fprintf(os.Stderr, "warning: no stash created — %s\n", hint)
		return false, nil
	}
	return true, nil
}

// errSkipDirty signals that the caller should abandon the operation —
// either because the user cancelled the stash prompt or because we're
// running on a non-TTY without explicit --yes/--autostash.
var errSkipDirty = errors.New("dirty: skip")
