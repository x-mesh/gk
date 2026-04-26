package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultNvidiaEndpoint = "https://integrate.api.nvidia.com/v1/chat/completions"
	defaultNvidiaModel    = "meta/llama-3.1-8b-instruct"
	defaultNvidiaTimeout  = 60 * time.Second
	defaultNvidiaMaxRetry = 3
)

// Nvidia adapts the NVIDIA Chat Completions API (OpenAI-compatible).
//
// Unlike gemini/qwen/kiro which shell out to a CLI binary via
// CommandRunner, Nvidia talks HTTP directly through HTTPClient.
// This makes it usable without installing any external binary —
// only an API key is required.
type Nvidia struct {
	Client    HTTPClient
	Endpoint  string            // default: defaultNvidiaEndpoint
	Model     string            // default: defaultNvidiaModel
	APIKey    string            // from NVIDIA_API_KEY env
	Timeout   time.Duration     // default: 60s
	MaxRetry  int               // default: 3
	EnvLookup func(string) string
	// SleepFn overrides the default sleepCtx for testing. When nil,
	// sleepCtx is used. Returns true if the full duration elapsed.
	SleepFn func(ctx context.Context, d time.Duration) bool
}

// NewNvidia returns a Nvidia adapter with sensible defaults.
func NewNvidia() *Nvidia {
	return &Nvidia{
		Client:    NewDefaultHTTPClient(defaultNvidiaTimeout),
		Endpoint:  defaultNvidiaEndpoint,
		Model:     defaultNvidiaModel,
		Timeout:   defaultNvidiaTimeout,
		MaxRetry:  defaultNvidiaMaxRetry,
		EnvLookup: os.Getenv,
	}
}

// Name implements Provider.
func (n *Nvidia) Name() string { return "nvidia" }

// Locality implements Provider. NVIDIA uploads prompts to NVIDIA's API.
func (n *Nvidia) Locality() Locality { return LocalityRemote }

// Available verifies the NVIDIA_API_KEY environment variable is set.
func (n *Nvidia) Available(_ context.Context) error {
	lookup := n.EnvLookup
	if lookup == nil {
		lookup = os.Getenv
	}
	key := lookup("NVIDIA_API_KEY")
	if key == "" {
		return fmt.Errorf("%w: NVIDIA_API_KEY not set", ErrUnauthenticated)
	}
	return nil
}

// Classify implements Provider.
func (n *Nvidia) Classify(ctx context.Context, in ClassifyInput) (ClassifyResult, error) {
	userPrompt := buildClassifyUserPrompt(in, string(concatFileDiffs(in.Files)))
	content, model, tokens, err := n.invoke(ctx, systemPrompt, userPrompt, true)
	if err != nil {
		return ClassifyResult{}, err
	}
	res, err := parseClassifyResponse([]byte(content))
	if err != nil {
		return ClassifyResult{}, err
	}
	res.Model = model
	res.TokensUsed = tokens
	return res, nil
}

// Compose implements Provider.
func (n *Nvidia) Compose(ctx context.Context, in ComposeInput) (ComposeResult, error) {
	userPrompt := buildComposeUserPrompt(in)
	content, model, tokens, err := n.invoke(ctx, systemPrompt, userPrompt, true)
	if err != nil {
		return ComposeResult{}, err
	}
	res, err := parseComposeResponse([]byte(content))
	if err != nil {
		return ComposeResult{}, err
	}
	res.Model = model
	res.TokensUsed = tokens
	return res, nil
}

// Summarize implements Summarizer. Unlike Classify/Compose the output
// is free-form text, so we disable json_object response_format.
func (n *Nvidia) Summarize(ctx context.Context, in SummarizeInput) (SummarizeResult, error) {
	userPrompt := buildSummarizeUserPrompt(in)
	content, model, tokens, err := n.invoke(ctx, summarizeSystemPrompt, userPrompt, false)
	if err != nil {
		return SummarizeResult{}, err
	}
	return SummarizeResult{Text: content, Model: model, TokensUsed: tokens}, nil
}

// SuggestGitignore implements GitignoreSuggester.
func (n *Nvidia) SuggestGitignore(ctx context.Context, projectInfo string) ([]string, error) {
	userPrompt := gitignoreUserPromptPrefix + projectInfo
	content, _, _, err := n.invoke(ctx, gitignoreSystemPrompt, userPrompt, false)
	if err != nil {
		return nil, err
	}
	return parseGitignoreLines(content), nil
}

// ── Chat Completions data models ─────────────────────────────────────

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	ResponseFormat *responseFormat  `json:"response_format,omitempty"`
	Temperature    float64         `json:"temperature,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   *chatUsage   `json:"usage,omitempty"`
}

type chatChoice struct {
	Index   int         `json:"index"`
	Message chatMessage `json:"message"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ── invoke: core HTTP call with retry ────────────────────────────────

