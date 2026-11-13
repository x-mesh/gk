package gitleaksrules

import (
	"math/rand/v2"
	"reflect"
	"regexp/syntax"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestAnchorLength(t *testing.T) {
	cases := []struct {
		name, regex string
		keywords    []string
		anchored    bool
	}{
		{"keyword after a lazy prefix", `(?i)[\w.-]{0,50}?(?:twitter)(?:[ \t\w.-]{0,20})[=:]([a-z0-9]{35})`, []string{"twitter"}, true},
		{"keyword split by prefix factoring", `\b((?:A3T[A-Z0-9]|AKIA|ASIA|ABIA|ACCA)[A-Z2-7]{16})\b`, []string{"a3t", "akia", "asia", "abia", "acca"}, true},
		{"optional part inside the keyword", `(?i)passw(?:or)?d=[a-z]{8}`, []string{"passwd", "password"}, true},
		{"one spelling has no keyword", `(?i)passw(?:or)?d=[a-z]{8}`, []string{"password"}, false},
		{"every branch has a keyword", `(?:foo|bar)[0-9]{4}`, []string{"foo", "bar"}, true},
		{"one branch has no keyword", `(?:foo|bar)[0-9]{4}`, []string{"foo"}, false},
		{"keyword letter is optional", `fo?o[0-9]{4}`, []string{"foo"}, false},
		{"keyword is not in the regex", `\b(pat[a-z]{14})\b`, []string{"airtable"}, false},
		{"dot matches any character", `hooks.slack.com/[a-z]{10}`, []string{"hooks.slack.com"}, false},
		{"escaped dot", `hooks\.slack\.com/[a-z]{10}`, []string{"hooks.slack.com"}, true},
		{"no length bound", `key=[a-z]+`, []string{"key"}, false},
		{"no keywords", `key=[a-z]{8}`, nil, false},
		{"keyword is not ASCII", `clé=[a-z]{8}`, []string{"clé"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := anchorLength(c.regex, c.keywords) > 0; got != c.anchored {
				t.Errorf("anchored = %v, want %v", got, c.anchored)
			}
		})
	}
}

func TestMaxMatchBytes(t *testing.T) {
	cases := map[string]int{
		`key`:           3,
		`(?i)key`:       5, // k also matches the three-byte Kelvin sign
		`[a-z]{4}`:      4,
		`(?i)[a-z]{4}`:  12,
		`a|bcd`:         3,
		`\bab?c{2,3}$`:  5,
		`[^"]{2}`:       8,
		`x*`:            -1,
		`(?:ab){2,}`:    -1,
		`(?:a|b+)c{10}`: -1,
	}
	for pattern, want := range cases {
		re, err := syntax.Parse(pattern, syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		if got := maxMatchBytes(re); got != want {
			t.Errorf("maxMatchBytes(%q) = %d, want %d", pattern, got, want)
		}
	}
}

func TestAnchorSafe(t *testing.T) {
	cases := map[string]bool{
		"plain ASCII KEY=value":  true,
		"한글 주석 key=value":        true,
		"Ünïcödé keeps its size": true,
		"long s: ſecret":         false,
		"Kelvin sign: Key":       false,
		"dotted I: İ":            false,
		"invalid \xff byte":      false,
		"":                       true,
	}
	for text, want := range cases {
		if got := anchorSafe(text); got != want {
			t.Errorf("anchorSafe(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestAnchorWindows(t *testing.T) {
	t.Run("windows are cut on rune boundaries", func(t *testing.T) {
		// Each length puts the window edges at another byte of a 3-byte rune.
		text := strings.Repeat("한", 60) + "key" + strings.Repeat("글", 60)
		pos := strings.Index(text, "key")
		for maxLen := 20; maxLen < 23; maxLen++ {
			windows, ok := anchorWindows(text, strings.ToLower(text), []string{"key"}, maxLen)
			if !ok || len(windows) != 1 {
				t.Fatalf("length %d: windows = %v, ok = %v, want one window", maxLen, windows, ok)
			}
			w := windows[0]
			if !utf8.RuneStart(text[w[0]]) || !utf8.RuneStart(text[w[1]]) {
				t.Errorf("length %d: window %v cuts a rune", maxLen, w)
			}
			if w[0] > pos+3-maxLen || w[1] < pos+maxLen {
				t.Errorf("length %d: window %v does not hold every match around offset %d", maxLen, w, pos)
			}
		}
	})
	t.Run("overlapping windows merge, in text order", func(t *testing.T) {
		text := strings.Repeat(".", 500) + "key key" + strings.Repeat(".", 500) + "tok" + strings.Repeat(".", 500)
		windows, ok := anchorWindows(text, text, []string{"tok", "key"}, 20)
		if !ok || len(windows) != 2 || windows[0][1] >= windows[1][0] {
			t.Fatalf("windows = %v, ok = %v, want two separate windows in order", windows, ok)
		}
	})
	t.Run("dense keywords give up on windows", func(t *testing.T) {
		text := strings.Repeat("key ", 500)
		if _, ok := anchorWindows(text, text, []string{"key"}, 20); ok {
			t.Error("windows that cover the whole text must not be used")
		}
	})
}

// A keyword search of the lower-case text cannot stand in for the regex in
// these texts, so the rule must search the whole text.
func TestEngine_AnchoredRuleFindsSecretWhereKeywordSearchFails(t *testing.T) {
	e := synth([]jsonRule{{ID: "tok", Regex: `(?i)secret_[a-z0-9]{8}`, Keywords: []string{"secret"}}}, allowlist{})
	if e.rules[0].anchorLen == 0 {
		t.Fatal("the rule must be anchored for this test to mean anything")
	}
	filler := strings.Repeat("filler text without the word\n", 400)
	cases := map[string]string{
		// The regexp folds U+017F to "s"; the lower-case text has no
		// "secret" there, only at the decoy far away.
		"folded rune in the keyword": "ſecret_abcd1234\n" + filler + "secret\n",
		// strings.ToLower turns each invalid byte into three, so offsets
		// in the lower-case text point past the secret.
		"invalid UTF-8 before the secret": strings.Repeat("\xff", 3000) + "\nsecret_abcd1234\n" + filler,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if fs := e.scanFile(fileOf("a.txt", content)); len(fs) != 1 {
				t.Fatalf("got %d findings, want the one secret", len(fs))
			}
		})
	}
}

func fileOf(path, content string) fileDiff {
	h := hunk{lines: strings.Split(strings.TrimSuffix(content, "\n"), "\n")}
	h.numbers = make([]int, len(h.lines))
	for i := range h.numbers {
		h.numbers[i] = i + 1
	}
	h.joined = strings.Join(h.lines, "\n")
	h.lower = strings.ToLower(h.joined)
	return fileDiff{path: path, hunks: []hunk{h}}
}

// sampler writes a random string that a regex can match.
type sampler struct {
	rng *rand.Rand
	b   strings.Builder
}

func (s *sampler) write(re *syntax.Regexp) {
	switch re.Op {
	case syntax.OpLiteral:
		for _, r := range re.Rune {
			if re.Flags&syntax.FoldCase != 0 && s.rng.IntN(2) == 0 {
				r = unicode.ToLower(r)
			}
			s.b.WriteRune(r)
		}
	case syntax.OpCharClass:
		s.b.WriteRune(s.classRune(re.Rune))
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		s.b.WriteByte(byte('a' + s.rng.IntN(26)))
	case syntax.OpCapture:
		s.write(re.Sub[0])
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest, syntax.OpRepeat:
		lo, hi := re.Min, re.Max
		switch re.Op {
		case syntax.OpStar:
			lo, hi = 0, 3
		case syntax.OpPlus:
			lo, hi = 1, 3
		case syntax.OpQuest:
			lo, hi = 0, 1
		}
		if hi < 0 {
			hi = lo + 3
		}
		// The ends of the range are where a length bound can be wrong.
		n := lo
		switch s.rng.IntN(3) {
		case 1:
			n = hi
		case 2:
			n = lo + s.rng.IntN(hi-lo+1)
		}
		for range n {
			s.write(re.Sub[0])
		}
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			s.write(sub)
		}
	case syntax.OpAlternate:
		s.write(re.Sub[s.rng.IntN(len(re.Sub))])
	}
}

// classRune picks a printable ASCII rune of the class when it has one, so
// the sample stays readable and anchorSafe.
func (s *sampler) classRune(ranges []rune) rune {
	var printable []rune
	for i := 0; i+1 < len(ranges); i += 2 {
		for r := max(ranges[i], ' '); r <= min(ranges[i+1], '~'); r++ {
			printable = append(printable, r)
		}
	}
	if len(printable) > 0 {
		return printable[s.rng.IntN(len(printable))]
	}
	return ranges[2*s.rng.IntN(len(ranges)/2)]
}

func (s *sampler) sample(re *syntax.Regexp) string {
	s.b.Reset()
	s.write(re)
	return s.b.String()
}

// TestAnchoredMatches_EqualWholeTextSearch checks, for every anchored rule of
// the embedded ruleset, that the keyword search returns the matches a
// whole-text search returns. The texts hold strings generated from the
// regex of the rule, because the repositories this engine was measured on
// have no finding for most of these rules. It also checks the two facts the
// windows rest on: a match contains a keyword, and it is not longer than the
// bound.
func TestAnchoredMatches_EqualWholeTextSearch(t *testing.T) {
	if testing.Short() {
		t.Skip("runs every anchored rule over generated text")
	}
	rng := rand.New(rand.NewPCG(1, 2))
	s := &sampler{rng: rng}
	allWords := []string{"lorem", "ipsum", "dolor", "sit", "amet", "return", "function", "const", "value", "한글", "주석", "0x1f", "42"}
	var words []string
	filler := func(n int, sep string) string {
		var b strings.Builder
		for b.Len() < n {
			b.WriteString(words[rng.IntN(len(words))])
			b.WriteString(sep)
		}
		return b.String()
	}

	anchored, matched, windowed := 0, 0, 0
	for _, r := range get().rules {
		if r.anchorLen == 0 {
			continue
		}
		anchored++
		re, err := syntax.Parse(r.regex.String(), syntax.Perl)
		if err != nil {
			t.Fatal(err)
		}
		// A filler word that holds a keyword would put a window everywhere.
		words = words[:0]
		for _, w := range allWords {
			if !allContain([]string{w}, r.keywords) {
				words = append(words, w)
			}
		}
		gap := 10 * r.anchorLen
		decoy := r.keywords[rng.IntN(len(r.keywords))]
		texts := []string{
			// A sample at each end of the text, where a window is clamped.
			s.sample(re) + "\n" + filler(gap, " ") + "\n" + decoy + "\n" + filler(gap, "\n") + s.sample(re),
			// Samples close together, and a keyword with no secret.
			filler(gap, "\n") + s.sample(re) + " " + s.sample(re) + "\n" + s.sample(re) + "\n" + filler(gap, " ") + strings.ToUpper(decoy) + filler(gap, " "),
			// One long line, with multi-byte text against the sample.
			filler(gap, ";") + "한글" + s.sample(re) + "한글;" + filler(gap, ";") + "\"" + s.sample(re) + "\";" + filler(gap, ";"),
		}
		ruleMatched, ruleWindowed := false, false
		for i, text := range texts {
			if !anchorSafe(text) {
				t.Fatalf("%s: text %d is not anchor-safe; the generator must keep it so", r.id, i)
			}
			lower := strings.ToLower(text)
			want := r.regex.FindAllStringSubmatchIndex(text, -1)
			got := anchoredMatches(r, pass{text: text, lower: lower})
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: text %d: keyword search found %v, whole-text search found %v", r.id, i, got, want)
			}
			if _, ok := anchorWindows(text, lower, r.keywords, r.anchorLen); ok {
				ruleWindowed = true
			}
			for _, loc := range want {
				ruleMatched = true
				m := lower[loc[0]:loc[1]]
				if loc[1]-loc[0] > r.anchorLen {
					t.Errorf("%s: a match of %d bytes is longer than the bound %d", r.id, loc[1]-loc[0], r.anchorLen)
				}
				if !allContain([]string{m}, r.keywords) {
					t.Errorf("%s: a match holds none of the keywords %v", r.id, r.keywords)
				}
			}
		}
		if ruleMatched {
			matched++
		} else {
			t.Errorf("%s: no generated text matched, so the rule was not checked", r.id)
		}
		if ruleWindowed {
			windowed++
		} else {
			t.Errorf("%s: every text fell back to the whole-text search", r.id)
		}
	}
	if anchored < 190 {
		t.Errorf("only %d rules are anchored; the proof lost rules it covered", anchored)
	}
	t.Logf("anchored %d, with a match %d, searched by window %d", anchored, matched, windowed)
}
