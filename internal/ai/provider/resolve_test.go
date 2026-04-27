package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// ── Task 5.4: [PBT] Property 2 — AI 응답 파싱 정규화 ──
// Feature: ai-resolve, Property 2: AI 응답 파싱 정규화
// **Validates: Requirements 5.2, 5.3**

func TestProperty2_ConflictResolutionResponseNormalization(t *testing.T) {
	validStrategies := []string{"ours", "theirs", "merged"}

	rapid.Check(t, func(t *rapid.T) {
		nResolutions := rapid.IntRange(1, 5).Draw(t, "nResolutions")
		resolutions := make([]ConflictResolutionOutput, nResolutions)
		for i := range resolutions {
			// Rationale: 0-200 runes (may exceed 120 limit)
			rationaleLen := rapid.IntRange(0, 200).Draw(t, fmt.Sprintf("rationaleLen_%d", i))
			rationale := rapid.StringMatching(`[a-zA-Z0-9 가-힣]{0,200}`).Draw(t, fmt.Sprintf("rationale_%d", i))
			if len([]rune(rationale)) > rationaleLen {
				rationale = string([]rune(rationale)[:rationaleLen])
			}

			nLines := rapid.IntRange(0, 5).Draw(t, fmt.Sprintf("nLines_%d", i))
			lines := make([]string, nLines)
			for j := range lines {
				lines[j] = rapid.StringMatching(`[a-zA-Z0-9 \t]{0,40}`).Draw(t, fmt.Sprintf("line_%d_%d", i, j))
			}

			resolutions[i] = ConflictResolutionOutput{
				Index:     rapid.IntRange(0, 10).Draw(t, fmt.Sprintf("index_%d", i)),
				Strategy:  rapid.SampledFrom(validStrategies).Draw(t, fmt.Sprintf("strategy_%d", i)),
				Resolved:  lines,
				Rationale: rationale,
			}
		}

		input := ConflictResolutionResult{Resolutions: resolutions}
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}

		result, err := parseConflictResolutionResponse(data)
		if err != nil {
			t.Fatalf("parseConflictResolutionResponse: %v", err)
		}

		if len(result.Resolutions) != len(resolutions) {
			t.Fatalf("Resolutions len: got %d, want %d", len(result.Resolutions), len(resolutions))
		}

		for i, r := range result.Resolutions {
			// Property: strategy is one of valid values
			switch r.Strategy {
			case "ours", "theirs", "merged":
				// ok
			default:
				t.Errorf("[%d] invalid strategy: %q", i, r.Strategy)
			}

			// Property: rationale is ≤120 runes
			if runeLen := len([]rune(r.Rationale)); runeLen > 120 {
				t.Errorf("[%d] rationale rune length = %d, want <= 120", i, runeLen)
			}
		}
	})
}

// ── Task 5.4 (continued): Invalid strategy defaults to "ours" ──

func TestProperty2_InvalidStrategyDefaultsToOurs(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate an invalid strategy value
		invalidStrategy := rapid.StringMatching(`[a-z]{1,10}`).
			Filter(func(s string) bool {
				return s != "ours" && s != "theirs" && s != "merged"
			}).Draw(t, "invalidStrategy")

		input := ConflictResolutionResult{
			Resolutions: []ConflictResolutionOutput{{
				Index:     0,
				Strategy:  invalidStrategy,
				Resolved:  []string{"line1"},
				Rationale: "test",
			}},
		}
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}

		result, err := parseConflictResolutionResponse(data)
		if err != nil {
			t.Fatalf("parseConflictResolutionResponse: %v", err)
		}

		if result.Resolutions[0].Strategy != "ours" {
			t.Errorf("Strategy = %q, want %q for invalid input %q",
				result.Resolutions[0].Strategy, "ours", invalidStrategy)
		}
	})
}

// ── Task 5.5: Unit tests — ConflictResolver type assertion 및 Nvidia 구현 ──

func TestConflictResolver_NvidiaSatisfiesInterface(t *testing.T) {
	var p Provider = NewNvidia()
	resolver, ok := p.(ConflictResolver)
	if !ok {
		t.Fatal("Nvidia should satisfy ConflictResolver interface")
	}
	if resolver == nil {
		t.Fatal("ConflictResolver assertion returned nil")
	}
}

func TestConflictResolver_FakeDoesNotSatisfy(t *testing.T) {
	var p Provider = NewFake()
	if _, ok := p.(ConflictResolver); ok {
		t.Error("Fake should NOT satisfy ConflictResolver interface")
	}
}

// ── parseConflictResolutionResponse unit tests ──

func TestParseConflictResolutionResponse_ValidJSON(t *testing.T) {
	raw := []byte(`{"resolutions":[{"index":0,"strategy":"merged","resolved":["line1","line2"],"rationale":"combined both changes"}]}`)
	res, err := parseConflictResolutionResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Resolutions) != 1 {
		t.Fatalf("expected 1 resolution, got %d", len(res.Resolutions))
	}
	r := res.Resolutions[0]
	if r.Strategy != "merged" {
		t.Errorf("Strategy = %q, want %q", r.Strategy, "merged")
	}
	if len(r.Resolved) != 2 {
		t.Errorf("Resolved len = %d, want 2", len(r.Resolved))
	}
}

