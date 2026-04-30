package ui

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/easy"
	"pgregory.net/rapid"
)

// ansiRE matches any ANSI escape sequence.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestNewEasyFormatter(t *testing.T) {
	emoji := easy.NewEmojiMapper(true)
	f := NewEasyFormatter(emoji, false)
	if f == nil {
		t.Fatal("NewEasyFormatter returned nil")
	}
	if f.noColor {
		t.Error("expected noColor=false")
	}
}

func TestSectionHeader_WithColor(t *testing.T) {
	emoji := easy.NewEmojiMapper(true)
	f := NewEasyFormatter(emoji, false)

	got := f.SectionHeader("success", "커밋 준비된 변경사항")

	// Should contain the success emoji
	if !strings.Contains(got, "✅") {
		t.Errorf("expected success emoji, got %q", got)
	}
	// Should contain the text
	if !strings.Contains(got, "커밋 준비된 변경사항") {
		t.Errorf("expected header text, got %q", got)
	}
	// Should contain ANSI bold codes
	if !strings.Contains(got, ansiBold) {
		t.Errorf("expected ANSI bold code, got %q", got)
	}
	if !strings.Contains(got, ansiReset) {
		t.Errorf("expected ANSI reset code, got %q", got)
	}
}

func TestSectionHeader_NoColor(t *testing.T) {
	emoji := easy.NewEmojiMapper(true)
	f := NewEasyFormatter(emoji, true)

	got := f.SectionHeader("success", "커밋 준비된 변경사항")

	// Should contain the emoji (emoji preserved in no-color mode)
	if !strings.Contains(got, "✅") {
		t.Errorf("expected success emoji, got %q", got)
	}
	// Should contain the text
	if !strings.Contains(got, "커밋 준비된 변경사항") {
		t.Errorf("expected header text, got %q", got)
	}
	// Should NOT contain ANSI codes
	if ansiRE.MatchString(got) {
		t.Errorf("expected no ANSI codes in no-color mode, got %q", got)
	}
}

func TestSectionHeader_EmojiDisabled(t *testing.T) {
	emoji := easy.NewEmojiMapper(false)
	f := NewEasyFormatter(emoji, false)

	got := f.SectionHeader("success", "Header Text")

	// Should NOT contain emoji
	if strings.Contains(got, "✅") {
		t.Errorf("expected no emoji when disabled, got %q", got)
	}
	// Should still contain bold text
	if !strings.Contains(got, ansiBold) {
		t.Errorf("expected ANSI bold code, got %q", got)
	}
	if !strings.Contains(got, "Header Text") {
		t.Errorf("expected header text, got %q", got)
	}
}

func TestSectionHeader_NilEmoji(t *testing.T) {
	f := NewEasyFormatter(nil, false)
	got := f.SectionHeader("success", "Test")
	if !strings.Contains(got, "Test") {
		t.Errorf("expected text with nil emoji mapper, got %q", got)
	}
}

func TestCommandBox_WithDescription(t *testing.T) {
	emoji := easy.NewEmojiMapper(true)
	f := NewEasyFormatter(emoji, false)

	got := f.CommandBox("gk commit", "변경사항을 저장합니다")

	if !strings.Contains(got, "→ gk commit") {
		t.Errorf("expected command with arrow, got %q", got)
	}
	if !strings.Contains(got, "    변경사항을 저장합니다") {
		t.Errorf("expected indented description, got %q", got)
	}
}

