package i18n

import (
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

// Feature: easy-mode, Property 2: 카탈로그 폴백 체인 — For any key/language/mode
// combination, the fallback chain must be followed correctly.
//
// **Validates: Requirements 2.3, 2.4, 2.5**
func TestProperty_CatalogFallbackChain(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Generate a unique language code per test iteration to avoid
		// polluting the global registry across iterations.
		iterID := rapid.IntRange(0, 999_999).Draw(rt, "iterID")
		testLang := fmt.Sprintf("tl%d", iterID)

		// ── Generate partial catalogs ──────────────────────────────
		keys := genKeySet(rt)
		testMsgs := genPartialCatalog(rt, keys)
		enMsgs := genPartialCatalog(rt, keys)

		// Register test language and "en" fallback messages.
		// Save and restore previous registry state for the test language.
		prevTest := registry[testLang]
		prevEN := registry["en"]
		defer func() {
			if prevTest == nil {
				delete(registry, testLang)
			} else {
				registry[testLang] = prevTest
			}
			registry["en"] = prevEN
		}()

		RegisterMessages(testLang, testMsgs)
		// Merge enMsgs into "en" (on top of whatever init() already put there).
		RegisterMessages("en", enMsgs)

		// ── Test both modes ───────────────────────────────────────
		for _, mode := range []Mode{ModeEasy, ModeNormal} {
			cat := New(testLang, mode)

			for _, key := range keys {
				got := cat.Get(key)

				// Compute expected value following the fallback chain:
				// 1. testLang, requested mode
				// 2. testLang, normal mode
				// 3. "en", requested mode
				// 4. "en", normal mode
				// 5. key itself
				expected := expectedFallback(testMsgs, enMsgs, key, mode)

				if got != expected {
					rt.Fatalf(
						"key=%q lang=%q mode=%q: got %q, want %q\n"+
							"  testMsgs[key]=%v\n  enMsgs[key]=%v",
						key, testLang, mode, got, expected,
						testMsgs[key], enMsgs[key],
					)
				}
			}
		}
	})
}

// ── Generators ────────────────────────────────────────────────────────

// genKeySet generates a set of 1–6 unique message keys.
func genKeySet(rt *rapid.T) []string {
	n := rapid.IntRange(1, 6).Draw(rt, "numKeys")
	seen := make(map[string]bool, n)
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		k := rapid.SampledFrom([]string{
			"status.staged.header",
			"status.unstaged.header",
			"hint.status.has_staged",
			"error.push_failed",
			"general.success",
			"general.branch_info",
			"commit.success",
			"push.success",
			"pull.success",
			"nonexistent.key.alpha",
			"nonexistent.key.beta",
			"nonexistent.key.gamma",
		}).Draw(rt, fmt.Sprintf("key_%d", i))
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		keys = append(keys, "fallback.test.key")
	}
	return keys
}

// genPartialCatalog generates a partial message catalog for the given keys.
// For each key, it randomly decides:
//   - 0: key is missing entirely
//   - 1: key has only a normal variant
//   - 2: key has both normal and easy variants
func genPartialCatalog(rt *rapid.T, keys []string) map[string]map[Mode]string {
	msgs := make(map[string]map[Mode]string)
	for _, key := range keys {
		presence := rapid.IntRange(0, 2).Draw(rt, fmt.Sprintf("presence_%s", key))
		switch presence {
		case 0:
			// Key missing entirely — do not add to msgs.
		case 1:
			// Only normal variant.
			msgs[key] = map[Mode]string{
				ModeNormal: fmt.Sprintf("normal:%s", key),
			}
		case 2:
			// Both normal and easy variants.
			msgs[key] = map[Mode]string{
				ModeNormal: fmt.Sprintf("normal:%s", key),
				ModeEasy:   fmt.Sprintf("easy:%s", key),
			}
		}
	}
	return msgs
}

// expectedFallback computes the expected result of Catalog.Get(key)
// following the documented fallback chain:
//  1. testLang, requested mode (e.g. easy)
//  2. testLang, normal mode
//  3. "en", requested mode
//  4. "en", normal mode
//  5. key itself
func expectedFallback(testMsgs, enMsgs map[string]map[Mode]string, key string, mode Mode) string {
	// Step 1: testLang, requested mode
	if modes, ok := testMsgs[key]; ok {
		if msg := modes[mode]; msg != "" {
			return msg
		}
	}

	// Step 2: testLang, normal mode (only if mode != normal)
	if mode != ModeNormal {
		if modes, ok := testMsgs[key]; ok {
			if msg := modes[ModeNormal]; msg != "" {
				return msg
			}
		}
	}

	// Step 3: "en", requested mode
	// Note: we must also check the real "en" registry since init() already
	// registered enMessages. Our enMsgs were merged on top, so we look up
	// from the actual registry["en"] state. However, since we want a pure
	// oracle, we check enMsgs (which we registered) plus the pre-existing
	// "en" entries. For simplicity, we check the merged registry directly.
	if modes, ok := registry["en"][key]; ok {
		if msg := modes[mode]; msg != "" {
			return msg
		}
	}

	// Step 4: "en", normal mode
	if mode != ModeNormal {
		if modes, ok := registry["en"][key]; ok {
			if msg := modes[ModeNormal]; msg != "" {
				return msg
			}
		}
	}

	// Step 5: key itself
	return key
}

