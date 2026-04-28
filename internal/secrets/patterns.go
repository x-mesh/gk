// Package secrets provides built-in secret-pattern scanning for gk push.
package secrets

import (
	"regexp"
	"strings"
)

// Finding is a single secret-pattern hit.
type Finding struct {
	Kind   string // short label: "aws-access-key", "github-token", "private-key", ...
	Line   int    // 1-based line number within the input blob
	Sample string // a masked sample suitable for display
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
	for i, line := range lines {
		for _, p := range BuiltinPatterns {
			if m := p.Regex.FindString(line); m != "" {
				// Placeholder filter — apply to high-volume false-positive
				// kinds. generic-secret is regex-broad; aws-access-key is
				// fixed-shape but AWS docs themselves use AKIAIOSFODNN7EXAMPLE
				// as the canonical sample, so any line containing
				// "example"/"dummy"/etc. is overwhelmingly fixture data.
				if (p.Kind == "generic-secret" || p.Kind == "aws-access-key") && isPlaceholder(line) {
					continue
				}
				out = append(out, Finding{Kind: p.Kind, Line: i + 1, Sample: mask(m)})
			}
		}
		for _, re := range extra {
			if m := re.FindString(line); m != "" {
				label := re.String()
				if len(label) > 12 {
					label = label[:12]
				}
				out = append(out, Finding{
					Kind:   "custom-" + strings.TrimSpace(label),
					Line:   i + 1,
					Sample: mask(m),
				})
			}
		}
	}
	return out
}

// isPlaceholder returns true for lines that contain example/placeholder values.
// Only applied to generic-secret to reduce false positives.
func isPlaceholder(line string) bool {
	lower := strings.ToLower(line)
	for _, kw := range []string{
		"example", "placeholder", "your_", "your-", "xxx", "changeme",
		"replace_me", "todo", "fixme", "insert_", "dummy", "sample",
		"test_key", "test_secret", "fake_key", "fake_secret",
		"<your", "\"your", "'your",
	} {
		if strings.Contains(lower, kw) {
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
