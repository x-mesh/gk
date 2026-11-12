// Package rules holds the built-in gk guard policy rule implementations.
// Each file in this package contributes exactly one Rule to policy.Default.
package rules

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/policy"
	"github.com/x-mesh/gk/internal/scan"
	"github.com/x-mesh/gk/internal/secrets"
)

// SecretPatternsRule scans with the gitleaks ruleset embedded in gk and adds
// an informational Violation naming the scanner. The embedded engine follows
// the gitleaks detection steps (it matched gitleaks 8.30.1 finding for
// finding on the gk history) and scans faster because large files are split
// across cores. The gitleaks binary
// runs instead only when the repo carries a gitleaks config (.gitleaks.toml,
// .gitleaksignore, or GITLEAKS_CONFIG*), because the embedded engine does
// not read those files.
type SecretPatternsRule struct {
	// GitleaksOpts are forwarded to scan.RunGitleaks. WorkDir is filled
	// from Input.WorkDir so callers typically leave it empty.
	GitleaksOpts scan.GitleaksOptions

	// ScanDiff scans a unified diff and ScanFile one file's content with the
	// built-in patterns. The rules package cannot import the cli package that
	// owns the scanners, so the caller injects them. Either nil disables the
	// built-in scan.
	ScanDiff func(diff string) []secrets.Finding
	ScanFile func(path, content string) []secrets.Finding

	// ScannerLabel describes the injected scanner in the fallback notice. It
	// is a func so a lazily loaded ruleset is not compiled until a scan
	// actually falls back. Nil reports the built-in pattern count.
	ScannerLabel func() string

	// Verify checks the credentials the embedded engine attached to its
	// findings when Input.Verify is set, keyed by Credential.ID. The caller
	// injects it so this package makes no network calls. Nil skips
	// verification.
	Verify func(context.Context, []*secrets.Credential) map[string]policy.Verification

	// runGitleaks is a test seam; nil means scan.RunGitleaks.
	runGitleaks func(context.Context, scan.GitleaksOptions) ([]scan.GitleaksFinding, error)
}

// NewSecretPatternsRule returns a SecretPatternsRule with redaction enabled.
// The gitleaks mode follows the input scope; callers can add flags through
// GitleaksOpts.ExtraArgs after construction.
func NewSecretPatternsRule() *SecretPatternsRule {
	return &SecretPatternsRule{GitleaksOpts: scan.GitleaksOptions{Redact: true}}
}

// Name implements policy.Rule.
func (*SecretPatternsRule) Name() string { return "secret_patterns" }

// Evaluate scans with the embedded ruleset unless the repo has a gitleaks
// config, in which case it runs gitleaks and maps each finding to a
// Violation with SeverityError. A missing gitleaks binary falls back to the
// embedded ruleset with a notice that the config was not read.
func (r *SecretPatternsRule) Evaluate(ctx context.Context, in policy.Input) ([]policy.Violation, error) {
	opts := r.gitleaksOptions(in)
	if in.Runner != nil && r.builtinReady() && !scan.HasGitleaksConfig(opts.WorkDir) {
		return r.evaluateBuiltin(ctx, in, r.embeddedNotice(in))
	}
	run := r.runGitleaks
	if run == nil {
		run = scan.RunGitleaks
	}
	findings, err := run(ctx, opts)
	if err != nil {
		if errors.Is(err, scan.ErrGitleaksNotInstalled) {
			return r.evaluateBuiltin(ctx, in, r.builtinNotice(in))
		}
		return nil, fmt.Errorf("secret_patterns: %w", err)
	}

	out := convertFindings(findings)
	if in.Verify {
		out = append(out, policy.Violation{
			RuleID:   r.Name(),
			Severity: policy.SeverityInfo,
			Message:  "nothing was verified: --verify checks findings of the embedded ruleset, and gitleaks ran instead",
			Hint:     "gitleaks runs because the repo has .gitleaks.toml or .gitleaksignore, or GITLEAKS_CONFIG is set",
		})
	}
	return out, nil
}

func (r *SecretPatternsRule) builtinReady() bool { return r.ScanDiff != nil && r.ScanFile != nil }

// gitleaksOptions maps the input scope onto a gitleaks invocation: the
// working directory for the default scope, staged changes, a pushed range,
// or the history of every ref.
func (r *SecretPatternsRule) gitleaksOptions(in policy.Input) scan.GitleaksOptions {
	opts := r.GitleaksOpts
	if opts.WorkDir == "" {
		opts.WorkDir = in.WorkDir
	}
	var scope []string
	switch {
	case in.Staged:
		opts.Mode, scope = "git", []string{"--staged"}
	case in.PushRange != "":
		opts.Mode, scope = "git", []string{"--log-opts=" + in.PushRange}
	case in.History:
		opts.Mode = "git"
	default:
		opts.Mode = "dir"
	}
	opts.ExtraArgs = append(scope, opts.ExtraArgs...)
	return opts
}

