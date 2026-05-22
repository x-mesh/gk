package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// Summarize must forward a positive SummarizeInput.MaxTokens to the wire
// request (the field was previously ignored entirely).
func TestNvidiaSummarizeHonoursMaxTokens(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{okResponse(chatResponse{
			Model:   "test-model",
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "ok"}}},
			Usage:   &chatUsage{TotalTokens: 3},
		})},
	}
	nv := newTestNvidia(client, "test-key")
	if _, err := nv.Summarize(context.Background(), SummarizeInput{
		Kind: "review", Diff: "d", Lang: "en", MaxTokens: 1234,
	}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	req := decodeChatRequest(t, client)
	if req.MaxTokens != 1234 {
		t.Errorf("max_tokens = %d, want 1234", req.MaxTokens)
	}
}

// A zero MaxTokens leaves max_tokens unset (omitempty) so the provider's
// own default applies.
func TestNvidiaSummarizeOmitsMaxTokensWhenZero(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{okResponse(chatResponse{
			Model:   "test-model",
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: "ok"}}},
			Usage:   &chatUsage{TotalTokens: 3},
		})},
	}
	nv := newTestNvidia(client, "test-key")
	if _, err := nv.Summarize(context.Background(), SummarizeInput{
		Kind: "review", Diff: "d", Lang: "en",
	}); err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if req := decodeChatRequest(t, client); req.MaxTokens != 0 {
		t.Errorf("max_tokens = %d, want 0 (omitted)", req.MaxTokens)
	}
}

func decodeChatRequest(t *testing.T, client *FakeHTTPClient) chatRequest {
	t.Helper()
	if len(client.Calls) == 0 {
		t.Fatal("no HTTP calls recorded")
	}
	body, _ := io.ReadAll(client.Calls[0].Req.Body)
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	return req
}
