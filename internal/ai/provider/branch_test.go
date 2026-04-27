package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// ── Task 5.4: [PBT] Property 2 — BranchAnalysisResult JSON round-trip ──
// Feature: ai-branch-clean, Property 2: BranchAnalysisResult JSON round-trip
// **Validates: Requirements 3.2, 3.3**

func TestProperty2_BranchAnalysisResultJSONRoundTrip(t *testing.T) {
	validCategories := []string{"completed", "experiment", "in_progress", "preserve"}

	rapid.Check(t, func(t *rapid.T) {
		nAnalyses := rapid.IntRange(0, 10).Draw(t, "nAnalyses")
		analyses := make([]BranchAnalysis, nAnalyses)
		for i := range analyses {
			// Summary: up to 80 runes (ASCII safe for round-trip)
			summaryLen := rapid.IntRange(0, 80).Draw(t, "summaryLen")
			summary := rapid.StringMatching(`[a-zA-Z0-9 ]{0,80}`).Draw(t, "summary")
			if len([]rune(summary)) > summaryLen {
				summary = string([]rune(summary)[:summaryLen])
			}
			// Ensure summary <= 80 runes
			if len([]rune(summary)) > 80 {
				summary = string([]rune(summary)[:80])
			}

			analyses[i] = BranchAnalysis{
				Name:       rapid.StringMatching(`[a-z][a-z0-9\-/]{0,30}`).Draw(t, "name"),
				Category:   rapid.SampledFrom(validCategories).Draw(t, "category"),
				Summary:    summary,
				SafeDelete: rapid.Bool().Draw(t, "safeDelete"),
			}
		}

		model := rapid.StringMatching(`[a-z0-9\-/]{1,30}`).Draw(t, "model")
		tokensUsed := rapid.IntRange(0, 100000).Draw(t, "tokensUsed")

		original := BranchAnalysisResult{
			Analyses:   analyses,
			Model:      model,
			TokensUsed: tokensUsed,
		}

		// Marshal
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}

		// Unmarshal
		var restored BranchAnalysisResult
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatalf("json.Unmarshal: %v", err)
		}

		// Verify equality
		if restored.Model != original.Model {
			t.Errorf("Model: got %q, want %q", restored.Model, original.Model)
		}
		if restored.TokensUsed != original.TokensUsed {
			t.Errorf("TokensUsed: got %d, want %d", restored.TokensUsed, original.TokensUsed)
		}
		if len(restored.Analyses) != len(original.Analyses) {
			t.Fatalf("Analyses len: got %d, want %d", len(restored.Analyses), len(original.Analyses))
		}
		for i, got := range restored.Analyses {
			want := original.Analyses[i]
			if got.Name != want.Name {
				t.Errorf("[%d] Name: got %q, want %q", i, got.Name, want.Name)
			}
			if got.Category != want.Category {
				t.Errorf("[%d] Category: got %q, want %q", i, got.Category, want.Category)
			}
			if got.Summary != want.Summary {
				t.Errorf("[%d] Summary: got %q, want %q", i, got.Summary, want.Summary)
			}
			if got.SafeDelete != want.SafeDelete {
				t.Errorf("[%d] SafeDelete: got %v, want %v", i, got.SafeDelete, want.SafeDelete)
			}
		}
	})
}


// ── Task 5.5: Unit tests — BranchAnalyzer type assertion 및 Nvidia 구현 ──

func TestBranchAnalyzer_NvidiaSatisfiesInterface(t *testing.T) {
	var p Provider = NewNvidia()
	analyzer, ok := p.(BranchAnalyzer)
	if !ok {
		t.Fatal("Nvidia should satisfy BranchAnalyzer interface")
	}
	if analyzer == nil {
		t.Fatal("BranchAnalyzer assertion returned nil")
	}
}

func TestBranchAnalyzer_GeminiDoesNotSatisfy(t *testing.T) {
	var p Provider = NewGemini()
	if _, ok := p.(BranchAnalyzer); ok {
		t.Error("Gemini should NOT satisfy BranchAnalyzer interface")
	}
}

func TestBranchAnalyzer_GroqDoesNotSatisfy(t *testing.T) {
	var p Provider = NewGroq()
	if _, ok := p.(BranchAnalyzer); ok {
		t.Error("Groq should NOT satisfy BranchAnalyzer interface")
	}
}

