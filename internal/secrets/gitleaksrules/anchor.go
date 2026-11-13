package gitleaksrules

import (
	"cmp"
	"regexp/syntax"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A rule is anchored when every match contains one of its keywords and has a
// bounded length. Every match then lies within that length of a keyword, so
// the raw text is searched only around its keyword occurrences. About 200 of
// the rules qualify, and the ones shaped `[\w.-]{0,50}?(?:keyword)...` took
// over 80% of the regex time on bundle-heavy history: the regexp package
// runs them about ten times slower per byte than a rule with a literal
// prefix, while their keywords cover a fraction of a percent of such text.
//
// gitleaks uses keywords only to skip a fragment, so the ruleset does not
// promise that a match contains one. anchorLength proves it per rule from
// the regex; a rule that fails the proof searches the whole text as before.

// anchorSlack is the context kept around a window so that the rune before and
// after any match is inside the window, as `\b`, `^`, and `$` need.
const anchorSlack = utf8.UTFMax

// anchorLength returns the longest match of the rule in bytes when the rule
// can be anchored, and 0 when it cannot.
func anchorLength(pattern string, keywords []string) int {
	if pattern == "" || len(keywords) == 0 {
		return 0
	}
	for _, kw := range keywords {
		if kw == "" || !isASCII(kw) {
			return 0
		}
	}
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return 0
	}
	n := maxMatchBytes(re)
	if n <= 0 || !requiresKeyword(re, keywords, exactDepth) {
		return 0
	}
	return n
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// Bounds of the keyword proof. A regex beyond them is not anchored, which
// costs speed and never a finding.
const (
	exactMax   = 16 // strings in one exact set
	exactBytes = 64 // bytes in one exact string
	exactDepth = 3  // alternations expanded inside one concatenation
)

// requiresKeyword reports whether every match of re contains one of the
// keywords, compared in lower case.
func requiresKeyword(re *syntax.Regexp, keywords []string, depth int) bool {
	if set, ok := exactSet(re); ok {
		return allContain(set, keywords)
	}
	switch re.Op {
	case syntax.OpCapture, syntax.OpPlus:
		return requiresKeyword(re.Sub[0], keywords, depth)
	case syntax.OpRepeat:
		return re.Min >= 1 && requiresKeyword(re.Sub[0], keywords, depth)
	case syntax.OpAlternate:
		for _, sub := range re.Sub {
			if !requiresKeyword(sub, keywords, depth) {
				return false
			}
		}
		return len(re.Sub) > 0
	case syntax.OpConcat:
		return concatRequires(flattenConcat(re.Sub), keywords, depth)
	}
	return false
}

// concatRequires reports whether every match of the concatenated parts
// contains a keyword. It holds when one part requires a keyword, or when the
// strings that a run of adjacent parts can match all contain one. The parser
// factors common prefixes out of an alternation (`AKIA|ASIA` becomes
// `A(?:KIA|SIA)`), which splits a keyword between two parts, so an
// alternation is also expanded into one concatenation per branch.
func concatRequires(parts []*syntax.Regexp, keywords []string, depth int) bool {
	run := []string{""}
	for _, part := range parts {
		set, exact := exactSet(part)
		if exact {
			if next, ok := product(run, set); ok {
				run = next
				continue
			}
		}
		if allContain(run, keywords) {
			return true
		}
		if !exact && requiresKeyword(part, keywords, depth) {
			return true
		}
		run = []string{""}
		if exact {
			run = set
		}
	}
	if allContain(run, keywords) {
		return true
	}
	if depth == 0 {
		return false
	}
	for i, part := range parts {
		if part.Op != syntax.OpAlternate || len(part.Sub) > exactMax {
			continue
		}
		if _, exact := exactSet(part); exact {
			continue
		}
		every := true
		for _, branch := range part.Sub {
			expanded := slices.Concat(parts[:i], flattenConcat([]*syntax.Regexp{branch}), parts[i+1:])
			if !concatRequires(expanded, keywords, depth-1) {
				every = false
				break
			}
		}
		if every {
			return true
		}
	}
	return false
}

// flattenConcat returns parts with nested concatenations and capture groups
// replaced by their own parts; neither changes what the sequence matches.
func flattenConcat(parts []*syntax.Regexp) []*syntax.Regexp {
	out := make([]*syntax.Regexp, 0, len(parts))
	for _, part := range parts {
		for part.Op == syntax.OpCapture {
			part = part.Sub[0]
		}
		if part.Op == syntax.OpConcat {
			out = append(out, flattenConcat(part.Sub)...)
			continue
		}
		out = append(out, part)
	}
	return out
}

// exactSet returns every string re can match, in lower case, when that set
// is small.
func exactSet(re *syntax.Regexp) ([]string, bool) {
	switch re.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText,
		syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return []string{""}, true
	case syntax.OpLiteral:
		s := strings.ToLower(string(re.Rune))
		return []string{s}, len(s) <= exactBytes
	case syntax.OpCharClass:
		var out []string
		for i := 0; i+1 < len(re.Rune); i += 2 {
			for r := re.Rune[i]; r <= re.Rune[i+1]; r++ {
				if len(out) == exactMax {
					return nil, false
				}
				if s := strings.ToLower(string(r)); !slices.Contains(out, s) {
					out = append(out, s)
				}
			}
		}
		return out, len(out) > 0
	case syntax.OpCapture:
		return exactSet(re.Sub[0])
	case syntax.OpQuest:
		set, ok := exactSet(re.Sub[0])
		if !ok {
			return nil, false
		}
		return union(set, []string{""})
	case syntax.OpRepeat:
		if re.Min != re.Max {
			return nil, false
		}
		set, ok := exactSet(re.Sub[0])
		out := []string{""}
		for i := 0; ok && i < re.Min; i++ {
			out, ok = product(out, set)
		}
		return out, ok
	case syntax.OpConcat:
		out := []string{""}
		for _, sub := range re.Sub {
			set, ok := exactSet(sub)
			if !ok {
				return nil, false
			}
			if out, ok = product(out, set); !ok {
				return nil, false
			}
		}
		return out, true
	case syntax.OpAlternate:
		var out []string
		for _, sub := range re.Sub {
			set, ok := exactSet(sub)
			if !ok {
				return nil, false
			}
			if out, ok = union(out, set); !ok {
				return nil, false
			}
		}
		return out, len(out) > 0
	}
	return nil, false
}

