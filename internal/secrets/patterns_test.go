package secrets

import (
	"regexp"
	"strings"
	"testing"
)

func TestScan_BuiltinPatterns(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantKind  string
		wantFound bool
	}{
		{
			name: "aws-access-key",
			// Use AKIA1234567890ABCDEF instead of AKIAIOSFODNN7EXAMPLE —
			// the latter trips the isPlaceholder filter (contains EXAMPLE)
			// since aws-access-key now opts into the same placeholder
			// suppression as generic-secret.
			input:     "export AWS_KEY=AKIA1234567890ABCDEF",
			wantKind:  "aws-access-key",
			wantFound: true,
		},
		{
			name:      "github-token ghp_",
			input:     "token = ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij",
			wantKind:  "github-token",
			wantFound: true,
		},
		{
			name:      "private-key rsa header",
			input:     "-----BEGIN RSA PRIVATE KEY-----",
			wantKind:  "private-key",
			wantFound: true,
		},
		{
			name:      "generic-secret api_key with quotes",
			input:     `api_key = "abcdefghijklmnopqrstuvwxyz1234"`,
			wantKind:  "generic-secret",
			wantFound: true,
		},
		{
			name:      "normal variable name password_hasher - no match",
			input:     "func password_hasher(input string) string {",
			wantKind:  "",
			wantFound: false,
		},
		{
			name:      "openai key",
			input:     "OPENAI_KEY=sk-abcdefghijklmnopqrstuvwxyz123456",
			wantKind:  "openai-key",
			wantFound: true,
		},
		{
			name:      "slack token",
			input:     "slack_token = xoxb-abcdefghij-klmnopqrstuv",
			wantKind:  "slack-token",
			wantFound: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			findings := Scan(tc.input, nil)
			if tc.wantFound {
				if len(findings) == 0 {
					t.Fatalf("expected at least one finding, got none")
				}
				found := false
				for _, f := range findings {
					if f.Kind == tc.wantKind {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected finding of kind %q, got %v", tc.wantKind, findings)
				}
			} else {
				if len(findings) != 0 {
					t.Errorf("expected no findings, got %v", findings)
				}
			}
		})
	}
}

func TestScan_MultiLineMultiplefindings(t *testing.T) {
	blob := strings.Join([]string{
		"normal line",
		"export KEY=AKIA1234567890ABCDEF",
		"another normal line",
		"token = ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij",
	}, "\n")

	findings := Scan(blob, nil)
	if len(findings) < 2 {
		t.Fatalf("expected at least 2 findings, got %d: %v", len(findings), findings)
	}

	// Verify line numbers are correct (1-based)
	for _, f := range findings {
		if f.Line < 1 {
			t.Errorf("line number should be >= 1, got %d", f.Line)
		}
	}

	// Verify order: aws finding should come before github finding
	awsIdx, ghIdx := -1, -1
	for i, f := range findings {
		if f.Kind == "aws-access-key" && awsIdx == -1 {
			awsIdx = i
		}
		if f.Kind == "github-token" && ghIdx == -1 {
			ghIdx = i
		}
	}
	if awsIdx == -1 {
		t.Error("missing aws-access-key finding")
	}
	if ghIdx == -1 {
		t.Error("missing github-token finding")
	}
	if awsIdx != -1 && ghIdx != -1 && awsIdx > ghIdx {
		t.Error("aws finding should appear before github finding (line order)")
	}
}

func TestScan_PayloadFileHeader_PopulatesFile(t *testing.T) {
	blob := strings.Join([]string{
		PayloadFileHeader("internal/config/app.go"),
		"normal line",
		"token = ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij",
	}, "\n")

	findings := Scan(blob, nil)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %v", len(findings), findings)
	}
	f := findings[0]
	if f.File != "internal/config/app.go" {
		t.Errorf("File = %q, want internal/config/app.go", f.File)
	}
	// header at blob line 1, hit at blob line 3 → file line 2.
	if f.FileLine != 2 {
		t.Errorf("FileLine = %d, want 2", f.FileLine)
	}
	// Line stays the raw blob position so callers that remap it (aicommit,
	// privacy_gate) are unaffected.
	if f.Line != 3 {
		t.Errorf("Line = %d, want 3 (blob position)", f.Line)
	}
	if f.Location() != "internal/config/app.go:2" {
		t.Errorf("Location() = %q, want internal/config/app.go:2", f.Location())
	}
}

