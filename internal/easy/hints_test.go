package easy

import (
	"testing"

	"github.com/x-mesh/gk/internal/i18n"
	"pgregory.net/rapid"
)

// Feature: easy-mode, Property 7: 힌트 생성 동작 — For any 힌트 키와 상태 조합에 대해:
// - HintLevel == HintOff이면 Generate는 항상 빈 문자열을 반환한다
// - HintLevel != HintOff이고 유효한 상태 조합(hasStaged, hasUnstaged, hasUntracked,
//   hasConflict 중 하나 이상 true)이면 StatusHint는 비어있지 않은 문자열을 반환한다
// - HintLevel != HintOff이고 모든 bool이 false이면 StatusHint는 빈 문자열을 반환한다
//
// **Validates: Requirements 5.4, 5.5**
func TestProperty_HintGeneration(t *testing.T) {
	// genHintLevel generates a random HintLevel value.
	genHintLevel := rapid.Custom(func(rt *rapid.T) HintLevel {
		return rapid.SampledFrom([]HintLevel{
			HintVerbose,
			HintMinimal,
			HintOff,
		}).Draw(rt, "hintLevel")
	})

	// genHintInputs generates a random (HintLevel, hasStaged, hasUnstaged,
	// hasUntracked, hasConflict) combination.
	type hintInputs struct {
		Level        HintLevel
		HasStaged    bool
		HasUnstaged  bool
		HasUntracked bool
		HasConflict  bool
	}

	genHintInputs := rapid.Custom(func(rt *rapid.T) hintInputs {
		return hintInputs{
			Level:        genHintLevel.Draw(rt, "level"),
			HasStaged:    rapid.Bool().Draw(rt, "hasStaged"),
			HasUnstaged:  rapid.Bool().Draw(rt, "hasUnstaged"),
			HasUntracked: rapid.Bool().Draw(rt, "hasUntracked"),
			HasConflict:  rapid.Bool().Draw(rt, "hasConflict"),
		}
	})

	// Helper: create a HintGenerator with the given level using a real catalog.
	makeGenerator := func(level HintLevel) *HintGenerator {
		cat := i18n.New("ko", i18n.ModeEasy)
		emoji := NewEmojiMapper(true)
		return NewHintGenerator(level, cat, emoji)
	}

	t.Run("hint_off_always_returns_empty", func(t *testing.T) {
		// Property: When HintLevel == HintOff, StatusHint always returns ""
		// regardless of the boolean flags.
		rapid.Check(t, func(rt *rapid.T) {
			hasStaged := rapid.Bool().Draw(rt, "hasStaged")
			hasUnstaged := rapid.Bool().Draw(rt, "hasUnstaged")
			hasUntracked := rapid.Bool().Draw(rt, "hasUntracked")
			hasConflict := rapid.Bool().Draw(rt, "hasConflict")

			gen := makeGenerator(HintOff)
			got := gen.StatusHint(hasStaged, hasUnstaged, hasUntracked, hasConflict)

			if got != "" {
				rt.Fatalf("StatusHint(%v, %v, %v, %v) with HintOff = %q, want empty",
					hasStaged, hasUnstaged, hasUntracked, hasConflict, got)
			}
		})
	})

	t.Run("active_level_with_true_flag_returns_non_empty", func(t *testing.T) {
		// Property: When HintLevel != HintOff AND at least one bool is true,
		// StatusHint returns a non-empty string.
		rapid.Check(t, func(rt *rapid.T) {
			level := rapid.SampledFrom([]HintLevel{
				HintVerbose,
				HintMinimal,
			}).Draw(rt, "level")

			hasStaged := rapid.Bool().Draw(rt, "hasStaged")
			hasUnstaged := rapid.Bool().Draw(rt, "hasUnstaged")
			hasUntracked := rapid.Bool().Draw(rt, "hasUntracked")
			hasConflict := rapid.Bool().Draw(rt, "hasConflict")

			// Ensure at least one flag is true.
			if !hasStaged && !hasUnstaged && !hasUntracked && !hasConflict {
				// Force at least one true to test the non-empty property.
				idx := rapid.IntRange(0, 3).Draw(rt, "forceIdx")
				switch idx {
				case 0:
					hasStaged = true
				case 1:
					hasUnstaged = true
				case 2:
					hasUntracked = true
				case 3:
					hasConflict = true
				}
			}

			gen := makeGenerator(level)
			got := gen.StatusHint(hasStaged, hasUnstaged, hasUntracked, hasConflict)

			if got == "" {
				rt.Fatalf("StatusHint(%v, %v, %v, %v) with level=%q returned empty, want non-empty",
					hasStaged, hasUnstaged, hasUntracked, hasConflict, level)
			}
		})
	})

	t.Run("active_level_all_false_returns_empty", func(t *testing.T) {
		// Property: When HintLevel != HintOff AND all bools are false,
		// StatusHint returns "".
		rapid.Check(t, func(rt *rapid.T) {
			level := rapid.SampledFrom([]HintLevel{
				HintVerbose,
				HintMinimal,
			}).Draw(rt, "level")

			gen := makeGenerator(level)
			got := gen.StatusHint(false, false, false, false)

			if got != "" {
				rt.Fatalf("StatusHint(false, false, false, false) with level=%q = %q, want empty",
					level, got)
			}
		})
	})

	t.Run("combined_property", func(t *testing.T) {
		// Combined property: For any random (HintLevel, bools) combination,
		// the three rules above hold simultaneously.
		rapid.Check(t, func(rt *rapid.T) {
			input := genHintInputs.Draw(rt, "input")

			gen := makeGenerator(input.Level)
			got := gen.StatusHint(input.HasStaged, input.HasUnstaged,
				input.HasUntracked, input.HasConflict)

			anyTrue := input.HasStaged || input.HasUnstaged ||
				input.HasUntracked || input.HasConflict

			switch {
			case input.Level == HintOff:
				// Rule 1: HintOff → always empty
				if got != "" {
					rt.Fatalf("HintOff: StatusHint = %q, want empty", got)
				}

			case anyTrue:
				// Rule 2: active level + at least one true → non-empty
				if got == "" {
					rt.Fatalf("level=%q, anyTrue=true: StatusHint returned empty",
						input.Level)
				}

			default:
				// Rule 3: active level + all false → empty
				if got != "" {
					rt.Fatalf("level=%q, allFalse: StatusHint = %q, want empty",
						input.Level, got)
				}
			}
		})
	})
}

