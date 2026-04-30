package easy

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Feature: easy-mode, Property 5: 이모지 Prefix 동작 — For any 알려진 상태 키에 대해:
// - enabled == true이면 EmojiMapper.Prefix(key)는 이모지 + 공백(" ")을 반환한다
// - enabled == false이면 EmojiMapper.Prefix(key)는 빈 문자열을 반환한다
// - 알려지지 않은 키에 대해서는 enabled 여부와 무관하게 빈 문자열을 반환한다
//
// **Validates: Requirements 4.1, 4.3, 4.5, 1.8**
func TestProperty_EmojiPrefix(t *testing.T) {
	// Collect known status keys from the defaultEmojis map.
	knownKeys := make([]string, 0, len(defaultEmojis))
	for k := range defaultEmojis {
		knownKeys = append(knownKeys, k)
	}

	// unknownKeys are keys that are NOT in the defaultEmojis map.
	unknownKeys := []string{
		"unknown", "foo", "bar", "baz", "qux",
		"invalid", "missing", "nope", "zilch", "absent",
	}

	// genEmojiKey generates either a known status key or an unknown key.
	genEmojiKey := rapid.Custom(func(rt *rapid.T) string {
		useKnown := rapid.Bool().Draw(rt, "useKnown")
		if useKnown && len(knownKeys) > 0 {
			return rapid.SampledFrom(knownKeys).Draw(rt, "knownKey")
		}
		return rapid.SampledFrom(unknownKeys).Draw(rt, "unknownKey")
	})

	t.Run("enabled_known_key_returns_emoji_space", func(t *testing.T) {
		// Property: When enabled == true and key is known,
		// Prefix(key) returns a non-empty string ending with a space.
		mapper := NewEmojiMapper(true)

		rapid.Check(t, func(rt *rapid.T) {
			key := rapid.SampledFrom(knownKeys).Draw(rt, "key")
			prefix := mapper.Prefix(key)

			if prefix == "" {
				rt.Fatalf("Prefix(%q) on enabled mapper returned empty string", key)
			}
			if !strings.HasSuffix(prefix, " ") {
				rt.Fatalf("Prefix(%q) = %q, expected to end with space", key, prefix)
			}

			// The prefix should be exactly emoji + " ".
			emoji := mapper.Get(key)
			expected := emoji + " "
			if prefix != expected {
				rt.Fatalf("Prefix(%q) = %q, want %q", key, prefix, expected)
			}
		})
	})

	t.Run("disabled_always_returns_empty", func(t *testing.T) {
		// Property: When enabled == false, Prefix(key) always returns ""
		// regardless of whether the key is known or unknown.
		mapper := NewEmojiMapper(false)

		rapid.Check(t, func(rt *rapid.T) {
			key := genEmojiKey.Draw(rt, "key")
			prefix := mapper.Prefix(key)

			if prefix != "" {
				rt.Fatalf("Prefix(%q) on disabled mapper = %q, want empty", key, prefix)
			}
		})
	})

	t.Run("unknown_key_always_returns_empty", func(t *testing.T) {
		// Property: For unknown keys, Prefix returns "" regardless of
		// the enabled state.
		rapid.Check(t, func(rt *rapid.T) {
			enabled := rapid.Bool().Draw(rt, "enabled")
			mapper := NewEmojiMapper(enabled)
			key := rapid.SampledFrom(unknownKeys).Draw(rt, "unknownKey")

			prefix := mapper.Prefix(key)
			if prefix != "" {
				rt.Fatalf("Prefix(%q) with enabled=%v = %q, want empty for unknown key",
					key, enabled, prefix)
			}
		})
	})

	t.Run("nil_mapper_returns_empty", func(t *testing.T) {
		// Property: A nil EmojiMapper always returns "" from Prefix.
		rapid.Check(t, func(rt *rapid.T) {
			key := genEmojiKey.Draw(rt, "key")
			var nilMapper *EmojiMapper
			prefix := nilMapper.Prefix(key)

			if prefix != "" {
				rt.Fatalf("Prefix(%q) on nil mapper = %q, want empty", key, prefix)
			}
		})
	})

	t.Run("get_and_prefix_consistency", func(t *testing.T) {
		// Property: For any key and enabled state, if Get(key) returns
		// a non-empty string, then Prefix(key) == Get(key) + " ".
		// If Get(key) returns "", then Prefix(key) == "".
		rapid.Check(t, func(rt *rapid.T) {
			enabled := rapid.Bool().Draw(rt, "enabled")
			mapper := NewEmojiMapper(enabled)
			key := genEmojiKey.Draw(rt, "key")

			emoji := mapper.Get(key)
			prefix := mapper.Prefix(key)

			if emoji == "" {
				if prefix != "" {
					rt.Fatalf("Get(%q)=\"\" but Prefix(%q)=%q, want empty",
						key, key, prefix)
				}
			} else {
				expected := emoji + " "
				if prefix != expected {
					rt.Fatalf("Get(%q)=%q but Prefix(%q)=%q, want %q",
						key, emoji, key, prefix, expected)
				}
			}
		})
	})
}

