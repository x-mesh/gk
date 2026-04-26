package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// ── Type assertion tests ─────────────────────────────────────────────

func TestNvidiaImplementsSummarizer(t *testing.T) {
	var p Provider = NewNvidia()
	if _, ok := p.(Summarizer); !ok {
		t.Error("*Nvidia does not implement Summarizer")
	}
}

func TestGeminiDoesNotImplementSummarizer(t *testing.T) {
	var p Provider = &Gemini{}
	if _, ok := p.(Summarizer); ok {
		t.Error("*Gemini unexpectedly implements Summarizer")
	}
}

func TestFakeImplementsSummarizer(t *testing.T) {
	var p Provider = NewFake()
	if _, ok := p.(Summarizer); !ok {
		t.Error("*Fake does not implement Summarizer")
	}
}

// ── Basic Summarize flow with FakeHTTPClient ─────────────────────────

func TestNvidiaSummarize_PR(t *testing.T) {
	wantText := "## Summary\nAdded new feature\n## Changes\n- a.go added"
	client := &FakeHTTPClient{
		Responses: []*http.Response{okResponse(chatResponse{
			Model: "test-model",
			Choices: []chatChoice{{
				Message: chatMessage{Role: "assistant", Content: wantText},
			}},
			Usage: &chatUsage{TotalTokens: 42},
		})},
	}
	nv := newTestNvidia(client, "test-key")

	res, err := nv.Summarize(context.Background(), SummarizeInput{
		Kind:    "pr",
		Diff:    "+func main() {}",
		Commits: []string{"feat: add main"},
		Lang:    "en",
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if res.Text != wantText {
		t.Errorf("Text = %q, want %q", res.Text, wantText)
	}
	if res.Model != "test-model" {
		t.Errorf("Model = %q, want %q", res.Model, "test-model")
	}
	if res.TokensUsed != 42 {
		t.Errorf("TokensUsed = %d, want 42", res.TokensUsed)
	}
}

func TestNvidiaSummarize_Review(t *testing.T) {
	wantText := "## a.go\n- [warning] missing error check"
	client := &FakeHTTPClient{
		Responses: []*http.Response{okResponse(chatResponse{
			Model:   "test-model",
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: wantText}}},
			Usage:   &chatUsage{TotalTokens: 10},
		})},
	}
	nv := newTestNvidia(client, "test-key")

	res, err := nv.Summarize(context.Background(), SummarizeInput{
		Kind: "review",
		Diff: "+if err != nil { }",
		Lang: "en",
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if res.Text != wantText {
		t.Errorf("Text = %q, want %q", res.Text, wantText)
	}
}

func TestNvidiaSummarize_Changelog(t *testing.T) {
	wantText := "## Features\n- add login\n## Bug Fixes\n- fix crash"
	client := &FakeHTTPClient{
		Responses: []*http.Response{okResponse(chatResponse{
			Model:   "test-model",
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: wantText}}},
			Usage:   &chatUsage{TotalTokens: 15},
		})},
	}
	nv := newTestNvidia(client, "test-key")

	res, err := nv.Summarize(context.Background(), SummarizeInput{
		Kind:    "changelog",
		Diff:    "+new code",
		Commits: []string{"feat: add login", "fix: fix crash"},
		Lang:    "ko",
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if res.Text != wantText {
		t.Errorf("Text = %q, want %q", res.Text, wantText)
	}
}

// ── Summarize does NOT use json_object response_format ───────────────

func TestNvidiaSummarize_NoJSONResponseFormat(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{okResponse(chatResponse{
			Model:   "test-model",
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "summary text"}}},
			Usage:   &chatUsage{TotalTokens: 5},
		})},
	}
	nv := newTestNvidia(client, "test-key")

	_, err := nv.Summarize(context.Background(), SummarizeInput{
		Kind: "pr",
		Diff: "+code",
		Lang: "en",
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	if len(client.Calls) == 0 {
		t.Fatal("no HTTP calls recorded")
	}
	body, _ := io.ReadAll(client.Calls[0].Req.Body)
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if req.ResponseFormat != nil {
		t.Errorf("Summarize should not set response_format, got %+v", req.ResponseFormat)
	}
}

// ── Summarize prompt contains Kind-specific content ──────────────────

func TestNvidiaSummarize_PromptContainsKindContent(t *testing.T) {
	tests := []struct {
		kind     string
		wantSub  string // substring expected in the user prompt
	}{
		{"pr", "Pull Request"},
		{"review", "code review"},
		{"changelog", "changelog"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			client := &FakeHTTPClient{
				Responses: []*http.Response{okResponse(chatResponse{
					Model:   "m",
					Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "ok"}}},
					Usage:   &chatUsage{TotalTokens: 1},
				})},
			}
			nv := newTestNvidia(client, "test-key")

			_, err := nv.Summarize(context.Background(), SummarizeInput{
				Kind: tt.kind,
				Diff: "+x",
				Lang: "en",
			})
			if err != nil {
				t.Fatalf("Summarize: %v", err)
			}

			body, _ := io.ReadAll(client.Calls[0].Req.Body)
			var req chatRequest
			_ = json.Unmarshal(body, &req)

			// Find user message.
			var userContent string
			for _, m := range req.Messages {
				if m.Role == "user" {
					userContent = m.Content
				}
			}
			if !strings.Contains(userContent, tt.wantSub) {
				t.Errorf("user prompt for kind=%q missing %q", tt.kind, tt.wantSub)
			}
		})
	}
}

// ── Fake Summarizer tests ────────────────────────────────────────────

func TestFakeSummarizeCyclesThroughResponses(t *testing.T) {
	f := NewFake()
	f.SummarizeResponses = []SummarizeResult{
		{Text: "first", Model: "m1", TokensUsed: 10},
		{Text: "second", Model: "m2", TokensUsed: 20},
	}

	ctx := context.Background()
	r1, err := f.Summarize(ctx, SummarizeInput{Kind: "pr"})
	if err != nil {
		t.Fatalf("Summarize 1: %v", err)
	}
	if r1.Text != "first" {
		t.Errorf("first Text = %q, want %q", r1.Text, "first")
	}

	r2, err := f.Summarize(ctx, SummarizeInput{Kind: "review"})
	if err != nil {
		t.Fatalf("Summarize 2: %v", err)
	}
	if r2.Text != "second" {
		t.Errorf("second Text = %q, want %q", r2.Text, "second")
	}

	// Exhausted → zero value.
	r3, err := f.Summarize(ctx, SummarizeInput{Kind: "changelog"})
	if err != nil {
		t.Fatalf("Summarize 3: %v", err)
	}
	if r3.Text != "" {
		t.Errorf("exhausted Text = %q, want empty", r3.Text)
	}

	// Verify call recording.
	want := []string{"Summarize", "Summarize", "Summarize"}
	if len(f.Calls) != 3 {
		t.Fatalf("Calls = %v, want %v", f.Calls, want)
	}
	for i, c := range f.Calls {
		if c != want[i] {
			t.Errorf("Calls[%d] = %q, want %q", i, c, want[i])
		}
	}
}
