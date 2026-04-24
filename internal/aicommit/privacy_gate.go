package aicommit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// PrivacyGateOptions configures the Redact function.
type PrivacyGateOptions struct {
	DenyPaths      []string         // glob patterns (filepath.Match syntax)
	SecretPatterns []*regexp.Regexp  // extra patterns beyond built-in
	AuditEnabled   bool
	AuditPath      string // ".gk/ai-audit.jsonl"
	MaxSecrets     int    // default: 10, abort threshold
}

// RedactFinding records one redaction event.
type RedactFinding struct {
	Kind        string `json:"kind"`        // "secret" | "path" | "pii"
	Original    string `json:"original"`    // masked sample (first 4 chars + "***")
	Placeholder string `json:"placeholder"` // "[SECRET_1]", "[PATH_1]"
	Line        int    `json:"line"`
}

// builtinSecretPatterns are compiled once at init time.
// These are single-line patterns applied per line.
var builtinSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(api[_\-]?key|apikey)\s*[:=]\s*['"]?([A-Za-z0-9_\-]{20,})['"]?`),
	regexp.MustCompile(`(?i)(token|bearer)\s*[:=]\s*['"]?([A-Za-z0-9_\-\.]{20,})['"]?`),
	regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[:=]\s*['"]?(\S{8,})['"]?`),
	regexp.MustCompile(`(?i)(AKIA[0-9A-Z]{16})`),
	regexp.MustCompile(`(?i)(secret|private[_\-]?key)\s*[:=]\s*['"]?(\S{10,})['"]?`),
}

// builtinMultiLinePatterns match across line boundaries (e.g. PEM blocks).
// Applied to the full payload before line-by-line processing.
var builtinMultiLinePatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]+-----[\s\S]*?-----END [A-Z ]+-----`),
}


// Redact scans payload for deny_paths matches and secret patterns,
// replacing each with a numbered placeholder. Returns the redacted
// payload, a slice of findings, and an error if the secret count
// exceeds MaxSecrets.
func Redact(payload string, opts PrivacyGateOptions) (string, []RedactFinding, error) {
	maxSecrets := opts.MaxSecrets
	if maxSecrets == 0 {
		maxSecrets = 10
	}

	allPatterns := append(builtinSecretPatterns[:len(builtinSecretPatterns):len(builtinSecretPatterns)], opts.SecretPatterns...)

	var findings []RedactFinding
	secretIdx, pathIdx := 0, 0

	// Phase 1: multi-line patterns (e.g. PEM blocks) on the full payload.
	for _, pat := range builtinMultiLinePatterns {
		matches := pat.FindAllString(payload, -1)
		for _, m := range matches {
			if !strings.Contains(payload, m) {
				continue
			}
			secretIdx++
			placeholder := fmt.Sprintf("[SECRET_%d]", secretIdx)
			// Determine the line number of the match start.
			lineNum := strings.Count(payload[:strings.Index(payload, m)], "\n") + 1
			payload = strings.Replace(payload, m, placeholder, 1)
			findings = append(findings, RedactFinding{
				Kind:        "secret",
				Original:    maskOriginal(m),
				Placeholder: placeholder,
				Line:        lineNum,
			})
		}
	}

	// Phase 2: line-by-line processing for single-line patterns.
	lines := strings.Split(payload, "\n")

	for i := range lines {
		lineNum := i + 1

		// Check deny_paths globs against path-like tokens in the line.
		if len(opts.DenyPaths) > 0 {
			lines[i], findings, pathIdx = redactPaths(lines[i], lineNum, opts.DenyPaths, findings, pathIdx)
		}

		// Check secret patterns (built-in + custom).
		lines[i], findings, secretIdx = redactSecrets(lines[i], lineNum, allPatterns, findings, secretIdx)

		// Abort early if too many secrets.
		if secretIdx > maxSecrets {
			return "", findings, fmt.Errorf("aicommit: privacy gate: %d secrets detected (threshold %d) — aborting", secretIdx, maxSecrets)
		}
	}

	redacted := strings.Join(lines, "\n")

	if opts.AuditEnabled && opts.AuditPath != "" && len(findings) > 0 {
		if err := writeRedactionAudit(opts.AuditPath, findings); err != nil {
			return "", nil, fmt.Errorf("aicommit: privacy gate audit: %w", err)
		}
	}

	return redacted, findings, nil
}

// redactPaths replaces path-like tokens matching any deny glob with
// [PATH_N] placeholders.
func redactPaths(line string, lineNum int, denyPaths []string, findings []RedactFinding, idx int) (string, []RedactFinding, int) {
	tokens := extractPathTokens(line)
	for _, tok := range tokens {
		if matchDenyGlob(tok, denyPaths) {
			idx++
			placeholder := fmt.Sprintf("[PATH_%d]", idx)
			line = strings.ReplaceAll(line, tok, placeholder)
			findings = append(findings, RedactFinding{
				Kind:        "path",
				Original:    maskOriginal(tok),
				Placeholder: placeholder,
				Line:        lineNum,
			})
		}
	}
	return line, findings, idx
}

// redactSecrets replaces secret pattern matches with [SECRET_N] placeholders.
func redactSecrets(line string, lineNum int, patterns []*regexp.Regexp, findings []RedactFinding, idx int) (string, []RedactFinding, int) {
	for _, pat := range patterns {
		matches := pat.FindAllString(line, -1)
		for _, m := range matches {
			if !strings.Contains(line, m) {
				continue // already replaced by a previous pattern
			}
			idx++
			placeholder := fmt.Sprintf("[SECRET_%d]", idx)
			line = strings.Replace(line, m, placeholder, 1)
			findings = append(findings, RedactFinding{
				Kind:        "secret",
				Original:    maskOriginal(m),
				Placeholder: placeholder,
				Line:        lineNum,
			})
		}
	}
	return line, findings, idx
}

// extractPathTokens splits a line into tokens that look like file paths.
// A token is path-like if it contains a '/' or a '.'.
func extractPathTokens(line string) []string {
	var paths []string
	for _, tok := range strings.Fields(line) {
		// Strip common surrounding punctuation.
		tok = strings.Trim(tok, `"',:;()[]{}`)
		if tok == "" {
			continue
		}
		if strings.Contains(tok, "/") || strings.Contains(tok, "\\") {
			paths = append(paths, tok)
		}
	}
	return paths
}

// matchDenyGlob returns true if path matches any deny glob (by basename
// first, then full path), mirroring the existing matchDeny in gather.go.
func matchDenyGlob(path string, patterns []string) bool {
	base := filepath.Base(path)
	for _, g := range patterns {
		if g == "" {
			continue
		}
		if ok, _ := filepath.Match(g, base); ok {
			return true
		}
		if ok, _ := filepath.Match(g, path); ok {
			return true
		}
	}
	return false
}

// maskOriginal returns the first 4 characters of s followed by "***".
// If s is shorter than 4 characters, the entire string is shown.
func maskOriginal(s string) string {
	runes := []rune(s)
	if len(runes) <= 4 {
		return s + "***"
	}
	return string(runes[:4]) + "***"
}

// redactionAuditEntry is the JSONL shape written to the audit log.
type redactionAuditEntry struct {
	Timestamp string          `json:"timestamp"`
	Event     string          `json:"event"`
	Findings  []RedactFinding `json:"findings"`
}

// writeRedactionAudit appends a single JSONL line to the audit file.
func writeRedactionAudit(path string, findings []RedactFinding) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	entry := redactionAuditEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Event:     "redaction",
		Findings:  findings,
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	b = append(b, '\n')

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if _, err := f.Write(b); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}
