package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// ── helpers ──────────────────────────────────────────────────────────

// okResponse builds an *http.Response with status 200 and the given
// chatResponse JSON as body.
func okResponse(cr chatResponse) *http.Response {
	b, _ := json.Marshal(cr)
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(string(b))),
	}
}

// errResponse builds an *http.Response with the given status code and
// plain-text body.
func errResponse(code int, body string) *http.Response {
	h := http.Header{}
	return &http.Response{
		StatusCode: code,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// validClassifyContent returns a JSON string that parseClassifyResponse
// accepts.
func validClassifyContent() string {
	return `{"groups":[{"type":"feat","files":["a.go"],"rationale":"new"}]}`
}

// validComposeContent returns a JSON string that parseComposeResponse
// accepts.
func validComposeContent() string {
	return `{"subject":"add feature","body":"details"}`
}

// newTestNvidia returns a Nvidia configured for fast tests with the
// given FakeHTTPClient and an instant sleep function.
func newTestNvidia(client *FakeHTTPClient, apiKey string) *Nvidia {
	return &Nvidia{
		Client:    client,
		APIKey:    apiKey,
		Endpoint:  "https://test.example.com/v1/chat/completions",
		Model:     "test-model",
		Timeout:   5 * time.Second,
		MaxRetry:  3,
		EnvLookup: func(string) string { return "" },
		SleepFn:   func(_ context.Context, _ time.Duration) bool { return true },
	}
}

// ── Task 3.1: Unit tests ─────────────────────────────────────────────

func TestNvidiaName(t *testing.T) {
	n := NewNvidia()
	if n.Name() != "nvidia" {
		t.Errorf("Name() = %q, want %q", n.Name(), "nvidia")
	}
}

func TestNvidiaLocality(t *testing.T) {
	n := NewNvidia()
	if n.Locality() != LocalityRemote {
		t.Errorf("Locality() = %q, want %q", n.Locality(), LocalityRemote)
	}
}

func TestNvidiaAvailableWithKey(t *testing.T) {
	n := &Nvidia{EnvLookup: func(k string) string {
		if k == "NVIDIA_API_KEY" {
			return "nvapi-test-key"
		}
		return ""
	}}
	if err := n.Available(context.Background()); err != nil {
		t.Errorf("Available() with key: %v", err)
	}
}

func TestNvidiaAvailableWithoutKey(t *testing.T) {
	n := &Nvidia{EnvLookup: func(string) string { return "" }}
	err := n.Available(context.Background())
	if !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Available() without key: got %v, want ErrUnauthenticated", err)
	}
}

func TestNvidiaDefaultModel(t *testing.T) {
	n := NewNvidia()
	if n.Model != defaultNvidiaModel {
		t.Errorf("default Model = %q, want %q", n.Model, defaultNvidiaModel)
	}
}

func TestNvidiaDefaultEndpoint(t *testing.T) {
	n := NewNvidia()
	if n.Endpoint != defaultNvidiaEndpoint {
		t.Errorf("default Endpoint = %q, want %q", n.Endpoint, defaultNvidiaEndpoint)
	}
}

func TestNvidiaDefaultTimeout(t *testing.T) {
	n := NewNvidia()
	if n.Timeout != defaultNvidiaTimeout {
		t.Errorf("default Timeout = %v, want %v", n.Timeout, defaultNvidiaTimeout)
	}
}

func TestNvidiaDefaultMaxRetry(t *testing.T) {
	n := NewNvidia()
	if n.MaxRetry != defaultNvidiaMaxRetry {
		t.Errorf("default MaxRetry = %d, want %d", n.MaxRetry, defaultNvidiaMaxRetry)
	}
}

// ── Task 3.2: [PBT] Property 1 — Bearer token header ────────────────
// Feature: nvidia-ai-provider, Property 1: API key가 Bearer 토큰으로 전달됨
// **Validates: Requirements 2.4**

func TestProperty1_BearerToken(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a non-empty API key.
		apiKey := rapid.StringMatching(`[A-Za-z0-9_\-]{1,64}`).Draw(t, "apiKey")

		client := &FakeHTTPClient{
			Responses: []*http.Response{okResponse(chatResponse{
				Model:   "test-model",
				Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: validClassifyContent()}}},
				Usage:   &chatUsage{TotalTokens: 10},
			})},
		}
		nv := newTestNvidia(client, apiKey)

		_, err := nv.Classify(context.Background(), ClassifyInput{
			Files:        []FileChange{{Path: "a.go", Status: "added", DiffHint: "+pkg\n"}},
			AllowedTypes: []string{"feat"},
		})
		if err != nil {
			t.Fatalf("Classify: %v", err)
		}
		if len(client.Calls) == 0 {
			t.Fatal("no HTTP calls recorded")
		}
		got := client.Calls[0].Req.Header.Get("Authorization")
		want := "Bearer " + apiKey
		if got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
	})
}

