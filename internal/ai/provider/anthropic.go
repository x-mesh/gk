package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultAnthropicEndpoint = "https://api.anthropic.com/v1/messages"
	defaultAnthropicModel    = "claude-sonnet-4-5-20250929"
	defaultAnthropicVersion  = "2023-06-01"
	defaultAnthropicMaxTok   = 4096
	defaultAnthropicTimeout  = 60 * time.Second
	defaultAnthropicMaxRetry = 3
)

// Anthropic adapts the Claude Messages API. Unlike the OpenAI-compatible
// providers (Nvidia / Groq / OpenAI) Claude has its own request shape —
// `system` is a top-level field and responses come back as a `content`
// array of typed blocks — so this adapter speaks HTTP directly instead
// of delegating to Nvidia's invoke.
type Anthropic struct {
	Client    HTTPClient
	Endpoint  string
	Model     string
	Version   string
	APIKey    string
	MaxTokens int
	Timeout   time.Duration
	MaxRetry  int
	EnvLookup func(string) string
	SleepFn   func(ctx context.Context, d time.Duration) bool
}

// NewAnthropic returns a Claude adapter with sensible defaults.
func NewAnthropic() *Anthropic {
	return &Anthropic{
		Client:    NewDefaultHTTPClient(defaultAnthropicTimeout),
		Endpoint:  defaultAnthropicEndpoint,
		Model:     defaultAnthropicModel,
		Version:   defaultAnthropicVersion,
		MaxTokens: defaultAnthropicMaxTok,
		Timeout:   defaultAnthropicTimeout,
		MaxRetry:  defaultAnthropicMaxRetry,
		EnvLookup: os.Getenv,
	}
}

func (a *Anthropic) Name() string       { return "anthropic" }
func (a *Anthropic) Locality() Locality { return LocalityRemote }

func (a *Anthropic) Available(_ context.Context) error {
	if a.apiKey() == "" {
		return fmt.Errorf("%w: ANTHROPIC_API_KEY not set", ErrUnauthenticated)
	}
	return nil
}