func product(a, b []string) ([]string, bool) {
	if len(a)*len(b) > exactMax {
		return nil, false
	}
	out := make([]string, 0, len(a)*len(b))
	for _, x := range a {
		for _, y := range b {
			if len(x)+len(y) > exactBytes {
				return nil, false
			}
			out = append(out, x+y)
		}
	}
	return out, true
}

func union(a, b []string) ([]string, bool) {
	out := slices.Clone(a)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out, len(out) <= exactMax
}

// allContain reports whether every string of the set contains a keyword.
func allContain(set, keywords []string) bool {
	for _, s := range set {
		if !slices.ContainsFunc(keywords, func(kw string) bool { return strings.Contains(s, kw) }) {
			return false
		}
	}
	return len(set) > 0
}

// anchorSafe reports whether a keyword offset found in strings.ToLower(text)
// is the offset of that keyword in text, and whether a search for the
// lower-case keyword finds every place a keyword literal of a regex can
// match. strings.ToLower changes the byte length of invalid UTF-8 and of a
// few letters, and the regexp package folds U+017F and U+212A to "s" and
// "k", which no lower-case search for an ASCII keyword finds.
func anchorSafe(text string) bool {
	for i := 0; i < len(text); {
		if text[i] < utf8.RuneSelf {
			i++
			continue
		}
		r, width := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && width == 1 {
			return false
		}
		if r == 'ſ' || r == 'K' {
			return false
		}
		if lower := unicode.ToLower(r); lower != r && utf8.RuneLen(lower) != width {
			return false
		}
		i += width
	}
	return true
}

// anchorWindows returns the merged ranges of text that hold every match of a
// rule whose matches contain one of keywords and are at most maxLen bytes
// long. lower is strings.ToLower(text), and anchorSafe(text) must hold. ok is
// false when the ranges cover so much of the text that one whole-text search
// is cheaper.
func anchorWindows(text, lower string, keywords []string, maxLen int) (windows [][2]int, ok bool) {
	var ranges [][2]int
	for _, kw := range keywords {
		for from := 0; ; {
			i := strings.Index(lower[from:], kw)
			if i < 0 {
				break
			}
			pos := from + i
			// A match that contains this occurrence starts at or after
			// pos+len(kw)-maxLen and ends at or before pos+maxLen.
			start := max(0, pos+len(kw)-maxLen-anchorSlack)
			end := min(len(text), pos+maxLen+anchorSlack)
			// A cut inside a rune would make the regexp read other runes
			// than it reads in the whole text.
			for start > 0 && !utf8.RuneStart(text[start]) {
				start--
			}
			for end < len(text) && !utf8.RuneStart(text[end]) {
				end++
			}
			ranges = append(ranges, [2]int{start, end})
			from = pos + 1
		}
	}
	slices.SortFunc(ranges, func(a, b [2]int) int { return cmp.Compare(a[0], b[0]) })
	covered := 0
	for _, r := range ranges {
		if n := len(windows); n > 0 && r[0] <= windows[n-1][1] {
			if r[1] > windows[n-1][1] {
				covered += r[1] - windows[n-1][1]
				windows[n-1][1] = r[1]
			}
			continue
		}
		covered += r[1] - r[0]
		windows = append(windows, r)
	}
	return windows, 2*covered < len(text)
}

// anchoredMatches returns the matches of an anchored rule in the raw pass p,
// in text order, as a search of the whole text returns them. No match crosses
// a window edge: a match contains a keyword occurrence, so it lies inside
// the range built for that occurrence, anchorSlack bytes clear of the edge.
func anchoredMatches(r rule, p pass) [][]int {
	windows, ok := anchorWindows(p.text, p.lower, r.keywords, r.anchorLen)
	if !ok {
		return r.regex.FindAllStringSubmatchIndex(p.text, -1)
	}
	var out [][]int
	for _, w := range windows {
		for _, loc := range r.regex.FindAllStringSubmatchIndex(p.text[w[0]:w[1]], -1) {
			for i := range loc {
				if loc[i] >= 0 {
					loc[i] += w[0]
				}
			}
			out = append(out, loc)
		}
	}
	return out
}