func TestParseConflictResolutionResponse_WithFences(t *testing.T) {
	raw := []byte("```json\n{\"resolutions\":[{\"index\":0,\"strategy\":\"ours\",\"resolved\":[\"a\"],\"rationale\":\"keep local\"}]}\n```")
	res, err := parseConflictResolutionResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Resolutions[0].Strategy != "ours" {
		t.Errorf("Strategy = %q, want %q", res.Resolutions[0].Strategy, "ours")
	}
}

func TestParseConflictResolutionResponse_InvalidJSON(t *testing.T) {
	raw := []byte(`{not valid json}`)
	_, err := parseConflictResolutionResponse(raw)
	if !errors.Is(err, ErrProviderResponse) {
		t.Errorf("expected ErrProviderResponse, got %v", err)
	}
}

func TestParseConflictResolutionResponse_RationaleTruncation(t *testing.T) {
	longRationale := strings.Repeat("가", 150) // 150 runes
	raw, _ := json.Marshal(ConflictResolutionResult{
		Resolutions: []ConflictResolutionOutput{{
			Index: 0, Strategy: "theirs", Resolved: []string{"x"}, Rationale: longRationale,
		}},
	})
	res, err := parseConflictResolutionResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runeLen := len([]rune(res.Resolutions[0].Rationale)); runeLen > 120 {
		t.Errorf("Rationale rune length = %d, want <= 120", runeLen)
	}
}

func TestParseConflictResolutionResponse_EmptyResolutions(t *testing.T) {
	raw := []byte(`{"resolutions":[]}`)
	res, err := parseConflictResolutionResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Resolutions) != 0 {
		t.Errorf("expected 0 resolutions, got %d", len(res.Resolutions))
	}
}

// ── buildConflictResolutionUserPrompt unit tests ──

func TestBuildConflictResolutionUserPrompt_ContainsInput(t *testing.T) {
	in := ConflictResolutionInput{
		FilePath: "internal/config/config.go",
		Hunks: []ConflictHunkInput{{
			Index:  0,
			Ours:   []string{"func NewConfig() *Config {"},
			Theirs: []string{"func NewConfig() *Config {", "  return &Config{Timeout: 60}"},
		}},
		OperationType: "merge",
		Lang:          "ko",
	}
	prompt := buildConflictResolutionUserPrompt(in)

	if !strings.Contains(prompt, "internal/config/config.go") {
		t.Error("prompt should contain file path")
	}
	if !strings.Contains(prompt, "merge") {
		t.Error("prompt should contain operation_type")
	}
	if !strings.Contains(prompt, "ko") {
		t.Error("prompt should contain lang")
	}
	if !strings.Contains(prompt, "resolutions") {
		t.Error("prompt should contain response schema with 'resolutions'")
	}
	if !strings.Contains(prompt, "strategy") {
		t.Error("prompt should contain strategy in schema")
	}
}

// ── Nvidia ResolveConflicts integration test ──

func TestNvidiaResolveConflicts_Success(t *testing.T) {
	respJSON := `{"resolutions":[{"index":0,"strategy":"merged","resolved":["combined line"],"rationale":"merged both"}]}`
	resp := okResponse(chatResponse{
		Model:   "test-model",
		Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: respJSON}}},
		Usage:   &chatUsage{TotalTokens: 55},
	})
	client := &FakeHTTPClient{Responses: []*http.Response{resp}}
	nv := newTestNvidia(client, "test-key")

	res, err := nv.ResolveConflicts(context.Background(), ConflictResolutionInput{
		FilePath: "main.go",
		Hunks: []ConflictHunkInput{{
			Index:  0,
			Ours:   []string{"a"},
			Theirs: []string{"b"},
		}},
		OperationType: "merge",
		Lang:          "ko",
	})
	if err != nil {
		t.Fatalf("ResolveConflicts: %v", err)
	}
	if res.Model != "test-model" {
		t.Errorf("Model = %q, want %q", res.Model, "test-model")
	}
	if res.TokensUsed != 55 {
		t.Errorf("TokensUsed = %d, want 55", res.TokensUsed)
	}
	if len(res.Resolutions) != 1 {
		t.Fatalf("Resolutions len = %d, want 1", len(res.Resolutions))
	}
	if res.Resolutions[0].Strategy != "merged" {
		t.Errorf("Strategy = %q, want %q", res.Resolutions[0].Strategy, "merged")
	}
}

func TestNvidiaResolveConflicts_APIError(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{errResponse(401, "unauthorized")},
	}
	nv := newTestNvidia(client, "test-key")

	_, err := nv.ResolveConflicts(context.Background(), ConflictResolutionInput{
		FilePath:      "main.go",
		Hunks:         []ConflictHunkInput{{Index: 0, Ours: []string{"a"}, Theirs: []string{"b"}}},
		OperationType: "merge",
	})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestNvidiaResolveConflicts_InvalidResponseJSON(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{okResponse(chatResponse{
			Model:   "test-model",
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "not json"}}},
			Usage:   &chatUsage{TotalTokens: 1},
		})},
	}
	nv := newTestNvidia(client, "test-key")

	_, err := nv.ResolveConflicts(context.Background(), ConflictResolutionInput{
		FilePath:      "main.go",
		Hunks:         []ConflictHunkInput{{Index: 0, Ours: []string{"a"}, Theirs: []string{"b"}}},
		OperationType: "merge",
	})
	if !errors.Is(err, ErrProviderResponse) {
		t.Errorf("expected ErrProviderResponse, got %v", err)
	}
}
