package cli

import "testing"

func TestIsBuiltinProvider(t *testing.T) {
	builtin := []string{"anthropic", "claude", "openai", "nvidia", "groq", "gemini", "qwen", "kiro", "kiro-cli"}
	for _, p := range builtin {
		if !isBuiltinProvider(p) {
			t.Errorf("isBuiltinProvider(%q) = false, want true", p)
		}
	}
	custom := []string{"kiro-api", "my-gw", "", "openai-proxy"}
	for _, p := range custom {
		if isBuiltinProvider(p) {
			t.Errorf("isBuiltinProvider(%q) = true, want false", p)
		}
	}
}

func TestMaskSecret(t *testing.T) {
	cases := map[string]string{
		"sk-secret12345": "sk-s******",
		"abcd":           "****",
		"":               "****",
		"x":              "****",
	}
	for in, want := range cases {
		if got := maskSecret(in); got != want {
			t.Errorf("maskSecret(%q) = %q, want %q", in, got, want)
		}
	}
}
