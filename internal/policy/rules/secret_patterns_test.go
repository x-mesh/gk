package rules

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/policy"
	"github.com/x-mesh/gk/internal/scan"
)

func TestSecretPatternsRule_Name(t *testing.T) {
	r := NewSecretPatternsRule()
	if r.Name() != "secret_patterns" {
		t.Errorf("Name() = %q, want secret_patterns", r.Name())
	}
}

func TestSecretPatternsRule_ImplementsPolicyRule(t *testing.T) {
	var _ policy.Rule = (*SecretPatternsRule)(nil)
}

func TestSecretPatternsRule_NoGitleaks_EmitsInfoViolation(t *testing.T) {
	// Only exercise the sentinel path when gitleaks is genuinely absent.
	if _, _, ok := scan.FindGitleaks(); ok {
		t.Skip("gitleaks installed; cannot exercise missing-binary path here")
	}

	r := NewSecretPatternsRule()
	v, err := r.Evaluate(context.Background(), policy.Input{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(v) != 1 {
		t.Fatalf("got %d violations, want 1", len(v))
	}
	if v[0].Severity != policy.SeverityInfo {
		t.Errorf("Severity = %v, want Info", v[0].Severity)
	}
	if !strings.Contains(v[0].Message, "gitleaks not installed") {
		t.Errorf("Message = %q", v[0].Message)
	}
	if !strings.Contains(v[0].Hint, "brew install gitleaks") {
		t.Errorf("Hint = %q", v[0].Hint)
	}
}

// TestSecretPatternsRule_FindingsMap_ConvertFromSampleStruct exercises the
// GitleaksFinding → policy.Violation mapping by constructing the findings
// directly (no subprocess). Uses the same field shape that
// scan.ParseGitleaksFindings yields.
func TestSecretPatternsRule_FindingsMap_ConvertFromSampleStruct(t *testing.T) {
	findings := []scan.GitleaksFinding{
		{
			RuleID:      "aws-access-key-id",
			Description: "AWS Access Key",
			File:        "config/secrets.yml",
			StartLine:   5,
		},
		{
			RuleID:      "generic-api-key",
			Description: "", // trigger fallback message path
			File:        "src/client.go",
			StartLine:   12,
		},
	}

	violations := convertFindings(findings)
	if len(violations) != 2 {
		t.Fatalf("got %d violations, want 2", len(violations))
	}

	first := violations[0]
	if first.RuleID != "secret_patterns" {
		t.Errorf("RuleID = %q, want secret_patterns", first.RuleID)
	}
	if first.Severity != policy.SeverityError {
		t.Errorf("Severity = %v, want Error", first.Severity)
	}
	if first.File != "config/secrets.yml" || first.Line != 5 {
		t.Errorf("file/line = %q:%d, want config/secrets.yml:5", first.File, first.Line)
	}
	if !strings.Contains(first.Message, "AWS Access Key") ||
		!strings.Contains(first.Message, "aws-access-key-id") {
		t.Errorf("Message = %q", first.Message)
	}

	// Fallback message path when Description is empty.
	if !strings.Contains(violations[1].Message, "secret matched rule generic-api-key") {
		t.Errorf("fallback Message = %q", violations[1].Message)
	}
}

func TestConvertFindings_Empty(t *testing.T) {
	v := convertFindings(nil)
	if len(v) != 0 {
		t.Errorf("got %d violations for nil input, want 0", len(v))
	}
}