func (a *Anthropic) Classify(ctx context.Context, in ClassifyInput) (ClassifyResult, error) {
	userPrompt := buildClassifyUserPrompt(in, string(concatFileDiffs(in.Files)))
	content, model, tokens, err := a.invoke(ctx, systemPrompt, userPrompt, classifyMaxTokens(len(in.Files)))
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

func (a *Anthropic) Compose(ctx context.Context, in ComposeInput) (ComposeResult, error) {
	userPrompt := buildComposeUserPrompt(in)
	content, model, tokens, err := a.invoke(ctx, systemPrompt, userPrompt, 0)
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

func (a *Anthropic) Summarize(ctx context.Context, in SummarizeInput) (SummarizeResult, error) {
	userPrompt := buildSummarizeUserPrompt(in)
	content, model, tokens, err := a.invoke(ctx, summarizeSystem(in), userPrompt, in.MaxTokens)
	if err != nil {
		return SummarizeResult{}, err
	}
	return SummarizeResult{Text: content, Model: model, TokensUsed: tokens, Provider: a.Name()}, nil
}

func (a *Anthropic) SuggestGitignore(ctx context.Context, projectInfo string) ([]string, error) {
	userPrompt := gitignoreUserPromptPrefix + projectInfo
	content, _, _, err := a.invoke(ctx, gitignoreSystemPrompt, userPrompt, 0)
	if err != nil {
		return nil, err
	}
	return parseGitignoreLines(content), nil
}

// ── HTTP plumbing ─────────────────────────────────────────────────────

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicCacheControl marks a content block as a prompt-cache checkpoint.
// The only supported type today is "ephemeral" (~5 min TTL).
type anthropicCacheControl struct {
	Type string `json:"type"`
}

// anthropicSystemBlock is one structured `system` content block. We send
// the system prompt as a single-element array (rather than a plain string)
// so we can attach cache_control and have Anthropic cache the large,
// stable prefix. gk commit calls Compose N times per group with an
// identical system prompt, so caching the prefix turns repeated full-price
// input tokens into cheap cache reads.
type anthropicSystemBlock struct {
	Type         string                 `json:"type"`
	Text         string                 `json:"text"`
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicRequest struct {
	Model     string                 `json:"model"`
	MaxTokens int                    `json:"max_tokens"`
	System    []anthropicSystemBlock `json:"system,omitempty"`
	Messages  []anthropicMessage     `json:"messages"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// tool_use blocks (present only when the request carried tools).
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// Cache accounting. Anthropic returns these only when prompt caching
	// is in play: cache_creation counts tokens written to the cache on a
	// miss, cache_read counts tokens served from the cache on a hit.
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

type anthropicResponse struct {
	Model      string                  `json:"model"`
	Content    []anthropicContentBlock `json:"content"`
	Usage      anthropicUsage          `json:"usage"`
	Type       string                  `json:"type"`
	StopReason string                  `json:"stop_reason"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// invoke sends a Messages API request and returns the joined text from
// every content block plus model/token bookkeeping. Retries on
// 429/5xx using the same backoff strategy as Nvidia.invoke.
func (a *Anthropic) invoke(ctx context.Context, sys, user string, maxTokens int) (content, model string, tokensUsed int, err error) {
	if HTTPHook != nil {
		mdl := a.modelOrDefault()
		start := time.Now()
		defer func() { HTTPHook(a.Name(), mdl, time.Since(start), err) }()
	}
	if a.apiKey() == "" {
		return "", "", 0, fmt.Errorf("%w: ANTHROPIC_API_KEY not set", ErrUnauthenticated)
	}
	body, mErr := json.Marshal(anthropicRequest{
		Model:     a.modelOrDefault(),
		MaxTokens: a.resolveMaxTokens(maxTokens),
		System:    systemBlocks(sys),
		Messages:  []anthropicMessage{{Role: "user", Content: user}},
	})
	if mErr != nil {
		return "", "", 0, fmt.Errorf("anthropic: marshal request: %w", mErr)
	}
	parsed, err := a.post(ctx, body)
	if err != nil {
		return "", "", 0, err
	}
	var sb strings.Builder
	for _, b := range parsed.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "", "", 0, fmt.Errorf("%w: empty content", ErrProviderResponse)
	}
	return text, parsed.Model, parsed.Usage.total(), nil
}

// post sends one Messages API request body and returns the parsed
// response, retrying 429/5xx with exponential backoff. Shared by invoke
// (single-user-message capabilities) and ChatWithTools (multi-turn chat) so
// both speak identical HTTP: same headers, retry policy, and error
// classification.
func (a *Anthropic) post(ctx context.Context, body []byte) (anthropicResponse, error) {
	endpoint := a.endpoint()
	maxRetry := a.maxRetry()
	for attempt := 0; attempt <= maxRetry; attempt++ {
		req, rErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if rErr != nil {
			return anthropicResponse{}, fmt.Errorf("anthropic: build request: %w", rErr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", a.apiKey())
		req.Header.Set("anthropic-version", a.version())

		resp, doErr := a.Client.Do(ctx, req)
		if doErr != nil {
			if attempt < maxRetry && a.sleep(ctx, time.Duration(1<<uint(attempt))*time.Second) {
				continue
			}
			return anthropicResponse{}, fmt.Errorf("anthropic: http: %w", doErr)
		}
		body2, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		// Retry on transient failures.
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			if attempt < maxRetry && a.sleep(ctx, time.Duration(1<<uint(attempt))*time.Second) {
				continue
			}
			return anthropicResponse{}, fmt.Errorf("%w: anthropic %d: %s", ErrProviderResponse, resp.StatusCode, truncateBody(body2))
		}
		if resp.StatusCode >= 400 {
			return anthropicResponse{}, fmt.Errorf("%w: anthropic %d: %s", a.classifyStatus(resp.StatusCode), resp.StatusCode, truncateBody(body2))
		}

		var parsed anthropicResponse
		if pErr := json.Unmarshal(body2, &parsed); pErr != nil {
			return anthropicResponse{}, fmt.Errorf("%w: decode: %v: %s", ErrProviderResponse, pErr, truncateBody(body2))
		}
		if parsed.Error != nil {
			return anthropicResponse{}, fmt.Errorf("%w: %s: %s", ErrProviderResponse, parsed.Error.Type, parsed.Error.Message)
		}
		return parsed, nil
	}
	return anthropicResponse{}, errors.New("anthropic: exhausted retries")
}

// ── Tool-calling chat ─────────────────────────────────────────────────

// anthropicToolDef is one entry in the request `tools` array.
type anthropicToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// anthropicChatBlock is one typed content block in a chat message. Which
// fields apply depends on Type: "text" uses Text, "tool_use" uses
// ID/Name/Input, "tool_result" uses ToolUseID/Content/IsError.
type anthropicChatBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// anthropicChatMessage carries structured content blocks — unlike
// anthropicMessage (plain string content) it can hold tool_use and
// tool_result blocks. The two request shapes coexist so the text-only
// capabilities (Classify/Compose/Summarize) keep their proven wire format.
type anthropicChatMessage struct {
	Role    string               `json:"role"`
	Content []anthropicChatBlock `json:"content"`
}

type anthropicChatRequest struct {
	Model     string                 `json:"model"`
	MaxTokens int                    `json:"max_tokens"`
	System    []anthropicSystemBlock `json:"system,omitempty"`
	Messages  []anthropicChatMessage `json:"messages"`
	Tools     []anthropicToolDef     `json:"tools,omitempty"`
}

// ChatWithTools implements ToolCaller: one Messages API round-trip with
// the full conversation and tool definitions. The caller owns the agentic
// loop; this only translates shapes and sends.
func (a *Anthropic) ChatWithTools(ctx context.Context, in ChatInput) (res ChatResult, err error) {
	if HTTPHook != nil {
		mdl := a.modelOrDefault()
		start := time.Now()
		defer func() { HTTPHook(a.Name(), mdl, time.Since(start), err) }()
	}
	if a.apiKey() == "" {
		return ChatResult{}, fmt.Errorf("%w: ANTHROPIC_API_KEY not set", ErrUnauthenticated)
	}
	msgs, cErr := anthropicChatMessages(in.Messages)
	if cErr != nil {
		return ChatResult{}, cErr
	}
	tools := make([]anthropicToolDef, 0, len(in.Tools))
	for _, t := range in.Tools {
		tools = append(tools, anthropicToolDef(t))
	}
	body, mErr := json.Marshal(anthropicChatRequest{
		Model:     a.modelOrDefault(),
		MaxTokens: a.resolveMaxTokens(in.MaxTokens),
		System:    systemBlocks(in.System),
		Messages:  msgs,
		Tools:     tools,
	})
	if mErr != nil {
		return ChatResult{}, fmt.Errorf("anthropic: marshal chat request: %w", mErr)
	}
	parsed, pErr := a.post(ctx, body)
	if pErr != nil {
		return ChatResult{}, pErr
	}
	out := ChatResult{
		Model:      parsed.Model,
		TokensUsed: parsed.Usage.total(),
		StopReason: normalizeAnthropicStop(parsed.StopReason),
	}
	var sb strings.Builder
	for _, b := range parsed.Content {
		switch b.Type {
		case "text":
			sb.WriteString(b.Text)
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Input: b.Input})
		}
	}
	out.Text = strings.TrimSpace(sb.String())
	if out.Text == "" && len(out.ToolCalls) == 0 {
		return ChatResult{}, fmt.Errorf("%w: empty content", ErrProviderResponse)
	}
	return out, nil
}

// anthropicChatMessages converts the vendor-neutral history into Claude's
// wire shape. Claude has no "tool" role: results travel in a user message
// as tool_result blocks, and every result answering one assistant turn
// must share a SINGLE user message — consecutive tool messages are
// coalesced, or the API rejects the request.
func anthropicChatMessages(history []ChatMessage) ([]anthropicChatMessage, error) {
	out := make([]anthropicChatMessage, 0, len(history))
	for _, m := range history {
		switch m.Role {
		case "user":
			out = append(out, anthropicChatMessage{
				Role:    "user",
				Content: []anthropicChatBlock{{Type: "text", Text: m.Text}},
			})
		case "assistant":
			blocks := make([]anthropicChatBlock, 0, 1+len(m.ToolCalls))
			if m.Text != "" {
				blocks = append(blocks, anthropicChatBlock{Type: "text", Text: m.Text})
			}
			for _, c := range m.ToolCalls {
				input := c.Input
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, anthropicChatBlock{Type: "tool_use", ID: c.ID, Name: c.Name, Input: input})
			}
			if len(blocks) == 0 {
				return nil, fmt.Errorf("anthropic: assistant message with no content")
			}
			out = append(out, anthropicChatMessage{Role: "assistant", Content: blocks})
		case "tool":
			if m.ToolResult == nil {
				return nil, fmt.Errorf("anthropic: tool message missing result")
			}
			block := anthropicChatBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolResult.ToolCallID,
				Content:   m.ToolResult.Content,
				IsError:   m.ToolResult.IsError,
			}
			if n := len(out); n > 0 && out[n-1].Role == "user" &&
				len(out[n-1].Content) > 0 && out[n-1].Content[0].Type == "tool_result" {
				out[n-1].Content = append(out[n-1].Content, block)
			} else {
				out = append(out, anthropicChatMessage{Role: "user", Content: []anthropicChatBlock{block}})
			}
		default:
			return nil, fmt.Errorf("anthropic: unknown chat role %q", m.Role)
		}
	}
	return out, nil
}

