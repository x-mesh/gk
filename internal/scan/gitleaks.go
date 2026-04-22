// Package scan centralizes secret-scanning helpers used by gk guard.
//
// Per the 2026-04-22 probe verdict, gk prefers the industry-standard
// `gitleaks` binary over maintaining a parallel scanner. This file provides
// adapter primitives: JSON parsing of gitleaks output, version probing,
// and binary detection. Invocation wiring (spawning gitleaks and piping
// findings into gk guard rules) lives in SCAN-10 and GD-07.
package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// GitleaksFinding mirrors the shape of a single entry in gitleaks' JSON
// report output (gitleaks v8+, `--report-format json`). Only the fields gk
// needs are included; gitleaks emits more keys (Tags, Fingerprint, Entropy,
// etc.) that downstream rules can pick up later via a json.RawMessage on
// GitleaksFinding if needed.
type GitleaksFinding struct {
	Description string  `json:"Description"`
	File        string  `json:"File"`
	StartLine   int     `json:"StartLine"`
	EndLine     int     `json:"EndLine"`
	StartColumn int     `json:"StartColumn"`
	EndColumn   int     `json:"EndColumn"`
	Match       string  `json:"Match"`
	Secret      string  `json:"Secret"`
	RuleID      string  `json:"RuleID"`
	Commit      string  `json:"Commit"`
	Author      string  `json:"Author"`
	Email       string  `json:"Email"`
	Date        string  `json:"Date"`
	Message     string  `json:"Message"`
	Entropy     float64 `json:"Entropy"`
}

// ParseGitleaksFindings parses a gitleaks JSON report payload into typed
// findings. Empty input (or a payload containing just `null` / `[]`) returns
// an empty slice without error — gitleaks emits those shapes when no
// findings are present.
func ParseGitleaksFindings(data []byte) ([]GitleaksFinding, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	var out []GitleaksFinding
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, fmt.Errorf("parse gitleaks findings: %w", err)
	}
	return out, nil
}

// FindGitleaks locates the gitleaks binary via $PATH and, if present, runs
// `gitleaks version` to extract a version string. Returns (path, version, ok=true)
// when detected. When missing or the version probe fails, version may be
// empty but path/ok are still set accurately.
func FindGitleaks() (path, version string, ok bool) {
	p, err := exec.LookPath("gitleaks")
	if err != nil {
		return "", "", false
	}
	out, err := exec.Command(p, "version").Output()
	if err != nil {
		return p, "", true
	}
	return p, strings.TrimSpace(string(out)), true
}

// ErrGitleaksNotInstalled is returned by callers that require gitleaks but
// could not find it in $PATH. Not used by the parser itself — intended for
// SCAN-10 / GD-07 wiring.
var ErrGitleaksNotInstalled = errors.New("gitleaks binary not found in $PATH")

// GitleaksOptions customize RunGitleaks invocation.
type GitleaksOptions struct {
	// WorkDir runs gitleaks with the given directory as cwd. Empty uses the
	// process cwd.
	WorkDir string
	// Mode selects the gitleaks scan subcommand ("git", "dir", "detect", ...).
	// Empty defaults to "git" (scan the git history).
	Mode string
	// ExtraArgs appends additional flags after the defaults (e.g. "--log-opts=-1").
	ExtraArgs []string
	// Redact masks discovered secret values in the output. Defaults to true
	// because gk prints findings to terminals / CI logs by default.
	Redact bool
}

// RunGitleaks spawns the gitleaks binary with the canonical "emit JSON to
// stdout" flags and parses the findings via ParseGitleaksFindings.
//
// Exit codes:
//   - 0  → no findings (empty slice returned)
//   - 1  → findings present (parsed + returned; NOT an error)
//   - other → surfaced as error
//
// If gitleaks is not installed (FindGitleaks returns ok=false) this function
// returns ErrGitleaksNotInstalled immediately without spawning.
func RunGitleaks(ctx context.Context, opts GitleaksOptions) ([]GitleaksFinding, error) {
	path, _, ok := FindGitleaks()
	if !ok {
		return nil, ErrGitleaksNotInstalled
	}
	mode := opts.Mode
	if mode == "" {
		mode = "git"
	}
	args := []string{mode,
		"--report-format", "json",
		"--report-path", "/dev/stdout",
		"--no-banner",
	}
	if opts.Redact {
		args = append(args, "--redact")
	}
	args = append(args, opts.ExtraArgs...)

	cmd := exec.CommandContext(ctx, path, args...)
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}
	out, runErr := cmd.Output()

	findings, parseErr := interpretGitleaksExit(out, runErr)
	return findings, parseErr
}

// interpretGitleaksExit isolates the exit-code handling from the actual
// subprocess call so the logic is unit-testable via synthetic
// exec.ExitError / nil pairs.
func interpretGitleaksExit(out []byte, runErr error) ([]GitleaksFinding, error) {
	if runErr == nil {
		// Exit 0 — no findings.
		return ParseGitleaksFindings(out)
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		code := ee.ExitCode()
		switch code {
		case 1:
			// Findings present — not an error per the gitleaks contract.
			return ParseGitleaksFindings(out)
		default:
			// Anything else is a real failure. Include stderr tail when
			// available so diagnostics stay useful.
			stderrTail := strings.TrimSpace(string(ee.Stderr))
			if stderrTail == "" {
				return nil, fmt.Errorf("gitleaks exited %d", code)
			}
			// Keep the first line only — gitleaks is verbose on setup errors.
			if idx := strings.IndexByte(stderrTail, '\n'); idx >= 0 {
				stderrTail = stderrTail[:idx]
			}
			return nil, fmt.Errorf("gitleaks exited %d: %s", code, stderrTail)
		}
	}
	// Not an ExitError (timeout, binary missing mid-run, etc.) — surface as-is.
	return nil, runErr
}
