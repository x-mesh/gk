package cli

import (
	"strings"
	"testing"
)

func TestCheckAIProviderMissingBinaryIsWarn(t *testing.T) {
	c := checkAIProvider("nonexistent-provider-xyz")
	if c.Status != statusWarn {
		t.Errorf("missing binary: want WARN, got %s", c.Status)
	}
	if !strings.Contains(c.Detail, "not found") {
		t.Errorf("detail: %q", c.Detail)
	}
}

func TestCheckAIProviderKiroCLIHintDistinguishesIDELauncher(t *testing.T) {
	c := checkAIProvider("kiro-cli")
	// On the dev machine the headless kiro-cli may or may not be present;
	// regardless, the Fix message must never be misleading.
	if c.Status == statusWarn && strings.Contains(c.Detail, "IDE launcher") {
		if !strings.Contains(c.Fix, "kiro-cli") {
			t.Errorf("fix should suggest kiro-cli install: %q", c.Fix)
		}
	}
}

func TestProviderAuthHintReturnsOKWhenEnvSet(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "x")
	ok, _ := providerAuthHint("gemini")
	if !ok {
		t.Error("gemini should be OK when GEMINI_API_KEY is set")
	}
	t.Setenv("GEMINI_API_KEY", "")
	ok, hint := providerAuthHint("gemini")
	if ok {
		t.Error("gemini should be not-OK when no env key")
	}
	if hint == "" {
		t.Error("hint should be non-empty")
	}
}