// ── Task 3.3: [PBT] Property 2 — Request body structure ─────────────
// Feature: nvidia-ai-provider, Property 2: Request body에 system/user 메시지와 JSON format이 포함됨
// **Validates: Requirements 2.7, 2.10**

func TestProperty2_RequestBodyStructure(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate random classify input.
		nFiles := rapid.IntRange(1, 5).Draw(t, "nFiles")
		files := make([]FileChange, nFiles)
		for i := range files {
			files[i] = FileChange{
				Path:     rapid.StringMatching(`[a-z]{1,10}\.go`).Draw(t, fmt.Sprintf("path_%d", i)),
				Status:   rapid.SampledFrom([]string{"added", "modified", "deleted"}).Draw(t, fmt.Sprintf("status_%d", i)),
				DiffHint: "+line\n",
			}
		}
		lang := rapid.SampledFrom([]string{"en", "ko", "ja"}).Draw(t, "lang")

		client := &FakeHTTPClient{
			Responses: []*http.Response{okResponse(chatResponse{
				Model:   "test-model",
				Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: validClassifyContent()}}},
				Usage:   &chatUsage{TotalTokens: 5},
			})},
		}
		nv := newTestNvidia(client, "test-key")

		_, err := nv.Classify(context.Background(), ClassifyInput{
			Files:        files,
			AllowedTypes: []string{"feat", "fix"},
			Lang:         lang,
		})
		if err != nil {
			t.Fatalf("Classify: %v", err)
		}
		if len(client.Calls) == 0 {
			t.Fatal("no HTTP calls recorded")
		}

		// Read and parse the request body.
		body, _ := io.ReadAll(client.Calls[0].Req.Body)
		var req chatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}

		// Verify system and user messages exist.
		hasSystem, hasUser := false, false
		for _, m := range req.Messages {
			if m.Role == "system" {
				hasSystem = true
			}
			if m.Role == "user" {
				hasUser = true
			}
		}
		if !hasSystem {
			t.Error("request body missing system message")
		}
		if !hasUser {
			t.Error("request body missing user message")
		}

		// Verify response_format.
		if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_object" {
			t.Errorf("response_format = %+v, want {type: json_object}", req.ResponseFormat)
		}
	})
}

// ── Task 3.4: [PBT] Property 3 — Response field extraction ──────────
// Feature: nvidia-ai-provider, Property 3: Response 필드 추출 정확성
// **Validates: Requirements 3.1, 3.6, 3.7**

func TestProperty3_ResponseFieldExtraction(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate random valid response fields.
		model := rapid.StringMatching(`[a-z0-9\-/]{3,30}`).Draw(t, "model")
		totalTokens := rapid.IntRange(1, 100000).Draw(t, "totalTokens")

		// Use a fixed valid classify content — the property is about
		// field extraction from the chatResponse envelope, not about
		// the inner JSON parsing.
		content := validClassifyContent()

		client := &FakeHTTPClient{
			Responses: []*http.Response{okResponse(chatResponse{
				Model: model,
				Choices: []chatChoice{{
					Message: chatMessage{Role: "assistant", Content: content},
				}},
				Usage: &chatUsage{TotalTokens: totalTokens},
			})},
		}
		nv := newTestNvidia(client, "test-key")

		res, err := nv.Classify(context.Background(), ClassifyInput{
			Files:        []FileChange{{Path: "a.go", Status: "added", DiffHint: "+pkg\n"}},
			AllowedTypes: []string{"feat"},
		})
		if err != nil {
			t.Fatalf("Classify: %v", err)
		}
		if res.Model != model {
			t.Errorf("Model = %q, want %q", res.Model, model)
		}
		if res.TokensUsed != totalTokens {
			t.Errorf("TokensUsed = %d, want %d", res.TokensUsed, totalTokens)
		}
	})
}

// ── Task 3.5: [PBT] Property 4 — Non-2xx status code error ──────────
// Feature: nvidia-ai-provider, Property 4: Non-2xx 상태 코드 에러 전파
// **Validates: Requirements 3.4**

