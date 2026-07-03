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
	Endpoint  string        // default: defaultNvidiaEndpoint
	Model     string        // default: defaultNvidiaModel
	APIKey    string        // from NVIDIA_API_KEY env
	Timeout   time.Duration // default: 60s
	MaxRetry  int           // default: 3
	EnvLookup func(string) string
	// SleepFn overrides the default sleepCtx for testing. When nil,
	// sleepCtx is used. Returns true if the full duration elapsed.
	SleepFn func(ctx context.Context, d time.Duration) bool
	// Brand overrides the prefix used in error messages. Empty defaults
	// to "nvidia". Groq and OpenAI delegate HTTP invoke through Nvidia
	// for OpenAI-compatible Chat Completions; setting Brand="groq" /
	// "openai" keeps error messages truthful about which provider
	// actually returned the failure.
	Brand string
}

// brand returns the prefix to use in error messages.
func (n *Nvidia) brand() string {
	if n.Brand != "" {
		return n.Brand
	}
	return "nvidia"
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
	content, model, tokens, err := n.invoke(ctx, systemPrompt, userPrompt, true, classifyMaxTokens(len(in.Files)))
	if err != nil {
		return ClassifyResult{}, err
	}
	res, err := parseClassifyResponse([]byte(content), in.Files)
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
	content, model, tokens, err := n.invoke(ctx, systemPrompt, userPrompt, true, 0)
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
	content, model, tokens, err := n.invoke(ctx, summarizeSystem(in), userPrompt, false, in.MaxTokens)
	if err != nil {
		return SummarizeResult{}, err
	}
	return SummarizeResult{Text: content, Model: model, TokensUsed: tokens, Provider: n.Name()}, nil
}

// SuggestGitignore implements GitignoreSuggester.
func (n *Nvidia) SuggestGitignore(ctx context.Context, projectInfo string) ([]string, error) {
	userPrompt := gitignoreUserPromptPrefix + projectInfo
	content, _, _, err := n.invoke(ctx, gitignoreSystemPrompt, userPrompt, false, 0)
	if err != nil {
		return nil, err
	}
	return parseGitignoreLines(content), nil
}

