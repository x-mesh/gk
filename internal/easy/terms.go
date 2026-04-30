package easy

import (
	"regexp"
	"sort"
	"strings"
)

// termsByLang holds per-language git term mappings.
// Each map entry maps an English git term to its translated equivalent.
var termsByLang = map[string]map[string]string{
	"ko": {
		"staged":       "커밋 준비됨",
		"unstaged":     "아직 준비 안 됨",
		"untracked":    "새로 만든 파일",
		"conflict":     "충돌 (같은 부분을 다르게 수정함)",
		"rebase":       "커밋 재정렬",
		"merge":        "브랜치 합치기",
		"commit":       "변경사항 저장",
		"push":         "서버에 올리기",
		"pull":         "서버에서 가져오기",
		"branch":       "작업 갈래",
		"HEAD":         "현재 위치",
		"upstream":     "원격 기준점",
		"stash":        "임시 보관함",
		"cherry-pick":  "커밋 골라 가져오기",
		"fast-forward": "빨리 감기 (충돌 없이 앞으로 이동)",
	},
}

// TermMapper translates git terminology into beginner-friendly
// language for a given locale. It replaces known terms in strings
// while preserving the original English term in parentheses.
type TermMapper struct {
	terms map[string]string
	lang  string

	// sorted holds terms sorted by length (longest first) to ensure
	// longer multi-word terms like "cherry-pick" and "fast-forward"
	// are matched before shorter substrings.
	sorted []string

	// patterns maps each term to a compiled regex with word boundaries
	// for accurate whole-word matching.
	patterns map[string]*regexp.Regexp
}

// NewTermMapper creates a TermMapper for the given language.
// If the language has no registered term mappings, the mapper is
// created with an empty map — TranslateTerm and Translate will
// return inputs unchanged.
func NewTermMapper(lang string) *TermMapper {
	terms := termsByLang[lang]
	if terms == nil {
		terms = make(map[string]string)
	}

	// Sort terms by length descending so longer terms are matched first.
	// This prevents "commit" from matching inside "cherry-pick" scenarios
	// and ensures "fast-forward" is matched before "fast".
	sorted := make([]string, 0, len(terms))
	for term := range terms {
		sorted = append(sorted, term)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[i]) > len(sorted[j])
	})

	// Pre-compile regex patterns with word boundaries for each term.
	// For terms containing hyphens (cherry-pick, fast-forward), the
	// hyphen is escaped. Case-insensitive matching is used for all
	// terms except "HEAD" which is case-sensitive.
	patterns := make(map[string]*regexp.Regexp, len(terms))
	for _, term := range sorted {
		escaped := regexp.QuoteMeta(term)
		var pattern string
		if term == "HEAD" {
			// HEAD is case-sensitive — match exactly.
			pattern = `\b` + escaped + `\b`
		} else {
			// Case-insensitive matching for all other terms.
			pattern = `(?i)\b` + escaped + `\b`
		}
		patterns[term] = regexp.MustCompile(pattern)
	}

	return &TermMapper{
		terms:    terms,
		lang:     lang,
		sorted:   sorted,
		patterns: patterns,
	}
}

// TranslateTerm translates a single git term. If a mapping exists,
// it returns "번역 (term)". If no mapping exists, the original
// term is returned unchanged.
func (m *TermMapper) TranslateTerm(term string) string {
	if m == nil {
		return term
	}

	// Try exact match first.
	if translated, ok := m.terms[term]; ok {
		return translated + " (" + term + ")"
	}

	// Try case-insensitive match for non-HEAD terms.
	lower := strings.ToLower(term)
	for orig, translated := range m.terms {
		if orig == "HEAD" {
			continue // HEAD is case-sensitive
		}
		if strings.ToLower(orig) == lower {
			return translated + " (" + term + ")"
		}
	}

	return term
}

// Translate replaces all known git terms in the string s with their
// beginner-friendly translations, appending the original English term
// in parentheses. Terms are matched using word boundaries to avoid
// partial replacements (e.g., "committed" does not match "commit").
//
// Longer terms are replaced first to prevent shorter terms from
// interfering with multi-word terms like "cherry-pick".
func (m *TermMapper) Translate(s string) string {
	if m == nil || len(m.terms) == 0 {
		return s
	}

	result := s
	for _, term := range m.sorted {
		pattern := m.patterns[term]
		translated := m.terms[term]

		result = pattern.ReplaceAllStringFunc(result, func(match string) string {
			return translated + " (" + match + ")"
		})
	}

	return result
}