func TestProperty4_NonSuccessStatusCode(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Generate a non-2xx, non-429, non-5xx status code to avoid retry logic.
		code := rapid.SampledFrom([]int{
			400, 401, 403, 404, 405, 408, 409, 410, 413, 415, 422,
		}).Draw(t, "statusCode")

		client := &FakeHTTPClient{
			Responses: []*http.Response{errResponse(code, "error body")},
		}
		nv := newTestNvidia(client, "test-key")

		_, err := nv.Classify(context.Background(), ClassifyInput{
			Files:        []FileChange{{Path: "a.go", Status: "added", DiffHint: "+pkg\n"}},
			AllowedTypes: []string{"feat"},
		})
		if err == nil {
			t.Fatalf("expected error for HTTP %d, got nil", code)
		}
		if !strings.Contains(err.Error(), strconv.Itoa(code)) {
			t.Errorf("error %q does not contain status code %d", err.Error(), code)
		}
	})
}

// ── Task 3.6: [PBT] Property 9 — 429 Retry-After ────────────────────
// Feature: nvidia-ai-provider, Property 9: 429 Retry-After 준수
// **Validates: Requirements 11.1**

// TimingHTTPClient wraps FakeHTTPClient and records the requested sleep
// durations via the SleepFn hook.
type TimingHTTPClient struct {
	FakeHTTPClient
	mu         sync.Mutex
	SleepCalls []time.Duration
}

func TestProperty9_RetryAfter429(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		retryAfter := rapid.IntRange(1, 10).Draw(t, "retryAfterSecs")

		resp429 := errResponse(429, "rate limited")
		resp429.Header.Set("Retry-After", strconv.Itoa(retryAfter))

		resp200 := okResponse(chatResponse{
			Model:   "test-model",
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: validClassifyContent()}}},
			Usage:   &chatUsage{TotalTokens: 1},
		})

		client := &FakeHTTPClient{
			Responses: []*http.Response{resp429, resp200},
		}

		var sleepCalls []time.Duration
		nv := &Nvidia{
			Client:    client,
			APIKey:    "test-key",
			Endpoint:  "https://test.example.com/v1/chat/completions",
			Model:     "test-model",
			Timeout:   60 * time.Second,
			MaxRetry:  3,
			EnvLookup: func(string) string { return "" },
			SleepFn: func(_ context.Context, d time.Duration) bool {
				sleepCalls = append(sleepCalls, d)
				return true
			},
		}

		_, err := nv.Classify(context.Background(), ClassifyInput{
			Files:        []FileChange{{Path: "a.go", Status: "added", DiffHint: "+pkg\n"}},
			AllowedTypes: []string{"feat"},
		})
		if err != nil {
			t.Fatalf("Classify: %v", err)
		}

		// Provider must have made 2 HTTP calls (429 then 200).
		if len(client.Calls) != 2 {
			t.Fatalf("HTTP calls = %d, want 2", len(client.Calls))
		}

		// The sleep duration must match the Retry-After value.
		if len(sleepCalls) == 0 {
			t.Fatal("no sleep calls recorded")
		}
		wantDur := time.Duration(retryAfter) * time.Second
		if sleepCalls[0] != wantDur {
			t.Errorf("sleep duration = %v, want %v", sleepCalls[0], wantDur)
		}
	})
}

// ── Task 3.7: [PBT] Property 10 — 5xx Exponential Backoff ───────────
// Feature: nvidia-ai-provider, Property 10: 5xx Exponential Backoff
// **Validates: Requirements 11.2**

func TestProperty10_ExponentialBackoff5xx(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		statusCode := rapid.IntRange(500, 599).Draw(t, "statusCode")

		// Build responses: 3 failures (5xx) then 1 success.
		// With MaxRetry=3, attempts are 0,1,2,3.
		// Attempts 0,1,2 get 5xx and sleep; attempt 3 gets 200.
		responses := make([]*http.Response, 4)
		for i := 0; i < 3; i++ {
			responses[i] = errResponse(statusCode, "server error")
		}
		responses[3] = okResponse(chatResponse{
			Model:   "test-model",
			Choices: []chatChoice{{Message: chatMessage{Role: "assistant", Content: validClassifyContent()}}},
			Usage:   &chatUsage{TotalTokens: 1},
		})

		client := &FakeHTTPClient{Responses: responses}

		var sleepCalls []time.Duration
		nv := &Nvidia{
			Client:    client,
			APIKey:    "test-key",
			Endpoint:  "https://test.example.com/v1/chat/completions",
			Model:     "test-model",
			Timeout:   60 * time.Second,
			MaxRetry:  3,
			EnvLookup: func(string) string { return "" },
			SleepFn: func(_ context.Context, d time.Duration) bool {
				sleepCalls = append(sleepCalls, d)
				return true
			},
		}

		_, err := nv.Classify(context.Background(), ClassifyInput{
			Files:        []FileChange{{Path: "a.go", Status: "added", DiffHint: "+pkg\n"}},
			AllowedTypes: []string{"feat"},
		})
		if err != nil {
			t.Fatalf("Classify: %v", err)
		}

		// 4 HTTP calls: 3 failures + 1 success.
		if len(client.Calls) != 4 {
			t.Fatalf("HTTP calls = %d, want 4", len(client.Calls))
		}

		// Verify exponential backoff pattern: 1s, 2s, 4s.
		if len(sleepCalls) != 3 {
			t.Fatalf("sleep calls = %d, want 3", len(sleepCalls))
		}
		expected := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second}
		for i, want := range expected {
			if sleepCalls[i] != want {
				t.Errorf("sleep[%d] = %v, want %v", i, sleepCalls[i], want)
			}
		}
	})
}

