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
			name:      "aws-access-key",
			input:     "export AWS_KEY=AKIAIOSFODNN7EXAMPLE",
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
		"export KEY=AKIAIOSFODNN7EXAMPLE",
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
