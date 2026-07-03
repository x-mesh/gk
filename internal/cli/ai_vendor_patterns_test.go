package cli

import (
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/aicommit"
)

// A bare vendor token (no "token=" keyword) must be redacted by the AI
// privacy gate now that the push scanner's patterns are wired in —
// aicommit's keyword-shaped builtins alone cannot catch these.
func TestVendorSecretPatternsWired(t *testing.T) {
	payload := "commit says: use ghp_" + strings.Repeat("a", 36) + " for CI\n" +
		"slack hook xoxb-1234567890-abcdefghij\n"
	red, findings, err := aicommit.Redact(payload, aicommit.PrivacyGateOptions{
		SecretPatterns: vendorSecretPatterns,
		MaxSecrets:     -1,
	})
	if err != nil {
		t.Fatalf("Redact: %v", err)
	}
	if strings.Contains(red, "ghp_") || strings.Contains(red, "xoxb-") {
		t.Errorf("vendor tokens survived redaction:\n%s", red)
	}
	if len(findings) < 2 {
		t.Errorf("findings = %d, want >= 2", len(findings))
	}
}