// invoke sends a Chat Completions request and returns the extracted
// content, model identifier, and token count. Retry logic handles
// 429 (Retry-After) and 5xx (exponential backoff).
//
// jsonMode controls response_format: true sets {"type":"json_object"}
// (used by Classify/Compose), false omits it (used by Summarize
// which returns free-form text).
func (n *Nvidia) invoke(ctx context.Context, sysPrompt, userPrompt string, jsonMode bool) (content, model string, tokensUsed int, err error) {
	endpoint := n.endpoint()
	apiKey := n.apiKey()
	mdl := n.model()

	if apiKey == "" {
		return "", "", 0, fmt.Errorf("%w: NVIDIA_API_KEY not set", ErrUnauthenticated)
	}

	reqBody := chatRequest{
		Model: mdl,
		Messages: []chatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: userPrompt},
		},
	}
	if jsonMode {
		reqBody.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", "", 0, fmt.Errorf("nvidia: marshal request: %w", err)
	}

	deadline := time.Now().Add(n.timeout())
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	maxRetry := n.maxRetry()
	var lastErr error

	for attempt := 0; attempt <= maxRetry; attempt++ {
		// Check context before each attempt.
		if ctx.Err() != nil {
			if lastErr != nil {
				return "", "", 0, lastErr
			}
			return "", "", 0, fmt.Errorf("nvidia: %w", ctx.Err())
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return "", "", 0, fmt.Errorf("nvidia: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := n.Client.Do(ctx, req)
		if err != nil {
			lastErr = fmt.Errorf("nvidia: http call: %w", err)
			// Network error on first attempt → no retry for non-server errors.
			if attempt == maxRetry {
				return "", "", 0, lastErr
			}
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			if !n.sleep(ctx, backoff) {
				return "", "", 0, lastErr
			}
			continue
		}

		content, model, tokensUsed, lastErr = n.handleResponse(resp)
		if lastErr == nil {
			return content, model, tokensUsed, nil
		}

		// Decide whether to retry based on status code.
		statusCode := resp.StatusCode
		switch {
		case statusCode == http.StatusTooManyRequests:
			// 429: wait Retry-After seconds then retry.
			wait := parseRetryAfter(resp.Header.Get("Retry-After"))
			if !n.sleep(ctx, wait) {
				return "", "", 0, lastErr
			}
			continue

		case statusCode >= 500 && statusCode < 600:
			// 5xx: exponential backoff (1s, 2s, 4s).
			if attempt >= maxRetry {
				return "", "", 0, lastErr
			}
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			if !n.sleep(ctx, backoff) {
				return "", "", 0, lastErr
			}
			continue

		default:
			// 4xx (non-429): no retry.
			return "", "", 0, lastErr
		}
	}

	return "", "", 0, lastErr
}

// ── Response handling ────────────────────────────────────────────────

// handleResponse reads and parses the HTTP response body.
func (n *Nvidia) handleResponse(resp *http.Response) (content, model string, tokensUsed int, err error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", 0, fmt.Errorf("nvidia: read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", 0, fmt.Errorf("nvidia: HTTP %d: %s", resp.StatusCode, truncateBody(body))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", 0, fmt.Errorf("%w: invalid JSON: %s", ErrProviderResponse, truncateBody(body))
	}

	if len(parsed.Choices) == 0 {
		return "", "", 0, fmt.Errorf("%w: empty choices", ErrProviderResponse)
	}

	content = strings.TrimSpace(parsed.Choices[0].Message.Content)
	if content == "" {
		return "", "", 0, fmt.Errorf("%w: empty content", ErrProviderResponse)
	}

	model = parsed.Model
	if parsed.Usage != nil {
		tokensUsed = parsed.Usage.TotalTokens
	}
	return content, model, tokensUsed, nil
}

// ── Helpers ─────────────────────────────────────────────────────────

func (n *Nvidia) endpoint() string {
	if n.Endpoint == "" {
		return defaultNvidiaEndpoint
	}
	return n.Endpoint
}

func (n *Nvidia) model() string {
	if n.Model == "" {
		return defaultNvidiaModel
	}
	return n.Model
}

func (n *Nvidia) apiKey() string {
	if n.APIKey != "" {
		return n.APIKey
	}
	lookup := n.EnvLookup
	if lookup == nil {
		lookup = os.Getenv
	}
	return lookup("NVIDIA_API_KEY")
}

func (n *Nvidia) timeout() time.Duration {
	if n.Timeout <= 0 {
		return defaultNvidiaTimeout
	}
	return n.Timeout
}

func (n *Nvidia) maxRetry() int {
	if n.MaxRetry <= 0 {
		return defaultNvidiaMaxRetry
	}
	return n.MaxRetry
}

// sleep delegates to SleepFn when set, otherwise falls back to sleepCtx.
func (n *Nvidia) sleep(ctx context.Context, d time.Duration) bool {
	if n.SleepFn != nil {
		return n.SleepFn(ctx, d)
	}
	return sleepCtx(ctx, d)
}

// parseRetryAfter extracts the wait duration from a Retry-After header.
// Falls back to 1s if the header is missing or unparseable.
func parseRetryAfter(val string) time.Duration {
	if val == "" {
		return 1 * time.Second
	}
	secs, err := strconv.Atoi(strings.TrimSpace(val))
	if err != nil || secs <= 0 {
		return 1 * time.Second
	}
	return time.Duration(secs) * time.Second
}

// sleepCtx sleeps for d or until ctx is cancelled. Returns true if the
// full duration elapsed, false if the context was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// truncateBody returns at most 512 bytes of body for error messages.
func truncateBody(b []byte) string {
	const max = 512
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}

// Compile-time interface checks.
var (
	_ Provider   = (*Nvidia)(nil)
	_ Summarizer = (*Nvidia)(nil)
)
