package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/aicommit"
)

func TestShowPromptFlagOnRoot(t *testing.T) {
	f := rootCmd.PersistentFlags().Lookup("show-prompt")
	if f == nil {
		t.Fatal("--show-prompt persistent flag not found on root command")
		return // unreachable, but staticcheck SA5011 needs the explicit terminator
	}
	if f.DefValue != "false" {
		t.Errorf("--show-prompt default: want %q, got %q", "false", f.DefValue)
	}
}

// TestRenderPrivacyFindingsSeparatesTally guards the report the user reads
// when the gate fires. The finding list and the threshold count deliberately
// differ — every match is redacted and reported, but only distinct real
// candidates are counted — so the output has to say which lines it is asking
// the user to look at, or the numbers read as a contradiction.
func TestRenderPrivacyFindingsSeparatesTally(t *testing.T) {
	findings := []aicommit.RedactFinding{
		{Kind: "secret", Placeholder: "[SECRET_1]", File: "main.go", FileLine: 10,
			Pattern: "api_key", Original: "sk_l***"},
		{Kind: "secret", Placeholder: "[SECRET_2]", File: "main_test.go", FileLine: 4,
			Pattern: "aws_access_key", Original: "AKIA***", Untallied: true, Reason: "placeholder"},
		{Kind: "secret", Placeholder: "[SECRET_3]", File: "main_test.go", FileLine: 9,
			Pattern: "api_key", Original: "sk_l***", Untallied: true, Reason: "duplicate"},
	}

	var buf bytes.Buffer
	renderPrivacyFindings(&buf, findings)
	out := buf.String()

	if !strings.Contains(out, "counted 1 of 3 against the threshold") {
		t.Errorf("summary must reconcile the tally with the finding count, got:\n%s", out)
	}
	if !strings.Contains(out, "(not counted: placeholder)") {
		t.Errorf("an exempted finding must name its reason, got:\n%s", out)
	}
	if !strings.Contains(out, "(not counted: duplicate)") {
		t.Errorf("a repeated value must be marked as such, got:\n%s", out)
	}
	// The tallied finding carries no marker — silence means "this one counted".
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "[SECRET_1]") && strings.Contains(line, "not counted") {
			t.Errorf("a tallied finding must not be marked exempt: %s", line)
		}
	}
}

// TestRenderPrivacyFindingsNoSummaryWhenAllCounted keeps the common case quiet:
// with nothing exempted there is no discrepancy to explain.
func TestRenderPrivacyFindingsNoSummaryWhenAllCounted(t *testing.T) {
	findings := []aicommit.RedactFinding{
		{Kind: "secret", Placeholder: "[SECRET_1]", File: "main.go", FileLine: 10,
			Pattern: "api_key", Original: "sk_l***"},
	}

	var buf bytes.Buffer
	renderPrivacyFindings(&buf, findings)

	if strings.Contains(buf.String(), "against the threshold") {
		t.Errorf("no exemptions means no reconciliation line, got:\n%s", buf.String())
	}
}
