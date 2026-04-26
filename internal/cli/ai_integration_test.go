package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// ── Task 11.5: Integration tests ────────────────────────────────────
//
// These tests wire NVIDIA provider (via Fake) + Privacy Gate +
// FallbackChain together to verify the end-to-end flow.

// TestIntegration_PrivacyGateRedactsForRemoteProvider verifies that
// when a remote provider is used, the Privacy Gate redacts secrets
// from the payload before it reaches the provider.
func TestIntegration_PrivacyGateRedactsForRemoteProvider(t *testing.T) {
	var capturedInput provider.SummarizeInput

	// Remote provider (like NVIDIA).
	fake := provider.NewFake()
	fake.NameVal = "nvidia"
	fake.LocalityVal = provider.LocalityRemote
	fake.SummarizeResponses = []provider.SummarizeResult{
		{Text: "## PR Summary\nChanges look good.\n", Model: "test-model", TokensUsed: 50},
	}
	fake.OnSummarize = func(in provider.SummarizeInput) {
		capturedInput = in
	}

	// Diff containing a secret.
	secretDiff := `diff --git a/config.go b/config.go
+api_key = "AKIA1234567890ABCDEF"
+password = "super-secret-password-12345"
`

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --short refs/remotes/origin/HEAD": {Stdout: "origin/main\n"},
			"merge-base HEAD main":                          {Stdout: "abc1234\n"},
			"diff abc1234..HEAD":                            {Stdout: secretDiff},
			"log --oneline abc1234..HEAD":                   {Stdout: "abc1234 feat: add config"},
		},
	}

	out := &bytes.Buffer{}
	ai := config.Defaults().AI
	ai.Commit.DenyPaths = []string{".env", ".env.*"}

	err := runAIPRCore(context.Background(), aiPRDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		BaseCfg:  "",
		Remote:   "origin",
		AI:       ai,
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiPRFlags{output: "stdout"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The diff sent to the provider should NOT contain the raw secret.
	if strings.Contains(capturedInput.Diff, "AKIA1234567890ABCDEF") {
		t.Error("redacted diff should not contain raw AWS key")
	}
	if strings.Contains(capturedInput.Diff, "super-secret-password-12345") {
		t.Error("redacted diff should not contain raw password")
	}

	// Should contain placeholders instead.
	if !strings.Contains(capturedInput.Diff, "[SECRET_") {
		t.Error("redacted diff should contain [SECRET_N] placeholders")
	}

	// Output should still contain the provider's result.
	if !strings.Contains(out.String(), "## PR Summary") {
		t.Errorf("output should contain summarize result, got: %s", out.String())
	}
}

// TestIntegration_PrivacyGateSkipsLocalProvider verifies that the
// Privacy Gate does NOT redact when the provider is local.
func TestIntegration_PrivacyGateSkipsLocalProvider(t *testing.T) {
	var capturedInput provider.SummarizeInput

	// Local provider.
	fake := provider.NewFake()
	fake.NameVal = "local-llm"
	fake.LocalityVal = provider.LocalityLocal
	fake.SummarizeResponses = []provider.SummarizeResult{
		{Text: "review output", Model: "local", TokensUsed: 10},
	}
	fake.OnSummarize = func(in provider.SummarizeInput) {
		capturedInput = in
	}

	secretDiff := `diff --git a/app.go b/app.go
+token = "my-secret-token-value-1234567890"
`

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --cached": {Stdout: secretDiff},
		},
	}

	out := &bytes.Buffer{}
	ai := config.Defaults().AI

	err := runAIReviewCore(context.Background(), aiReviewDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		AI:       ai,
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiReviewFlags{format: "text"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// For local providers, the diff should be passed through unchanged.
	if !strings.Contains(capturedInput.Diff, "my-secret-token-value-1234567890") {
		t.Error("local provider should receive unredacted diff")
	}
}

// TestIntegration_FallbackChainFirstFailsSecondSucceeds verifies that
// when the first provider in a FallbackChain fails, the second one
// is tried and succeeds.
func TestIntegration_FallbackChainFirstFailsSecondSucceeds(t *testing.T) {
	// First provider: fails on Summarize.
	p1 := provider.NewFake()
	p1.NameVal = "nvidia"
	p1.LocalityVal = provider.LocalityRemote
	p1.SummarizeErrs = []error{context.DeadlineExceeded}

	// Second provider: succeeds.
	var capturedInput provider.SummarizeInput
	p2 := provider.NewFake()
	p2.NameVal = "gemini"
	p2.LocalityVal = provider.LocalityRemote
	p2.SummarizeResponses = []provider.SummarizeResult{
		{Text: "## Changelog\n- feat: new feature\n", Model: "gemini-model", TokensUsed: 30},
	}
	p2.OnSummarize = func(in provider.SummarizeInput) {
		capturedInput = in
	}

	var logs []string
	fc := &provider.FallbackChain{
		Providers: []provider.Provider{p1, p2},
		Dbg:       func(f string, a ...any) { logs = append(logs, f) },
	}

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"log --oneline v1.0.0..HEAD": {Stdout: "abc1234 feat: new feature"},
		},
	}

	out := &bytes.Buffer{}
	ai := config.Defaults().AI

	err := runAIChangelogCore(context.Background(), aiChangelogDeps{
		Runner:   runner,
		Provider: fc,
		Lang:     "en",
		AI:       ai,
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiChangelogFlags{from: "v1.0.0", format: "markdown"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Second provider should have been called.
	if capturedInput.Kind != "changelog" {
		t.Errorf("Kind: want %q, got %q", "changelog", capturedInput.Kind)
	}

	// Output should contain the second provider's result.
	if !strings.Contains(out.String(), "## Changelog") {
		t.Errorf("output should contain second provider's result, got: %s", out.String())
	}

	// Fallback should have logged the first failure.
	if len(logs) == 0 {
		t.Error("expected fallback debug log for first provider failure")
	}
}

// TestIntegration_PrivacyGateAbortOnTooManySecrets verifies that the
// Privacy Gate aborts when too many secrets are detected.
func TestIntegration_PrivacyGateAbortOnTooManySecrets(t *testing.T) {
	fake := provider.NewFake()
	fake.NameVal = "nvidia"
	fake.LocalityVal = provider.LocalityRemote
	fake.SummarizeResponses = []provider.SummarizeResult{{Text: "should not reach"}}

	// Build a diff with >10 secrets.
	var sb strings.Builder
	sb.WriteString("diff --git a/secrets.go b/secrets.go\n")
	for i := 0; i < 12; i++ {
		sb.WriteString("+api_key = \"AKIA")
		for j := 0; j < 16; j++ {
			sb.WriteByte('A' + byte(i))
		}
		sb.WriteString("\"\n")
	}
	secretDiff := sb.String()

	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --cached": {Stdout: secretDiff},
		},
	}

	out := &bytes.Buffer{}
	ai := config.Defaults().AI

	err := runAIReviewCore(context.Background(), aiReviewDeps{
		Runner:   runner,
		Provider: fake,
		Lang:     "en",
		AI:       ai,
		Out:      out,
		ErrOut:   &bytes.Buffer{},
	}, aiReviewFlags{format: "text"})

	if err == nil {
		t.Fatal("expected error when too many secrets detected")
	}
	if !strings.Contains(err.Error(), "privacy gate") {
		t.Errorf("error should mention privacy gate, got: %v", err)
	}
}

