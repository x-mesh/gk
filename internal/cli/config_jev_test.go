package cli

import (
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
)

func TestMaskJevYAML(t *testing.T) {
	cfg := config.Defaults()
	cfg.AI.Jev.APIKey = "test-jev-secret"
	out, err := maskJevYAML(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if strings.Contains(text, "test-jev-secret") || !strings.Contains(text, "test******") {
		t.Fatalf("masked output = %s", text)
	}
}

func TestMaskJevValueForParentKeys(t *testing.T) {
	value := map[string]any{"jev": map[string]any{"api_key": "test-jev-secret", "model": "m"}}
	got := maskJevValue("ai", value).(map[string]any)
	jev := got["jev"].(map[string]any)
	if jev["api_key"] == "test-jev-secret" {
		t.Fatal("ai parent value leaked the Jev key")
	}
	if got["jev"].(map[string]any)["model"] != "m" {
		t.Fatal("non-secret Jev value changed")
	}
}

func TestIsJevKey(t *testing.T) {
	for _, key := range []string{"ai", "ai.jev", "ai.jev.api_key", "ai.jev.suggest"} {
		if !isJevKey(key) {
			t.Errorf("isJevKey(%q) = false", key)
		}
	}
	if isJevKey("ai.openai.api_key") {
		t.Error("other provider must not be treated as Jev")
	}
}

func TestValidateConfigWriteScopeRejectsLocalJevSettings(t *testing.T) {
	for _, key := range []string{"ai.jev", "ai.jev.api_key", "ai.jev.find_rerank", "ai"} {
		if err := validateConfigWriteScope(key, true); err == nil {
			t.Errorf("local write for %q must fail", key)
		}
	}
	if err := validateConfigWriteScope("ai.jev.api_key", false); err != nil {
		t.Errorf("global Jev write must succeed: %v", err)
	}
	if err := validateConfigWriteScope("ai.openai.api_key", true); err != nil {
		t.Errorf("other provider local write must remain allowed: %v", err)
	}
}
