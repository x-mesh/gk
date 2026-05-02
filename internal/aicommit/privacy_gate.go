package aicommit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/secrets"
)

// PrivacyGateOptions configures the Redact function.
type PrivacyGateOptions struct {
	DenyPaths      []string         // glob patterns (filepath.Match syntax)
	SecretPatterns []*regexp.Regexp // extra patterns beyond built-in
	AuditEnabled   bool
	AuditPath      string // ".gk/ai-audit.jsonl"
	MaxSecrets     int    // default: 10, abort threshold (use -1 to disable)
}

// RedactFinding records one redaction event.
//
// Line is the 1-based line number within the full payload (the blob the
// gate scanned). When the payload is the
// `secrets.PayloadFileHeader(<path>)\n<contents>` shape produced by
// summariseForSecretScan, File and FileLine resolve back to the source
// file and its in-file line so error reporting can point the user at
// the original location.
type RedactFinding struct {
	Kind        string `json:"kind"`                // "secret" | "path" | "pii"
	Original    string `json:"original"`            // masked sample (first 4 chars + "***")
	Placeholder string `json:"placeholder"`         // "[SECRET_1]", "[PATH_1]"
	Line        int    `json:"line"`                // line within the redacted payload
	File        string `json:"file,omitempty"`      // resolved source file (may be "")
	FileLine    int    `json:"file_line,omitempty"` // 1-based line within File
	Pattern     string `json:"pattern,omitempty"`   // regex/source that matched
}

// namedPattern pairs a regex with a stable label so findings can carry
// a human-meaningful "what matched" hint instead of the raw expression.
type namedPattern struct {
	name string
	re   *regexp.Regexp
}

