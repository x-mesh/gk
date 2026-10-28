package rules

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/policy"
	"github.com/x-mesh/gk/internal/scan"
	"github.com/x-mesh/gk/internal/secrets"
)

func gitleaksMissing(context.Context, scan.GitleaksOptions) ([]scan.GitleaksFinding, error) {
	return nil, scan.ErrGitleaksNotInstalled
}

// stubRunner answers git calls by the joined argument string and records
// every call so tests can assert which diff the rule asked for.
type stubRunner struct {
	out   map[string]string
	fail  map[string]bool
	calls []string
}

func (s *stubRunner) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	key := strings.Join(args, " ")
	s.calls = append(s.calls, key)
	if s.fail[key] {
		return nil, nil, errors.New("exit 1")
	}
	return []byte(s.out[key]), nil, nil
}

func builtinRule(diffFinding ...secrets.Finding) *SecretPatternsRule {
	r := NewSecretPatternsRule()
	r.runGitleaks = gitleaksMissing
	r.ScanDiff = func(string) []secrets.Finding { return diffFinding }
	return r
}

func severities(v []policy.Violation) (info, errs int) {
	for _, x := range v {
		switch x.Severity {
		case policy.SeverityInfo:
			info++
		case policy.SeverityError:
			errs++
		}
	}
	return
}

func TestSecretPatternsRule_NoGitleaks_StagedScansCachedDiff(t *testing.T) {
	run := &stubRunner{out: map[string]string{"diff --cached --no-color": "DIFF"}}
	r := builtinRule(secrets.Finding{Kind: "aws-access-key", File: "a.env", FileLine: 3, Sample: "AKIA****"})
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, Staged: true})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if info, errs := severities(v); info != 1 || errs != 1 {
		t.Fatalf("info=%d err=%d, want 1/1: %+v", info, errs, v)
	}
	var f policy.Violation
	for _, x := range v {
		if x.Severity == policy.SeverityError {
			f = x
		}
	}
	if f.File != "a.env" || f.Line != 3 || !strings.Contains(f.Message, "aws-access-key") {
		t.Errorf("violation = %+v", f)
	}
	if len(run.calls) != 1 || run.calls[0] != "diff --cached --no-color" {
		t.Errorf("calls = %v", run.calls)
	}
}

func TestSecretPatternsRule_NoGitleaks_PushRangeScansRangeDiff(t *testing.T) {
	run := &stubRunner{out: map[string]string{"diff --no-color origin/main..HEAD": "DIFF"}}
	r := builtinRule(secrets.Finding{Kind: "github-token", File: "x.go", FileLine: 9})
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, PushRange: "origin/main..HEAD"})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if _, errs := severities(v); errs != 1 {
		t.Fatalf("errs=%d, want 1", errs)
	}
	if len(run.calls) != 1 || run.calls[0] != "diff --no-color origin/main..HEAD" {
		t.Errorf("calls = %v", run.calls)
	}
}

func TestSecretPatternsRule_NoGitleaks_DefaultScansHistory(t *testing.T) {
	run := &stubRunner{out: map[string]string{"log -p --no-color HEAD": "LOG"}}
	r := builtinRule(secrets.Finding{Kind: "private-key", File: "k.pem", FileLine: 1})
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if info, errs := severities(v); info != 1 || errs != 1 {
		t.Fatalf("info=%d err=%d, want 1/1", info, errs)
	}
	if !strings.Contains(v[0].Message, "built-in patterns") {
		t.Errorf("notice = %q", v[0].Message)
	}
}

func TestSecretPatternsRule_NoGitleaks_UnbornHeadReportsNoticeOnly(t *testing.T) {
	run := &stubRunner{fail: map[string]bool{"rev-parse --verify --quiet HEAD^{commit}": true}}
	r := builtinRule(secrets.Finding{Kind: "aws-access-key"})
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if info, errs := severities(v); info != 1 || errs != 0 {
		t.Fatalf("info=%d err=%d, want 1/0", info, errs)
	}
	for _, c := range run.calls {
		if strings.HasPrefix(c, "log ") {
			t.Errorf("log must not run on an unborn HEAD: %v", run.calls)
		}
	}
}

func TestSecretPatternsRule_NoGitleaks_GitFailureSurfaces(t *testing.T) {
	run := &stubRunner{fail: map[string]bool{"diff --cached --no-color": true}}
	r := builtinRule()
	if _, err := r.Evaluate(context.Background(), policy.Input{Runner: run, Staged: true}); err == nil {
		t.Fatal("want error when the diff command fails")
	}
}

func TestSecretPatternsRule_NoGitleaks_NilRunnerKeepsNoOpNotice(t *testing.T) {
	r := builtinRule(secrets.Finding{Kind: "aws-access-key"})
	v, err := r.Evaluate(context.Background(), policy.Input{})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(v) != 1 || !strings.Contains(v[0].Message, "no-op") {
		t.Fatalf("violations = %+v", v)
	}
}

func TestSecretPatternsRule_GitleaksPresent_SkipsBuiltin(t *testing.T) {
	run := &stubRunner{}
	r := NewSecretPatternsRule()
	r.runGitleaks = func(context.Context, scan.GitleaksOptions) ([]scan.GitleaksFinding, error) {
		return []scan.GitleaksFinding{{RuleID: "r", File: "f", StartLine: 1}}, nil
	}
	r.ScanDiff = func(string) []secrets.Finding { t.Fatal("built-in scan must not run"); return nil }
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, Staged: true})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(v) != 1 || len(run.calls) != 0 {
		t.Fatalf("violations=%+v calls=%v", v, run.calls)
	}
}

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
	r := NewSecretPatternsRule()
	r.runGitleaks = gitleaksMissing
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

func TestSecretPatternsRule_NoGitleaks_NoticeUsesScannerLabel(t *testing.T) {
	run := &stubRunner{out: map[string]string{"diff --cached --no-color": "DIFF"}}
	r := builtinRule()
	r.ScannerLabel = func() string { return "the embedded ruleset vX" }
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: run, Staged: true})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(v) != 1 || !strings.Contains(v[0].Message, "scanned with the embedded ruleset vX") {
		t.Fatalf("violations = %+v", v)
	}
}
