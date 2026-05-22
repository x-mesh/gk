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
)

// anthropicOKResponse builds a 200 *http.Response carrying the given
// anthropicResponse as JSON.
func anthropicOKResponse(r anthropicResponse) *http.Response {
	b, _ := json.Marshal(r)
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(string(b))),
	}
}

// anthropicErrResponse builds a non-2xx *http.Response with a plain body.
func anthropicErrResponse(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// newTestAnthropic returns an Anthropic wired to the fake client with an
// instant sleep so retries don't slow tests.
func newTestAnthropic(client *FakeHTTPClient) *Anthropic {
	return &Anthropic{
		Client:    client,
		APIKey:    "test-key",
		Endpoint:  "https://test.example.com/v1/messages",
		Model:     "test-model",
		Version:   defaultAnthropicVersion,
		MaxTokens: 256,
		Timeout:   5 * time.Second,
		MaxRetry:  3,
		EnvLookup: func(string) string { return "" },
		SleepFn:   func(_ context.Context, _ time.Duration) bool { return true },
	}
}

// decodeAnthropicRequest reads the recorded request body into an
// anthropicRequest for assertions.
func decodeAnthropicRequest(t *testing.T, call FakeHTTPCall) anthropicRequest {
	t.Helper()
	b, err := io.ReadAll(call.Req.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var req anthropicRequest
	if err := json.Unmarshal(b, &req); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	return req
}

func TestAnthropicNameAndLocality(t *testing.T) {
	a := NewAnthropic()
	if a.Name() != "anthropic" {
		t.Errorf("Name() = %q, want %q", a.Name(), "anthropic")
	}
	if a.Locality() != LocalityRemote {
		t.Errorf("Locality() = %v, want %v", a.Locality(), LocalityRemote)
	}
}

// TestAnthropicSystemSentAsCachedBlock verifies the system prompt is sent
// as a structured content-block array carrying an ephemeral cache_control
// marker (the whole point of this feature).
func TestAnthropicSystemSentAsCachedBlock(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{anthropicOKResponse(anthropicResponse{
			Model:   "test-model",
			Content: []anthropicContentBlock{{Type: "text", Text: validComposeContent()}},
			Usage:   anthropicUsage{InputTokens: 100, OutputTokens: 20},
		})},
	}
	a := newTestAnthropic(client)

	_, err := a.Compose(context.Background(), ComposeInput{
		Group:            Group{Type: "feat", Files: []string{"a.go"}},
		Lang:             "en",
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
		Diff:             "+x\n",
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if len(client.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(client.Calls))
	}

	req := decodeAnthropicRequest(t, client.Calls[0])
	if len(req.System) != 1 {
		t.Fatalf("System blocks = %d, want 1: %+v", len(req.System), req.System)
	}
	block := req.System[0]
	if block.Type != "text" {
		t.Errorf("System[0].Type = %q, want %q", block.Type, "text")
	}
	if block.Text != systemPrompt {
		t.Errorf("System[0].Text = %q, want systemPrompt", block.Text)
	}
	if block.CacheControl == nil {
		t.Fatal("System[0].CacheControl is nil, want ephemeral marker")
	}
	if block.CacheControl.Type != "ephemeral" {
		t.Errorf("CacheControl.Type = %q, want %q", block.CacheControl.Type, "ephemeral")
	}
}

// TestAnthropicSystemRawJSONShape checks the on-the-wire JSON matches the
// Messages API caching format exactly (array of {type,text,cache_control}).
func TestAnthropicSystemRawJSONShape(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{anthropicOKResponse(anthropicResponse{
			Model:   "test-model",
			Content: []anthropicContentBlock{{Type: "text", Text: validComposeContent()}},
			Usage:   anthropicUsage{InputTokens: 1, OutputTokens: 1},
		})},
	}
	a := newTestAnthropic(client)
	if _, err := a.Compose(context.Background(), ComposeInput{
		Group:            Group{Type: "feat", Files: []string{"a.go"}},
		MaxSubjectLength: 72,
	}); err != nil {
		t.Fatalf("Compose: %v", err)
	}

	b, _ := io.ReadAll(client.Calls[0].Req.Body)
	var raw struct {
		System []struct {
			Type         string          `json:"type"`
			Text         string          `json:"text"`
			CacheControl json.RawMessage `json:"cache_control"`
		} `json:"system"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if len(raw.System) != 1 {
		t.Fatalf("raw system len = %d, want 1", len(raw.System))
	}
	if got := string(raw.System[0].CacheControl); got != `{"type":"ephemeral"}` {
		t.Errorf("cache_control JSON = %s, want {\"type\":\"ephemeral\"}", got)
	}
}

// TestSystemBlocksEmptyOmitted confirms an empty system prompt produces no
// system blocks (so the omitempty field is dropped from the request).
func TestSystemBlocksEmptyOmitted(t *testing.T) {
	if blocks := systemBlocks(""); blocks != nil {
		t.Errorf("systemBlocks(\"\") = %+v, want nil", blocks)
	}

	body, err := json.Marshal(anthropicRequest{
		Model:     "m",
		MaxTokens: 1,
		System:    systemBlocks(""),
		Messages:  []anthropicMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "system") {
		t.Errorf("empty system should be omitted, body = %s", body)
	}
}

// TestSystemBlocksNonEmpty confirms a non-empty prompt yields exactly one
// cached block.
func TestSystemBlocksNonEmpty(t *testing.T) {
	blocks := systemBlocks("you are a bot")
	if len(blocks) != 1 {
		t.Fatalf("len = %d, want 1", len(blocks))
	}
	if blocks[0].Text != "you are a bot" || blocks[0].Type != "text" {
		t.Errorf("block = %+v", blocks[0])
	}
	if blocks[0].CacheControl == nil || blocks[0].CacheControl.Type != "ephemeral" {
		t.Errorf("cache_control = %+v", blocks[0].CacheControl)
	}
}

// TestAnthropicUsageTotalIncludesCacheTokens verifies cache_creation and
// cache_read input tokens are folded into the reported total.
func TestAnthropicUsageTotalIncludesCacheTokens(t *testing.T) {
	u := anthropicUsage{
		InputTokens:              10,
		OutputTokens:             5,
		CacheCreationInputTokens: 100,
		CacheReadInputTokens:     200,
	}
	if got := u.total(); got != 315 {
		t.Errorf("total() = %d, want 315", got)
	}
}

// TestAnthropicComposeReportsCacheTokens drives a full Compose where the
// response includes cache read tokens, and checks they flow into
// TokensUsed.
func TestAnthropicComposeReportsCacheTokens(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{anthropicOKResponse(anthropicResponse{
			Model:   "claude-test",
			Content: []anthropicContentBlock{{Type: "text", Text: validComposeContent()}},
			Usage: anthropicUsage{
				InputTokens:          5,
				OutputTokens:         7,
				CacheReadInputTokens: 90,
			},
		})},
	}
	a := newTestAnthropic(client)
	res, err := a.Compose(context.Background(), ComposeInput{
		Group:            Group{Type: "feat", Files: []string{"a.go"}},
		MaxSubjectLength: 72,
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if res.Subject != "add feature" {
		t.Errorf("Subject = %q", res.Subject)
	}
	if res.Model != "claude-test" {
		t.Errorf("Model = %q", res.Model)
	}
	if res.TokensUsed != 102 { // 5 + 7 + 90
		t.Errorf("TokensUsed = %d, want 102", res.TokensUsed)
	}
}

func TestAnthropicClassifySuccess(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{anthropicOKResponse(anthropicResponse{
			Model:   "claude-test",
			Content: []anthropicContentBlock{{Type: "text", Text: validClassifyContent()}},
			Usage:   anthropicUsage{InputTokens: 12, OutputTokens: 8},
		})},
	}
	a := newTestAnthropic(client)
	res, err := a.Classify(context.Background(), ClassifyInput{
		Files:        []FileChange{{Path: "a.go", Status: "added", DiffHint: "+package main\n"}},
		AllowedTypes: []string{"feat", "fix"},
		Lang:         "en",
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(res.Groups) != 1 || res.Groups[0].Type != "feat" {
		t.Errorf("groups = %+v", res.Groups)
	}
	if res.TokensUsed != 20 {
		t.Errorf("TokensUsed = %d, want 20", res.TokensUsed)
	}

	// system block must still be present + cached for Classify.
	req := decodeAnthropicRequest(t, client.Calls[0])
	if len(req.System) != 1 || req.System[0].CacheControl == nil {
		t.Errorf("Classify did not send a cached system block: %+v", req.System)
	}
}

func TestAnthropicSummarizeSuccess(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{anthropicOKResponse(anthropicResponse{
			Model:   "claude-test",
			Content: []anthropicContentBlock{{Type: "text", Text: "Risk: low\nInspect: file.go"}},
			Usage:   anthropicUsage{InputTokens: 30, OutputTokens: 10},
		})},
	}
	a := newTestAnthropic(client)
	res, err := a.Summarize(context.Background(), SummarizeInput{
		Kind:    "merge-plan",
		Diff:    "Target: main\n",
		Commits: []string{"abc123 feat: incoming"},
		Lang:    "en",
	})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if res.Text != "Risk: low\nInspect: file.go" {
		t.Errorf("Text = %q", res.Text)
	}
	if res.Provider != "anthropic" {
		t.Errorf("Provider = %q", res.Provider)
	}
	if res.TokensUsed != 40 {
		t.Errorf("TokensUsed = %d, want 40", res.TokensUsed)
	}

	// The summarize system prompt should also be cached.
	req := decodeAnthropicRequest(t, client.Calls[0])
	if len(req.System) != 1 || req.System[0].Text != summarizeSystemPrompt {
		t.Errorf("summarize system block wrong: %+v", req.System)
	}
}

func TestAnthropicHeadersSet(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{anthropicOKResponse(anthropicResponse{
			Model:   "test-model",
			Content: []anthropicContentBlock{{Type: "text", Text: validComposeContent()}},
			Usage:   anthropicUsage{InputTokens: 1, OutputTokens: 1},
		})},
	}
	a := newTestAnthropic(client)
	if _, err := a.Compose(context.Background(), ComposeInput{
		Group:            Group{Type: "feat", Files: []string{"a.go"}},
		MaxSubjectLength: 72,
	}); err != nil {
		t.Fatalf("Compose: %v", err)
	}
	h := client.Calls[0].Req.Header
	if h.Get("x-api-key") != "test-key" {
		t.Errorf("x-api-key = %q", h.Get("x-api-key"))
	}
	if h.Get("anthropic-version") != defaultAnthropicVersion {
		t.Errorf("anthropic-version = %q", h.Get("anthropic-version"))
	}
	if h.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", h.Get("Content-Type"))
	}
}

func TestAnthropicUnauthenticatedWithoutKey(t *testing.T) {
	a := &Anthropic{
		Client:    &FakeHTTPClient{},
		EnvLookup: func(string) string { return "" },
	}
	if err := a.Available(context.Background()); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Available err = %v, want ErrUnauthenticated", err)
	}
	_, err := a.Compose(context.Background(), ComposeInput{
		Group:            Group{Type: "feat", Files: []string{"a.go"}},
		MaxSubjectLength: 72,
	})
	if !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Compose err = %v, want ErrUnauthenticated", err)
	}
}

func TestAnthropicAPIErrorStatusMapped(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{anthropicErrResponse(401, "bad key")},
	}
	a := newTestAnthropic(client)
	_, err := a.Compose(context.Background(), ComposeInput{
		Group:            Group{Type: "feat", Files: []string{"a.go"}},
		MaxSubjectLength: 72,
	})
	if !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("err = %v, want ErrUnauthenticated", err)
	}
}

func TestAnthropicRetriesThen200(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{
			anthropicErrResponse(429, "slow down"),
			anthropicOKResponse(anthropicResponse{
				Model:   "test-model",
				Content: []anthropicContentBlock{{Type: "text", Text: validComposeContent()}},
				Usage:   anthropicUsage{InputTokens: 1, OutputTokens: 1},
			}),
		},
	}
	a := newTestAnthropic(client)
	res, err := a.Compose(context.Background(), ComposeInput{
		Group:            Group{Type: "feat", Files: []string{"a.go"}},
		MaxSubjectLength: 72,
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if res.Subject != "add feature" {
		t.Errorf("Subject = %q", res.Subject)
	}
	if len(client.Calls) != 2 {
		t.Errorf("calls = %d, want 2 (retry)", len(client.Calls))
	}
}

func TestAnthropicEmptyContentIsError(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{anthropicOKResponse(anthropicResponse{
			Model:   "test-model",
			Content: []anthropicContentBlock{},
			Usage:   anthropicUsage{InputTokens: 1, OutputTokens: 0},
		})},
	}
	a := newTestAnthropic(client)
	_, err := a.Compose(context.Background(), ComposeInput{
		Group:            Group{Type: "feat", Files: []string{"a.go"}},
		MaxSubjectLength: 72,
	})
	if !errors.Is(err, ErrProviderResponse) {
		t.Errorf("err = %v, want ErrProviderResponse", err)
	}
}