func TestBranchAnalyzer_FakeDoesNotSatisfy(t *testing.T) {
	var p Provider = NewFake()
	if _, ok := p.(BranchAnalyzer); ok {
		t.Error("Fake should NOT satisfy BranchAnalyzer interface")
	}
}

// ── parseBranchAnalysisResponse tests ──

func TestParseBranchAnalysisResponse_ValidJSON(t *testing.T) {
	raw := []byte(`{"analyses":[{"name":"feat/login","category":"completed","summary":"OAuth2 login","safe_delete":true}]}`)
	res, err := parseBranchAnalysisResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Analyses) != 1 {
		t.Fatalf("expected 1 analysis, got %d", len(res.Analyses))
	}
	a := res.Analyses[0]
	if a.Name != "feat/login" {
		t.Errorf("Name = %q, want %q", a.Name, "feat/login")
	}
	if a.Category != "completed" {
		t.Errorf("Category = %q, want %q", a.Category, "completed")
	}
	if a.Summary != "OAuth2 login" {
		t.Errorf("Summary = %q, want %q", a.Summary, "OAuth2 login")
	}
	if !a.SafeDelete {
		t.Error("SafeDelete = false, want true")
	}
}

func TestParseBranchAnalysisResponse_WithFences(t *testing.T) {
	raw := []byte("```json\n{\"analyses\":[{\"name\":\"fix/bug\",\"category\":\"experiment\",\"summary\":\"bug fix\",\"safe_delete\":true}]}\n```")
	res, err := parseBranchAnalysisResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Analyses) != 1 {
		t.Fatalf("expected 1 analysis, got %d", len(res.Analyses))
	}
	if res.Analyses[0].Category != "experiment" {
		t.Errorf("Category = %q, want %q", res.Analyses[0].Category, "experiment")
	}
}

func TestParseBranchAnalysisResponse_InvalidCategory(t *testing.T) {
	raw := []byte(`{"analyses":[{"name":"feat/x","category":"unknown","summary":"test","safe_delete":false}]}`)
	res, err := parseBranchAnalysisResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Invalid category should default to "preserve"
	if res.Analyses[0].Category != "preserve" {
		t.Errorf("Category = %q, want %q (default for invalid)", res.Analyses[0].Category, "preserve")
	}
}

func TestParseBranchAnalysisResponse_SummaryTruncation(t *testing.T) {
	// 100-char summary should be truncated to 80
	longSummary := strings.Repeat("가", 100) // 100 runes of Korean chars
	raw, _ := json.Marshal(BranchAnalysisResult{
		Analyses: []BranchAnalysis{{
			Name: "feat/long", Category: "completed", Summary: longSummary, SafeDelete: true,
		}},
	})
	res, err := parseBranchAnalysisResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runeLen := len([]rune(res.Analyses[0].Summary)); runeLen > 80 {
		t.Errorf("Summary rune length = %d, want <= 80", runeLen)
	}
}

func TestParseBranchAnalysisResponse_InvalidJSON(t *testing.T) {
	raw := []byte(`{not valid json}`)
	_, err := parseBranchAnalysisResponse(raw)
	if !errors.Is(err, ErrProviderResponse) {
		t.Errorf("expected ErrProviderResponse, got %v", err)
	}
}

func TestParseBranchAnalysisResponse_EmptyAnalyses(t *testing.T) {
	raw := []byte(`{"analyses":[]}`)
	res, err := parseBranchAnalysisResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Analyses) != 0 {
		t.Errorf("expected 0 analyses, got %d", len(res.Analyses))
	}
}

// ── buildBranchAnalysisUserPrompt tests ──

func TestBuildBranchAnalysisUserPrompt_ContainsBranchJSON(t *testing.T) {
	in := BranchAnalysisInput{
		Branches: []BranchInfo{
			{
				Name:           "feat/login",
				LastCommitMsg:  "add OAuth2",
				DiffStat:       "5 files changed",
				LastCommitDate: time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
				Status:         "squash-merged",
			},
		},
		BaseBranch: "main",
		Lang:       "ko",
	}
	prompt := buildBranchAnalysisUserPrompt(in)

	// Must contain branch name
	if !strings.Contains(prompt, "feat/login") {
		t.Error("prompt should contain branch name")
	}
	// Must contain base branch
	if !strings.Contains(prompt, "main") {
		t.Error("prompt should contain base branch")
	}
	// Must contain lang
	if !strings.Contains(prompt, "ko") {
		t.Error("prompt should contain lang")
	}
	// Must contain JSON schema hint
	if !strings.Contains(prompt, "analyses") {
		t.Error("prompt should contain response schema with 'analyses'")
	}
	// Must contain category options
	if !strings.Contains(prompt, "completed") {
		t.Error("prompt should contain category options")
	}
}

