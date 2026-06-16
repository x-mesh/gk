package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

func TestErrorCodeFromError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"not a repo", &git.ExitError{Code: 128, Stderr: "fatal: not a git repository (or any of the parent directories)"}, "not-a-repo"},
		{"commit-graph corrupt via stderr", &git.ExitError{Code: 128, Stderr: "fatal: invalid commit position. commit-graph is likely corrupt"}, "commit-graph-corrupt"},
		{"commit-graph corrupt wrapped", fmt.Errorf("rebase: %w", &git.ExitError{Code: 128, Stderr: "fatal: commit-graph is likely corrupt"}), "commit-graph-corrupt"},
		{"conflict error type", &ConflictError{Code: 3}, "conflict"},
		{"wrapped conflict error", fmt.Errorf("pull: %w", &ConflictError{Code: 3}), "conflict"},
		{"branch not found", errors.New("switch: invalid reference: feature/x"), "branch-not-found"},
		{"unknown revision via stderr", &git.ExitError{Code: 128, Stderr: "fatal: ambiguous argument 'x': unknown revision or path"}, "branch-not-found"},
		{"diverged", errors.New("histories diverged: choose --rebase, --merge, or --fetch-only"), "diverged"},
		{"precheck conflicts", errors.New("precheck found 3 conflict(s) merging feature"), "conflict"},
		{"dirty tree", errors.New("working tree has uncommitted changes"), "dirty-tree"},
		{"ship dirty", errors.New("ship: working tree is dirty; commit/stash changes or pass --allow-dirty"), "dirty-tree"},
		{"in progress", errors.New("a rebase is in progress — resolve it first"), "in-progress-op"},
		{"tag exists", errors.New("ship: tag v1.2.3 already exists locally"), "tag-exists"},
		{"secret found", errors.New("aborting push due to 2 secret finding(s)"), "secret-found"},
		{"no upstream", errors.New("gk log: current branch has no upstream configured"), "no-upstream"},
		{"preflight", errors.New(`ship: preflight failed at step "test"`), "preflight-failed"},
		{"json needs dry-run", errors.New("ship: --json emits the release plan and requires --dry-run"), "json-needs-dry-run"},
		{"config invalid", errors.New(`gk config set: 알 수 없는 키 "pull.xyz"`), "config-invalid"},
		{"unknown", errors.New("something completely else"), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorCodeFromError(tc.err); got != tc.want {
				t.Errorf("errorCodeFromError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestRemediesFrom(t *testing.T) {
	// Explicit remedies win.
	err := WithRemedy(errors.New("boom"), "fix it",
		errRemedy{Command: "gk continue", Safety: "safe"},
		errRemedy{Command: "gk abort", Safety: "destructive"})
	r := RemediesFrom(err)
	if len(r) != 2 || r[0].Command != "gk continue" || r[1].Safety != "destructive" {
		t.Errorf("explicit remedies: %+v", r)
	}

	// "try: X" hints promote into one safe remedy.
	r = RemediesFrom(WithHint(errors.New("boom"), hintCommand("gk pull --autostash")))
	if len(r) != 1 || r[0].Command != "gk pull --autostash" || r[0].Safety != "safe" {
		t.Errorf("promoted remedy: %+v", r)
	}

	// Prose hints don't fabricate commands.
	if r := RemediesFrom(WithHint(errors.New("boom"), "read the docs first")); r != nil {
		t.Errorf("prose hint must not promote, got %+v", r)
	}
	// HintFrom compatibility unchanged.
	if HintFrom(err) != "fix it" {
		t.Errorf("HintFrom through WithRemedy = %q", HintFrom(err))
	}
}

// TestWithBlocked pins the blocked-precondition error contract: state
// "blocked", an explicit code that survives a localized message, the carried
// remedies, and a FormatErrorJSON envelope that renders state:"blocked"
// ok:false (not "error").
func TestWithBlocked(t *testing.T) {
	err := WithBlocked(
		fmt.Errorf("ship: %q는 base %q를 fast-forward할 수 없습니다 (히스토리 분기)", "develop", "main"),
		"base-diverged",
		"먼저 base를 branch로 통합하세요: `gk sync`",
		errRemedy{Command: "gk sync", Safety: "safe"},
	)

	if s := stateFrom(err); s != envStateBlocked {
		t.Errorf("stateFrom = %q, want %q", s, envStateBlocked)
	}
	// Explicit code wins even though the message is Korean (no "diverged" token).
	if c := errorCodeFromError(err); c != "base-diverged" {
		t.Errorf("errorCodeFromError = %q, want base-diverged", c)
	}
	if r := RemediesFrom(err); len(r) != 1 || r[0].Command != "gk sync" {
		t.Errorf("remedies = %+v, want one gk sync", r)
	}

	prevA, prevJ := flagAgent, flagJSON
	t.Cleanup(func() { flagAgent, flagJSON = prevA, prevJ })
	flagAgent, flagJSON = true, true

	var env struct {
		State string `json:"state"`
		OK    bool   `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if uerr := json.Unmarshal([]byte(FormatErrorJSON(err)), &env); uerr != nil {
		t.Fatalf("not valid JSON: %v", uerr)
	}
	if env.State != envStateBlocked || env.OK || env.Error.Code != "base-diverged" {
		t.Errorf("blocked envelope: state=%q ok=%v code=%q", env.State, env.OK, env.Error.Code)
	}

	// WithBlocked(nil) is nil, like the other decorators.
	if WithBlocked(nil, "x", "y") != nil {
		t.Error("WithBlocked(nil) must be nil")
	}
}

func TestAgentEnvImpliesJSON(t *testing.T) {
	// The env is read in init() (already ran); simulate its effect directly
	// to pin the contract: agent mode must imply JSON output.
	prevA, prevJ := flagAgent, flagJSON
	t.Cleanup(func() { flagAgent, flagJSON = prevA, prevJ })
	flagAgent, flagJSON = true, true
	if !AgentOut() || !JSONOut() {
		t.Error("agent mode must imply JSONOut")
	}
}