// ── Chat Completions data models ─────────────────────────────────────

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	Temperature    float64         `json:"temperature,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Tools          []chatToolDef   `json:"tools,omitempty"`
}

// chatMessage covers every Chat Completions role. Content is omitempty so
// an assistant turn that is pure tool_calls doesn't emit "content":"" —
// some OpenAI-compatible servers reject that. The pre-existing
// system/user paths always carry non-empty content, so the tag change is
// invisible to them.
type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// chatToolDef is one request `tools` entry (type "function").
type chatToolDef struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// chatToolCall is a model-requested invocation. Arguments is a
// JSON-encoded STRING per the OpenAI spec, not a nested object.
type chatToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function chatToolCallFunction `json:"function"`
}

type chatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
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
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
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
func (n *Nvidia) invoke(ctx context.Context, sysPrompt, userPrompt string, jsonMode bool, maxTokens int) (content, model string, tokensUsed int, err error) {
	apiKey := n.apiKey()
	mdl := n.model()

	if HTTPHook != nil {
		start := time.Now()
		defer func() { HTTPHook(n.brand(), mdl, time.Since(start), err) }()
	}

	if apiKey == "" {
		return "", "", 0, fmt.Errorf("%w: NVIDIA_API_KEY not set", ErrUnauthenticated)
	}

	reqBody := chatRequest{
		Model: mdl,
		Messages: []chatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: userPrompt},
		},
		// max_tokens is omitempty: 0 leaves the provider default in place,
		// a positive per-call cap (SummarizeInput.MaxTokens) is honoured.
		MaxTokens: maxTokens,
	}
	if jsonMode {
		reqBody.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", "", 0, fmt.Errorf("%s: marshal request: %w", n.brand(), err)
	}

	parsed, err := n.send(ctx, bodyBytes)
	if err != nil {
		return "", "", 0, err
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

// send posts one marshaled Chat Completions body with the full
// retry/backoff policy (429 Retry-After, 5xx exponential, 4xx immediate)
// and returns the parsed response with at least one choice. Shared by
// invoke (text capabilities) and ChatWithTools so both speak identical
// HTTP.
func (n *Nvidia) send(ctx context.Context, bodyBytes []byte) (chatResponse, error) {
	endpoint := n.endpoint()
	apiKey := n.apiKey()

	deadline := time.Now().Add(n.timeout())
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	maxRetry := n.maxRetry()
	var lastErr error

	for attempt := 0; attempt <= maxRetry; attempt++ {
		// Check context before each attempt.
		if ctx.Err() != nil {
			if lastErr != nil {
				return chatResponse{}, lastErr
			}
			return chatResponse{}, fmt.Errorf("%s: %w", n.brand(), ctx.Err())
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return chatResponse{}, fmt.Errorf("%s: build request: %w", n.brand(), err)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := n.Client.Do(ctx, req)
		if err != nil {
			lastErr = fmt.Errorf("%s: http call: %w", n.brand(), err)
			// Network error on first attempt → no retry for non-server errors.
			if attempt == maxRetry {
				return chatResponse{}, lastErr
			}
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			if !n.sleep(ctx, backoff) {
				return chatResponse{}, lastErr
			}
			continue
		}

		var parsed chatResponse
		parsed, lastErr = n.parseResponse(resp)
		if lastErr == nil {
			return parsed, nil
		}

		// Decide whether to retry based on status code.
		statusCode := resp.StatusCode
		switch {
		case statusCode == http.StatusTooManyRequests:
			// 429: wait Retry-After seconds then retry.
			wait := parseRetryAfter(resp.Header.Get("Retry-After"))
			if !n.sleep(ctx, wait) {
				return chatResponse{}, lastErr
			}
			continue

		case statusCode >= 500 && statusCode < 600:
			// 5xx: exponential backoff (1s, 2s, 4s).
			if attempt >= maxRetry {
				return chatResponse{}, lastErr
			}
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			if !n.sleep(ctx, backoff) {
				return chatResponse{}, lastErr
			}
			continue

		default:
			// 4xx (non-429): no retry.
			return chatResponse{}, lastErr
		}
	}

	return chatResponse{}, lastErr
}

// ── Response handling ────────────────────────────────────────────────

// parseResponse reads the HTTP response into a chatResponse, enforcing
// status, JSON validity, and a non-empty choices array. Content-level
// checks stay with the callers: invoke requires non-empty text, chat
// accepts tool_calls without text.
func (n *Nvidia) parseResponse(resp *http.Response) (chatResponse, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return chatResponse{}, fmt.Errorf("%s: read body: %w", n.brand(), err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return chatResponse{}, fmt.Errorf("%s: HTTP %d: %s", n.brand(), resp.StatusCode, truncateBody(body))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return chatResponse{}, fmt.Errorf("%w: invalid JSON: %s", ErrProviderResponse, truncateBody(body))
	}

	if len(parsed.Choices) == 0 {
		return chatResponse{}, fmt.Errorf("%w: empty choices", ErrProviderResponse)
	}
	return parsed, nil
}

// ── Tool-calling chat ────────────────────────────────────────────────

// ChatWithTools implements ToolCaller for every OpenAI-compatible
// provider (nvidia, and openai/groq via delegation): one Chat Completions
// round-trip carrying the running conversation and tool definitions.
func (n *Nvidia) ChatWithTools(ctx context.Context, in ChatInput) (res ChatResult, err error) {
	mdl := n.model()
	if HTTPHook != nil {
		start := time.Now()
		defer func() { HTTPHook(n.brand(), mdl, time.Since(start), err) }()
	}
	if n.apiKey() == "" {
		return ChatResult{}, fmt.Errorf("%w: NVIDIA_API_KEY not set", ErrUnauthenticated)
	}
	msgs, cErr := openAIChatMessages(in.System, in.Messages)
	if cErr != nil {
		return ChatResult{}, cErr
	}
	tools := make([]chatToolDef, 0, len(in.Tools))
	for _, t := range in.Tools {
		params := t.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object"}`)
		}
		tools = append(tools, chatToolDef{
			Type:     "function",
			Function: chatToolFunction{Name: t.Name, Description: t.Description, Parameters: params},
		})
	}
	bodyBytes, mErr := json.Marshal(chatRequest{
		Model:     mdl,
		Messages:  msgs,
		MaxTokens: in.MaxTokens,
		Tools:     tools,
	})
	if mErr != nil {
		return ChatResult{}, fmt.Errorf("%s: marshal chat request: %w", n.brand(), mErr)
	}
	parsed, sErr := n.send(ctx, bodyBytes)
	if sErr != nil {
		return ChatResult{}, sErr
	}
	choice := parsed.Choices[0]
	out := ChatResult{
		Model:      parsed.Model,
		StopReason: normalizeOpenAIStop(choice.FinishReason),
		Text:       strings.TrimSpace(choice.Message.Content),
	}
	if parsed.Usage != nil {
		out.TokensUsed = parsed.Usage.TotalTokens
	}
	for _, tc := range choice.Message.ToolCalls {
		input := json.RawMessage(tc.Function.Arguments)
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Input: input})
	}
	if out.Text == "" && len(out.ToolCalls) == 0 {
		return ChatResult{}, fmt.Errorf("%w: empty content", ErrProviderResponse)
	}
	return out, nil
}