// Feature: easy-mode, Property 3: Normal 변형 필수 존재 — For any registered
// message key, the ModeNormal variant must exist with a non-empty value.
//
// **Validates: Requirements 2.7**
func TestProperty_NormalVariantInvariant(t *testing.T) {
	// Collect all registered languages from the global registry.
	langs := make([]string, 0, len(registry))
	for lang := range registry {
		langs = append(langs, lang)
	}
	if len(langs) == 0 {
		t.Fatal("registry is empty; expected at least one registered language")
	}

	rapid.Check(t, func(rt *rapid.T) {
		// Pick a random language from the registry.
		lang := rapid.SampledFrom(langs).Draw(rt, "lang")
		msgs := registry[lang]

		// Pick a random key from that language's messages.
		keys := make([]string, 0, len(msgs))
		for k := range msgs {
			keys = append(keys, k)
		}
		if len(keys) == 0 {
			// Language has no keys — nothing to check.
			return
		}
		key := rapid.SampledFrom(keys).Draw(rt, "key")

		// The ModeNormal variant must exist and be non-empty.
		modes := msgs[key]
		normalVal, ok := modes[ModeNormal]
		if !ok {
			rt.Fatalf("lang=%q key=%q: ModeNormal variant is missing from the map", lang, key)
		}
		if normalVal == "" {
			rt.Fatalf("lang=%q key=%q: ModeNormal variant exists but is empty", lang, key)
		}
	})

	// Additionally, do an exhaustive check over ALL languages and ALL keys
	// to ensure the invariant holds universally (not just for sampled keys).
	for lang, msgs := range registry {
		for key, modes := range msgs {
			normalVal, ok := modes[ModeNormal]
			if !ok {
				t.Errorf("lang=%q key=%q: ModeNormal variant is missing", lang, key)
			} else if normalVal == "" {
				t.Errorf("lang=%q key=%q: ModeNormal variant is empty", lang, key)
			}
		}
	}
}

// ── Unit Tests ────────────────────────────────────────────────────────

// saveAndRestoreEN saves the current "en" registry entry and restores it
// when the returned cleanup function is called. This prevents pollution
// from property tests that merge into the "en" registry.
func saveAndRestoreEN(t *testing.T) {
	t.Helper()
	prev := registry["en"]
	t.Cleanup(func() { registry["en"] = prev })
	// Re-register the canonical enMessages to ensure a clean state.
	registry["en"] = make(map[string]map[Mode]string, len(enMessages))
	for k, v := range enMessages {
		registry["en"][k] = v
	}
}

// TestGetf_Placeholder verifies that Getf() correctly applies fmt.Sprintf
// placeholders to the resolved message string.
//
// Validates: Requirements 2.6
func TestGetf_Placeholder(t *testing.T) {
	saveAndRestoreEN(t)

	cat := New("en", ModeNormal)

	// "general.branch_info" has a %s placeholder: "On branch %s"
	got := cat.Getf("general.branch_info", "main")
	want := "On branch main"
	if got != want {
		t.Errorf("Getf(general.branch_info, main): got %q, want %q", got, want)
	}

	// Easy mode should also resolve the placeholder.
	catEasy := New("en", ModeEasy)
	got = catEasy.Getf("general.branch_info", "feature/login")
	want = "▸ Current branch: feature/login"
	if got != want {
		t.Errorf("Getf(general.branch_info, feature/login) easy: got %q, want %q", got, want)
	}

	// Getf with no args should behave like Get (no formatting applied).
	got = cat.Getf("general.success")
	want = "Success"
	if got != want {
		t.Errorf("Getf(general.success) no args: got %q, want %q", got, want)
	}
}

// TestGet_NonExistentKey verifies that Get() returns the key itself when
// the key doesn't exist in any catalog (neither the requested language
// nor the English fallback).
//
// Validates: Requirements 2.6 (fallback chain step 5)
func TestGet_NonExistentKey(t *testing.T) {
	saveAndRestoreEN(t)

	cat := New("en", ModeNormal)

	key := "this.key.does.not.exist.anywhere"
	got := cat.Get(key)
	if got != key {
		t.Errorf("Get(%q): got %q, want the key itself", key, got)
	}

	// Also verify with a non-English catalog — the fallback chain should
	// exhaust both ko and en, then return the key.
	catKo := New("ko", ModeEasy)
	got = catKo.Get(key)
	if got != key {
		t.Errorf("Get(%q) ko/easy: got %q, want the key itself", key, got)
	}
}
