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
	// Commit is the introducing git commit when the input is `git log -p` output.
	Commit string `json:"commit,omitempty"`
	Sample string // a masked sample suitable for display
	// Secret is the value that Sample masks. It formats masked too; only
	// Secret.Reveal returns the value.
	Secret Secret `json:"-"`
	// LineText, ContextBefore, and ContextAfter back verbose ("show the
	// surrounding source") output. They are populated only by diff-aware
	// callers (the push/ship scanner) and are all secret-masked, so printing
	// them never leaks an adjacent credential. Empty when not collected or at
	// a hunk edge.
	LineText      string `json:"line_text,omitempty"`
	ContextBefore string `json:"context_before,omitempty"`
	ContextAfter  string `json:"context_after,omitempty"`
	// Credential is set on an embedded-engine finding that
	// `gk guard check --verify` can test (AWS, Slack, GCP); it formats masked.
	Credential *Credential `json:"-"`
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
	// literals lists the strings that every match of Regex starts with,
	// after a \b when wordStart is set, compared ASCII case-insensitively
	// when fold is set. Scan then tries anchored, which is Regex anchored at
	// the start, only where a literal occurs. The regexp package does not
	// search for the literal itself when a pattern starts with \b or (?i),
	// and on the long lines of minified files the push scan of a large
	// history spent most of its time there. A nil anchored means Regex runs
	// on the whole line.
	literals  []string
	fold      bool
	wordStart bool
	anchored  *regexp.Regexp
}

// literalPattern returns a Pattern whose matches all start with one of
// literals, after a \b when wordStart is set.
func literalPattern(kind, expr string, wordStart, fold bool, literals ...string) Pattern {
	return Pattern{
		Kind: kind, Regex: regexp.MustCompile(expr),
		literals: literals, fold: fold, wordStart: wordStart,
		anchored: regexp.MustCompile(`^(?:` + expr + `)`),
	}
}

// find returns what p.Regex.FindStringSubmatch(line) returns. lower is
// asciiLower(line).
func (p Pattern) find(line, lower string) []string {
	// (?i) also matches U+017F for s and U+212A (Kelvin sign) for k, the
	// only non-ASCII letters that fold to ASCII ones.
	if p.anchored == nil || p.fold && strings.ContainsAny(line, "\u017f\u212a") {
		return p.Regex.FindStringSubmatch(line)
	}
	in := line
	if p.fold {
		in = lower
	}
	// next[i] is the next start of literals[i] at or after pos, -1 when
	// there is none. The leftmost start that matches gives the leftmost
	// match, so the first hit is the match that Regex finds.
	var buf [4]int
	next := buf[:0]
	for _, lit := range p.literals {
		next = append(next, strings.Index(in, lit))
	}
	for pos := 0; ; {
		at := -1
		for i, lit := range p.literals {
			if next[i] >= 0 && next[i] < pos {
				if j := strings.Index(in[pos:], lit); j >= 0 {
					next[i] = pos + j
				} else {
					next[i] = -1
				}
			}
			if next[i] >= 0 && (at < 0 || next[i] < at) {
				at = next[i]
			}
		}
		if at < 0 {
			return nil
		}
		// The anchored regex sees the start of its input as a word
		// boundary, so the character before the literal decides \b.
		if !p.wordStart || at == 0 || !isWordByte(line[at-1]) {
			if sm := p.anchored.FindStringSubmatch(line[at:]); sm != nil {
				return sm
			}
		}
		pos = at + 1
	}
}

