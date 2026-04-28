package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/resolve"
)

// ---------------------------------------------------------------------------
// Command registration
// ---------------------------------------------------------------------------

func TestResolveRegistered(t *testing.T) {
	found, _, err := rootCmd.Find([]string{"resolve"})
	if err != nil {
		t.Fatalf("rootCmd.Find(resolve): %v", err)
	}
	if found.Use != "resolve [files...]" {
		t.Errorf("Use: want %q, got %q", "resolve [files...]", found.Use)
	}
}

func TestResolveHelpListsFlags(t *testing.T) {
	buf := &bytes.Buffer{}
	found, _, err := rootCmd.Find([]string{"resolve"})
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	found.SetOut(buf)
	found.SetErr(buf)
	if err := found.Help(); err != nil {
		t.Fatalf("Help: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"--dry-run", "--no-ai", "--no-backup", "--strategy",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing flag %q\n%s", want, out)
		}
	}
}

// ---------------------------------------------------------------------------
// Strategy validation
// ---------------------------------------------------------------------------

func TestResolveInvalidStrategy(t *testing.T) {
	cmd, _, _ := rootCmd.Find([]string{"resolve"})
	_ = cmd.Flags().Set("strategy", "invalid-value")
	defer func() { _ = cmd.Flags().Set("strategy", "") }()

	err := runResolveWithContext(t, cmd, nil)
	if err == nil {
		t.Fatal("expected error for invalid strategy")
	}
	if !strings.Contains(err.Error(), "invalid strategy") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveStrategyOursIsValid(t *testing.T) {
	cmd, _, _ := rootCmd.Find([]string{"resolve"})
	_ = cmd.Flags().Set("strategy", "ours")
	defer func() { _ = cmd.Flags().Set("strategy", "") }()

	// runResolve will fail at gitstate.Detect (nil context in test),
	// but it must NOT fail at strategy validation.
	err := runResolveWithContext(t, cmd, nil)
	if err != nil && strings.Contains(err.Error(), "invalid strategy") {
		t.Errorf("ours should be a valid strategy, got: %v", err)
	}
}

func TestResolveStrategyTheirsIsValid(t *testing.T) {
	cmd, _, _ := rootCmd.Find([]string{"resolve"})
	_ = cmd.Flags().Set("strategy", "theirs")
	defer func() { _ = cmd.Flags().Set("strategy", "") }()

	err := runResolveWithContext(t, cmd, nil)
	if err != nil && strings.Contains(err.Error(), "invalid strategy") {
		t.Errorf("theirs should be a valid strategy, got: %v", err)
	}
}

func TestResolveStrategyAiIsValid(t *testing.T) {
	cmd, _, _ := rootCmd.Find([]string{"resolve"})
	_ = cmd.Flags().Set("strategy", "ai")
	defer func() { _ = cmd.Flags().Set("strategy", "") }()

	err := runResolveWithContext(t, cmd, nil)
	if err != nil && strings.Contains(err.Error(), "invalid strategy") {
		t.Errorf("ai should be a valid strategy, got: %v", err)
	}
}

// runResolveWithContext calls runResolve with a real context to avoid nil-context panics.
func runResolveWithContext(t *testing.T, cmd *cobra.Command, args []string) error {
	t.Helper()
	ctx := context.Background()
	cmd.SetContext(ctx)
	return cmd.RunE(cmd, args)
}

// ---------------------------------------------------------------------------
// printResolveResult
// ---------------------------------------------------------------------------

func TestPrintResolveResult_AllResolved(t *testing.T) {
	buf := &bytes.Buffer{}
	result := &resolve.ResolveResult{
		Resolved: []string{"a.go", "b.go"},
		Total:    2,
	}
	printResolveResult(buf, result)
	out := buf.String()
	if !strings.Contains(out, "all conflicts resolved") {
		t.Errorf("expected 'all conflicts resolved', got: %q", out)
	}
}

func TestPrintResolveResult_Partial(t *testing.T) {
	buf := &bytes.Buffer{}
	result := &resolve.ResolveResult{
		Resolved: []string{"a.go"},
		Total:    3,
	}
	printResolveResult(buf, result)
	out := buf.String()
	if !strings.Contains(out, "1/3 conflicts resolved") {
		t.Errorf("expected partial message, got: %q", out)
	}
	if !strings.Contains(out, "2 remaining") {
		t.Errorf("expected remaining count, got: %q", out)
	}
}

func TestPrintResolveResult_NoneResolved(t *testing.T) {
	buf := &bytes.Buffer{}
	result := &resolve.ResolveResult{
		Total: 2,
	}
	printResolveResult(buf, result)
	out := buf.String()
	if out != "" {
		t.Errorf("expected empty output when none resolved, got: %q", out)
	}
}

func TestPrintResolveResult_Nil(t *testing.T) {
	buf := &bytes.Buffer{}
	printResolveResult(buf, nil)
	if buf.Len() != 0 {
		t.Errorf("expected no output for nil result, got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// stateKindToOpType
// ---------------------------------------------------------------------------

func TestStateKindToOpType(t *testing.T) {
	tests := []struct {
		kind gitstate.StateKind
		want string
	}{
		{gitstate.StateMerge, "merge"},
		{gitstate.StateRebaseMerge, "rebase"},
		{gitstate.StateRebaseApply, "rebase"},
		{gitstate.StateCherryPick, "cherry-pick"},
		{gitstate.StateNone, "merge"},   // default
		{gitstate.StateRevert, "merge"}, // default
		{gitstate.StateBisect, "merge"}, // default
	}
	for _, tt := range tests {
		got := stateKindToOpType(tt.kind)
		if got != tt.want {
			t.Errorf("stateKindToOpType(%v) = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// --strategy ai + AI unavailable → error
// ---------------------------------------------------------------------------

func TestResolveStrategyAI_NoProvider(t *testing.T) {
	cmd, _, _ := rootCmd.Find([]string{"resolve"})
	_ = cmd.Flags().Set("strategy", "ai")
	defer func() { _ = cmd.Flags().Set("strategy", "") }()

	// flagRepo is package-global and gets clobbered by sibling tests
	// (reset_test.go etc.) that point it at a tempdir which has since
	// been cleaned up. Without this reset the test fails with a stale
	// "chdir: no such file or directory" instead of the AI-related
	// error the assertion is actually probing for.
	prevRepo := flagRepo
	flagRepo = ""
	defer func() { flagRepo = prevRepo }()

	// In test env, no AI provider is available. The command should error
	// about requiring an AI provider.
	err := runResolveWithContext(t, cmd, nil)
	if err == nil {
		t.Fatal("expected error for --strategy ai without AI provider")
	}
	if !strings.Contains(err.Error(), "ai") && !strings.Contains(err.Error(), "AI") {
		t.Errorf("expected AI-related error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Resolver-level tests using FakeRunner (no real git needed)
// ---------------------------------------------------------------------------

func TestResolverDryRun_NoFileWrite(t *testing.T) {
	// Verify that dry-run mode does not call WriteFile or git add.
	fr := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2": {
				Stdout: "u UU N... 100644 100644 100644 100644 abc123 def456 ghi789 conflict.go\n",
			},
		},
	}

	content := []byte("before\n<<<<<<< HEAD\nours line\n=======\ntheirs line\n>>>>>>> branch\nafter\n")
	var writeFileCalled bool

	r := &resolve.Resolver{
		Runner:   fr,
		Provider: nil,
		Stderr:   &bytes.Buffer{},
		Stdout:   &bytes.Buffer{},
		ReadFile: func(path string) ([]byte, error) {
			return content, nil
		},
		WriteFile: func(path string, data []byte, perm os.FileMode) error {
			writeFileCalled = true
			return nil
		},
	}

	state := &gitstate.State{Kind: gitstate.StateMerge}
	opts := resolve.ResolveOptions{
		DryRun:   true,
		Strategy: resolve.StrategyOurs,
	}

	result, err := r.Run(context.Background(), state, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if writeFileCalled {
		t.Error("WriteFile should not be called in dry-run mode")
	}

	// git add should not be called.
	for _, call := range fr.Calls {
		if len(call.Args) > 0 && call.Args[0] == "add" {
			t.Error("git add should not be called in dry-run mode")
		}
	}

	if len(result.Resolved) != 1 {
		t.Errorf("expected 1 resolved file, got %d", len(result.Resolved))
	}
}

func TestResolverNoBackup_NoOrigFile(t *testing.T) {
	fr := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2": {
				Stdout: "u UU N... 100644 100644 100644 100644 abc123 def456 ghi789 conflict.go\n",
			},
		},
	}

	content := []byte("before\n<<<<<<< HEAD\nours line\n=======\ntheirs line\n>>>>>>> branch\nafter\n")
	var writtenPaths []string

	r := &resolve.Resolver{
		Runner:   fr,
		Provider: nil,
		Stderr:   &bytes.Buffer{},
		Stdout:   &bytes.Buffer{},
		ReadFile: func(path string) ([]byte, error) {
			return content, nil
		},
		WriteFile: func(path string, data []byte, perm os.FileMode) error {
			writtenPaths = append(writtenPaths, path)
			return nil
		},
	}

	state := &gitstate.State{Kind: gitstate.StateMerge}
	opts := resolve.ResolveOptions{
		NoBackup: true,
		Strategy: resolve.StrategyOurs,
	}

	_, err := r.Run(context.Background(), state, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, p := range writtenPaths {
		if strings.HasSuffix(p, ".orig") {
			t.Errorf("should not create .orig file with --no-backup, but wrote: %s", p)
		}
	}
}

func TestResolverWithBackup_CreatesOrigFile(t *testing.T) {
	fr := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2": {
				Stdout: "u UU N... 100644 100644 100644 100644 abc123 def456 ghi789 conflict.go\n",
			},
		},
	}

	content := []byte("before\n<<<<<<< HEAD\nours line\n=======\ntheirs line\n>>>>>>> branch\nafter\n")
	var writtenPaths []string

	r := &resolve.Resolver{
		Runner:   fr,
		Provider: nil,
		Stderr:   &bytes.Buffer{},
		Stdout:   &bytes.Buffer{},
		ReadFile: func(path string) ([]byte, error) {
			return content, nil
		},
		WriteFile: func(path string, data []byte, perm os.FileMode) error {
			writtenPaths = append(writtenPaths, path)
			return nil
		},
	}

	state := &gitstate.State{Kind: gitstate.StateMerge}
	opts := resolve.ResolveOptions{
		NoBackup: false,
		Strategy: resolve.StrategyOurs,
	}

	_, err := r.Run(context.Background(), state, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	hasOrig := false
	for _, p := range writtenPaths {
		if strings.HasSuffix(p, ".orig") {
			hasOrig = true
			break
		}
	}
	if !hasOrig {
		t.Error("expected .orig backup file to be created")
	}
}

func TestResolverNoAI_SkipsProvider(t *testing.T) {
	fr := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"status --porcelain=v2": {
				Stdout: "u UU N... 100644 100644 100644 100644 abc123 def456 ghi789 conflict.go\n",
			},
		},
	}

	content := []byte("before\n<<<<<<< HEAD\nours line\n=======\ntheirs line\n>>>>>>> branch\nafter\n")

	fakeProv := provider.NewFake()
	r := &resolve.Resolver{
		Runner:   fr,
		Provider: fakeProv,
		Stderr:   &bytes.Buffer{},
		Stdout:   &bytes.Buffer{},
		ReadFile: func(path string) ([]byte, error) {
			return content, nil
		},
		WriteFile: func(path string, data []byte, perm os.FileMode) error {
			return nil
		},
	}

	state := &gitstate.State{Kind: gitstate.StateMerge}
	opts := resolve.ResolveOptions{
		NoAI:     true,
		Strategy: resolve.StrategyOurs,
	}

	result, err := r.Run(context.Background(), state, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.AIUsed {
		t.Error("AIUsed should be false when --no-ai is set")
	}
}