// scopeLabel names what a scan covered, for the notice.
func scopeLabel(in policy.Input) string {
	switch {
	case in.Staged:
		return "staged changes"
	case in.PushRange != "":
		return "push range " + in.PushRange
	case in.History:
		return "the history of every ref"
	default:
		return "tracked files"
	}
}

// evaluateBuiltin scans with the built-in patterns. Scope follows the input:
// staged changes, the pushed range, the history of every ref, or by default
// the current content of the tracked files.
func (r *SecretPatternsRule) evaluateBuiltin(ctx context.Context, in policy.Input, notice policy.Violation) ([]policy.Violation, error) {
	if in.Runner == nil || !r.builtinReady() {
		return []policy.Violation{{
			RuleID:   r.Name(),
			Severity: policy.SeverityInfo,
			Message:  "gitleaks not installed; secret_patterns is a no-op",
			Hint:     "brew install gitleaks  (gk doctor will confirm)",
		}}, nil
	}

	var findings []secrets.Finding
	switch {
	case in.Staged:
		diff, err := scanCommand(ctx, in.Runner, "diff", "--cached", "--no-color")
		if err != nil {
			return nil, err
		}
		findings = r.ScanDiff(diff)
	case in.PushRange != "":
		diff, err := scanCommand(ctx, in.Runner, "diff", "--no-color", in.PushRange)
		if err != nil {
			return nil, err
		}
		findings = r.ScanDiff(diff)
	case in.History:
		var err error
		if findings, err = r.scanHistory(ctx, in.Runner); err != nil {
			return nil, err
		}
	default:
		var err error
		if findings, err = r.scanTrackedFiles(ctx, in.Runner); err != nil {
			return nil, err
		}
	}

	var verified map[string]policy.Verification
	if in.Verify && r.Verify != nil {
		var creds []*secrets.Credential
		for _, f := range findings {
			if f.Credential != nil {
				creds = append(creds, f.Credential)
			}
		}
		if len(creds) > 0 {
			verified = r.Verify(ctx, creds)
		}
	}

	out := []policy.Violation{notice}
	for _, f := range findings {
		v := policy.Violation{
			RuleID:   r.Name(),
			Severity: policy.SeverityError,
			File:     f.File,
			Line:     f.FileLine,
			Message:  fmt.Sprintf("secret matched built-in pattern %s (%s)", f.Kind, f.Sample),
			Hint:     "rotate the secret and add a justified allowlist entry if it was intentional",
		}
		if f.Credential != nil && verified != nil {
			if res, ok := verified[f.Credential.ID()]; ok {
				v.Verification = &res
				if hint, ok := liveHint[res.Provider]; ok && res.Status == verifyStatusValid {
					v.Hint = hint
				}
			}
		}
		out = append(out, v)
	}
	if in.Verify && r.Verify != nil {
		out = append(out, verifySummary(r.Name(), verified))
	}
	return out, nil
}

// verifyStatusValid mirrors verify.StatusValid without importing the
// network package here.
const verifyStatusValid = "valid"

// liveHint replaces the generic hint on a finding the issuer accepted. A
// live GCP API key can be a client key that is public by design, so its
// hint points at restrictions instead of deletion only.
var liveHint = map[string]string{
	secrets.ProviderAWS:   "the key is live: deactivate it in IAM now, then rotate it",
	secrets.ProviderSlack: "the token is live: revoke it now (auth.revoke, or reinstall the app), then issue a new one",
	secrets.ProviderGCP:   "the key exists: delete it, or if it is a client key that must stay public, restrict it to its APIs and apps in the Google Cloud console",
}

var providerLabel = map[string]string{
	secrets.ProviderAWS:   "AWS",
	secrets.ProviderSlack: "Slack",
	secrets.ProviderGCP:   "GCP",
}

// verifySummary counts the distinct credentials per outcome, so a value
// repeated across files or commits counts once.
func verifySummary(rule string, verified map[string]policy.Verification) policy.Violation {
	if len(verified) == 0 {
		return policy.Violation{RuleID: rule, Severity: policy.SeverityInfo, Message: "verify: no AWS, Slack, or GCP credential to check"}
	}
	counts := map[string]map[string]int{}
	for _, v := range verified {
		if counts[v.Provider] == nil {
			counts[v.Provider] = map[string]int{}
		}
		counts[v.Provider][v.Status]++
	}
	var groups []string
	for _, p := range []string{secrets.ProviderAWS, secrets.ProviderSlack, secrets.ProviderGCP} {
		var parts []string
		for _, s := range []string{"valid", "rejected", "unknown", "unverifiable"} {
			if n := counts[p][s]; n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, s))
			}
		}
		if len(parts) > 0 {
			groups = append(groups, providerLabel[p]+" "+strings.Join(parts, ", "))
		}
	}
	v := policy.Violation{
		RuleID:   rule,
		Severity: policy.SeverityInfo,
		Message:  fmt.Sprintf("verify: checked %d credential(s): %s", len(verified), strings.Join(groups, "; ")),
	}
	if counts[secrets.ProviderAWS]["rejected"] > 0 {
		v.Hint = "AWS rejected means the global STS endpoint refused the key; a China or GovCloud key is refused there too"
	}
	return v
}