func TestScan_NoHeader_LocationFallsBackToBlobLine(t *testing.T) {
	findings := Scan("token = ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij", nil)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	if f := findings[0]; f.File != "" || f.Location() != "line 1" {
		t.Errorf("got File=%q Location=%q, want File=\"\" Location=\"line 1\"", f.File, f.Location())
	}
}

func TestScan_HeaderLineNotScanned(t *testing.T) {
	// A path that itself contains a token-shaped substring must not be
	// flagged: the boundary marker is metadata, not content.
	blob := PayloadFileHeader("ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij.txt") + "\nclean line\n"
	if findings := Scan(blob, nil); len(findings) != 0 {
		t.Errorf("expected header line to be skipped, got %v", findings)
	}
}

func TestScan_SuppressesSeparatedPlaceholders(t *testing.T) {
	// Real-world false positive: a dev fallback default written with
	// hyphens. "change-me"/"insecure" must normalize to the joined
	// keywords and suppress the finding.
	cases := []string{
		`_FALLBACK_SECRET = "dev-insecure-secret-change-me"`,
		`secret = "please_change_me_before_deploying"`,
		`api_key = "not-real-fake-key-placeholder-1234"`,
	}
	for _, in := range cases {
		if f := Scan(in, nil); len(f) != 0 {
			t.Errorf("expected %q suppressed as placeholder, got %v", in, f)
		}
	}
}

func TestScan_GenericSecretMasksValueNotKeyword(t *testing.T) {
	// A real-looking value (no placeholder token) so the finding survives.
	// The sample must reveal the value's prefix, not the "api_token" keyword,
	// so a reader can judge whether it's a true hit.
	findings := Scan(`api_token = "Ab12Cd34Ef56Gh78Ij90Kl12Mn34"`, nil)
	if len(findings) == 0 {
		t.Fatal("expected a generic-secret finding")
	}
	s := findings[0].Sample
	if !strings.HasPrefix(s, "Ab12") {
		t.Errorf("Sample = %q, want it to mask the value (prefix Ab12)", s)
	}
	if strings.Contains(s, "api") || strings.Contains(s, "token") {
		t.Errorf("Sample = %q must not expose the keyword", s)
	}
}

func TestScan_ExtraPatterns(t *testing.T) {
	re := regexp.MustCompile(`MY_SECRET_[A-Z0-9]{16}`)
	blob := "config: MY_SECRET_ABCD1234EFGH5678"
	findings := Scan(blob, []*regexp.Regexp{re})
	if len(findings) == 0 {
		t.Fatal("expected finding from custom pattern, got none")
	}
	if !strings.HasPrefix(findings[0].Kind, "custom-") {
		t.Errorf("expected kind to start with 'custom-', got %q", findings[0].Kind)
	}
}

