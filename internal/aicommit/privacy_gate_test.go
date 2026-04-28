package aicommit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// ---------------------------------------------------------------------------
// Unit tests (Task 6.5)
// ---------------------------------------------------------------------------

func TestRedactBasicSecretPattern(t *testing.T) {
	payload := `config:
  api_key = "sk_live_abcdefghij1234567890"
  token = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"
`
	redacted, findings, err := Redact(payload, PrivacyGateOptions{})
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if strings.Contains(redacted, "sk_live_abcdefghij1234567890") {
		t.Error("redacted output still contains the API key")
	}
	if strings.Contains(redacted, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9") {
		t.Error("redacted output still contains the token")
	}
	if len(findings) < 2 {
		t.Errorf("expected at least 2 findings, got %d: %+v", len(findings), findings)
	}
	for _, f := range findings {
		if f.Kind != "secret" {
			t.Errorf("expected kind=secret, got %q", f.Kind)
		}
	}
}

func TestRedactDenyPaths(t *testing.T) {
	payload := `diff --git a/internal/secrets/key.pem b/internal/secrets/key.pem
+++ some content
`
	opts := PrivacyGateOptions{
		DenyPaths: []string{"*.pem"},
	}
	redacted, findings, err := Redact(payload, opts)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	// The path-like token containing .pem should be replaced.
	pathFindings := 0
	for _, f := range findings {
		if f.Kind == "path" {
			pathFindings++
		}
	}
	if pathFindings == 0 {
		t.Error("expected at least one path finding for *.pem deny glob")
	}
	_ = redacted
}

func TestRedactPlaceholderFormat(t *testing.T) {
	payload := `password = "supersecretpassword123"
api_key = "AKIAIOSFODNN7EXAMPLE1234"
`
	redacted, findings, err := Redact(payload, PrivacyGateOptions{})
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	for _, f := range findings {
		if f.Kind == "secret" && !strings.HasPrefix(f.Placeholder, "[SECRET_") {
			t.Errorf("secret placeholder %q doesn't match [SECRET_N] format", f.Placeholder)
		}
		if f.Kind == "path" && !strings.HasPrefix(f.Placeholder, "[PATH_") {
			t.Errorf("path placeholder %q doesn't match [PATH_N] format", f.Placeholder)
		}
		if !strings.HasSuffix(f.Original, "***") {
			t.Errorf("Original %q should be masked with ***", f.Original)
		}
	}
	// Verify placeholders appear in the redacted output.
	for _, f := range findings {
		if !strings.Contains(redacted, f.Placeholder) {
			t.Errorf("redacted output missing placeholder %q", f.Placeholder)
		}
	}
}

func TestRedactSecretThresholdExceeded(t *testing.T) {
	// Build a payload with 11 distinct secrets.
	var lines []string
	for i := 0; i < 11; i++ {
		lines = append(lines, fmt.Sprintf("api_key = \"key_%02d_abcdefghijklmnopqrst\"", i))
	}
	payload := strings.Join(lines, "\n")

	_, _, err := Redact(payload, PrivacyGateOptions{})
	if err == nil {
		t.Fatal("expected error when secret count > 10")
	}
	if !strings.Contains(err.Error(), "threshold") {
		t.Errorf("error should mention threshold: %v", err)
	}
}

func TestRedactSecretThresholdNotExceeded(t *testing.T) {
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf("api_key = \"key_%02d_abcdefghijklmnopqrst\"", i))
	}
	payload := strings.Join(lines, "\n")

	_, _, err := Redact(payload, PrivacyGateOptions{})
	if err != nil {
		t.Fatalf("expected no error for 10 secrets, got: %v", err)
	}
}

func TestRedactAuditLogging(t *testing.T) {
	tmpDir := t.TempDir()
	auditPath := filepath.Join(tmpDir, ".gk", "ai-audit.jsonl")

	payload := `token = "mytoken_abcdefghijklmnopqrst"`
	opts := PrivacyGateOptions{
		AuditEnabled: true,
		AuditPath:    auditPath,
	}
	_, _, err := Redact(payload, opts)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}

	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 audit line, got %d", len(lines))
	}

	var entry redactionAuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("parse audit entry: %v", err)
	}
	if entry.Event != "redaction" {
		t.Errorf("event=%q, want redaction", entry.Event)
	}
	if entry.Timestamp == "" {
		t.Error("timestamp should be set")
	}
	if len(entry.Findings) == 0 {
		t.Error("findings should not be empty")
	}
}

func TestRedactAuditNotWrittenWhenDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	auditPath := filepath.Join(tmpDir, ".gk", "ai-audit.jsonl")

	payload := `token = "mytoken_abcdefghijklmnopqrst"`
	opts := PrivacyGateOptions{
		AuditEnabled: false,
		AuditPath:    auditPath,
	}
	_, _, err := Redact(payload, opts)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}

	if _, err := os.Stat(auditPath); err == nil {
		t.Error("audit file should not exist when audit is disabled")
	}
}

