package cli

import "testing"

// TestResolveResponseLang locks in the conversational-language precedence:
// --lang > deliberate non-en ai.lang > output.lang > ai.lang(en) > "en".
// The headline case is the English-commit / Korean-CLI setup
// (ai.lang=en, output.lang=ko), which must yield Korean conversational output.
func TestResolveResponseLang(t *testing.T) {
	cases := []struct {
		name     string
		override string
		aiLang   string
		outLang  string
		want     string
	}{
		{"english commits, korean cli", "", "en", "ko", "ko"},
		{"override beats everything", "ja", "en", "ko", "ja"},
		{"override en is explicit", "en", "ko", "ko", "en"},
		{"deliberate non-en ai wins", "", "ja", "ko", "ja"},
		{"ai unset follows output", "", "", "ko", "ko"},
		{"ai en, output unset -> en", "", "en", "", "en"},
		{"all empty -> en", "", "", "", "en"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveResponseLang(tc.override, tc.aiLang, tc.outLang); got != tc.want {
				t.Errorf("resolveResponseLang(%q, %q, %q) = %q, want %q",
					tc.override, tc.aiLang, tc.outLang, got, tc.want)
			}
		})
	}
}
