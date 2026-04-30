package easy

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Feature: easy-mode, Property 4: 용어 치환 정확성 — For any 알려진 git 용어가
// 포함된 문자열에 대해, TermMapper.Translate(s)의 결과는 다음을 만족해야 한다:
// - 매핑이 존재하는 용어는 번역된 표현으로 치환된다
// - 치환된 용어 뒤에 원본 영어 용어가 괄호 안에 병기된다 (예: "커밋 준비됨 (staged)")
// - 매핑이 존재하지 않는 용어는 원본 그대로 유지된다
//
// **Validates: Requirements 3.1, 3.3, 3.5**
func TestProperty_TermTranslation(t *testing.T) {
	// Collect known terms and their translations for "ko".
	koTerms := termsByLang["ko"]
	knownTerms := make([]string, 0, len(koTerms))
	for term := range koTerms {
		knownTerms = append(knownTerms, term)
	}

	// unknownWords are words that are NOT in the term map.
	unknownWords := []string{
		"hello", "world", "file", "directory", "error",
		"success", "running", "output", "config", "version",
	}

	// genTermString generates random strings that may contain known git terms
	// interspersed with unknown words.
	genTermString := rapid.Custom(func(rt *rapid.T) string {
		numParts := rapid.IntRange(1, 6).Draw(rt, "numParts")
		parts := make([]string, numParts)
		for i := 0; i < numParts; i++ {
			useKnown := rapid.Bool().Draw(rt, "useKnown")
			if useKnown && len(knownTerms) > 0 {
				idx := rapid.IntRange(0, len(knownTerms)-1).Draw(rt, "termIdx")
				parts[i] = knownTerms[idx]
			} else {
				idx := rapid.IntRange(0, len(unknownWords)-1).Draw(rt, "unknownIdx")
				parts[i] = unknownWords[idx]
			}
		}
		return strings.Join(parts, " ")
	})

	mapper := NewTermMapper("ko")

	t.Run("mapped_terms_translated_with_parenthetical", func(t *testing.T) {
		// Property: For each known term present in the input, the output
		// must contain "번역 (원본)" format.
		rapid.Check(t, func(rt *rapid.T) {
			input := genTermString.Draw(rt, "input")
			result := mapper.Translate(input)

			for term, translation := range koTerms {
				// Check if the term appears as a whole word in the input.
				if !mapper.patterns[term].MatchString(input) {
					continue
				}

				// The translated output must contain "translation (term)".
				expected := translation + " (" + term + ")"
				if !strings.Contains(result, expected) {
					// For case-insensitive terms, the parenthetical preserves
					// the original case from the input. Check with the actual
					// matches from the input.
					matches := mapper.patterns[term].FindAllString(input, -1)
					found := false
					for _, match := range matches {
						candidate := translation + " (" + match + ")"
						if strings.Contains(result, candidate) {
							found = true
							break
						}
					}
					if !found {
						rt.Fatalf(
							"term %q (translation %q) not found in translated output\n"+
								"  input:  %q\n"+
								"  output: %q",
							term, translation, input, result,
						)
					}
				}
			}
		})
	})

	t.Run("unmapped_terms_preserved", func(t *testing.T) {
		// Property: Words that are NOT in the term map must appear
		// unchanged in the output.
		rapid.Check(t, func(rt *rapid.T) {
			// Generate a string with only unknown words.
			numWords := rapid.IntRange(1, 5).Draw(rt, "numWords")
			words := make([]string, numWords)
			for i := 0; i < numWords; i++ {
				idx := rapid.IntRange(0, len(unknownWords)-1).Draw(rt, "wordIdx")
				words[i] = unknownWords[idx]
			}
			input := strings.Join(words, " ")
			result := mapper.Translate(input)

			if result != input {
				rt.Fatalf(
					"unmapped input should be preserved unchanged\n"+
						"  input:  %q\n"+
						"  output: %q",
					input, result,
				)
			}
		})
	})

	t.Run("translation_format_is_correct", func(t *testing.T) {
		// Property: Every known term in the input, after translation,
		// must follow the exact format "번역 (원본)" — translation,
		// space, open paren, original, close paren.
		rapid.Check(t, func(rt *rapid.T) {
			// Pick a single known term and wrap it in a sentence.
			idx := rapid.IntRange(0, len(knownTerms)-1).Draw(rt, "termIdx")
			term := knownTerms[idx]
			translation := koTerms[term]

			prefix := rapid.SampledFrom([]string{"", "the ", "my ", "your "}).Draw(rt, "prefix")
			suffix := rapid.SampledFrom([]string{"", " is ready", " files", " now"}).Draw(rt, "suffix")
			input := prefix + term + suffix

			result := mapper.Translate(input)

			// The result must contain the exact pattern: "translation (term)"
			expectedPattern := translation + " (" + term + ")"
			if !strings.Contains(result, expectedPattern) {
				rt.Fatalf(
					"expected format 'translation (original)' not found\n"+
						"  term:     %q\n"+
						"  expected: %q\n"+
						"  input:    %q\n"+
						"  output:   %q",
					term, expectedPattern, input, result,
				)
			}
		})
	})

	t.Run("nil_mapper_preserves_input", func(t *testing.T) {
		// Property: A nil TermMapper must return the input unchanged.
		rapid.Check(t, func(rt *rapid.T) {
			input := genTermString.Draw(rt, "input")
			var nilMapper *TermMapper
			result := nilMapper.Translate(input)
			if result != input {
				rt.Fatalf("nil mapper should preserve input\n  input: %q\n  output: %q",
					input, result)
			}
		})
	})

	t.Run("empty_lang_mapper_preserves_input", func(t *testing.T) {
		// Property: A TermMapper for an unknown language (no term mappings)
		// must return the input unchanged.
		rapid.Check(t, func(rt *rapid.T) {
			input := genTermString.Draw(rt, "input")
			emptyMapper := NewTermMapper("zz") // no registered terms
			result := emptyMapper.Translate(input)
			if result != input {
				rt.Fatalf("empty mapper should preserve input\n  input: %q\n  output: %q",
					input, result)
			}
		})
	})
}

