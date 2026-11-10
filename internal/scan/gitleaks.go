// Package scan centralizes secret-scanning helpers used by gk guard.
//
// gk guard scans with the gitleaks ruleset embedded in gk and spawns the
// gitleaks binary only for repos with a gitleaks config. This file provides
// the adapter primitives for that path: JSON parsing of gitleaks output,
// version probing, binary detection, and invocation.
package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
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

// findingsExitCode is passed as --exit-code. gitleaks exits 1 both on a
// fatal error (an unwritable report path, a bad config) and, by default, on
// findings, so a distinct code is the only way to tell them apart.
const findingsExitCode = 42

// RunGitleaks spawns the gitleaks binary, has it write a JSON report to a
// temp file, and parses the findings via ParseGitleaksFindings. The report
// goes to a file because gitleaks 8.30.1 on macOS rejected
// --report-path /dev/stdout with "permission denied".
func RunGitleaks(ctx context.Context, opts GitleaksOptions) ([]GitleaksFinding, error) {
	path, _, ok := FindGitleaks()
	if !ok {
		return nil, ErrGitleaksNotInstalled
	}
	report, err := os.CreateTemp("", "gk-gitleaks-*.json")
	if err != nil {
		return nil, fmt.Errorf("gitleaks report file: %w", err)
	}
	reportPath := report.Name()
	_ = report.Close()
	defer os.Remove(reportPath)

	mode := opts.Mode
	if mode == "" {
		mode = "git"
	}
	args := []string{mode,
		"--report-format", "json",
		"--report-path", reportPath,
		"--exit-code", strconv.Itoa(findingsExitCode),
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
	_, runErr := cmd.Output()
	if runErr != nil && !isExitCode(runErr, findingsExitCode) {
		return nil, gitleaksFailure(runErr)
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		return nil, fmt.Errorf("read gitleaks report: %w", err)
	}
	return interpretGitleaksExit(data, runErr)
}

func isExitCode(err error, code int) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == code
}

func gitleaksFailure(runErr error) error {
	var ee *exec.ExitError
	if !errors.As(runErr, &ee) {
		// Not an ExitError (timeout, binary missing mid-run, etc.).
		return runErr
	}
	// gitleaks is verbose on setup errors; the first stderr line names the cause.
	first, _, _ := strings.Cut(strings.TrimSpace(string(ee.Stderr)), "\n")
	if first == "" {
		return fmt.Errorf("gitleaks exited %d", ee.ExitCode())
	}
	return fmt.Errorf("gitleaks exited %d: %s", ee.ExitCode(), first)
}

// interpretGitleaksExit maps a finished run onto findings. The temp report
// starts empty and gitleaks writes "[]" on a clean run, so an empty report
// means gitleaks never wrote it; returning zero findings then would turn a
// failed scan into a clean one.
func interpretGitleaksExit(report []byte, runErr error) ([]GitleaksFinding, error) {
	if strings.TrimSpace(string(report)) == "" {
		if runErr != nil {
			return nil, fmt.Errorf("gitleaks reported leaks but wrote no report")
		}
		return nil, fmt.Errorf("gitleaks exited 0 but wrote no report")
	}
	return ParseGitleaksFindings(report)
}