// isWordByte reports whether c is an ASCII word character, which is what
// \b in the regexp package tests. Any byte of a multi-byte rune is not one.
func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// asciiLower returns s with A-Z mapped to a-z, and s itself when it has no
// upper-case ASCII letter.
func asciiLower(s string) string {
	i := strings.IndexFunc(s, func(r rune) bool { return r >= 'A' && r <= 'Z' })
	if i < 0 {
		return s
	}
	b := []byte(s)
	for j := i; j < len(b); j++ {
		if c := b[j]; c >= 'A' && c <= 'Z' {
			b[j] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// BuiltinPatterns is the default scan set. Keep it small and high-signal.
// Each entry must be broadly unambiguous (avoid matching normal code).
var BuiltinPatterns = []Pattern{
	literalPattern("aws-access-key", `\bAKIA[0-9A-Z]{16}\b`, true, false, "AKIA"),
	literalPattern("aws-secret-key", `(?i)aws[_-]?(?:secret|sk)[^"']{0,5}["']?([A-Za-z0-9/+=]{40})`, false, true, "aws"),
	literalPattern("github-token", `\bghp_[A-Za-z0-9]{36}\b`, true, false, "ghp_"),
	literalPattern("github-fine", `\bgithub_pat_[A-Za-z0-9_]{82}\b`, true, false, "github_pat_"),
	literalPattern("slack-token", `\bxox[abpr]-[A-Za-z0-9-]{10,}\b`, true, false, "xox"),
	literalPattern("openai-key", `\bsk-[A-Za-z0-9]{20,}\b`, true, false, "sk-"),
	{Kind: "private-key", Regex: regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |DSA |ENCRYPTED |PGP )?PRIVATE KEY-----`)},
	literalPattern("generic-secret", `(?i)(api[_-]?key|secret|token|password)\s*[:=]\s*["']([A-Za-z0-9/_\-+=]{24,})["']`, false, true, "api", "secret", "token", "password"),
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
		lower := asciiLower(line)
		for _, p := range BuiltinPatterns {
			if sm := p.find(line, lower); sm != nil {
				val := SecretValue(sm)
				// Drop fixture/example credentials. Applied to every kind
				// except private-key, whose PEM header is an unambiguous
				// signature with no value to weigh. IsPlaceholderLine catches
				// a line carrying example/dummy/test words — AWS docs ship
				// AKIAIOSFODNN7EXAMPLE, and a ghp_ token sitting next to
				// "example" is fixture data; IsLowEntropySecret catches
				// monotonous fakes like ghp_0000…0000 that name no such word
				// but cannot be a real high-entropy token. Without this, every
				// fixed-prefix kind (github-token, openai-key, …) flagged its
				// own placeholders.
				if p.Kind != "private-key" && (IsPlaceholderLine(line) || IsLowEntropySecret(val)) {
					continue
				}
				out = append(out, newFinding(p.Kind, i+1, header, curFile, val))
			}
		}
		for _, re := range extra {
			if sm := re.FindStringSubmatch(line); sm != nil {
				label := re.String()
				if len(label) > 12 {
					label = label[:12]
				}
				out = append(out, newFinding("custom-"+strings.TrimSpace(label), i+1, header, curFile, SecretValue(sm)))
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
	return Finding{Kind: kind, Line: blobLine, File: file, FileLine: fileLine, Sample: mask(match), Secret: NewSecret(match)}
}

// SecretValue returns the substring worth masking for display. Patterns
// like generic-secret (group 1 = keyword, group 2 = value) or aws-secret-key
// (group 1 = value) capture the credential separately from surrounding
// boilerplate; we mask the last non-empty capture group so the sample shows
// the value's prefix (e.g. "dev-****") instead of the keyword ("secr****"),
// which is what a reader needs to judge a false positive. Patterns with no
// capture groups fall back to the full match.
//
// Exported so the aicommit privacy gate identifies the credential inside its
// own (differently-grouped) patterns the same way, rather than weighing the
// whole `key = "value"` match as if it were the secret.
func SecretValue(sm []string) string {
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

// IsPlaceholderLine returns true when the line contains an example/placeholder
// value (example/dummy/test/changeme/…). Applied to every kind except
// private-key.
//
// Judged on the whole LINE, not the value alone, because the giveaway usually
// sits beside the credential — a comment, a variable name, an assert. That
// breadth is why callers that BLOCK on a hit (the push/ship scanner) drop the
// finding outright, while callers that merely REDACT (the aicommit privacy
// gate) keep redacting and only exempt the value from their abort threshold.
func IsPlaceholderLine(line string) bool {
	norm := placeholderSeps.Replace(strings.ToLower(line))
	for _, kw := range placeholderKeywords {
		if strings.Contains(norm, kw) {
			return true
		}
	}
	return false
}

// isLowEntropySecret reports whether a matched value is too monotonous to be a
// real credential. Genuine tokens are near-random over a large alphabet
// (base62 for github-token/openai-key); fixtures repeat a tiny one —
// "ghp_0000…0000", "sk-aaaa…aaaa", "1234…". Only values of 20+ characters are
// judged, so the distinct-rune floor never trips a real key: any real base62
// token that long clears 8 distinct characters with overwhelming probability,
// while reproduction-by-hand fakes do not.
func IsLowEntropySecret(value string) bool {
	if len(value) < 20 {
		return false
	}
	seen := make(map[rune]bool, len(value))
	for _, r := range value {
		seen[r] = true
	}
	return len(seen) < 8
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

// MaskLine returns line with every built-in secret match masked in place. The
// credential value is replaced by its mask() form while the surrounding code
// (variable names, comments) stays intact, so verbose context output around a
// hit reads naturally yet never exposes an adjacent secret. For keyword=value
// kinds (generic-secret) only the value is masked, not the keyword.
func MaskLine(line string) string {
	if line == "" {
		return ""
	}
	out := line
	for _, p := range BuiltinPatterns {
		out = p.Regex.ReplaceAllStringFunc(out, func(m string) string {
			v := SecretValue(p.Regex.FindStringSubmatch(m))
			if v == "" || v == m {
				return mask(m)
			}
			return strings.Replace(m, v, mask(v), 1)
		})
	}
	return out
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