// ---------------------------------------------------------------------------
// Unit tests for TermMapper — Validates: Requirements 3.2
// ---------------------------------------------------------------------------

// TestTermMapper_AllRequiredMappings verifies that all 15 required Korean
// term mappings exist in the "ko" language term map.
func TestTermMapper_AllRequiredMappings(t *testing.T) {
	requiredTerms := []string{
		"staged",
		"unstaged",
		"untracked",
		"conflict",
		"rebase",
		"merge",
		"commit",
		"push",
		"pull",
		"branch",
		"HEAD",
		"upstream",
		"stash",
		"cherry-pick",
		"fast-forward",
	}

	mapper := NewTermMapper("ko")

	if len(mapper.terms) < len(requiredTerms) {
		t.Errorf("expected at least %d term mappings, got %d", len(requiredTerms), len(mapper.terms))
	}

	for _, term := range requiredTerms {
		if _, ok := mapper.terms[term]; !ok {
			t.Errorf("required term mapping missing: %q", term)
		}
	}
}

// TestTermMapper_SpecificTranslations verifies that specific term translations
// produce the exact expected output with the original English term in parentheses.
func TestTermMapper_SpecificTranslations(t *testing.T) {
	mapper := NewTermMapper("ko")

	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "staged files are ready",
			expected: "커밋 준비됨 (staged) files are ready",
		},
		{
			input:    "unstaged changes detected",
			expected: "아직 준비 안 됨 (unstaged) changes detected",
		},
		{
			input:    "untracked files found",
			expected: "새로 만든 파일 (untracked) files found",
		},
		{
			input:    "conflict in main.go",
			expected: "충돌 (같은 부분을 다르게 수정함) (conflict) in main.go",
		},
		{
			input:    "rebase in progress",
			expected: "커밋 재정렬 (rebase) in progress",
		},
		{
			input:    "merge completed",
			expected: "브랜치 합치기 (merge) completed",
		},
		{
			input:    "commit your changes",
			expected: "변경사항 저장 (commit) your changes",
		},
		{
			input:    "push to remote",
			expected: "서버에 올리기 (push) to remote",
		},
		{
			input:    "pull from origin",
			expected: "서버에서 가져오기 (pull) from origin",
		},
		{
			input:    "branch main",
			expected: "작업 갈래 (branch) main",
		},
		{
			input:    "HEAD is detached",
			expected: "현재 위치 (HEAD) is detached",
		},
		{
			input:    "upstream is set",
			expected: "원격 기준점 (upstream) is set",
		},
		{
			input:    "stash your work",
			expected: "임시 보관함 (stash) your work",
		},
		{
			input:    "cherry-pick abc123",
			expected: "커밋 골라 가져오기 (cherry-pick) abc123",
		},
		{
			input:    "fast-forward merge",
			expected: "빨리 감기 (충돌 없이 앞으로 이동) (fast-forward) 브랜치 합치기 (merge)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := mapper.Translate(tt.input)
			if result != tt.expected {
				t.Errorf("Translate(%q)\n  got:  %q\n  want: %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestTranslateTerm_SingleTerm verifies that TranslateTerm correctly translates
// individual git terms with the "번역 (원본)" format.
func TestTranslateTerm_SingleTerm(t *testing.T) {
	mapper := NewTermMapper("ko")

	tests := []struct {
		term     string
		expected string
	}{
		{"staged", "커밋 준비됨 (staged)"},
		{"unstaged", "아직 준비 안 됨 (unstaged)"},
		{"untracked", "새로 만든 파일 (untracked)"},
		{"conflict", "충돌 (같은 부분을 다르게 수정함) (conflict)"},
		{"rebase", "커밋 재정렬 (rebase)"},
		{"merge", "브랜치 합치기 (merge)"},
		{"commit", "변경사항 저장 (commit)"},
		{"push", "서버에 올리기 (push)"},
		{"pull", "서버에서 가져오기 (pull)"},
		{"branch", "작업 갈래 (branch)"},
		{"HEAD", "현재 위치 (HEAD)"},
		{"upstream", "원격 기준점 (upstream)"},
		{"stash", "임시 보관함 (stash)"},
		{"cherry-pick", "커밋 골라 가져오기 (cherry-pick)"},
		{"fast-forward", "빨리 감기 (충돌 없이 앞으로 이동) (fast-forward)"},
	}

	for _, tt := range tests {
		t.Run(tt.term, func(t *testing.T) {
			result := mapper.TranslateTerm(tt.term)
			if result != tt.expected {
				t.Errorf("TranslateTerm(%q)\n  got:  %q\n  want: %q", tt.term, result, tt.expected)
			}
		})
	}
}

// TestTranslateTerm_UnknownTerm verifies that TranslateTerm returns unknown
// terms unchanged.
func TestTranslateTerm_UnknownTerm(t *testing.T) {
	mapper := NewTermMapper("ko")

	unknowns := []string{
		"clone",
		"fetch",
		"reset",
		"checkout",
		"tag",
		"remote",
		"diff",
		"log",
		"blame",
		"bisect",
	}

	for _, term := range unknowns {
		t.Run(term, func(t *testing.T) {
			result := mapper.TranslateTerm(term)
			if result != term {
				t.Errorf("TranslateTerm(%q) should return unchanged, got %q", term, result)
			}
		})
	}

	// Also verify nil mapper returns term unchanged.
	var nilMapper *TermMapper
	result := nilMapper.TranslateTerm("staged")
	if result != "staged" {
		t.Errorf("nil mapper TranslateTerm(\"staged\") = %q, want \"staged\"", result)
	}
}
