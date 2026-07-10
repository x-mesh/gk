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
	c := checkAIAPIProvider("fake", "FAKE_KEY", "", false, false)
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
	c := checkAIAPIProvider("fake", "FAKE_KEY", "https://example.invalid", false, false)
	if c.Status != statusWarn {
		t.Fatalf("want WARN for unset key, got %s", c.Status)
	}
	if !strings.Contains(c.Detail, "not set") {
		t.Errorf("detail should say not set, got %q", c.Detail)
	}
}

// TestCheckAIAPIKeyFromConfigIsPass verifies that a key supplied only via
// config (api_key) — with no env var — still counts as authenticated, and
// the detail names the config source rather than the env var.
func TestCheckAIAPIKeyFromConfigIsPass(t *testing.T) {
	t.Setenv("FAKE_KEY", "")
	c := checkAIAPIProvider("fake", "FAKE_KEY", "", false, true)
	if c.Status != statusPass {
		t.Fatalf("want PASS for config-supplied key, got %s", c.Status)
	}
	if !strings.Contains(c.Detail, "ai.fake.api_key set") {
		t.Errorf("detail should name the config source, got %q", c.Detail)
	}
}

// TestAIDoctorChecksHonorsConfigAPIKey verifies aiDoctorChecks reads
// ai.<provider>.api_key so a key kept only in config is not falsely
// reported as missing.
func TestAIDoctorChecksHonorsConfigAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	cfg := config.Defaults()
	cfg.AI.OpenAI.APIKey = "config-key-do-not-print"

	checks := aiDoctorChecks(&cfg)
	c := findAICheck(checks, "ai api: openai")
	if c.Name == "" {
		t.Fatal("openai check missing")
	}
	if c.Status != statusPass {
		t.Errorf("config api_key should make openai PASS, got %s (%q)", c.Status, c.Detail)
	}
	if strings.Contains(c.Detail, "config-key-do-not-print") {
		t.Errorf("detail must never echo the key value: %q", c.Detail)
	}
}

