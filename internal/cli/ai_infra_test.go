package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/x-mesh/gk/internal/config"
)

func TestAIChatMaxTokens(t *testing.T) {
	if got := aiChatMaxTokens(config.AIConfig{}); got != 4096 {
		t.Errorf("default = %d, want 4096", got)
	}
	ai := config.AIConfig{Chat: config.AIChatConfig{MaxTokens: 1000}}
	if got := aiChatMaxTokens(ai); got != 1000 {
		t.Errorf("configured = %d, want 1000", got)
	}
}

func TestAICacheKey(t *testing.T) {
	a := aiCacheKey("review", "diff", "en", "fake")
	if a != aiCacheKey("review", "diff", "en", "fake") {
		t.Error("key must be deterministic")
	}
	if a == aiCacheKey("review", "DIFF", "en", "fake") {
		t.Error("key must change when an input changes")
	}
	if len(a) != 16 {
		t.Errorf("key length = %d, want 16", len(a))
	}
}

func TestWriteAIJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := writeAIJSON(&buf, map[string]string{"provider": "fake", "review": "x"}); err != nil {
		t.Fatalf("writeAIJSON: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if got["provider"] != "fake" || got["review"] != "x" {
		t.Errorf("round-trip mismatch: %v", got)
	}
}
