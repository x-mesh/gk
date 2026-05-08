// Package easy implements the Easy Mode engine for gk CLI.
//
// Easy Mode translates technical git output into beginner-friendly
// language with emoji, term translations, and contextual hints.
// The engine coordinates activation logic, message catalog access,
// and graceful fallback when any component fails.
package easy

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/i18n"
)

// Engine coordinates Easy Mode activation, message formatting, term
// translation, and hint generation. All formatting methods are safe to
// call regardless of whether Easy Mode is enabled — when disabled they
// return the key or input string unchanged.
type Engine struct {
	enabled bool
	catalog *i18n.Catalog
	lang    string

	// debugFn emits diagnostic lines when --debug is active.
	// nil means debug output is suppressed.
	debugFn func(string, ...interface{})

	// startupLoadDur is stashed by NewEngine so that SetDebugFn can
	// emit the catalog-load timing even when debug wasn't wired at
	// construction time.
	startupLoadDur time.Duration

	// terms translates git terminology into beginner-friendly language.
	terms *TermMapper

	// emoji maps status keys to emoji characters for visual output.
	emoji *EmojiMapper

	// hints generates contextual next-action hints based on the
	// configured verbosity level.
	hints *HintGenerator

	// fallbackHints is a lazily-built normal-mode HintGenerator used by
	// effectiveHints when Easy Mode is disabled. Construction happens at
	// most once per Engine; subsequent calls reuse the same instance to
	// avoid per-hint catalog/EmojiMapper allocations on the disabled
	// path. Guarded by fallbackOnce so the lazy init is race-free.
	fallbackHints *HintGenerator
	fallbackOnce  sync.Once
}

// NewEngine creates an Engine based on the resolved OutputConfig and
// CLI flags. Activation priority: CLI flags > env vars > config file.
//
// The cfg parameter already reflects env-var and config-file values
// (viper merges them before unmarshalling into OutputConfig). The
// flagEasy / flagNoEasy parameters represent the --easy / --no-easy
// CLI flags which take highest precedence.
//
// If catalog loading fails the engine falls back to disabled mode with
// a warning on stderr.
func NewEngine(cfg config.OutputConfig, flagEasy, flagNoEasy bool) *Engine {
	e := &Engine{
		lang: cfg.Lang,
	}

	// Resolve enabled state with priority:
	//   1. --no-easy flag  → always disabled
	//   2. --easy flag     → always enabled
	//   3. config (which already includes env var via viper) → as-is
	e.enabled = resolveEnabled(cfg.Easy, flagEasy, flagNoEasy)

	if !e.enabled {
		return e
	}

	// Load message catalog.
	start := time.Now()
	cat := loadCatalog(cfg.Lang)
	loadDur := time.Since(start)

	if cat == nil {
		// Catalog loading failed — warn and fall back to normal mode.
		fmt.Fprintln(os.Stderr, "gk: Easy Mode catalog load failed, falling back to normal mode")
		e.enabled = false
		return e
	}

	e.catalog = cat
	e.terms = NewTermMapper(cfg.Lang)
	e.emoji = NewEmojiMapper(cfg.Emoji)
	e.hints = NewHintGenerator(HintLevel(cfg.Hints), cat, e.emoji)

	// Store a debug closure that callers can invoke later once the
	// debug function is wired (see SetDebugFn).
	// Emit initial diagnostics if debug is already wired.
	if e.debugFn != nil {
		e.emitStartupDiag(loadDur)
	} else {
		// Stash load duration so we can emit it when debugFn is set.
		e.startupLoadDur = loadDur
	}

	return e
}

// resolveEnabled applies the activation priority:
//
//	--no-easy  → false  (highest)
//	--easy     → true
//	config/env → cfg value
func resolveEnabled(cfgEasy, flagEasy, flagNoEasy bool) bool {
	if flagNoEasy {
		return false
	}
	if flagEasy {
		return true
	}
	// Fall through to config value (which already incorporates env vars
	// via viper's BindEnv for GK_EASY).
	return cfgEasy
}

// loadCatalog creates an i18n.Catalog for the given language in easy
// mode. Returns nil if the catalog cannot be created (e.g. registry is
// empty).
func loadCatalog(lang string) *i18n.Catalog {
	cat := i18n.New(lang, i18n.ModeEasy)
	if cat == nil {
		return nil
	}
	return cat
}

// IsEnabled reports whether Easy Mode is active for this invocation.
func (e *Engine) IsEnabled() bool {
	if e == nil {
		return false
	}
	return e.enabled
}

// Format looks up a message key from the catalog and applies
// fmt.Sprintf-style arguments. When Easy Mode is disabled or the
// catalog is nil, the key itself is returned (with args applied if
// present).
//
// Any panic inside the formatting pipeline is recovered and the
// function falls back to returning the raw key — satisfying the
// fallback safety requirement (Property 10).
func (e *Engine) Format(key string, args ...interface{}) (result string) {
	// Panic recovery — always return something sensible.
	defer func() {
		if r := recover(); r != nil {
			if e != nil && e.debugFn != nil {
				e.debugFn("easy: Format panic recovered: %v", r)
			}
			// Fallback: apply args to key directly if possible.
			if len(args) > 0 {
				result = fmt.Sprintf(key, args...)
			} else {
				result = key
			}
		}
	}()

	if e == nil || !e.enabled || e.catalog == nil {
		if len(args) > 0 {
			return fmt.Sprintf(key, args...)
		}
		return key
	}

	return e.catalog.Getf(key, args...)
}