// normalizeAnthropicStop maps Claude stop reasons onto the vendor-neutral
// set. stop_sequence is a normal completion from the caller's perspective.
func normalizeAnthropicStop(s string) string {
	switch s {
	case "stop_sequence":
		return "end_turn"
	default:
		return s
	}
}

// systemBlocks turns a plain system prompt into the structured `system`
// array Anthropic expects, attaching an ephemeral cache_control marker so
// the (large, stable) prompt prefix is cached across calls. cache_control
// is GA — no `anthropic-beta` header is required; the marker alone enables
// caching. An empty prompt yields nil so the `omitempty` field is dropped
// from the request entirely, preserving the no-system behaviour.
func systemBlocks(sys string) []anthropicSystemBlock {
	if sys == "" {
		return nil
	}
	return []anthropicSystemBlock{{
		Type:         "text",
		Text:         sys,
		CacheControl: &anthropicCacheControl{Type: "ephemeral"},
	}}
}

// total returns the full input+output token count, folding in cache
// creation/read tokens. Anthropic reports cached input separately from
// input_tokens, so summing them keeps the bookkeeping consistent with the
// pre-caching behaviour (where all input was counted in input_tokens).
func (u anthropicUsage) total() int {
	return u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

func (a *Anthropic) classifyStatus(code int) error {
	switch code {
	case 401, 403:
		return ErrUnauthenticated
	default:
		return ErrProviderResponse
	}
}

// ── helpers ───────────────────────────────────────────────────────────

func (a *Anthropic) endpoint() string {
	if a.Endpoint != "" {
		return a.Endpoint
	}
	return defaultAnthropicEndpoint
}

func (a *Anthropic) modelOrDefault() string {
	if a.Model != "" {
		return a.Model
	}
	return defaultAnthropicModel
}

func (a *Anthropic) version() string {
	if a.Version != "" {
		return a.Version
	}
	return defaultAnthropicVersion
}

func (a *Anthropic) maxTokens() int {
	if a.MaxTokens > 0 {
		return a.MaxTokens
	}
	return defaultAnthropicMaxTok
}

// resolveMaxTokens honours a per-call cap (SummarizeInput.MaxTokens) when
// positive, else falls back to the adapter's configured/default cap.
func (a *Anthropic) resolveMaxTokens(n int) int {
	if n > 0 {
		return n
	}
	return a.maxTokens()
}

func (a *Anthropic) maxRetry() int {
	if a.MaxRetry > 0 {
		return a.MaxRetry
	}
	return defaultAnthropicMaxRetry
}

func (a *Anthropic) apiKey() string {
	if a.APIKey != "" {
		return a.APIKey
	}
	lookup := a.EnvLookup
	if lookup == nil {
		lookup = os.Getenv
	}
	return lookup("ANTHROPIC_API_KEY")
}

func (a *Anthropic) sleep(ctx context.Context, d time.Duration) bool {
	if a.SleepFn != nil {
		return a.SleepFn(ctx, d)
	}
	return sleepCtx(ctx, d)
}

var (
	_ Provider           = (*Anthropic)(nil)
	_ Summarizer         = (*Anthropic)(nil)
	_ GitignoreSuggester = (*Anthropic)(nil)
	_ ToolCaller         = (*Anthropic)(nil)
)
