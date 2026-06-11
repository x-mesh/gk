package cli

import (
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
)

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

func TestExistingAPISummary(t *testing.T) {
	// No provider → nothing to preserve.
	empty := config.Defaults()
	if got := existingAPISummary(&empty); got != nil {
		t.Errorf("no provider: want nil, got %v", got)
	}

	// Built-in provider: one line, no endpoint/key detail.
	builtin := config.Defaults()
	builtin.AI.Provider = "anthropic"
	if got := existingAPISummary(&builtin); len(got) != 1 || got[0] != "provider: anthropic" {
		t.Errorf("builtin: got %v", got)
	}

	// Custom provider: endpoint shown, key masked.
	custom := config.Defaults()
	custom.AI.Provider = "kiro-api"
	custom.AI.Providers = map[string]config.AICustomProviderConfig{
		"kiro-api": {
			Endpoint: "https://gw.example.com/v1/chat/completions",
			Model:    "kiro/claude-haiku-4.5",
			APIKey:   "sk-secret12345",
		},
	}
	got := existingAPISummary(&custom)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "https://gw.example.com") {
		t.Errorf("endpoint missing: %v", got)
	}
	if strings.Contains(joined, "sk-secret12345") {
		t.Errorf("api key leaked unmasked: %v", got)
	}
	if !strings.Contains(joined, "sk-s******") {
		t.Errorf("masked key missing: %v", got)
	}
}

func TestPruneNoopChanges(t *testing.T) {
	cur := config.Defaults()
	cur.AI.Provider = "kiro-api"
	cur.Output.Lang = "ko"
	cur.Log.Vis = []string{"cc", "safety", "tags-rule"}
	cur.AI.Providers = map[string]config.AICustomProviderConfig{
		"kiro-api": {Endpoint: "https://gw.example.com", APIKey: "sk-old"},
	}

	changes := map[string]string{
		"ai.provider":                   "kiro-api",                // same → drop
		"output.lang":                   "en",                      // differs → keep
		"output.easy":                   "false",                   // same as default → drop
		"ai.providers.kiro-api.api_key": "sk-old",                  // same → drop
		"ai.providers.kiro-api.model":   "kiro/claude-haiku-4.5",   // empty now → keep
		"log.graph":                     "false",                   // same as default → drop
	}
	lists := map[string][]string{
		"log.vis":    {"safety", "cc", "tags-rule"}, // same set, reordered → drop
		"status.vis": {"gauge", "tree"},             // differs → keep
	}

	pruneNoopChanges(&cur, changes, lists)

	wantKept := []string{"output.lang", "ai.providers.kiro-api.model"}
	if len(changes) != len(wantKept) {
		t.Errorf("changes after prune = %v", changes)
	}
	for _, k := range wantKept {
		if _, ok := changes[k]; !ok {
			t.Errorf("%s must survive prune; have %v", k, changes)
		}
	}
	if _, ok := lists["log.vis"]; ok {
		t.Errorf("log.vis (same set) must be pruned; have %v", lists)
	}
	if _, ok := lists["status.vis"]; !ok {
		t.Errorf("status.vis must survive prune; have %v", lists)
	}
}

func TestPreviewLogVis(t *testing.T) {
	// Every layer leaves its fingerprint in the sample.
	fingerprints := map[string]string{
		"cc":        "scope:",
		"safety":    "◇",
		"tags-rule": "──┤",
		"impact":    "+210 −15",
		"graph":     "●─┘",
		"pulse":     "pulse",
		"calendar":  "W1",
		"hotspots":  "◉",
		"trailers":  "AI-Assisted-By",
		"lanes":     "jinwoo ●",
		"breaking":  "‼",
		"squash":    "⊟",
		"wip":       "≡2",
	}
	for layer, mark := range fingerprints {
		if got := previewLogVis([]string{layer}); !strings.Contains(got, mark) {
			t.Errorf("previewLogVis(%s) missing %q:\n%s", layer, mark, got)
		}
	}
	// Unselected layers stay out.
	if got := previewLogVis([]string{"impact"}); strings.Contains(got, "scope:") {
		t.Errorf("cc header leaked into impact-only preview:\n%s", got)
	}
	if got := previewLogVis(nil); got == "" {
		t.Error("empty selection must still render a placeholder")
	}
}

func TestPreviewStatusVis(t *testing.T) {
	fingerprints := map[string]string{
		"gauge":      "↑2 ↓1",
		"base":       "from main",
		"bar":        "tree: [",
		"progress":   "clean: [",
		"types":      "types:",
		"tree":       "├─",
		"staleness":  "· 2h",
		"local":      "2 unstaged",
		"since-push": "unpushed 2h",
		"conflict":   "conflict:",
		"churn":      "churn:",
		"risk":       "risk:",
		"stash":      "stash:",
		"heatmap":    "heatmap:",
		"wip":        "wip: ×3",
		"squash":     "◈ 3",
		"ancestry":   "depth:",
		"collision":  "⊠",
	}
	for layer, mark := range fingerprints {
		sel := []string{layer}
		if layer == "staleness" {
			sel = []string{"tree", "staleness"} // ages hang off the tree rows
		}
		if got := previewStatusVis(sel); !strings.Contains(got, mark) {
			t.Errorf("previewStatusVis(%s) missing %q:\n%s", layer, mark, got)
		}
	}
	// The skeleton renders even with nothing selected.
	if got := previewStatusVis(nil); !strings.Contains(got, "█  BRANCH") {
		t.Errorf("skeleton missing:\n%s", got)
	}
}

func TestValidateVisNames(t *testing.T) {
	if err := validateVisNames([]string{"cc", "impact"}, logVisChoices); err != nil {
		t.Errorf("valid names rejected: %v", err)
	}
	if err := validateVisNames([]string{"cc", "typo"}, logVisChoices); err == nil {
		t.Error("unknown name accepted")
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