// TestEmojiMapper_AllRequiredMappings verifies that all 13 required emoji
// mappings defined in the design document exist in the default emoji set.
//
// Requirements: 4.2
func TestEmojiMapper_AllRequiredMappings(t *testing.T) {
	mapper := NewEmojiMapper(true)

	// 설계 문서에 정의된 필수 이모지 매핑 13개
	required := map[string]string{
		"success":  "✅",
		"warning":  "⚠️",
		"error":    "❌",
		"conflict": "💥",
		"new":      "🆕",
		"modified": "✏️",
		"deleted":  "🗑️",
		"staged":   "📦",
		"push":     "🚀",
		"pull":     "📥",
		"branch":   "🌿",
		"merge":    "🔀",
		"hint":     "💡",
	}

	if len(required) != 13 {
		t.Fatalf("expected 13 required mappings, got %d", len(required))
	}

	for key, wantEmoji := range required {
		got := mapper.Get(key)
		if got == "" {
			t.Errorf("missing required emoji mapping for key %q", key)
			continue
		}
		if got != wantEmoji {
			t.Errorf("emoji for key %q = %q, want %q", key, got, wantEmoji)
		}
	}
}

// TestEmojiMapper_EmojiPreservedWithNoColor verifies that the EmojiMapper
// itself always returns emoji characters regardless of any color settings.
// The ANSI stripping responsibility belongs to the formatter layer, not the
// emoji mapper. This test confirms that emoji values contain no ANSI escape
// sequences and are pure Unicode emoji characters.
//
// Requirements: 4.4
func TestEmojiMapper_EmojiPreservedWithNoColor(t *testing.T) {
	mapper := NewEmojiMapper(true)

	for key, wantEmoji := range defaultEmojis {
		got := mapper.Get(key)

		// 이모지 값이 ANSI 이스케이프 시퀀스를 포함하지 않아야 한다
		if strings.Contains(got, "\x1b[") {
			t.Errorf("emoji for key %q contains ANSI escape sequence: %q", key, got)
		}

		// 이모지 값이 기대한 유니코드 이모지와 일치해야 한다
		if got != wantEmoji {
			t.Errorf("emoji for key %q = %q, want %q", key, got, wantEmoji)
		}

		// Prefix도 이모지 + 공백만 포함하고 ANSI 코드가 없어야 한다
		prefix := mapper.Prefix(key)
		if strings.Contains(prefix, "\x1b[") {
			t.Errorf("Prefix(%q) contains ANSI escape sequence: %q", key, prefix)
		}

		expectedPrefix := wantEmoji + " "
		if prefix != expectedPrefix {
			t.Errorf("Prefix(%q) = %q, want %q", key, prefix, expectedPrefix)
		}
	}
}
