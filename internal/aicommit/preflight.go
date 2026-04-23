package aicommit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

// PreflightInput collects everything Preflight needs. Callers inject
// Runner so tests can fake git state.
type PreflightInput struct {
	Runner      git.Runner
	WorkDir     string
	AI          config.AIConfig
	Provider    provider.Provider
	EnvLookup   func(string) string // defaults to os.Getenv
	SkipGPGKey  bool                // test hook
	AllowRemote bool                // cfg.AI.Commit.AllowRemote, copied here for clarity
}

// Preflight blocks the run when any safety / policy condition fails:
//
//  1. AI feature disabled (config or GK_AI_DISABLE=1) → ErrAIDisabled.
//  2. Remote provider when AllowRemote is false → ErrRemoteNotAllowed.
//  3. In-progress git op (rebase/merge/cherry-pick/revert/bisect)
//     → ErrGitStateNotNone.
//  4. commit.gpgsign=true but no signing key available → ErrGPGKeyMissing.
//  5. Provider.Available() non-nil → wrapped.
//
// Every error is a sentinel (or wraps one) so the CLI can switch on
// errors.Is and print a tailored hint.
func Preflight(ctx context.Context, in PreflightInput) error {
	lookup := in.EnvLookup
	if lookup == nil {
		lookup = os.Getenv
	}

	if !in.AI.Enabled {
		return fmt.Errorf("%w: set ai.enabled=true in .gk.yaml or unset GK_AI_DISABLE", ErrAIDisabled)
	}
	if strings.EqualFold(lookup("GK_AI_DISABLE"), "1") {
		return fmt.Errorf("%w: GK_AI_DISABLE=1 environment override", ErrAIDisabled)
	}

	if in.Provider != nil && in.Provider.Locality() == provider.LocalityRemote && !in.AllowRemote {
		return fmt.Errorf("%w: provider %q is remote; set ai.commit.allow_remote=true to opt in",
			ErrRemoteNotAllowed, in.Provider.Name())
	}

	if in.Runner != nil {
		state, err := gitstate.Detect(ctx, in.WorkDir)
		if err == nil && state != nil && state.Kind != gitstate.StateNone {
			return fmt.Errorf("%w: %s in progress — resolve with `git %s --continue` or `--abort` first",
				ErrGitStateNotNone, state.Kind.String(), gitStateContinueVerb(state.Kind))
		}
	}

	if !in.SkipGPGKey {
		if err := checkGPGKey(ctx, in.Runner, lookup); err != nil {
			return err
		}
	}

	if in.Provider != nil {
		if err := in.Provider.Available(ctx); err != nil {
			return fmt.Errorf("provider %s: %w", in.Provider.Name(), err)
		}
	}

	return nil
}

// Sentinel errors — callers branch on these via errors.Is.
var (
	ErrAIDisabled       = errors.New("aicommit: AI feature disabled")
	ErrRemoteNotAllowed = errors.New("aicommit: remote provider disallowed by policy")
	ErrGitStateNotNone  = errors.New("aicommit: git is in the middle of another operation")
	ErrGPGKeyMissing    = errors.New("aicommit: commit.gpgsign is on but no signing key is available")
)

// checkGPGKey verifies that when git is configured to sign commits
// (commit.gpgsign=true / core.gpgsign=true / config signing.keyid is set)
// a key is actually available. If sign is off or Runner is nil the
// function returns nil — we don't spawn subprocesses we don't need.
//
// This check is intentionally approximate: inspecting the gpg / ssh
// agent exhaustively is fragile and slow. We only catch the common
// misconfiguration of "gpgsign=true + no user.signingkey set".
func checkGPGKey(ctx context.Context, runner git.Runner, lookup func(string) string) error {
	if runner == nil {
		return nil
	}
	signOut, _, err := runner.Run(ctx, "config", "--bool", "commit.gpgsign")
	if err != nil || strings.TrimSpace(string(signOut)) != "true" {
		return nil
	}
	keyOut, _, err := runner.Run(ctx, "config", "--get", "user.signingkey")
	if err == nil && strings.TrimSpace(string(keyOut)) != "" {
		return nil
	}
	// signing on, no keyid set → still OK if a gpg agent is reachable
	// via env (GPG_TTY) + a default key is configured, but that's
	// outside gk's ability to check. Demand an explicit signingkey.
	return fmt.Errorf("%w: set user.signingkey or disable commit.gpgsign for this repo",
		ErrGPGKeyMissing)
}

// gitStateContinueVerb maps StateKind to the short verb used in the
// resolve hint. Kept local to avoid churning the public gitstate API.
func gitStateContinueVerb(k gitstate.StateKind) string {
	switch k {
	case gitstate.StateRebaseMerge, gitstate.StateRebaseApply:
		return "rebase"
	case gitstate.StateMerge:
		return "merge"
	case gitstate.StateCherryPick:
		return "cherry-pick"
	case gitstate.StateRevert:
		return "revert"
	case gitstate.StateBisect:
		return "bisect"
	default:
		return "op"
	}
}
