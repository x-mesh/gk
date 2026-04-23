package aicommit

import (
	"context"
	"errors"
	"testing"

	"github.com/x-mesh/gk/internal/scan"
)

type fakeGitleaks struct {
	findings []scan.GitleaksFinding
	err      error
}

func (f fakeGitleaks) Run(context.Context) ([]scan.GitleaksFinding, error) {
	return f.findings, f.err
}

func TestScanPayloadBuiltinOnly(t *testing.T) {
	payload := "api_key: \"AKIAIOSFODNN7EXAMPLE\"\n"
	got, err := ScanPayload(context.Background(), payload, SecretGateOptions{}, fakeGitleaks{})
	if err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("findings: %+v", got)
	}
	if got[0].Source != "builtin" || got[0].Kind != "aws-access-key" {
		t.Errorf("finding: %+v", got[0])
	}
}

func TestScanPayloadAllowKindsSuppresses(t *testing.T) {
	payload := "AKIAIOSFODNN7EXAMPLE\n"
	got, err := ScanPayload(context.Background(), payload,
		SecretGateOptions{AllowKinds: []string{"aws-access-key"}}, fakeGitleaks{})
	if err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("AllowKinds should suppress aws-access-key, got %+v", got)
	}
}

func TestScanPayloadGitleaksAugments(t *testing.T) {
	payload := "\n" // no builtin hit
	gl := fakeGitleaks{findings: []scan.GitleaksFinding{{
		RuleID:    "stripe-key",
		StartLine: 7,
		File:      "config.yaml",
		Match:     "sk_live_***REDACTED***",
	}}}
	got, err := ScanPayload(context.Background(), payload, SecretGateOptions{RunGitleaks: true}, gl)
	if err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("findings: %+v", got)
	}
	if got[0].Source != "gitleaks" || got[0].Kind != "stripe-key" {
		t.Errorf("finding: %+v", got[0])
	}
	if got[0].File != "config.yaml" || got[0].Line != 7 {
		t.Errorf("finding location: %+v", got[0])
	}
}

func TestScanPayloadGitleaksMissingIsSilent(t *testing.T) {
	gl := fakeGitleaks{err: scan.ErrGitleaksNotInstalled}
	got, err := ScanPayload(context.Background(), "hello world\n",
		SecretGateOptions{RunGitleaks: true}, gl)
	if err != nil {
		t.Fatalf("ScanPayload: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("findings: %+v", got)
	}
}

func TestScanPayloadGitleaksRealErrorPropagates(t *testing.T) {
	gl := fakeGitleaks{err: errors.New("gitleaks: i/o timeout")}
	_, err := ScanPayload(context.Background(), "x\n",
		SecretGateOptions{RunGitleaks: true}, gl)
	if err == nil {
		t.Fatal("want wrapped error")
	}
}
