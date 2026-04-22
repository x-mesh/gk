package gitsafe

import "errors"

// Keypress identifiers consumed by DecideStrategy. These mirror the keys the
// `gk timemachine` / `gk undo` confirmation modal binds: `y` commits the
// default safe strategy, `R` requests hard reset, `r` asks for a preview only,
// `n`/`Esc` aborts.
const (
	KeyConfirm    = 'y'
	KeyHardReset  = 'R'
	KeyPreview    = 'r'
	KeyAbort      = 'n'
	KeyAbortAlias = 'N'
)

// ErrRequiresForceDiscard is returned by DecideStrategy when the user presses
// `R` on a dirty tree with autostash disabled. Hard reset in that state would
// silently destroy uncommitted work, so the caller must either (a) enable
// autostash, (b) route through `--force-discard`, or (c) clean the tree.
var ErrRequiresForceDiscard = errors.New("hard reset on dirty tree without autostash; pass --force-discard to proceed")

// ErrAbort is returned when the user explicitly declines (`n` / `Esc`).
// Callers treat this as a non-error early exit.
var ErrAbort = errors.New("aborted")

// ErrPreview is returned when the user requests a preview (`r`) without
// committing to the restore. Callers display the dry-run plan and re-prompt.
var ErrPreview = errors.New("preview requested; restore not executed")

// DecideStrategy maps (dirty, autostash, keypress) to the appropriate
// Strategy per the TM-14 hard-reset decision table (SYN_v2.1):
//
//	| dirty | autostash | key      | → Strategy         |
//	|-------|-----------|----------|--------------------|
//	| false |    —      | y        | Mixed              |
//	| false |    —      | R        | Hard               |
//	| true  |  true     | y        | Hard (+ autostash) |
//	| true  |  false    | y        | Keep               |
//	| true  |  false    | R        | ErrRequiresForceDiscard |
//	| any   |    —      | r        | ErrPreview         |
//	| any   |    —      | n/Esc    | ErrAbort           |
//
// When err != nil, the returned Strategy has no meaning and should be
// ignored. The caller decides whether to re-prompt (on ErrPreview) or exit
// (on ErrAbort / ErrRequiresForceDiscard).
//
// Callers with autostash always enabled (TM-18 WithAutostash) should pass
// autostash=true so the decision table picks Hard-with-autostash instead of
// Keep on dirty+`y`.
func DecideStrategy(dirty, autostash bool, key rune) (Strategy, error) {
	switch key {
	case KeyAbort, KeyAbortAlias, 0x1b: // n, N, Esc
		return 0, ErrAbort
	case KeyPreview:
		return 0, ErrPreview
	case KeyConfirm:
		if dirty && !autostash {
			return StrategyKeep, nil
		}
		if dirty && autostash {
			return StrategyHard, nil
		}
		return StrategyMixed, nil
	case KeyHardReset:
		if dirty && !autostash {
			return 0, ErrRequiresForceDiscard
		}
		return StrategyHard, nil
	default:
		return 0, ErrAbort
	}
}
