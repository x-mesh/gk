package cli

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/easy"
	"github.com/x-mesh/gk/internal/gitstate"
)

// disableEasyForTest forces Easy Mode off for the duration of the test
// regardless of the developer's ~/.config/gk/config.yaml. Tests that
// assert the non-Easy "gk:" error prefix must call this — without it the
// EasyEngine resolves from user config and FormatError takes the EasyFormatter
// branch (which prefixes with ✗), causing local-only test failures.
func disableEasyForTest(t *testing.T) {
	t.Helper()
	prevEng := easyEngine
	prevNoEasy := flagNoEasy
	flagNoEasy = true
	easyEngine = nil
	easyEngineOnce = sync.Once{}
	t.Cleanup(func() {
		easyEngine = prevEng
		flagNoEasy = prevNoEasy
		easyEngineOnce = sync.Once{}
	})
}

func TestInProgressHint(t *testing.T) {
	cases := []struct {
		name  string
		state *gitstate.State
		op    string // expected operation word; "" means hint must be empty
	}{
		{"nil", nil, ""},
		{"none", &gitstate.State{Kind: gitstate.StateNone}, ""},
		{"rebase-merge", &gitstate.State{Kind: gitstate.StateRebaseMerge}, "rebase"},
		{"rebase-apply", &gitstate.State{Kind: gitstate.StateRebaseApply}, "rebase"},
		{"merge", &gitstate.State{Kind: gitstate.StateMerge}, "merge"},
		{"cherry-pick", &gitstate.State{Kind: gitstate.StateCherryPick}, "cherry-pick"},
		{"revert", &gitstate.State{Kind: gitstate.StateRevert}, "revert"},
		{"bisect", &gitstate.State{Kind: gitstate.StateBisect}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inProgressHint(tc.state)
			if tc.op == "" {
				if got != "" {
					t.Fatalf("inProgressHint = %q, want empty", got)
				}
				return
			}
			if !strings.HasPrefix(got, tc.op+" in progress") {
				t.Errorf("hint %q does not start with %q", got, tc.op+" in progress")
			}
			if !strings.Contains(got, "gk abort") {
				t.Errorf("hint %q missing 'gk abort'", got)
			}
			if !strings.Contains(got, "gk continue") {
				t.Errorf("hint %q missing 'gk continue'", got)
			}
		})
	}
}

// withNoColor disables fatih/color's ANSI escapes for the duration of
// a test so string-equality assertions stay stable regardless of the
// runtime TTY detection (which other tests in the same package may
// have flipped on).
func withNoColor(t *testing.T) {
	t.Helper()
	prev := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prev })
}

func TestWithHint_Nil(t *testing.T) {
	if WithHint(nil, "irrelevant") != nil {
		t.Error("WithHint(nil, ...) must return nil")
	}
}

func TestWithHint_EmptyHint(t *testing.T) {
	err := errors.New("boom")
	got := WithHint(err, "")
	if got != err {
		t.Errorf("empty hint should return original err, got %v", got)
	}
}

func TestWithHint_PreservesUnwrap(t *testing.T) {
	base := errors.New("boom")
	wrapped := WithHint(base, "try: x")
	if !errors.Is(wrapped, base) {
		t.Error("errors.Is should reach the base error through the hint wrapper")
	}
	if HintFrom(wrapped) != "try: x" {
		t.Errorf("HintFrom = %q, want 'try: x'", HintFrom(wrapped))
	}
}

func TestHintFrom_WalksChain(t *testing.T) {
	base := errors.New("root")
	withHint := WithHint(base, "try: outer")
	// Further wrapping via the standard library should not hide the hint.
	outer := wrapError("context", withHint)
	if HintFrom(outer) != "try: outer" {
		t.Errorf("HintFrom through wrapError = %q, want 'try: outer'", HintFrom(outer))
	}
}

func TestHintFrom_NoHint(t *testing.T) {
	if HintFrom(nil) != "" {
		t.Error("HintFrom(nil) must be empty")
	}
	if HintFrom(errors.New("plain")) != "" {
		t.Error("HintFrom on plain err must be empty")
	}
}

