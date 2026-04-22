// Package rules holds the built-in gk guard policy rule implementations.
// Each file in this package contributes exactly one Rule to policy.Default.
package rules

import (
	"context"
	"errors"
	"fmt"

	"github.com/x-mesh/gk/internal/policy"
	"github.com/x-mesh/gk/internal/scan"
)

// SecretPatternsRule delegates secret scanning to the gitleaks binary when
// present, and returns a single informational Violation when the binary is
// missing so users see why the rule is a no-op in their environment.
//
// Per the 2026-04-22 probe verdict, gk prefers the industry-standard
// gitleaks over maintaining a parallel scanner. The rule is intentionally
// thin — all detection logic lives in gitleaks; this adapter just maps
// GitleaksFindings to policy.Violations.
type SecretPatternsRule struct {
	// GitleaksOpts are forwarded to scan.RunGitleaks. WorkDir is filled
	// from Input.WorkDir so callers typically leave it empty.
	GitleaksOpts scan.GitleaksOptions
}

// NewSecretPatternsRule returns a SecretPatternsRule with redaction enabled
// and git-history scan mode. Callers can tweak the exposed GitleaksOpts
// field after construction.
func NewSecretPatternsRule() *SecretPatternsRule {
	return &SecretPatternsRule{
		GitleaksOpts: scan.GitleaksOptions{
			Mode:   "git",
			Redact: true,
		},
	}
}

// Name implements policy.Rule.
func (*SecretPatternsRule) Name() string { return "secret_patterns" }

// Evaluate runs gitleaks against the repo rooted at in.WorkDir. Each
// gitleaks Finding becomes a Violation with SeverityError. When gitleaks
// is absent, Evaluate emits one Info Violation explaining the fallback so
// users see why no findings appeared.
func (r *SecretPatternsRule) Evaluate(ctx context.Context, in policy.Input) ([]policy.Violation, error) {
	opts := r.GitleaksOpts
	if opts.WorkDir == "" {
		opts.WorkDir = in.WorkDir
	}
	findings, err := scan.RunGitleaks(ctx, opts)
	if err != nil {
		if errors.Is(err, scan.ErrGitleaksNotInstalled) {
			return []policy.Violation{{
				RuleID:   r.Name(),
				Severity: policy.SeverityInfo,
				Message:  "gitleaks not installed; secret_patterns is a no-op",
				Hint:     "brew install gitleaks  (gk doctor will confirm)",
			}}, nil
		}
		return nil, fmt.Errorf("secret_patterns: %w", err)
	}

	return convertFindings(findings), nil
}

// convertFindings maps gitleaks findings into policy.Violations. Exported
// within the package so unit tests can verify the mapping without spawning
// gitleaks.
func convertFindings(findings []scan.GitleaksFinding) []policy.Violation {
	out := make([]policy.Violation, 0, len(findings))
	for _, f := range findings {
		msg := f.Description
		if msg == "" {
			msg = "secret matched rule " + f.RuleID
		}
		out = append(out, policy.Violation{
			RuleID:   "secret_patterns",
			Severity: policy.SeverityError,
			File:     f.File,
			Line:     f.StartLine,
			Message:  fmt.Sprintf("%s (%s)", msg, f.RuleID),
			Hint:     "rotate the secret and add a justified allowlist entry if it was intentional",
		})
	}
	return out
}