// builtinSecretPatterns are compiled once at init time.
//
// Each pattern uses a "quoted-or-bare" alternation so values inside
// quotes can include broader characters (dots for JWTs, punctuation
// for passwords) while bare values are restricted to a tight key-safe
// alphabet (`A-Za-z0-9_-`). This keeps real secrets in matched while
// excluding common false positives like Rust/Python method chains
// (`let token = self.foo.bar()`) and struct-field assignments
// (`access_token: token_file.access_token`) where dots / parens / etc.
// drift the value off the secret-charset.
var builtinSecretPatterns = []namedPattern{
	{"api_key", regexp.MustCompile(`(?i)(api[_\-]?key|apikey)\s*[:=]\s*(?:["']([A-Za-z0-9_\-]{20,})["']|([A-Za-z0-9_\-]{20,}))`)},
	{"token", regexp.MustCompile(`(?i)(token|bearer)\s*[:=]\s*(?:["']([A-Za-z0-9_\-\.]{20,})["']|([A-Za-z0-9_\-]{20,}))`)},
	{"password", regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[:=]\s*(?:["'](\S{8,})["']|([A-Za-z0-9_\-!@#$%^&*+=]{8,}))`)},
	{"aws_access_key", regexp.MustCompile(`(?i)(AKIA[0-9A-Z]{16})`)},
	{"secret_or_private_key", regexp.MustCompile(`(?i)(secret|private[_\-]?key)\s*[:=]\s*(?:["'](\S{10,})["']|([A-Za-z0-9_\-]{10,}))`)},
}

// builtinMultiLinePatterns match across line boundaries (e.g. PEM blocks).
// Applied to the full payload before line-by-line processing.
var builtinMultiLinePatterns = []namedPattern{
	{"pem_block", regexp.MustCompile(`-----BEGIN [A-Z ]+-----[\s\S]*?-----END [A-Z ]+-----`)},
}

// payloadFileHeaderRE re-exports secrets.PayloadFileHeaderRE so code in
// this file keeps the local name without duplicating the regex source.
// The header format intentionally avoids markdown's `### ` prefix so
// real `### Foo` headings inside scanned content are not mistaken for
// file boundaries.
var payloadFileHeaderRE = secrets.PayloadFileHeaderRE

// Redact scans payload for deny_paths matches and secret patterns,
// replacing each with a numbered placeholder. Returns the redacted
// payload, a slice of findings, and an error if the secret count
// exceeds MaxSecrets.
//
// When the threshold is exceeded, the returned findings slice is still
// populated so callers can render a detailed report. Set MaxSecrets to
// a negative value to disable the abort while keeping redaction.
func Redact(payload string, opts PrivacyGateOptions) (string, []RedactFinding, error) {
	maxSecrets := opts.MaxSecrets
	if maxSecrets == 0 {
		maxSecrets = 10
	}

	custom := make([]namedPattern, 0, len(opts.SecretPatterns))
	for _, re := range opts.SecretPatterns {
		custom = append(custom, namedPattern{name: "custom", re: re})
	}
	allPatterns := append(builtinSecretPatterns[:len(builtinSecretPatterns):len(builtinSecretPatterns)], custom...)

	var findings []RedactFinding
	secretIdx, pathIdx := 0, 0

	// Phase 1: multi-line patterns (e.g. PEM blocks) on the full payload.
	for _, pat := range builtinMultiLinePatterns {
		matches := pat.re.FindAllString(payload, -1)
		for _, m := range matches {
			if !strings.Contains(payload, m) {
				continue
			}
			secretIdx++
			placeholder := fmt.Sprintf("[SECRET_%d]", secretIdx)
			// Determine the line number of the match start.
			lineNum := strings.Count(payload[:strings.Index(payload, m)], "\n") + 1
			payload = strings.Replace(payload, m, placeholder, 1)
			f := RedactFinding{
				Kind:        "secret",
				Original:    maskOriginal(m),
				Placeholder: placeholder,
				Line:        lineNum,
				Pattern:     pat.name,
			}
			findings = append(findings, f)
		}
	}

	// Phase 2: line-by-line processing for single-line patterns.
	lines := strings.Split(payload, "\n")

	// Track the most recent payload file header so single-line findings
	// can resolve back to the source file. fileLine counts non-header
	// lines since the last header.
	var currentFile string
	var currentFileLine int

	for i := range lines {
		lineNum := i + 1

		if h := payloadFileHeaderRE.FindStringSubmatch(lines[i]); h != nil {
			currentFile = h[1]
			currentFileLine = 0
			continue
		}
		currentFileLine++

		// Check deny_paths globs against path-like tokens in the line.
		if len(opts.DenyPaths) > 0 {
			lines[i], findings, pathIdx = redactPaths(lines[i], lineNum, opts.DenyPaths, findings, pathIdx, currentFile, currentFileLine)
		}

		// Check secret patterns (built-in + custom).
		lines[i], findings, secretIdx = redactSecrets(lines[i], lineNum, allPatterns, findings, secretIdx, currentFile, currentFileLine)

		// Abort early if too many secrets.
		if maxSecrets >= 0 && secretIdx > maxSecrets {
			return "", findings, fmt.Errorf("aicommit: privacy gate: %d secrets detected (threshold %d) — aborting", secretIdx, maxSecrets)
		}
	}

	// Resolve File/FileLine for multi-line findings (PEM blocks etc.) by
	// walking the original payload's headers up to each finding's line.
	resolveMultilineFindingFiles(payload, findings)

	redacted := strings.Join(lines, "\n")

	if opts.AuditEnabled && opts.AuditPath != "" && len(findings) > 0 {
		if err := writeRedactionAudit(opts.AuditPath, findings); err != nil {
			return "", nil, fmt.Errorf("aicommit: privacy gate audit: %w", err)
		}
	}

	return redacted, findings, nil
}

// resolveMultilineFindingFiles backfills File/FileLine for any finding
// whose File is empty (i.e. recorded by Phase 1, before per-line
// header tracking ran). Cheap because findings is small.
func resolveMultilineFindingFiles(payload string, findings []RedactFinding) {
	if len(findings) == 0 {
		return
	}
	needs := false
	for _, f := range findings {
		if f.File == "" {
			needs = true
			break
		}
	}
	if !needs {
		return
	}
	lines := strings.Split(payload, "\n")
	for i := range findings {
		if findings[i].File != "" {
			continue
		}
		var file string
		fileLine := 0
		for li := 0; li < len(lines) && li+1 <= findings[i].Line; li++ {
			if h := payloadFileHeaderRE.FindStringSubmatch(lines[li]); h != nil {
				file = h[1]
				fileLine = 0
				continue
			}
			fileLine++
		}
		findings[i].File = file
		findings[i].FileLine = fileLine
	}
}

// redactPaths replaces path-like tokens matching any deny glob with
// [PATH_N] placeholders.
func redactPaths(line string, lineNum int, denyPaths []string, findings []RedactFinding, idx int, file string, fileLine int) (string, []RedactFinding, int) {
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
				File:        file,
				FileLine:    fileLine,
				Pattern:     "deny_path",
			})
		}
	}
	return line, findings, idx
}

// redactSecrets replaces secret pattern matches with [SECRET_N] placeholders.
func redactSecrets(line string, lineNum int, patterns []namedPattern, findings []RedactFinding, idx int, file string, fileLine int) (string, []RedactFinding, int) {
	for _, pat := range patterns {
		matches := pat.re.FindAllString(line, -1)
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
				File:        file,
				FileLine:    fileLine,
				Pattern:     pat.name,
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

// matchDenyGlob returns true if path matches any deny glob. Delegates
// to matchDeny so basename / full-path / nested-component matching
// stays consistent across the two call sites (gather + redact).
func matchDenyGlob(path string, patterns []string) bool {
	return matchDeny(path, patterns) != ""
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