// ── Unit tests for HintGenerator ────────────────────────────────────────────
// Requirements: 5.2, 5.3, 5.6

// TestHintGenerator_VerboseOutput verifies that verbose mode includes the full
// description with emoji (💡) from the easy catalog.
func TestHintGenerator_VerboseOutput(t *testing.T) {
	cat := i18n.New("ko", i18n.ModeEasy)
	emoji := NewEmojiMapper(true)
	gen := NewHintGenerator(HintVerbose, cat, emoji)

	tests := []struct {
		name string
		key  string
		want string // expected substring
	}{
		{
			name: "staged hint contains emoji and description",
			key:  "hint.status.has_staged",
			want: "💡 다음 단계: 변경사항을 저장하려면 → gk commit",
		},
		{
			name: "unstaged hint contains emoji and description",
			key:  "hint.status.has_unstaged",
			want: "💡 다음 단계: 변경사항을 준비하려면 → gk add <파일>",
		},
		{
			name: "untracked hint contains emoji and description",
			key:  "hint.status.has_untracked",
			want: "💡 다음 단계: 새 파일을 추적하려면 → gk add <파일>",
		},
		{
			name: "conflict hint contains emoji and description",
			key:  "hint.status.has_conflict",
			want: "💡 다음 단계: 충돌을 해결한 뒤 → gk add <파일> → gk commit",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := gen.Generate(tc.key)
			if got != tc.want {
				t.Errorf("Generate(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

// TestHintGenerator_MinimalOutput verifies that minimal mode shows only the
// command (no full description, no emoji prefix).
func TestHintGenerator_MinimalOutput(t *testing.T) {
	cat := i18n.New("ko", i18n.ModeEasy)
	emoji := NewEmojiMapper(true)
	gen := NewHintGenerator(HintMinimal, cat, emoji)

	tests := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "staged minimal shows only command",
			key:  "hint.status.has_staged",
			want: "gk commit",
		},
		{
			name: "unstaged minimal shows only command",
			key:  "hint.status.has_unstaged",
			want: "gk add <파일>",
		},
		{
			name: "untracked minimal shows only command",
			key:  "hint.status.has_untracked",
			want: "gk add <파일>",
		},
		{
			name: "conflict minimal shows only command",
			key:  "hint.status.has_conflict",
			want: "gk add <파일> → gk commit",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := gen.Generate(tc.key)
			if got != tc.want {
				t.Errorf("Generate(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

// TestHintGenerator_ErrorHints verifies error hint formatting for push_failed,
// pull_failed, and merge_conflict in both verbose and minimal modes.
func TestHintGenerator_ErrorHints(t *testing.T) {
	cat := i18n.New("ko", i18n.ModeEasy)
	emoji := NewEmojiMapper(true)

	tests := []struct {
		name    string
		level   HintLevel
		key     string
		want    string
	}{
		// Verbose error hints
		{
			name:  "push_failed verbose",
			level: HintVerbose,
			key:   "hint.error.push_failed",
			want:  "💡 먼저 서버에서 가져오기를 실행하세요 → gk pull",
		},
		{
			name:  "pull_failed verbose",
			level: HintVerbose,
			key:   "hint.error.pull_failed",
			want:  "💡 먼저 변경사항을 저장하세요 → gk commit 또는 gk stash",
		},
		{
			name:  "merge_conflict verbose",
			level: HintVerbose,
			key:   "hint.error.merge_conflict",
			want:  "💡 충돌 파일을 편집한 뒤 → gk add <파일> → gk commit",
		},
		// Minimal error hints
		{
			name:  "push_failed minimal",
			level: HintMinimal,
			key:   "hint.error.push_failed",
			want:  "gk pull",
		},
		{
			name:  "pull_failed minimal",
			level: HintMinimal,
			key:   "hint.error.pull_failed",
			want:  "gk commit 또는 gk stash",
		},
		{
			name:  "merge_conflict minimal",
			level: HintMinimal,
			key:   "hint.error.merge_conflict",
			want:  "gk add <파일> → gk commit",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gen := NewHintGenerator(tc.level, cat, emoji)
			got := gen.Generate(tc.key)
			if got != tc.want {
				t.Errorf("Generate(%q) with level=%q = %q, want %q",
					tc.key, tc.level, got, tc.want)
			}
		})
	}

	// HintOff should return empty for all error hints
	t.Run("off_returns_empty_for_all_error_hints", func(t *testing.T) {
		gen := NewHintGenerator(HintOff, cat, emoji)
		for _, key := range []string{
			"hint.error.push_failed",
			"hint.error.pull_failed",
			"hint.error.merge_conflict",
		} {
			got := gen.Generate(key)
			if got != "" {
				t.Errorf("Generate(%q) with HintOff = %q, want empty", key, got)
			}
		}
	})
}

// TestHintGenerator_NilSafety verifies that a nil HintGenerator returns empty
// strings for all methods without panicking.
func TestHintGenerator_NilSafety(t *testing.T) {
	var gen *HintGenerator

	t.Run("Generate returns empty", func(t *testing.T) {
		got := gen.Generate("hint.status.has_staged")
		if got != "" {
			t.Errorf("nil.Generate() = %q, want empty", got)
		}
	})

	t.Run("StatusHint returns empty", func(t *testing.T) {
		got := gen.StatusHint(true, true, true, true)
		if got != "" {
			t.Errorf("nil.StatusHint() = %q, want empty", got)
		}
	})

	t.Run("Level returns HintOff", func(t *testing.T) {
		got := gen.Level()
		if got != HintOff {
			t.Errorf("nil.Level() = %q, want %q", got, HintOff)
		}
	})
}

// TestHintGenerator_Level verifies that the Level() accessor returns the
// correct HintLevel that was set during construction.
func TestHintGenerator_Level(t *testing.T) {
	cat := i18n.New("ko", i18n.ModeEasy)
	emoji := NewEmojiMapper(true)

	tests := []struct {
		name  string
		level HintLevel
	}{
		{"verbose", HintVerbose},
		{"minimal", HintMinimal},
		{"off", HintOff},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gen := NewHintGenerator(tc.level, cat, emoji)
			got := gen.Level()
			if got != tc.level {
				t.Errorf("Level() = %q, want %q", got, tc.level)
			}
		})
	}
}