func TestFormatError_NoHint(t *testing.T) {
	disableEasyForTest(t)
	withNoColor(t)
	got := FormatError(errors.New("boom"))
	want := "gk: boom"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatError_WithHint(t *testing.T) {
	disableEasyForTest(t)
	withNoColor(t)
	err := WithHint(errors.New("boom"), "try: gk abort")
	got := FormatError(err)

	if !strings.HasPrefix(got, "gk: boom") {
		t.Errorf("first line should be 'gk: boom', got: %q", got)
	}
	// The remediation renders as a branded advisory block so gk's guidance
	// is attributable to gk: "█  HINT" header, body indented below.
	if !strings.Contains(got, "█  HINT") {
		t.Errorf("output should contain the HINT block header, got: %q", got)
	}
	if !strings.Contains(got, "\n   try: gk abort") {
		t.Errorf("output should contain the indented hint body, got: %q", got)
	}
	if strings.Count(got, "\n") != 2 {
		t.Errorf("output should be exactly 3 lines, got %d newlines: %q", strings.Count(got, "\n"), got)
	}
}

func TestFormatError_Nil(t *testing.T) {
	if FormatError(nil) != "" {
		t.Error("FormatError(nil) must be empty string")
	}
}

func TestHintCommand(t *testing.T) {
	got := hintCommand("gk continue")
	if got != "try: gk continue" {
		t.Errorf("hintCommand = %q, want 'try: gk continue'", got)
	}
}

// wrapError is a tiny test helper that mimics fmt.Errorf("%s: %w") behavior
// without bringing in the full formatter — keeps the test table focused on
// error-chain semantics rather than string formatting.
type fmtWrap struct {
	msg string
	err error
}

func (f *fmtWrap) Error() string { return f.msg + ": " + f.err.Error() }
func (f *fmtWrap) Unwrap() error { return f.err }

func wrapError(msg string, err error) error { return &fmtWrap{msg: msg, err: err} }

// newKoEasyEngine builds an enabled ko Easy Mode engine for translation
// tests, independent of the developer's ~/.config/gk/config.yaml.
func newKoEasyEngine(t *testing.T) *easy.Engine {
	t.Helper()
	eng := easy.NewEngine(config.OutputConfig{Easy: true, Lang: "ko"}, false, false)
	if !eng.IsEnabled() {
		t.Skip("ko Easy catalog unavailable in this environment")
	}
	return eng
}

// TestTranslateErrorBody verifies that translateErrorBody translates the
// prose part of an error while leaving quoted child-process output (git
// stderr/stdout, lint output, exit-code tails) verbatim — the t3 invariant.
func TestTranslateErrorBody(t *testing.T) {
	eng := newKoEasyEngine(t)

	cases := []struct {
		name string
		in   string
		// wantContains: substrings that must appear in the output.
		wantContains []string
		// wantNotContains: substrings that must NOT appear (i.e. terms that
		// would only show up if a protected span had been translated).
		wantNotContains []string
	}{
		{
			// (stderr=...) quote: prose before it translates, the quoted
			// git output (including a `branch` token) stays raw.
			name:         "stderr-quote",
			in:           "aicommit: git commit: failed (stderr=fatal: branch foo not found)",
			wantContains: []string{"(stderr=fatal: branch foo not found)"},
			// "branch" inside the quote must not become "작업 갈래 (branch)".
			wantNotContains: []string{"작업 갈래 (branch) foo"},
		},
		{
			// (stderr=... stdout=...) — both in one paren group, protected to EOS.
			name:            "stderr-stdout-quote",
			in:              "aicommit: git commit: boom (stderr=err branch line stdout=out push line)",
			wantContains:    []string{"(stderr=err branch line stdout=out push line)"},
			wantNotContains: []string{"작업 갈래 (branch) line stdout", "서버에 올리기 (push) line)"},
		},
		{
			// exit code N: <stderr> — the git.ExitError tail is raw.
			name:            "exit-code-tail",
			in:              "git push origin: exit code 1: branch is behind upstream",
			wantContains:    []string{"exit code 1: branch is behind upstream"},
			wantNotContains: []string{"작업 갈래 (branch) is behind", "원격 기준점 (upstream)"},
		},
		{
			// preflight step output: exit status N: <combined output> is raw.
			name: "preflight-step-output",
			in:   `preflight failed at step "lint": exit status 1: Branch string ` + "`json:\"branch\"`",
			wantContains: []string{
				`exit status 1: Branch string ` + "`json:\"branch\"`",
			},
			wantNotContains: []string{"작업 갈래 (Branch) string", `작업 갈래 (branch)\"`},
		},
		{
			// Pure prose with no quote: a git term still translates normally.
			name:            "plain-prose-translates",
			in:              "cannot rebase: you have unstaged changes",
			wantContains:    []string{"커밋 재정렬 (rebase)", "아직 준비 안 됨 (unstaged)"},
			wantNotContains: nil,
		},
		{
			// Prose precedes a stderr quote: prose half translates, quote half raw.
			name:            "prose-plus-quote",
			in:              "rebase failed (stderr=could not apply commit abc123)",
			wantContains:    []string{"커밋 재정렬 (rebase) failed", "(stderr=could not apply commit abc123)"},
			wantNotContains: []string{"apply 변경사항 저장 (commit) abc123"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := translateErrorBody(eng, tc.in)
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("translateErrorBody(%q)\n  = %q\n  must contain %q", tc.in, got, want)
				}
			}
			for _, bad := range tc.wantNotContains {
				if strings.Contains(got, bad) {
					t.Errorf("translateErrorBody(%q)\n  = %q\n  must NOT contain %q", tc.in, got, bad)
				}
			}
			// The sentinel must never leak into user-facing output.
			if strings.Contains(got, "\x00") {
				t.Errorf("translateErrorBody(%q) leaked a NUL sentinel: %q", tc.in, got)
			}
		})
	}
}

// TestProtectedSpans pins the span-detection boundaries directly so a future
// format change that breaks them fails here with a precise location.
func TestProtectedSpans(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// want is the concatenation of the protected substrings, in order.
		// Empty means no protected spans.
		want []string
	}{
		{"none", "not a commit on this branch", nil},
		{"stderr", "x failed (stderr=oops)", []string{" (stderr=oops)"}},
		{"stdout", "x failed (stdout=hi)", []string{" (stdout=hi)"}},
		{"both-one-paren", "x (stderr=a stdout=b)", []string{" (stderr=a stdout=b)"}},
		{"exit-code", "git x: exit code 2: bad", []string{"bad"}},
		{"exit-status", `step "s": exit status 1: out`, []string{"out"}},
		{
			// exit code tail bounded by a trailing stderr quote.
			"exit-code-then-stderr",
			"git x: exit code 1: tail (stderr=more)",
			[]string{"tail", " (stderr=more)"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spans := protectedSpans(tc.in)
			if len(spans) != len(tc.want) {
				t.Fatalf("protectedSpans(%q) = %d spans %v, want %d", tc.in, len(spans), spans, len(tc.want))
			}
			for i, sp := range spans {
				got := tc.in[sp[0]:sp[1]]
				if got != tc.want[i] {
					t.Errorf("span %d = %q, want %q", i, got, tc.want[i])
				}
			}
		})
	}
}
