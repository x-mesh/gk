package cli

import (
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
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

// findAICheck returns the first check whose Name starts with the given
// prefix (e.g. "ai api: anthropic"), or a zero check when none matches.
func findAICheck(checks []doctorCheck, prefix string) doctorCheck {
	for _, c := range checks {
		if strings.HasPrefix(c.Name, prefix) {
			return c
		}
	}
	return doctorCheck{}
}

// TestCheckAIAPIKeySetNoProbeWording verifies the reachability/validity
// wording is unambiguous: a set key without a probe must say validity is
// not verified, never bare "set".
func TestCheckAIAPIKeySetNoProbeWording(t *testing.T) {
	t.Setenv("FAKE_KEY", "secret-value-do-not-print")
	c := checkAIAPIProvider("fake", "FAKE_KEY", "", false)
	if c.Status != statusPass {
		t.Fatalf("want PASS for set key without probe, got %s", c.Status)
	}
	if !strings.Contains(c.Detail, "validity not verified") {
		t.Errorf("detail should disclaim validity verification, got %q", c.Detail)
	}
	if strings.Contains(c.Detail, "secret-value-do-not-print") {
		t.Errorf("detail must never echo the key value: %q", c.Detail)
	}
}

// TestCheckAIAPIUnsetKeyIsWarn verifies an unset key is WARN with a
// remediation hint and no key echo.
func TestCheckAIAPIUnsetKeyIsWarn(t *testing.T) {
	t.Setenv("FAKE_KEY", "")
	c := checkAIAPIProvider("fake", "FAKE_KEY", "https://example.invalid", false)
	if c.Status != statusWarn {
		t.Fatalf("want WARN for unset key, got %s", c.Status)
	}
	if !strings.Contains(c.Detail, "not set") {
		t.Errorf("detail should say not set, got %q", c.Detail)
	}
}

// TestCheckAIAPICustomEndpointWording verifies a config-overridden
// endpoint is labelled "custom endpoint" so the user can tell the probe
// targeted their override rather than the built-in default. The probe
// targets an unroutable host so the row lands in the probe-failed branch
// without a real network round-trip succeeding.
func TestCheckAIAPICustomEndpointWording(t *testing.T) {
	t.Setenv("FAKE_KEY", "x")
	c := checkAIAPIProvider("fake", "FAKE_KEY", "http://127.0.0.1:1/unreachable", true)
	if !strings.Contains(c.Detail, "custom endpoint") {
		t.Errorf("overridden endpoint should be labelled custom, got %q", c.Detail)
	}
}

// TestAIDoctorChecksHonorsEndpointOverride verifies aiDoctorChecks reads
// the per-provider endpoint override from config and flags it as custom.
func TestAIDoctorChecksHonorsEndpointOverride(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "x")
	cfg := config.Defaults()
	cfg.AI.Anthropic.Endpoint = "http://127.0.0.1:1/anthropic-override"

	checks := aiDoctorChecks(&cfg)
	c := findAICheck(checks, "ai api: anthropic")
	if c.Name == "" {
		t.Fatal("anthropic check missing")
	}
	if !strings.Contains(c.Detail, "custom endpoint") {
		t.Errorf("override should surface as custom endpoint, got %q", c.Detail)
	}
}

// TestAIDoctorChecksFlagsDefaultProvider verifies the configured default
// provider (ai.provider) is annotated so the report shows which provider
// `gk commit` uses by default.
func TestAIDoctorChecksFlagsDefaultProvider(t *testing.T) {
	cfg := config.Defaults()
	cfg.AI.Provider = "gemini"

	checks := aiDoctorChecks(&cfg)
	c := findAICheck(checks, "ai provider: gemini")
	if c.Name == "" {
		t.Fatal("gemini check missing")
	}
	if !strings.Contains(c.Name, "(default)") {
		t.Errorf("configured default provider should be flagged, got name %q", c.Name)
	}
	// A non-default provider must NOT be flagged.
	other := findAICheck(checks, "ai provider: qwen")
	if strings.Contains(other.Name, "(default)") {
		t.Errorf("non-default provider should not be flagged: %q", other.Name)
	}
}

// TestAIDoctorChecksNilConfig verifies the AI section still renders with
// built-in defaults when config failed to load (nil).
func TestAIDoctorChecksNilConfig(t *testing.T) {
	checks := aiDoctorChecks(nil)
	if findAICheck(checks, "ai api: anthropic").Name == "" {
		t.Error("nil config should still produce the anthropic row")
	}
	if findAICheck(checks, "ai provider: gemini").Name == "" {
		t.Error("nil config should still produce the gemini row")
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
