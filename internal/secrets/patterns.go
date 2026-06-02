// Package secrets provides built-in secret-pattern scanning for gk push.
package secrets

import (
	"fmt"
	"regexp"
	"strings"
)

// Finding is a single secret-pattern hit.
type Finding struct {
	Kind string // short label: "aws-access-key", "github-token", "private-key", ...
	Line int    // 1-based line number within the input blob (kept verbatim for
	// callers — e.g. aicommit — that remap blob lines themselves)
	File     string // owning file path, recovered from PayloadFileHeader markers; "" when none present
	FileLine int    // 1-based line within File; equals Line when no file header precedes the hit
	Sample   string // a masked sample suitable for display
}

// Location renders the finding's position for display: "file:line" when the
// owning file is known (the scan input carried PayloadFileHeader markers),
// otherwise "line N" using the raw blob position. Keeps push/ship output in
// sync from a single source.
func (f Finding) Location() string {
	if f.File != "" {
		return fmt.Sprintf("%s:%d", f.File, f.FileLine)
	}
	return fmt.Sprintf("line %d", f.Line)
}

// Pattern is a named regex.
type Pattern struct {
	Kind  string
	Regex *regexp.Regexp
}

// BuiltinPatterns is the default scan set. Keep it small and high-signal.
// Each entry must be broadly unambiguous (avoid matching normal code).
var BuiltinPatterns = []Pattern{
	{Kind: "aws-access-key", Regex: regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{Kind: "aws-secret-key", Regex: regexp.MustCompile(`(?i)aws[_-]?(?:secret|sk)[^"']{0,5}["']?([A-Za-z0-9/+=]{40})`)},
	{Kind: "github-token", Regex: regexp.MustCompile(`\bghp_[A-Za-z0-9]{36}\b`)},
	{Kind: "github-fine", Regex: regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{82}\b`)},
	{Kind: "slack-token", Regex: regexp.MustCompile(`\bxox[abpr]-[A-Za-z0-9-]{10,}\b`)},
	{Kind: "openai-key", Regex: regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`)},
	{Kind: "private-key", Regex: regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |DSA |ENCRYPTED |PGP )?PRIVATE KEY-----`)},
	{Kind: "generic-secret", Regex: regexp.MustCompile(`(?i)(api[_-]?key|secret|token|password)\s*[:=]\s*["']([A-Za-z0-9/_\-+=]{24,})["']`)},
}

// Scan scans blob for secret findings.
// extra lets callers append user-provided compiled regexes from config.
// Returns findings in line order; multiple findings per line are included.
func Scan(blob string, extra []*regexp.Regexp) []Finding {
	var out []Finding
	lines := strings.Split(blob, "\n")
	// Track the most recent PayloadFileHeader so each hit can name its file
	// and a file-relative line. header is the 1-based blob line of that
	// marker (0 = none seen yet); curFile is its decoded path.
	curFile := ""
	header := 0
	for i, line := range lines {
		if m := PayloadFileHeaderRE.FindStringSubmatch(line); m != nil {
			curFile = m[1]
			header = i + 1
			continue // boundary marker is metadata, never scanned
		}
		for _, p := range BuiltinPatterns {
			if sm := p.Regex.FindStringSubmatch(line); sm != nil {
				// Placeholder filter — apply to high-volume false-positive
				// kinds. generic-secret is regex-broad; aws-access-key is
				// fixed-shape but AWS docs themselves use AKIAIOSFODNN7EXAMPLE
				// as the canonical sample, so any line containing
				// "example"/"dummy"/etc. is overwhelmingly fixture data.
				if (p.Kind == "generic-secret" || p.Kind == "aws-access-key") && isPlaceholder(line) {
					continue
				}
				out = append(out, newFinding(p.Kind, i+1, header, curFile, secretValue(sm)))
			}
		}
		for _, re := range extra {
			if sm := re.FindStringSubmatch(line); sm != nil {
				label := re.String()
				if len(label) > 12 {
					label = label[:12]
				}
				out = append(out, newFinding("custom-"+strings.TrimSpace(label), i+1, header, curFile, secretValue(sm)))
			}
		}
	}
	return out
}

// newFinding builds a Finding from a blob hit. blobLine is the 1-based line
// within the scanned blob; header is the blob line of the owning
// PayloadFileHeader (0 when none). FileLine is blobLine-header — the file
// content begins on the line *after* the marker, so the marker line itself
// maps to 0 and the first content line to 1.
func newFinding(kind string, blobLine, header int, file, match string) Finding {
	fileLine := blobLine
	if header > 0 {
		fileLine = blobLine - header
	}
	return Finding{Kind: kind, Line: blobLine, File: file, FileLine: fileLine, Sample: mask(match)}
}

// secretValue returns the substring worth masking for display. Patterns
// like generic-secret (group 1 = keyword, group 2 = value) or aws-secret-key
// (group 1 = value) capture the credential separately from surrounding
// boilerplate; we mask the last non-empty capture group so the sample shows
// the value's prefix (e.g. "dev-****") instead of the keyword ("secr****"),
// which is what a reader needs to judge a false positive. Patterns with no
// capture groups fall back to the full match.
func secretValue(sm []string) string {
	for i := len(sm) - 1; i >= 1; i-- {
		if sm[i] != "" {
			return sm[i]
		}
	}
	return sm[0]
}

// placeholderKeywords flag fixture/example values so they don't trip the
// scan. Stored in normalized form (lowercase, hyphens/underscores removed)
// and matched against a likewise-normalized line, so separator variants
// collapse together: "change-me", "change_me", and "changeme" all match the
// single "changeme" entry. This is what lets dev defaults like
// `_FALLBACK_SECRET = "dev-insecure-secret-change-me"` pass.
var placeholderKeywords = []string{
	"example", "placeholder", "your", "xxx", "changeme", "replaceme",
	"todo", "fixme", "insert", "dummy", "sample", "testkey", "testsecret",
	"fakekey", "fakesecret", "insecure", "donotuse", "notreal", "fallback",
}

// placeholderSeps strips the separators that split multi-word placeholder
// tokens before matching against placeholderKeywords.
var placeholderSeps = strings.NewReplacer("-", "", "_", "")

// isPlaceholder returns true when the line contains an example/placeholder
// value. Only applied to high-false-positive kinds (generic-secret,
// aws-access-key).
func isPlaceholder(line string) bool {
	norm := placeholderSeps.Replace(strings.ToLower(line))
	for _, kw := range placeholderKeywords {
		if strings.Contains(norm, kw) {
			return true
		}
	}
	return false
}

// CompilePatterns compiles user-supplied regex strings.
// Returns compiled list and the list of raw patterns that failed to compile.
func CompilePatterns(raw []string) (compiled []*regexp.Regexp, bad []string) {
	for _, s := range raw {
		re, err := regexp.Compile(s)
		if err != nil {
			bad = append(bad, s)
			continue
		}
		compiled = append(compiled, re)
	}
	return
}

// mask replaces everything after the 4th character with up to 8 asterisks.
func mask(s string) string {
	if len(s) <= 4 {
		return strings.Repeat("*", len(s))
	}
	stars := len(s) - 4
	if stars > 8 {
		stars = 8
	}
	return s[:4] + strings.Repeat("*", stars)
}