func TestMask(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"AKIA123456", "AKIA******"},
		{"AB", "**"},
		{"ABCD", "****"},
		{"ABCDE", "ABCD*"},
		{"ABCDEFGHIJKLMNOP", "ABCD********"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := mask(tc.input)
			if got != tc.want {
				t.Errorf("mask(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestCompilePatterns(t *testing.T) {
	t.Run("all valid", func(t *testing.T) {
		compiled, bad := CompilePatterns([]string{`foo\d+`, `bar[a-z]`})
		if len(bad) != 0 {
			t.Errorf("expected no bad patterns, got %v", bad)
		}
		if len(compiled) != 2 {
			t.Errorf("expected 2 compiled, got %d", len(compiled))
		}
	})

	t.Run("bad regex returned in bad list", func(t *testing.T) {
		compiled, bad := CompilePatterns([]string{`valid\d`, `[invalid`})
		if len(bad) != 1 || bad[0] != `[invalid` {
			t.Errorf("expected bad=[`[invalid`], got %v", bad)
		}
		if len(compiled) != 1 {
			t.Errorf("expected 1 compiled, got %d", len(compiled))
		}
	})
}

// TestScan_FixedPrefixPlaceholdersSuppressed covers the false-positive class
// that hit a real repo: fixed-prefix tokens (github-token, openai-key) whose
// value is a fixture, not a credential. Before the placeholder/low-entropy
// filter was extended past generic-secret/aws-access-key, every one of these
// blocked the push.
func TestScan_FixedPrefixPlaceholdersSuppressed(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		// example/dummy word on the line — caught by isPlaceholder.
		{"github example word", `gh = "ghp_ABCDEFGHIJ1234567890abcdefghij123456" // example token`},
		{"openai dummy word", `key = "sk-dummyAAAAAAAAAAAAAAAAAAAAAAAA"`},
		// monotonous value, no word — caught by isLowEntropySecret.
		{"github all-zero", `gh = "ghp_000000000000000000000000000000000000"`},
		{"openai single-char", `k = "sk-aaaaaaaaaaaaaaaaaaaaaa"`},
	}
	for _, c := range cases {
		if f := Scan(c.line, nil); len(f) != 0 {
			t.Errorf("%s: expected %q suppressed, got %v", c.name, c.line, f)
		}
	}
}

// TestScan_RealFixedPrefixTokensStillFlagged is the negative control for the
// suppression above: a high-entropy token with no placeholder word must still
// fire, or the filter would be hiding real leaks.
func TestScan_RealFixedPrefixTokensStillFlagged(t *testing.T) {
	cases := []struct {
		line string
		kind string
	}{
		{`let t = "ghp_abcdefghij1234567890ABCDEFGHIJ123456"`, "github-token"},
		{`let k = "sk-Xy7Qp2Rt9Vw4Zb1Nm6Kc3"`, "openai-key"},
	}
	for _, c := range cases {
		findings := Scan(c.line, nil)
		found := false
		for _, f := range findings {
			if f.Kind == c.kind {
				found = true
			}
		}
		if !found {
			t.Errorf("real %s should still be flagged in %q, got %v", c.kind, c.line, findings)
		}
	}
}

// TestMaskLine checks the in-place line masking used for verbose context: a
// clean line is untouched, a secret value is masked while its surrounding code
// stays readable, and keyword=value kinds mask only the value.
func TestMaskLine(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"no secret", "    init();", "    init();"},
		{
			"github token value masked, code intact",
			`    let gh = "ghp_abcdefghijklmnopqrstuvwxyz0123456789";`,
			`    let gh = "ghp_********";`,
		},
		{
			"generic secret masks value not keyword",
			`api_token = "Ab12Cd34Ef56Gh78Ij90Kl12Mn34"`,
			`api_token = "Ab12********"`,
		},
	}
	for _, tt := range tests {
		if got := MaskLine(tt.in); got != tt.want {
			t.Errorf("%s: MaskLine(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

// TestIsLowEntropySecret pins the distinct-rune floor: short values are never
// judged, monotonous long values are low-entropy, and a real base62 token is
// not.
func TestIsLowEntropySecret(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"short", false}, // below the 20-char floor
		{"ghp_000000000000000000000000000000000000", true},  // 5 distinct
		{"sk-aaaaaaaaaaaaaaaaaaaaaa", true},                 // 3 distinct
		{"ghp_abcdefghij1234567890ABCDEFGHIJ123456", false}, // ~30 distinct
	}
	for _, tt := range tests {
		if got := isLowEntropySecret(tt.value); got != tt.want {
			t.Errorf("isLowEntropySecret(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}