// TranslateTerms replaces git terminology in s with beginner-friendly
// translations. When disabled or the TermMapper is nil, the input
// string is returned unchanged.
func (e *Engine) TranslateTerms(s string) string {
	if e == nil || !e.enabled || e.terms == nil {
		return s
	}
	return e.terms.Translate(s)
}

// FormatHint generates a contextual hint for the given hint key.
// Delegates to the HintGenerator when available; falls back to
// catalog lookup when the generator is nil.
func (e *Engine) FormatHint(hintKey string, args ...interface{}) string {
	if e == nil || !e.enabled {
		return ""
	}
	// Prefer the HintGenerator (respects verbose/minimal/off levels).
	if e.hints != nil {
		return e.hints.Generate(hintKey, args...)
	}
	// Fallback: direct catalog lookup (should not happen in normal flow).
	if e.catalog != nil {
		return e.catalog.Getf(hintKey, args...)
	}
	return ""
}

// StatusHint generates a contextual next-step hint based on the
// current git working tree state. Delegates to HintGenerator.StatusHint.
func (e *Engine) StatusHint(hasStaged, hasUnstaged, hasUntracked, hasConflict bool) string {
	if e == nil || !e.enabled || e.hints == nil {
		return ""
	}
	return e.hints.StatusHint(hasStaged, hasUnstaged, hasUntracked, hasConflict)
}

// SyncHint generates a contextual hint based on upstream divergence
// when the working tree is clean. Delegates to HintGenerator.SyncHint.
func (e *Engine) SyncHint(ahead, behind int, hasUpstream bool) string {
	if e == nil || !e.enabled || e.hints == nil {
		return ""
	}
	return e.hints.SyncHint(ahead, behind, hasUpstream)
}

// effectiveHints returns a HintGenerator for the hint methods added in
// v0.39.0+ that must run in normal mode too. When the engine is enabled
// it reuses the configured generator; when disabled it returns a
// lazily-built, cached normal-mode generator so the calling command
// surfaces the hint instead of going silent — without re-allocating a
// catalog and EmojiMapper on every call.
//
// Returns nil when the language has no registered catalog at all.
func (e *Engine) effectiveHints() *HintGenerator {
	if e == nil {
		return nil
	}
	if e.hints != nil {
		return e.hints
	}
	e.fallbackOnce.Do(func() {
		cat := i18n.New(e.lang, i18n.ModeNormal)
		if cat == nil {
			return
		}
		e.fallbackHints = NewHintGenerator(HintVerbose, cat, NewEmojiMapper(false))
	})
	return e.fallbackHints
}

// MergeIntoNextHint generates next-step hints right after `gk merge --into`
// completes. Runs in both Easy Mode (uses configured hints) and normal
// mode (synthesizes a normal-mode generator) so users always see the
// next-step nudge.
func (e *Engine) MergeIntoNextHint(receiver string, sourceFullyMerged bool, source string) []string {
	g := e.effectiveHints()
	if g == nil {
		return nil
	}
	return g.MergeIntoNextHint(receiver, sourceFullyMerged, source)
}

// PushSummaryHint generates the one-line summary for a successful push.
// Runs in both Easy Mode and normal mode (see effectiveHints).
func (e *Engine) PushSummaryHint(n int, remote, branch, short string) string {
	g := e.effectiveHints()
	if g == nil {
		return ""
	}
	return g.PushSummaryHint(n, remote, branch, short)
}

// StatusCrossWorktreeHint generates a one-line summary of pending work
// across worktrees for the in-sync clean status path. Runs in both Easy
// Mode and normal mode (see effectiveHints).
func (e *Engine) StatusCrossWorktreeHint(items []WorktreeWorkItem, total int) string {
	g := e.effectiveHints()
	if g == nil {
		return ""
	}
	return g.StatusCrossWorktreeHint(items, total)
}

// Hints returns the underlying HintGenerator, or nil when the engine
// is disabled or not initialised.
func (e *Engine) Hints() *HintGenerator {
	if e == nil {
		return nil
	}
	return e.hints
}

// Emoji returns the underlying EmojiMapper so callers can wire it into
// out-of-package formatters (e.g. ui.NewEasyFormatter). Returns nil
// when the engine is disabled, uninitialised, or emoji is turned off.
func (e *Engine) Emoji() *EmojiMapper {
	if e == nil || !e.enabled {
		return nil
	}
	return e.emoji
}

// SetDebugFn installs a debug logging function (typically cli.Dbg).
// This is called after engine construction because the debug flag
// state may not be fully resolved at NewEngine time.
func (e *Engine) SetDebugFn(fn func(string, ...interface{})) {
	if e == nil {
		return
	}
	e.debugFn = fn

	// Emit deferred startup diagnostics.
	if e.startupLoadDur > 0 {
		e.emitStartupDiag(e.startupLoadDur)
		e.startupLoadDur = 0
	}
}

// Lang returns the active language code.
func (e *Engine) Lang() string {
	if e == nil {
		return ""
	}
	return e.lang
}

// emitStartupDiag outputs Easy Mode diagnostic information via debugFn.
func (e *Engine) emitStartupDiag(loadDur time.Duration) {
	if e.debugFn == nil {
		return
	}
	e.debugFn("easy: enabled=%v lang=%s catalog_load=%s", e.enabled, e.lang, loadDur.Round(time.Microsecond))
}