func scanCommand(ctx context.Context, runner git.Runner, args ...string) (string, error) {
	stdout, _, err := runner.Run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("secret_patterns: built-in scan: %w", err)
	}
	return string(stdout), nil
}

// binarySniff is how far git looks for a NUL byte to call a file binary.
const binarySniff = 8000

// scanTrackedFiles scans the working-tree content of every tracked file, so
// uncommitted edits count and ignored files do not. Files are read and
// scanned in parallel; findings keep `git ls-files` order. A binary file is
// matched by path only, which is what path rules such as pkcs12-file need.
func (r *SecretPatternsRule) scanTrackedFiles(ctx context.Context, runner git.Runner) ([]secrets.Finding, error) {
	top, err := scanCommand(ctx, runner, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	// Only the newline: a repo path may start or end with a space.
	root := strings.TrimSuffix(top, "\n")
	// The :/ pathspec lists the whole repo even when gk runs in a subdirectory.
	listing, err := scanCommand(ctx, runner, "ls-files", "-z", "--full-name", "--", ":/")
	if err != nil {
		return nil, err
	}
	var paths []string
	for p := range strings.SplitSeq(listing, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}

	results := make([][]secrets.Finding, len(paths))
	var g errgroup.Group
	g.SetLimit(runtime.GOMAXPROCS(0))
	for i, p := range paths {
		g.Go(func() error {
			full := filepath.Join(root, filepath.FromSlash(p))
			info, err := os.Lstat(full)
			if errors.Is(err, fs.ErrNotExist) {
				// Deleted in the working tree: nothing left to scan.
				return nil
			}
			if err != nil {
				// An unreadable path is not a clean one.
				return fmt.Errorf("secret_patterns: stat %s: %w", p, err)
			}
			if !info.Mode().IsRegular() {
				// Symlinks and submodule directories have no content of their own.
				return nil
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return fmt.Errorf("secret_patterns: read %s: %w", p, err)
			}
			content := string(data)
			if bytes.IndexByte(data[:min(len(data), binarySniff)], 0) >= 0 {
				content = ""
			}
			results[i] = r.ScanFile(p, content)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	var out []secrets.Finding
	for _, f := range results {
		out = append(out, f...)
	}
	return out, nil
}

// History chunk bounds. Each chunk is one `git log --no-walk` process, so a
// chunk too small spends more on process start than on diffs, and the upper
// bound keeps the SHA list well under the OS argument-length limit.
const (
	historyChunkMin = 32
	historyChunkMax = 1000
)

// scanHistory scans every commit reachable from any ref. A single
// `git log -p --all` produces the diffs on one core and took longer than
// the scan itself, so the commits are split into chunks that git diffs in
// parallel, and each chunk is scanned as soon as its diff arrives. The
// options match gitleaks' git mode (-U0, all refs), and findings keep
// rev-list order.
func (r *SecretPatternsRule) scanHistory(ctx context.Context, runner git.Runner) ([]secrets.Finding, error) {
	revs, err := scanCommand(ctx, runner, "rev-list", "--all")
	if err != nil {
		return nil, err
	}
	shas := strings.Fields(revs)
	if len(shas) == 0 {
		return nil, nil
	}
	workers := runtime.GOMAXPROCS(0)
	per := min(max((len(shas)+workers-1)/workers, historyChunkMin), historyChunkMax)
	var chunks [][]string
	for lo := 0; lo < len(shas); lo += per {
		chunks = append(chunks, shas[lo:min(lo+per, len(shas))])
	}

	results := make([][]secrets.Finding, len(chunks))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)
	for i, chunk := range chunks {
		g.Go(func() error {
			args := append([]string{"log", "-p", "-U0", "--no-color", "--no-walk=unsorted"}, chunk...)
			diff, err := scanCommand(gctx, runner, args...)
			if err != nil {
				return err
			}
			results[i] = r.ScanDiff(diff)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	var out []secrets.Finding
	for _, f := range results {
		out = append(out, f...)
	}
	return out, nil
}

func (r *SecretPatternsRule) scannerLabel() string {
	if r.ScannerLabel != nil {
		return r.ScannerLabel()
	}
	return fmt.Sprintf("%d built-in patterns", len(secrets.BuiltinPatterns))
}

func (r *SecretPatternsRule) embeddedNotice(in policy.Input) policy.Violation {
	return policy.Violation{
		RuleID:   r.Name(),
		Severity: policy.SeverityInfo,
		Message:  "scanned " + scopeLabel(in) + " with " + r.scannerLabel(),
		Hint:     "gitleaks runs instead when .gitleaks.toml or .gitleaksignore is present",
	}
}

func (r *SecretPatternsRule) builtinNotice(in policy.Input) policy.Violation {
	return policy.Violation{
		RuleID:   r.Name(),
		Severity: policy.SeverityInfo,
		Message:  "gitleaks not installed; scanned " + scopeLabel(in) + " with " + r.scannerLabel(),
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