func TestRedactCustomSecretPattern(t *testing.T) {
	payload := `CUSTOM_SECRET_XYZ_1234567890abcdef`
	custom := regexp.MustCompile(`CUSTOM_SECRET_[A-Za-z0-9_]{20,}`)
	opts := PrivacyGateOptions{
		SecretPatterns: []*regexp.Regexp{custom},
	}
	redacted, findings, err := Redact(payload, opts)
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if strings.Contains(redacted, "CUSTOM_SECRET_XYZ_1234567890abcdef") {
		t.Error("custom secret should be redacted")
	}
	if len(findings) == 0 {
		t.Error("expected at least one finding for custom pattern")
	}
}

func TestRedactMaskOriginal(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"abcdefgh", "abcd***"},
		{"ab", "ab***"},
		{"abcd", "abcd***"},
		{"", "***"},
	}
	for _, tt := range tests {
		got := maskOriginal(tt.input)
		if got != tt.want {
			t.Errorf("maskOriginal(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRedactPEMBlock(t *testing.T) {
	payload := `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEA0Z3VS5JJcds3xfn/ygWyF8PbnGy5AoV7
-----END RSA PRIVATE KEY-----`
	redacted, findings, err := Redact(payload, PrivacyGateOptions{})
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if strings.Contains(redacted, "BEGIN RSA PRIVATE KEY") {
		t.Error("PEM block should be redacted")
	}
	if len(findings) == 0 {
		t.Error("expected PEM finding")
	}
}

// TestRedactNoMatchOnRustMethodChains guards against the false-positive
// class where Rust/Python variable declarations like
//
//	let token = self.token_manager.get_valid_token().await?;
//	let access_token = new_token.access_token.clone();
//	access_token: token_file.access_token,
//
// were matched by `(token|bearer)\s*[:=]\s*[A-Za-z0-9_\-\.]{20,}` because
// the method chain itself satisfies the {20,} length once dots are
// allowed. The bare-value branch of the pattern must reject dots so
// those lines no longer trigger the privacy gate.
func TestRedactNoMatchOnRustMethodChains(t *testing.T) {
	cases := []string{
		`let new_token = self.load_from_sources().await?;`,
		`let access_token = new_token.access_token.clone();`,
		`access_token: token_file.access_token,`,
		`refresh_token: token_file.refresh_token,`,
		`let token = self.token_manager.get_valid_token().await?;`,
		`let token = manager.get_valid_token().await.unwrap();`,
		`let secret = config.value.unwrap_or_default();`,
		`pub fn password() -> Result<String, Error> {`,
	}
	for _, line := range cases {
		_, findings, err := Redact(line+"\n", PrivacyGateOptions{})
		if err != nil {
			t.Errorf("line %q: unexpected gate abort: %v", line, err)
		}
		var secretFindings []RedactFinding
		for _, f := range findings {
			if f.Kind == "secret" {
				secretFindings = append(secretFindings, f)
			}
		}
		if len(secretFindings) > 0 {
			t.Errorf("line %q: expected 0 secret findings, got %d: %+v",
				line, len(secretFindings), secretFindings)
		}
	}
}

// TestRedactStillMatchesQuotedSecrets verifies the regex tightening did
// not lose coverage of real secret literals. Quoted values may include
// dots (e.g. JWTs) and punctuation; bare values must use the tight
// key-safe alphabet to be matched.
func TestRedactStillMatchesQuotedSecrets(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"quoted api_key", `api_key = "sk_live_abcdefghijklmnop1234"`},
		{"quoted JWT token", `token = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig"`},
		{"quoted password with punctuation", `password = "P@ssw0rd-with!punct"`},
		{"quoted secret", `secret = "my-very-long-secret-value"`},
		{"bare api_key key-safe", `api_key = sk_live_abcdefghijklmnop1234`},
		{"bare token key-safe", `token = ghp_xxxxxxxxxxxxxxxxxxxxxxxx`},
		{"AWS access key", `aws_key = AKIAIOSFODNN7EXAMPLE`},
	}
	for _, tc := range cases {
		_, findings, err := Redact(tc.line+"\n", PrivacyGateOptions{})
		if err != nil {
			t.Errorf("%s: unexpected gate abort: %v", tc.name, err)
		}
		gotSecret := false
		for _, f := range findings {
			if f.Kind == "secret" {
				gotSecret = true
				break
			}
		}
		if !gotSecret {
			t.Errorf("%s: expected at least 1 secret finding for %q, got none",
				tc.name, tc.line)
		}
	}
}