func TestCommandBox_WithoutDescription(t *testing.T) {
	emoji := easy.NewEmojiMapper(true)
	f := NewEasyFormatter(emoji, false)

	got := f.CommandBox("gk status", "")

	if !strings.Contains(got, "→ gk status") {
		t.Errorf("expected command with arrow, got %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("expected single line when no description, got %q", got)
	}
}

func TestFormatError_WithHint(t *testing.T) {
	emoji := easy.NewEmojiMapper(true)
	f := NewEasyFormatter(emoji, false)

	err := errors.New("서버에 올리기 실패")
	got := f.FormatError(err, "먼저 서버에서 가져오기를 실행하세요 → gk pull")

	// Should contain error emoji and message
	if !strings.Contains(got, "❌") {
		t.Errorf("expected error emoji, got %q", got)
	}
	if !strings.Contains(got, "서버에 올리기 실패") {
		t.Errorf("expected error message, got %q", got)
	}
	// Should contain hint emoji and hint text
	if !strings.Contains(got, "💡") {
		t.Errorf("expected hint emoji, got %q", got)
	}
	if !strings.Contains(got, "gk pull") {
		t.Errorf("expected hint text, got %q", got)
	}
}

func TestFormatError_WithoutHint(t *testing.T) {
	emoji := easy.NewEmojiMapper(true)
	f := NewEasyFormatter(emoji, false)

	err := errors.New("something failed")
	got := f.FormatError(err, "")

	if !strings.Contains(got, "❌") {
		t.Errorf("expected error emoji, got %q", got)
	}
	if !strings.Contains(got, "something failed") {
		t.Errorf("expected error message, got %q", got)
	}
	// Should NOT have a second line
	if strings.Contains(got, "\n") {
		t.Errorf("expected single line when no hint, got %q", got)
	}
}

func TestFormatError_NoColor(t *testing.T) {
	emoji := easy.NewEmojiMapper(true)
	f := NewEasyFormatter(emoji, true)

	err := errors.New("error occurred")
	got := f.FormatError(err, "try this fix")

	// Emoji should be preserved
	if !strings.Contains(got, "❌") {
		t.Errorf("expected error emoji in no-color mode, got %q", got)
	}
	// No ANSI codes
	if ansiRE.MatchString(got) {
		t.Errorf("expected no ANSI codes in no-color mode, got %q", got)
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "bold text",
			input: "\033[1mhello\033[0m",
			want:  "hello",
		},
		{
			name:  "no ansi",
			input: "plain text",
			want:  "plain text",
		},
		{
			name:  "multiple codes",
			input: "\033[1m\033[31mred bold\033[0m",
			want:  "red bold",
		},
		{
			name:  "emoji preserved",
			input: "✅ \033[1mtext\033[0m",
			want:  "✅ text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripANSI(tt.input)
			if got != tt.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsJSONMode(t *testing.T) {
	if !IsJSONMode("json") {
		t.Error("expected true for 'json'")
	}
	if !IsJSONMode("JSON") {
		t.Error("expected true for 'JSON'")
	}
	if IsJSONMode("text") {
		t.Error("expected false for 'text'")
	}
	if IsJSONMode("") {
		t.Error("expected false for empty string")
	}
}

func TestFormatJSON(t *testing.T) {
	data := `{"status":"ok"}`
	got := FormatJSON(data)
	if got != data {
		t.Errorf("FormatJSON should pass through data unchanged, got %q", got)
	}
}

func TestBold(t *testing.T) {
	got := Bold("hello", false)
	if got != "\033[1mhello\033[0m" {
		t.Errorf("Bold with color = %q", got)
	}

	got = Bold("hello", true)
	if got != "hello" {
		t.Errorf("Bold noColor = %q", got)
	}
}

// Feature: easy-mode, Property 9: JSON 모드 Easy Mode 비활성화 — For any 포매팅 입력에 대해,
// jsonMode=true일 때 Easy Mode 포매팅이 적용되지 않아야 한다.
// FormatJSON(data)는 항상 입력 데이터를 변경 없이 그대로 반환해야 하며,
// IsJSONMode("json")은 true, IsJSONMode("text")는 false를 반환해야 한다.
//
// **Validates: Requirements 8.4**
func TestProperty_JSONModeBypass(t *testing.T) {
	// genJSONLikeString generates random strings that resemble JSON data,
	// including valid JSON objects, arrays, and arbitrary strings.
	genJSONLikeString := rapid.Custom(func(rt *rapid.T) string {
		kind := rapid.IntRange(0, 4).Draw(rt, "kind")
		switch kind {
		case 0:
			// Simple JSON object
			key := rapid.StringMatching(`[a-zA-Z_]{1,20}`).Draw(rt, "key")
			val := rapid.StringMatching(`[a-zA-Z0-9가-힣 _\-]{0,30}`).Draw(rt, "val")
			return fmt.Sprintf(`{"%s":"%s"}`, key, val)
		case 1:
			// JSON array
			n := rapid.IntRange(0, 5).Draw(rt, "arrLen")
			elems := make([]string, n)
			for i := range elems {
				elems[i] = fmt.Sprintf(`"%s"`, rapid.StringMatching(`[a-z0-9]{1,10}`).Draw(rt, fmt.Sprintf("elem%d", i)))
			}
			return fmt.Sprintf("[%s]", strings.Join(elems, ","))
		case 2:
			// Nested JSON
			inner := rapid.StringMatching(`[a-zA-Z0-9]{1,15}`).Draw(rt, "inner")
			return fmt.Sprintf(`{"data":{"nested":"%s"}}`, inner)
		case 3:
			// Arbitrary string (could contain ANSI, emoji, etc.)
			return rapid.StringMatching(`[a-zA-Z0-9가-힣✅❌💡⚠️🚀📦 \n\t\-:{}"\[\],]{0,100}`).Draw(rt, "arbitrary")
		default:
			// Empty string
			return ""
		}
	})

	// genFormatString generates format mode strings including "json", "JSON",
	// "text", "table", empty, and random strings.
	genFormatString := rapid.Custom(func(rt *rapid.T) string {
		kind := rapid.IntRange(0, 5).Draw(rt, "fmtKind")
		switch kind {
		case 0:
			return "json"
		case 1:
			return "JSON"
		case 2:
			return "text"
		case 3:
			return "table"
		case 4:
			return ""
		default:
			return rapid.StringMatching(`[a-zA-Z]{1,10}`).Draw(rt, "randomFmt")
		}
	})

	t.Run("FormatJSON_passthrough", func(t *testing.T) {
		// Property: FormatJSON(data) always returns data unchanged for any input.
		rapid.Check(t, func(rt *rapid.T) {
			data := genJSONLikeString.Draw(rt, "data")

			result := FormatJSON(data)

			if result != data {
				rt.Fatalf("FormatJSON(%q) = %q, want exact passthrough", data, result)
			}
		})
	})

	t.Run("IsJSONMode_correctness", func(t *testing.T) {
		// Property: IsJSONMode returns true only for case-insensitive "json",
		// and false for all other format strings.
		rapid.Check(t, func(rt *rapid.T) {
			format := genFormatString.Draw(rt, "format")

			got := IsJSONMode(format)
			want := strings.EqualFold(format, "json")

			if got != want {
				rt.Fatalf("IsJSONMode(%q) = %v, want %v", format, got, want)
			}
		})
	})

	t.Run("JSON_mode_bypasses_easy_formatting", func(t *testing.T) {
		// Property: When JSON mode is detected, the caller should use FormatJSON
		// instead of Easy Mode formatting. FormatJSON never modifies the input,
		// so the data is always preserved exactly.
		rapid.Check(t, func(rt *rapid.T) {
			data := genJSONLikeString.Draw(rt, "data")
			format := genFormatString.Draw(rt, "format")

			if IsJSONMode(format) {
				// In JSON mode, FormatJSON must return data unchanged.
				result := FormatJSON(data)
				if result != data {
					rt.Fatalf("JSON mode: FormatJSON(%q) = %q, expected exact passthrough", data, result)
				}

				// Verify that Easy Mode formatting methods would produce
				// different output (proving bypass is necessary).
				emoji := easy.NewEmojiMapper(true)
				f := NewEasyFormatter(emoji, false)
				header := f.SectionHeader("success", data)
				// SectionHeader adds emoji prefix and ANSI bold codes,
				// so it should differ from raw data (unless data is empty).
				if len(data) > 0 && header == data {
					rt.Fatalf("SectionHeader should modify non-empty data, but got identical output for %q", data)
				}
			}
		})
	})
}

// Feature: easy-mode, Property 6: NoColor 불변 속성 — For any Easy Mode 포매팅 결과에 대해,
// noColor == true일 때 출력에는 ANSI 이스케이프 시퀀스(\x1b[)가 포함되지 않아야 하며,
// 이모지와 텍스트 구조는 유지되어야 한다.
//
// **Validates: Requirements 4.4, 8.5**
func TestProperty_NoColorInvariant(t *testing.T) {
	// Known emoji keys from the default emoji map.
	knownEmojiKeys := []string{
		"success", "warning", "error", "conflict", "new",
		"modified", "deleted", "staged", "push", "pull",
		"branch", "merge", "hint",
	}

	// genEmojiKey generates either a known emoji key or a random string.
	genEmojiKey := rapid.Custom(func(rt *rapid.T) string {
		useKnown := rapid.Bool().Draw(rt, "useKnown")
		if useKnown {
			return rapid.SampledFrom(knownEmojiKeys).Draw(rt, "knownKey")
		}
		return rapid.StringMatching(`[a-z_]{1,20}`).Draw(rt, "randomKey")
	})

	// genText generates a random non-empty text string that may contain
	// Korean characters, ASCII, and special characters.
	genText := rapid.Custom(func(rt *rapid.T) string {
		base := rapid.StringMatching(`[a-zA-Z가-힣0-9 _\-]{1,50}`).Draw(rt, "text")
		return base
	})

	// genCommand generates a random command string.
	genCommand := rapid.Custom(func(rt *rapid.T) string {
		cmd := rapid.SampledFrom([]string{
			"gk status", "gk commit", "gk push", "gk pull",
			"gk sync", "gk undo", "gk branch", "gk doctor",
			"gk guide", "git add .", "git log --oneline",
		}).Draw(rt, "cmd")
		return cmd
	})

	// genDescription generates a random description string (may be empty).
	genDescription := rapid.Custom(func(rt *rapid.T) string {
		isEmpty := rapid.Bool().Draw(rt, "emptyDesc")
		if isEmpty {
			return ""
		}
		return rapid.StringMatching(`[a-zA-Z가-힣0-9 ]{1,80}`).Draw(rt, "desc")
	})

	// genErrorMsg generates a random error message.
	genErrorMsg := rapid.Custom(func(rt *rapid.T) string {
		return rapid.StringMatching(`[a-zA-Z가-힣0-9 \-:]{1,60}`).Draw(rt, "errMsg")
	})

	// genHint generates a random hint string (may be empty).
	genHint := rapid.Custom(func(rt *rapid.T) string {
		isEmpty := rapid.Bool().Draw(rt, "emptyHint")
		if isEmpty {
			return ""
		}
		return rapid.StringMatching(`[a-zA-Z가-힣0-9 →\-]{1,80}`).Draw(rt, "hint")
	})

	// ansiEscapeRE detects any ANSI escape sequence (CSI sequences).
	ansiEscapeRE := regexp.MustCompile(`\x1b\[`)

	t.Run("SectionHeader_noColor_no_ANSI", func(t *testing.T) {
		// Property: For any emoji key and text, SectionHeader with noColor=true
		// must not contain ANSI escape sequences.
		rapid.Check(t, func(rt *rapid.T) {
			emojiKey := genEmojiKey.Draw(rt, "emojiKey")
			text := genText.Draw(rt, "text")

			f := NewEasyFormatter(easy.NewEmojiMapper(true), true)
			result := f.SectionHeader(emojiKey, text)

			if ansiEscapeRE.MatchString(result) {
				rt.Fatalf("SectionHeader(%q, %q) with noColor=true contains ANSI escape: %q",
					emojiKey, text, result)
			}

			// Text must be preserved.
			if !strings.Contains(result, text) {
				rt.Fatalf("SectionHeader(%q, %q) with noColor=true lost text: %q",
					emojiKey, text, result)
			}
		})
	})

	t.Run("CommandBox_noColor_no_ANSI", func(t *testing.T) {
		// Property: For any command and description, CommandBox with noColor=true
		// must not contain ANSI escape sequences.
		rapid.Check(t, func(rt *rapid.T) {
			cmd := genCommand.Draw(rt, "cmd")
			desc := genDescription.Draw(rt, "desc")

			f := NewEasyFormatter(easy.NewEmojiMapper(true), true)
			result := f.CommandBox(cmd, desc)

			if ansiEscapeRE.MatchString(result) {
				rt.Fatalf("CommandBox(%q, %q) with noColor=true contains ANSI escape: %q",
					cmd, desc, result)
			}

			// Command must be preserved.
			if !strings.Contains(result, cmd) {
				rt.Fatalf("CommandBox(%q, %q) with noColor=true lost command: %q",
					cmd, desc, result)
			}

			// Description must be preserved when non-empty.
			if desc != "" && !strings.Contains(result, desc) {
				rt.Fatalf("CommandBox(%q, %q) with noColor=true lost description: %q",
					cmd, desc, result)
			}
		})
	})

	t.Run("FormatError_noColor_no_ANSI", func(t *testing.T) {
		// Property: For any error and hint, FormatError with noColor=true
		// must not contain ANSI escape sequences.
		rapid.Check(t, func(rt *rapid.T) {
			errMsg := genErrorMsg.Draw(rt, "errMsg")
			hint := genHint.Draw(rt, "hint")

			f := NewEasyFormatter(easy.NewEmojiMapper(true), true)
			result := f.FormatError(fmt.Errorf("%s", errMsg), hint)

			if ansiEscapeRE.MatchString(result) {
				rt.Fatalf("FormatError(%q, %q) with noColor=true contains ANSI escape: %q",
					errMsg, hint, result)
			}

			// Error message must be preserved.
			if !strings.Contains(result, errMsg) {
				rt.Fatalf("FormatError(%q, %q) with noColor=true lost error message: %q",
					errMsg, hint, result)
			}

			// Hint must be preserved when non-empty.
			if hint != "" && !strings.Contains(result, hint) {
				rt.Fatalf("FormatError(%q, %q) with noColor=true lost hint: %q",
					errMsg, hint, result)
			}
		})
	})

	t.Run("emoji_preserved_when_noColor", func(t *testing.T) {
		// Property: When emoji mapper is enabled and noColor=true,
		// known emoji keys should still produce emoji characters in the output.
		rapid.Check(t, func(rt *rapid.T) {
			emojiKey := rapid.SampledFrom(knownEmojiKeys).Draw(rt, "emojiKey")
			text := genText.Draw(rt, "text")

			emojiMapper := easy.NewEmojiMapper(true)
			expectedEmoji := emojiMapper.Get(emojiKey)

			f := NewEasyFormatter(emojiMapper, true)
			result := f.SectionHeader(emojiKey, text)

			// Emoji must be present in the output.
			if expectedEmoji != "" && !strings.Contains(result, expectedEmoji) {
				rt.Fatalf("SectionHeader(%q, %q) with noColor=true lost emoji %q: %q",
					emojiKey, text, expectedEmoji, result)
			}

			// No ANSI escape sequences.
			if ansiEscapeRE.MatchString(result) {
				rt.Fatalf("SectionHeader(%q, %q) with noColor=true contains ANSI escape: %q",
					emojiKey, text, result)
			}
		})
	})

	t.Run("FormatError_emoji_preserved_when_noColor", func(t *testing.T) {
		// Property: FormatError with noColor=true and emoji enabled should
		// preserve the error emoji in the output.
		rapid.Check(t, func(rt *rapid.T) {
			errMsg := genErrorMsg.Draw(rt, "errMsg")
			hint := genHint.Draw(rt, "hint")

			emojiMapper := easy.NewEmojiMapper(true)
			errorEmoji := emojiMapper.Get("error")

			f := NewEasyFormatter(emojiMapper, true)
			result := f.FormatError(errors.New(errMsg), hint)

			// Error emoji must be present.
			if errorEmoji != "" && !strings.Contains(result, errorEmoji) {
				rt.Fatalf("FormatError(%q, %q) with noColor=true lost error emoji %q: %q",
					errMsg, hint, errorEmoji, result)
			}

			// If hint is non-empty, hint emoji must be present.
			if hint != "" {
				hintEmoji := emojiMapper.Get("hint")
				if hintEmoji != "" && !strings.Contains(result, hintEmoji) {
					rt.Fatalf("FormatError(%q, %q) with noColor=true lost hint emoji %q: %q",
						errMsg, hint, hintEmoji, result)
				}
			}

			// No ANSI escape sequences.
			if ansiEscapeRE.MatchString(result) {
				rt.Fatalf("FormatError(%q, %q) with noColor=true contains ANSI escape: %q",
					errMsg, hint, result)
			}
		})
	})
}