// TestIntegration_ApplyPrivacyGateHelper verifies the helper function
// directly for both remote and local providers.
func TestIntegration_ApplyPrivacyGateHelper(t *testing.T) {
	ai := config.Defaults().AI
	ai.Commit.DenyPaths = []string{".env"}

	payload := `some code
password = "hunter2-secret-password"
path/to/.env content
`

	// Remote provider: should redact.
	remoteProv := provider.NewFake()
	remoteProv.LocalityVal = provider.LocalityRemote

	redacted, findings, err := applyPrivacyGate(remoteProv, payload, ai)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) == 0 {
		t.Error("expected findings for remote provider")
	}
	if strings.Contains(redacted, "hunter2-secret-password") {
		t.Error("redacted output should not contain raw password")
	}

	// Local provider: should pass through.
	localProv := provider.NewFake()
	localProv.LocalityVal = provider.LocalityLocal

	passthrough, findings2, err := applyPrivacyGate(localProv, payload, ai)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings2) != 0 {
		t.Error("local provider should have no findings")
	}
	if passthrough != payload {
		t.Error("local provider should return payload unchanged")
	}
}

// TestIntegration_PrivacyGateRedactFunction verifies the Redact
// function directly with secrets and deny_paths.
func TestIntegration_PrivacyGateRedactFunction(t *testing.T) {
	payload := `diff --git a/.env b/.env
+AWS_SECRET=AKIA1234567890ABCDEF
+DB_PASSWORD = "my-database-password-12345"
`

	redacted, findings, err := aicommit.Redact(payload, aicommit.PrivacyGateOptions{
		DenyPaths:  []string{".env"},
		MaxSecrets: 10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) == 0 {
		t.Error("expected redaction findings")
	}
	if strings.Contains(redacted, "AKIA1234567890ABCDEF") {
		t.Error("redacted output should not contain AWS key")
	}
	if !strings.Contains(redacted, "[SECRET_") || !strings.Contains(redacted, "[PATH_") {
		t.Errorf("redacted output should contain placeholders, got: %s", redacted)
	}
}
