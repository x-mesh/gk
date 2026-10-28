// Package rules holds the built-in gk guard policy rule implementations.
// Each file in this package contributes exactly one Rule to policy.Default.
package rules

import (
	"context"
	"errors"
	"fmt"

	"github.com/x-mesh/gk/internal/policy"
	"github.com/x-mesh/gk/internal/scan"
	"github.com/x-mesh/gk/internal/secrets"
)

// SecretPatternsRule delegates secret scanning to the gitleaks binary when
// present. When the binary is missing it scans the diff with the built-in
// patterns instead and adds an informational Violation, so users see both
// the findings and why coverage is narrower in their environment.
//
// Per the 2026-04-22 probe verdict, gk prefers the industry-standard
// gitleaks over maintaining a parallel scanner. The rule is intentionally
// thin — all detection logic lives in gitleaks; this adapter just maps
// GitleaksFindings to policy.Violations.
type SecretPatternsRule struct {
	// GitleaksOpts are forwarded to scan.RunGitleaks. WorkDir is filled
	// from Input.WorkDir so callers typically leave it empty.
	GitleaksOpts scan.GitleaksOptions

	// ScanDiff scans a unified diff with the built-in patterns. The rules
	// package cannot import the cli package that owns the diff scanner, so
	// the caller injects it. Nil disables the built-in fallback.
	ScanDiff func(diff string) []secrets.Finding

	// ScannerLabel describes the injected scanner in the fallback notice. It
	// is a func so a lazily loaded ruleset is not compiled until a scan
	// actually falls back. Nil reports the built-in pattern count.
	ScannerLabel func() string

	// runGitleaks is a test seam; nil means scan.RunGitleaks.
	runGitleaks func(context.Context, scan.GitleaksOptions) ([]scan.GitleaksFinding, error)
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
// is absent, Evaluate falls back to the built-in patterns and emits one
// Info Violation explaining the narrower coverage.
func (r *SecretPatternsRule) Evaluate(ctx context.Context, in policy.Input) ([]policy.Violation, error) {
	opts := r.GitleaksOpts
	if opts.WorkDir == "" {
		opts.WorkDir = in.WorkDir
	}
	run := r.runGitleaks
	if run == nil {
		run = scan.RunGitleaks
	}
	findings, err := run(ctx, opts)
	if err != nil {
		if errors.Is(err, scan.ErrGitleaksNotInstalled) {
			return r.evaluateBuiltin(ctx, in)
		}
		return nil, fmt.Errorf("secret_patterns: %w", err)
	}

	return convertFindings(findings), nil
}

// evaluateBuiltin scans with the built-in patterns. Scope follows the input:
// staged changes, the pushed range, or the whole reachable history (the same
// scope as gitleaks' git mode).
func (r *SecretPatternsRule) evaluateBuiltin(ctx context.Context, in policy.Input) ([]policy.Violation, error) {
	if in.Runner == nil || r.ScanDiff == nil {
		return []policy.Violation{{
			RuleID:   r.Name(),
			Severity: policy.SeverityInfo,
			Message:  "gitleaks not installed; secret_patterns is a no-op",
			Hint:     "brew install gitleaks  (gk doctor will confirm)",
		}}, nil
	}

	var args []string
	switch {
	case in.Staged:
		args = []string{"diff", "--cached", "--no-color"}
	case in.PushRange != "":
		args = []string{"diff", "--no-color", in.PushRange}
	default:
		// An unborn HEAD makes `git log -p HEAD` fail with "ambiguous
		// argument"; a repo with no commits has no history to scan.
		if _, _, err := in.Runner.Run(ctx, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
			return []policy.Violation{r.builtinNotice()}, nil
		}
		args = []string{"log", "-p", "--no-color", "HEAD"}
	}
	stdout, _, err := in.Runner.Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("secret_patterns: built-in scan: %w", err)
	}

	out := []policy.Violation{r.builtinNotice()}
	for _, f := range r.ScanDiff(string(stdout)) {
		out = append(out, policy.Violation{
			RuleID:   r.Name(),
			Severity: policy.SeverityError,
			File:     f.File,
			Line:     f.FileLine,
			Message:  fmt.Sprintf("secret matched built-in pattern %s (%s)", f.Kind, f.Sample),
			Hint:     "rotate the secret and add a justified allowlist entry if it was intentional",
		})
	}
	return out, nil
}

func (r *SecretPatternsRule) builtinNotice() policy.Violation {
	label := fmt.Sprintf("%d built-in patterns", len(secrets.BuiltinPatterns))
	if r.ScannerLabel != nil {
		label = r.ScannerLabel()
	}
	return policy.Violation{
		RuleID:   r.Name(),
		Severity: policy.SeverityInfo,
		Message:  "gitleaks not installed; scanned with " + label,
		Hint:     "brew install gitleaks for the latest rules and .gitleaks.toml support  (gk doctor will confirm)",
	}
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