// TestRedactFindingFileMapping verifies that findings inside a payload
// using the "### <path>" header convention resolve back to the source
// file and its in-file line number, so error reports can point at the
// real location rather than a payload-relative offset.
func TestRedactFindingFileMapping(t *testing.T) {
	payload := "### src/config.rs\n" +
		"const FOO: &str = \"hello\";\n" +
		"secret = \"sk_live_abcdefghij1234567890\"\n" +
		"### tools/cli.py\n" +
		"api_key = \"another_secret_value_1234\"\n"
	_, findings, err := Redact(payload, PrivacyGateOptions{MaxSecrets: 10})
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if len(findings) < 2 {
		t.Fatalf("expected ≥2 findings, got %d: %+v", len(findings), findings)
	}
	wantFiles := map[string]int{
		"src/config.rs": 2,
		"tools/cli.py":  1,
	}
	for _, f := range findings {
		want, ok := wantFiles[f.File]
		if !ok {
			t.Errorf("unexpected file %q in finding %+v", f.File, f)
			continue
		}
		if f.FileLine != want {
			t.Errorf("file %s: want line %d, got %d (finding=%+v)",
				f.File, want, f.FileLine, f)
		}
	}
}

// TestRedactDisabledThreshold verifies that a negative MaxSecrets keeps
// redaction running over arbitrarily many findings without aborting,
// so power users can audit findings via --show-prompt.
func TestRedactDisabledThreshold(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&b, "api_key = \"val_abcdefghij1234567890_%d\"\n", i)
	}
	_, findings, err := Redact(b.String(), PrivacyGateOptions{MaxSecrets: -1})
	if err != nil {
		t.Fatalf("expected no abort with MaxSecrets=-1, got %v", err)
	}
	if len(findings) < 25 {
		t.Errorf("expected ≥25 findings, got %d", len(findings))
	}
}

// ---------------------------------------------------------------------------
// Property-Based Tests (Task 6.6 & 6.7)
// ---------------------------------------------------------------------------

// Feature: nvidia-ai-provider, Property 5: Redaction 완전성
// For any payload containing deny_paths matches and secret patterns,
// the redacted output must NOT contain the original sensitive strings.
// Each redacted item must be replaced with [PATH_N] or [SECRET_N] format.
//
// **Validates: Requirements 9.2, 9.3, 9.4**
func TestPropertyRedactionCompleteness(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a random secret value (20+ chars to match patterns).
		secretValue := rapid.StringMatching(`[A-Za-z0-9_]{20,40}`).Draw(t, "secretValue")
		secretKind := rapid.SampledFrom([]string{"api_key", "token", "password"}).Draw(t, "secretKind")
		secretLine := fmt.Sprintf(`%s = "%s"`, secretKind, secretValue)

		// Generate a random path that matches a deny glob.
		pathBase := rapid.StringMatching(`[a-z]{3,10}`).Draw(t, "pathBase")
		denyExt := rapid.SampledFrom([]string{".pem", ".key", ".env"}).Draw(t, "denyExt")
		pathValue := fmt.Sprintf("config/%s%s", pathBase, denyExt)
		pathLine := fmt.Sprintf("file: %s modified", pathValue)

		// Combine into a payload (keep total secrets ≤ 10 to avoid threshold error).
		payload := pathLine + "\n" + secretLine

		denyGlob := "*" + denyExt
		opts := PrivacyGateOptions{
			DenyPaths: []string{denyGlob},
		}

		redacted, findings, err := Redact(payload, opts)
		if err != nil {
			t.Fatalf("Redact error: %v", err)
		}

		// Property: original sensitive strings must NOT appear in redacted output.
		for _, f := range findings {
			switch f.Kind {
			case "path":
				if strings.Contains(redacted, pathValue) {
					t.Errorf("redacted output contains original path %q", pathValue)
				}
				if !strings.HasPrefix(f.Placeholder, "[PATH_") || !strings.HasSuffix(f.Placeholder, "]") {
					t.Errorf("path placeholder %q doesn't match [PATH_N] format", f.Placeholder)
				}
			case "secret":
				// The full secret match (key=value) should be replaced.
				if strings.Contains(redacted, secretValue) {
					t.Errorf("redacted output contains original secret value %q", secretValue)
				}
				if !strings.HasPrefix(f.Placeholder, "[SECRET_") || !strings.HasSuffix(f.Placeholder, "]") {
					t.Errorf("secret placeholder %q doesn't match [SECRET_N] format", f.Placeholder)
				}
			}
		}

		// At least one finding should exist.
		if len(findings) == 0 {
			t.Error("expected at least one finding")
		}
	})
}

// Feature: nvidia-ai-provider, Property 6: Secret 임계값 초과 시 중단
// For any payload where secret count > 10: Redact returns error.
// For any payload where secret count <= 10: Redact returns successfully.
//
// **Validates: Requirements 9.7**
func TestPropertySecretThreshold(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		secretCount := rapid.IntRange(0, 20).Draw(t, "secretCount")

		var lines []string
		for i := 0; i < secretCount; i++ {
			// Each line has a unique secret to ensure distinct matches.
			lines = append(lines, fmt.Sprintf("api_key = \"unique_key_%02d_abcdefghijklmnop\"", i))
		}
		payload := strings.Join(lines, "\n")

		_, _, err := Redact(payload, PrivacyGateOptions{})

		if secretCount > 10 {
			if err == nil {
				t.Errorf("expected error for %d secrets (> 10), got nil", secretCount)
			}
		} else {
			if err != nil {
				t.Errorf("expected no error for %d secrets (<= 10), got: %v", secretCount, err)
			}
		}
	})
}