// TestCheckAIAPICustomEndpointWording verifies a config-overridden
// endpoint is labelled "custom endpoint" so the user can tell the probe
// targeted their override rather than the built-in default. The probe
// targets an unroutable host so the row lands in the probe-failed branch
// without a real network round-trip succeeding.
func TestCheckAIAPICustomEndpointWording(t *testing.T) {
	t.Setenv("FAKE_KEY", "x")
	c := checkAIAPIProvider("fake", "FAKE_KEY", "http://127.0.0.1:1/unreachable", true, false)
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

// TestProviderImplementsToolCallerMatchesResolveChatProviderCriterion
// verifies providerImplementsToolCaller agrees with the split
// resolveChatProviderChain (chat.go) actually enforces via its own
// `p.(provider.ToolCaller)` assertion: anthropic/openai/nvidia/groq
// support tool calling, gemini/qwen/kiro(-cli) do not.
func TestProviderImplementsToolCallerMatchesResolveChatProviderCriterion(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"anthropic", true},
		{"openai", true},
		{"nvidia", true},
		{"groq", true},
		{"gemini", false},
		{"qwen", false},
		{"kiro", false},
		{"kiro-cli", false},
		{"nonexistent-provider-xyz", false},
	}
	for _, tc := range cases {
		if got := providerImplementsToolCaller(tc.name); got != tc.want {
			t.Errorf("providerImplementsToolCaller(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestCheckChatAvailabilityNoToolCallingIsWarn verifies CLI adapters
// (gemini/qwen/kiro-cli) are reported chat-unavailable regardless of
// auth state — they never satisfy provider.ToolCaller.
func TestCheckChatAvailabilityNoToolCallingIsWarn(t *testing.T) {
	c := checkChatAvailability("gemini", true, "should not matter")
	if c.Status != statusWarn {
		t.Fatalf("want WARN for a non-tool-calling provider, got %s", c.Status)
	}
	if !strings.Contains(c.Detail, "no tool-calling support") {
		t.Errorf("detail should explain missing tool-calling support, got %q", c.Detail)
	}
	if !strings.Contains(c.Fix, "gk ask") {
		t.Errorf("fix should point at `gk ask`, got %q", c.Fix)
	}
}

// TestCheckChatAvailabilityAuthMissingIsWarn verifies a tool-calling
// provider (anthropic) without an available auth source is WARN and
// names the missing-auth hint verbatim.
func TestCheckChatAvailabilityAuthMissingIsWarn(t *testing.T) {
	c := checkChatAvailability("anthropic", false, "ANTHROPIC_API_KEY not set")
	if c.Status != statusWarn {
		t.Fatalf("want WARN when auth is missing, got %s", c.Status)
	}
	if !strings.Contains(c.Detail, "ANTHROPIC_API_KEY not set") {
		t.Errorf("detail should include the missing-auth hint, got %q", c.Detail)
	}
}

// TestCheckChatAvailabilityPass verifies a tool-calling provider with an
// available auth source is PASS.
func TestCheckChatAvailabilityPass(t *testing.T) {
	c := checkChatAvailability("anthropic", true, "")
	if c.Status != statusPass {
		t.Fatalf("want PASS when tool-calling + auth are both present, got %s (%q)", c.Status, c.Detail)
	}
	if !strings.Contains(c.Detail, "tool-calling supported") {
		t.Errorf("detail should confirm tool-calling support, got %q", c.Detail)
	}
}

// TestAIDoctorChecksIncludesChatRows verifies aiDoctorChecks appends an
// "ai chat: <name>" row per provider, and that a CLI provider's chat row
// stays WARN even when its own auth is satisfied — proving the row
// checks tool-calling support, not just the auth already reported by the
// "ai provider" row above it.
func TestAIDoctorChecksIncludesChatRows(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "x")
	t.Setenv("GEMINI_API_KEY", "x")

	checks := aiDoctorChecks(nil)

	anthropicChat := findAICheck(checks, "ai chat: anthropic")
	if anthropicChat.Name == "" {
		t.Fatal("ai chat: anthropic row missing")
	}
	if anthropicChat.Status != statusPass {
		t.Errorf("anthropic chat row: want PASS with key set, got %s (%q)", anthropicChat.Status, anthropicChat.Detail)
	}

	geminiChat := findAICheck(checks, "ai chat: gemini")
	if geminiChat.Name == "" {
		t.Fatal("ai chat: gemini row missing")
	}
	if geminiChat.Status != statusWarn {
		t.Errorf("gemini chat row: want WARN (no tool-calling) even with auth present, got %s (%q)", geminiChat.Status, geminiChat.Detail)
	}
	if !strings.Contains(geminiChat.Detail, "no tool-calling support") {
		t.Errorf("gemini chat detail should cite missing tool-calling support, got %q", geminiChat.Detail)
	}
}

// TestAIDoctorChecksChatRowMissingKeyIsWarn verifies the anthropic chat
// row is WARN — not silently PASS — when its API key is absent, even
// though anthropic itself supports tool calling.
func TestAIDoctorChecksChatRowMissingKeyIsWarn(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg := config.Defaults()

	checks := aiDoctorChecks(&cfg)
	c := findAICheck(checks, "ai chat: anthropic")
	if c.Name == "" {
		t.Fatal("ai chat: anthropic row missing")
	}
	if c.Status != statusWarn {
		t.Errorf("want WARN when key is missing, got %s (%q)", c.Status, c.Detail)
	}
	if !strings.Contains(c.Detail, "ANTHROPIC_API_KEY not set") {
		t.Errorf("detail should name the missing key, got %q", c.Detail)
	}
}

// TestAIDoctorChecksFlagsDefaultProviderChatRow verifies the "(default)"
// annotation reaches the chat row too, not just the "ai api"/"ai
// provider" row, so the report is unambiguous about which provider `gk
// chat` will actually try first.
func TestAIDoctorChecksFlagsDefaultProviderChatRow(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "x")
	cfg := config.Defaults()
	cfg.AI.Provider = "anthropic"

	checks := aiDoctorChecks(&cfg)
	c := findAICheck(checks, "ai chat: anthropic")
	if c.Name == "" {
		t.Fatal("ai chat: anthropic row missing")
	}
	if !strings.Contains(c.Name, "(default)") {
		t.Errorf("configured default provider's chat row should be flagged, got name %q", c.Name)
	}
}
