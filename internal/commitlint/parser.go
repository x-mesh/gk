package commitlint

import (
	"regexp"
	"strings"
)

// Message is a parsed commit message in Conventional Commits form.
type Message struct {
	Type        string   // "feat", "fix", ...
	Scope       string   // inside parens, may be ""
	Breaking    bool     // "!" marker or BREAKING CHANGE footer
	Subject     string   // the one-liner after the colon
	Body        string   // raw body (between subject+blank and footers), may be ""
	Footers     []Footer // trailers at the end
	Raw         string   // original input
	HeaderValid bool     // whether the first line parsed cleanly
}

// Footer is one trailer line, key: value.
type Footer struct {
	Token string // "BREAKING CHANGE" / "Signed-off-by" / "Refs"
	Value string
}

// Issue is a single lint failure.
type Issue struct {
	Code    string // stable identifier like "type-empty", "type-enum", "subject-max-length"
	Message string
}

// Rules is the set of lint rules to apply.
type Rules struct {
	AllowedTypes     []string
	ScopeRequired    bool
	MaxSubjectLength int // 0 → no limit
}

// headerRE matches: type(scope)!: subject  — scope and ! are optional.
var headerRE = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9_-]*)(?:\(([^\n)]+)\))?(!)?:[ \t]+(.+)$`)

// footerTokenRE matches footer token lines: "TOKEN: value" or "TOKEN #value".
// Token must be word chars or spaces/hyphens; BREAKING CHANGE is explicitly allowed.
var footerTokenRE = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9 -]*)(?::|[ \t]#)[ \t]*(.*)$`)

// Parse parses a raw commit message. Even on header failure it returns a
// Message with HeaderValid=false so callers can still report the raw input.
func Parse(raw string) Message {
	m := Message{Raw: raw}

	// Split on first blank line to get header vs the rest.
	header, rest := splitOnBlankLine(raw)
	header = strings.TrimRight(header, "\r\n\t ")

	// Try to parse header.
	groups := headerRE.FindStringSubmatch(header)
	if groups == nil {
		m.HeaderValid = false
		return m
	}
	m.HeaderValid = true
	m.Type = groups[1]
	m.Scope = groups[2]
	m.Breaking = groups[3] == "!"
	m.Subject = groups[4]

	if rest == "" {
		return m
	}

	// Split body+footers area: footers are a contiguous block at the end
	// where every non-blank line matches the footer token pattern.
	bodyLines, footers := splitBodyAndFooters(rest)
	m.Body = strings.TrimRight(strings.Join(bodyLines, "\n"), "\n\r\t ")
	m.Footers = footers

	// BREAKING CHANGE footer also sets Breaking=true.
	for _, f := range footers {
		if strings.EqualFold(f.Token, "breaking change") || strings.EqualFold(f.Token, "breaking-change") {
			m.Breaking = true
		}
	}

	return m
}

// splitOnBlankLine splits s into the part before the first blank line and the
// rest (after the blank line). If there is no blank line, rest is "".
func splitOnBlankLine(s string) (header, rest string) {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if strings.TrimRight(line, "\r\t ") == "" && i > 0 {
			return strings.Join(lines[:i], "\n"),
				strings.Join(lines[i+1:], "\n")
		}
	}
	return s, ""
}

// splitBodyAndFooters separates the combined body+footer block.
// It finds the last contiguous block of footer-token lines and
// treats everything before that block as body.
func splitBodyAndFooters(s string) (bodyLines []string, footers []Footer) {
	lines := strings.Split(strings.TrimLeft(s, "\n"), "\n")

	// Walk from the end, collecting footer lines.
	// A "footer block" is a set of consecutive non-blank lines at the end
	// that all match footerTokenRE (blank lines between footer tokens are
	// allowed per spec, but we keep it simple for v0.3).
	footerStart := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(lines[i], "\r\t ")
		if line == "" {
			// blank line breaks the contiguous footer block
			break
		}
		if footerTokenRE.MatchString(line) {
			footerStart = i
		} else {
			break
		}
	}

	bodyLines = lines[:footerStart]

	for _, line := range lines[footerStart:] {
		line = strings.TrimRight(line, "\r\t ")
		if line == "" {
			continue
		}
		g := footerTokenRE.FindStringSubmatch(line)
		if g != nil {
			footers = append(footers, Footer{
				Token: strings.TrimRight(g[1], " \t"),
				Value: g[2],
			})
		}
	}
	return bodyLines, footers
}

// Lint runs rules against a parsed message. Returns []Issue (empty = clean).
func Lint(m Message, r Rules) []Issue {
	var issues []Issue

	if !m.HeaderValid {
		issues = append(issues, Issue{
			Code:    "header-invalid",
			Message: "commit message header does not match Conventional Commits format",
		})
		return issues // further rules are meaningless without a parsed header
	}

	if m.Type == "" {
		issues = append(issues, Issue{
			Code:    "type-empty",
			Message: "type may not be empty",
		})
	} else if len(r.AllowedTypes) > 0 && !containsIgnoreCase(r.AllowedTypes, m.Type) {
		issues = append(issues, Issue{
			Code:    "type-enum",
			Message: "type \"" + m.Type + "\" is not allowed (allowed: " + strings.Join(r.AllowedTypes, ", ") + ")",
		})
	}

	if r.ScopeRequired && m.Scope == "" {
		issues = append(issues, Issue{
			Code:    "scope-required",
			Message: "scope is required but was not provided",
		})
	}

	if m.Subject == "" {
		issues = append(issues, Issue{
			Code:    "subject-empty",
			Message: "subject may not be empty",
		})
	} else if r.MaxSubjectLength > 0 && len(m.Subject) > r.MaxSubjectLength {
		issues = append(issues, Issue{
			Code:    "subject-max-length",
			Message: "subject length " + itoa(len(m.Subject)) + " exceeds maximum " + itoa(r.MaxSubjectLength),
		})
	}

	return issues
}

func containsIgnoreCase(list []string, s string) bool {
	sl := strings.ToLower(s)
	for _, v := range list {
		if strings.ToLower(v) == sl {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append([]byte{digits[n%10]}, buf...)
		n /= 10
	}
	return string(buf)
}