func TestBuildBranchAnalysisUserPrompt_EmptyBranches(t *testing.T) {
	in := BranchAnalysisInput{
		Branches:   []BranchInfo{},
		BaseBranch: "main",
		Lang:       "en",
	}
	prompt := buildBranchAnalysisUserPrompt(in)
	if prompt == "" {
		t.Error("prompt should not be empty even with no branches")
	}
	if !strings.Contains(prompt, "main") {
		t.Error("prompt should contain base branch")
	}
}

// ── Nvidia AnalyzeBranches integration test ──

func TestNvidiaAnalyzeBranches_Success(t *testing.T) {
	respJSON := `{"analyses":[{"name":"feat/login","category":"completed","summary":"OAuth2 login flow","safe_delete":true}]}`
	resp := okResponse(chatResponse{
		Model:   "test-model",
		Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: respJSON}}},
		Usage:   &chatUsage{TotalTokens: 42},
	})
	client := &FakeHTTPClient{Responses: []*http.Response{resp}}
	nv := newTestNvidia(client, "test-key")

	res, err := nv.AnalyzeBranches(context.Background(), BranchAnalysisInput{
		Branches: []BranchInfo{{
			Name:           "feat/login",
			LastCommitMsg:  "add OAuth2",
			DiffStat:       "5 files changed",
			LastCommitDate: time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC),
			Status:         "squash-merged",
		}},
		BaseBranch: "main",
		Lang:       "ko",
	})
	if err != nil {
		t.Fatalf("AnalyzeBranches: %v", err)
	}
	if res.Model != "test-model" {
		t.Errorf("Model = %q, want %q", res.Model, "test-model")
	}
	if res.TokensUsed != 42 {
		t.Errorf("TokensUsed = %d, want 42", res.TokensUsed)
	}
	if len(res.Analyses) != 1 {
		t.Fatalf("Analyses len = %d, want 1", len(res.Analyses))
	}
	if res.Analyses[0].Category != "completed" {
		t.Errorf("Category = %q, want %q", res.Analyses[0].Category, "completed")
	}
}

func TestNvidiaAnalyzeBranches_APIError(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{errResponse(401, "unauthorized")},
	}
	nv := newTestNvidia(client, "test-key")

	_, err := nv.AnalyzeBranches(context.Background(), BranchAnalysisInput{
		Branches:   []BranchInfo{{Name: "feat/x", Status: "stale"}},
		BaseBranch: "main",
	})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestNvidiaAnalyzeBranches_InvalidResponseJSON(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{okResponse(chatResponse{
			Model:   "test-model",
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "not json at all"}}},
			Usage:   &chatUsage{TotalTokens: 1},
		})},
	}
	nv := newTestNvidia(client, "test-key")

	_, err := nv.AnalyzeBranches(context.Background(), BranchAnalysisInput{
		Branches:   []BranchInfo{{Name: "feat/x", Status: "stale"}},
		BaseBranch: "main",
	})
	if !errors.Is(err, ErrProviderResponse) {
		t.Errorf("expected ErrProviderResponse, got %v", err)
	}
}

func TestNvidiaAnalyzeBranches_UsesJSONMode(t *testing.T) {
	respJSON := `{"analyses":[]}`
	client := &FakeHTTPClient{
		Responses: []*http.Response{okResponse(chatResponse{
			Model:   "test-model",
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: respJSON}}},
			Usage:   &chatUsage{TotalTokens: 1},
		})},
	}
	nv := newTestNvidia(client, "test-key")

	_, err := nv.AnalyzeBranches(context.Background(), BranchAnalysisInput{
		Branches:   []BranchInfo{},
		BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("AnalyzeBranches: %v", err)
	}

	// Verify JSON mode was requested
	if len(client.Calls) == 0 {
		t.Fatal("no HTTP calls recorded")
	}
	body, _ := io.ReadAll(client.Calls[0].Req.Body)
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_object" {
		t.Errorf("response_format = %+v, want {type: json_object}", req.ResponseFormat)
	}
}