// openAIChatMessages converts the vendor-neutral history into Chat
// Completions shape. The system prompt leads as a system message; tool
// results use the dedicated "tool" role keyed by tool_call_id. OpenAI has
// no is_error flag — error text simply travels in content, which the
// engine already prefixes.
func openAIChatMessages(system string, history []ChatMessage) ([]chatMessage, error) {
	out := make([]chatMessage, 0, len(history)+1)
	if system != "" {
		out = append(out, chatMessage{Role: "system", Content: system})
	}
	for _, m := range history {
		switch m.Role {
		case "user":
			out = append(out, chatMessage{Role: "user", Content: m.Text})
		case "assistant":
			msg := chatMessage{Role: "assistant", Content: m.Text}
			for _, c := range m.ToolCalls {
				args := string(c.Input)
				if args == "" {
					args = "{}"
				}
				msg.ToolCalls = append(msg.ToolCalls, chatToolCall{
					ID:       c.ID,
					Type:     "function",
					Function: chatToolCallFunction{Name: c.Name, Arguments: args},
				})
			}
			if msg.Content == "" && len(msg.ToolCalls) == 0 {
				return nil, fmt.Errorf("chat: assistant message with no content")
			}
			out = append(out, msg)
		case "tool":
			if m.ToolResult == nil {
				return nil, fmt.Errorf("chat: tool message missing result")
			}
			content := m.ToolResult.Content
			if content == "" {
				// The tool role requires content; an empty result would be
				// dropped by omitempty and rejected by the API.
				content = "(empty result)"
			}
			out = append(out, chatMessage{Role: "tool", Content: content, ToolCallID: m.ToolResult.ToolCallID})
		default:
			return nil, fmt.Errorf("chat: unknown chat role %q", m.Role)
		}
	}
	return out, nil
}

// normalizeOpenAIStop maps Chat Completions finish reasons onto the
// vendor-neutral set shared with Anthropic.
func normalizeOpenAIStop(s string) string {
	switch s {
	case "tool_calls":
		return "tool_use"
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	default:
		return s
	}
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

// AnalyzeBranches implements BranchAnalyzer.
func (n *Nvidia) AnalyzeBranches(ctx context.Context, in BranchAnalysisInput) (BranchAnalysisResult, error) {
	userPrompt := buildBranchAnalysisUserPrompt(in)
	content, model, tokens, err := n.invoke(ctx, branchAnalysisSystemPrompt, userPrompt, true, 0)
	if err != nil {
		return BranchAnalysisResult{}, err
	}
	res, err := parseBranchAnalysisResponse([]byte(content))
	if err != nil {
		return BranchAnalysisResult{}, err
	}
	res.Model = model
	res.TokensUsed = tokens
	return res, nil
}

// ResolveConflicts implements ConflictResolver.
func (n *Nvidia) ResolveConflicts(ctx context.Context, in ConflictResolutionInput) (ConflictResolutionResult, error) {
	userPrompt := buildConflictResolutionUserPrompt(in)
	content, model, tokens, err := n.invoke(ctx, conflictResolutionSystemPrompt, userPrompt, true, 0)
	if err != nil {
		return ConflictResolutionResult{}, err
	}
	res, err := parseConflictResolutionResponse([]byte(content))
	if err != nil {
		return ConflictResolutionResult{}, err
	}
	res.Model = model
	res.TokensUsed = tokens
	return res, nil
}

// Compile-time interface checks.
var (
	_ Provider         = (*Nvidia)(nil)
	_ Summarizer       = (*Nvidia)(nil)
	_ BranchAnalyzer   = (*Nvidia)(nil)
	_ ConflictResolver = (*Nvidia)(nil)
	_ ToolCaller       = (*Nvidia)(nil)
)