// ── Task 3.8: [PBT] Property 11 — Retry total time ≤ timeout ────────
// Feature: nvidia-ai-provider, Property 11: Retry 총 시간이 Timeout을 초과하지 않음
// **Validates: Requirements 11.3, 11.4**

func TestProperty11_RetryTotalTimeWithinTimeout(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		timeoutMs := rapid.IntRange(50, 500).Draw(t, "timeoutMs")
		maxRetry := rapid.IntRange(1, 4).Draw(t, "maxRetry")
		timeout := time.Duration(timeoutMs) * time.Millisecond

		// All responses are 5xx — the provider will exhaust retries or
		// hit the timeout, whichever comes first.
		responses := make([]*http.Response, maxRetry+1)
		for i := range responses {
			responses[i] = errResponse(500, "server error")
		}
		client := &FakeHTTPClient{Responses: responses}

		nv := &Nvidia{
			Client:    client,
			APIKey:    "test-key",
			Endpoint:  "https://test.example.com/v1/chat/completions",
			Model:     "test-model",
			Timeout:   timeout,
			MaxRetry:  maxRetry,
			EnvLookup: func(string) string { return "" },
			// Use real sleepCtx so wall-clock time is bounded by context.
		}

		start := time.Now()
		_, err := nv.Classify(context.Background(), ClassifyInput{
			Files:        []FileChange{{Path: "a.go", Status: "added", DiffHint: "+pkg\n"}},
			AllowedTypes: []string{"feat"},
		})
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("expected error from all-5xx responses")
		}

		// Total elapsed time must not exceed timeout + a generous buffer
		// for scheduling jitter.
		limit := timeout + 200*time.Millisecond
		if elapsed > limit {
			t.Errorf("elapsed %v exceeds timeout %v (limit %v)", elapsed, timeout, limit)
		}
	})
}

// ── Task 3.9: Edge case tests ────────────────────────────────────────

func TestNvidiaEmptyChoices(t *testing.T) {
	client := &FakeHTTPClient{
		Responses: []*http.Response{okResponse(chatResponse{
			Model:   "test-model",
			Choices: []chatChoice{},
			Usage:   &chatUsage{TotalTokens: 5},
		})},
	}
	nv := newTestNvidia(client, "test-key")

	_, err := nv.Classify(context.Background(), ClassifyInput{
		Files:        []FileChange{{Path: "a.go", Status: "added", DiffHint: "+pkg\n"}},
		AllowedTypes: []string{"feat"},
	})
	if !errors.Is(err, ErrProviderResponse) {
		t.Errorf("empty choices: got %v, want ErrProviderResponse", err)
	}
}

func TestNvidiaInvalidJSONResponse(t *testing.T) {
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{not valid json`)),
	}
	client := &FakeHTTPClient{Responses: []*http.Response{resp}}
	nv := newTestNvidia(client, "test-key")

	_, err := nv.Classify(context.Background(), ClassifyInput{
		Files:        []FileChange{{Path: "a.go", Status: "added", DiffHint: "+pkg\n"}},
		AllowedTypes: []string{"feat"},
	})
	if !errors.Is(err, ErrProviderResponse) {
		t.Errorf("invalid JSON: got %v, want ErrProviderResponse", err)
	}
}

func TestNvidiaEmptyResponseBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
	}
	client := &FakeHTTPClient{Responses: []*http.Response{resp}}
	nv := newTestNvidia(client, "test-key")

	_, err := nv.Classify(context.Background(), ClassifyInput{
		Files:        []FileChange{{Path: "a.go", Status: "added", DiffHint: "+pkg\n"}},
		AllowedTypes: []string{"feat"},
	})
	if !errors.Is(err, ErrProviderResponse) {
		t.Errorf("empty body: got %v, want ErrProviderResponse", err)
	}
}
