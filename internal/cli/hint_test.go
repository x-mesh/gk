package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/sessionaudit"
)

func TestHintRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"hint"})
	if err != nil {
		t.Fatalf("find hint: %v", err)
	}
	if cmd.Name() != "hint" {
		t.Errorf("resolved to %q, want hint", cmd.Name())
	}
}

func TestRenderHint(t *testing.T) {
	var covered bytes.Buffer
	renderHint(&covered, sessionaudit.HintResult{
		Covered:    true,
		CoveredBy:  []string{"git-kit context"},
		Suggestion: "Use git-kit context instead of separate probes.",
	})
	if !strings.Contains(covered.String(), "git-kit context") {
		t.Errorf("covered render missing replacement: %q", covered.String())
	}

	var clean bytes.Buffer
	renderHint(&clean, sessionaudit.HintResult{Covered: false})
	if !strings.Contains(clean.String(), "ok") {
		t.Errorf("clean render = %q, want an ok line", clean.String())
	}
}

func TestHintExitCode(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"hint"})
	if err != nil {
		t.Fatal(err)
	}
	old := hintExitFunc
	defer func() {
		hintExitFunc = old
		_ = cmd.Flags().Set("exit-code", "false")
	}()

	if err := cmd.Flags().Set("exit-code", "true"); err != nil {
		t.Fatal(err)
	}
	cmd.SetOut(new(bytes.Buffer))

	// Covered command → exit 1.
	got := -1
	hintExitFunc = func(code int) { got = code }
	if err := runHint(cmd, []string{"git", "add", "."}); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Errorf("covered exit code = %d, want 1", got)
	}

	// Clean command → exit func never fires.
	got = -1
	if err := runHint(cmd, []string{"git", "rev-parse", "HEAD"}); err != nil {
		t.Fatal(err)
	}
	if got != -1 {
		t.Errorf("clean command fired exit(%d), want no call", got)
	}
}
