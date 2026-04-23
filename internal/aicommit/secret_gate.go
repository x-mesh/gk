package aicommit

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/x-mesh/gk/internal/scan"
	"github.com/x-mesh/gk/internal/secrets"
)

// SecretFinding is one hit from the secret gate. Source names which
// scanner produced it ("builtin" or "gitleaks") so callers can phrase
// advice ("install gitleaks for deeper coverage") accurately.
type SecretFinding struct {
	Source string
	Kind   string
	File   string
	Line   int
	Sample string // masked / truncated; never raw secret
}

// SecretGateOptions configures the gate.
//
// ExtraPatterns adds repo-configured regexes to the built-in scanner
// (matches internal/secrets.Scan's second arg). AllowKinds suppresses
// findings whose Kind matches — used by `--allow-secret-kind` on the
// CLI. RunGitleaks gates whether to spawn gitleaks (when installed);
// default true.
type SecretGateOptions struct {
	ExtraPatterns []*regexp.Regexp
	AllowKinds    []string
	RunGitleaks   bool
}

// Gitleaks lets tests inject a fake runner. When nil the real
// scan.RunGitleaks (subprocess) is used.
type Gitleaks interface {
	Run(ctx context.Context) ([]scan.GitleaksFinding, error)
}

// realGitleaks invokes the actual gitleaks binary on the process cwd
// in "dir" mode (stage-agnostic) so unstaged + staged files are both
// covered. `git` mode would miss unstaged changes.
type realGitleaks struct{}

func (realGitleaks) Run(ctx context.Context) ([]scan.GitleaksFinding, error) {
	return scan.RunGitleaks(ctx, scan.GitleaksOptions{Mode: "dir", Redact: true})
}

// ScanPayload runs both scanners over the supplied text (usually the
// aggregated diff) and returns deduplicated SecretFindings. Errors
// from gitleaks not being installed are swallowed silently — the
// built-in scanner is always the baseline.
func ScanPayload(ctx context.Context, payload string, opts SecretGateOptions, gl Gitleaks) ([]SecretFinding, error) {
	allow := map[string]bool{}
	for _, k := range opts.AllowKinds {
		allow[k] = true
	}

	var out []SecretFinding

	builtin := secrets.Scan(payload, opts.ExtraPatterns)
	for _, f := range builtin {
		if allow[f.Kind] {
			continue
		}
		out = append(out, SecretFinding{
			Source: "builtin",
			Kind:   f.Kind,
			Line:   f.Line,
			Sample: f.Sample,
		})
	}

	if opts.RunGitleaks {
		if gl == nil {
			gl = realGitleaks{}
		}
		gf, err := gl.Run(ctx)
		switch {
		case err == nil:
			out = append(out, convertGitleaks(gf, allow)...)
		case errors.Is(err, scan.ErrGitleaksNotInstalled):
			// Optional scanner; silent skip.
		default:
			return nil, fmt.Errorf("aicommit: gitleaks: %w", err)
		}
	}

	return dedupeFindings(out), nil
}

func convertGitleaks(findings []scan.GitleaksFinding, allow map[string]bool) []SecretFinding {
	out := make([]SecretFinding, 0, len(findings))
	for _, f := range findings {
		kind := f.RuleID
		if kind == "" {
			kind = f.Description
		}
		if allow[kind] {
			continue
		}
		out = append(out, SecretFinding{
			Source: "gitleaks",
			Kind:   kind,
			File:   f.File,
			Line:   f.StartLine,
			Sample: f.Match,
		})
	}
	return out
}

// dedupeFindings drops exact duplicates (same source+kind+file+line).
// Different sources reporting the same secret are kept — users want
// to know both caught it.
func dedupeFindings(in []SecretFinding) []SecretFinding {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := make([]SecretFinding, 0, len(in))
	for _, f := range in {
		key := f.Source + "|" + f.Kind + "|" + f.File + "|" + strconv.Itoa(f.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}
