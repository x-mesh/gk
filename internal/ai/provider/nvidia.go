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
	// RetryBudget bounds send()'s WHOLE retry loop (every attempt plus
	// backoff), independent of Timeout, which bounds each INDIVIDUAL HTTP
	// attempt (both directly, via Client's http.Client.Timeout, and as
	// the loop's deadline when RetryBudget is unset). Zero — the default,
	// and every caller except gk chat leaves it that way — falls back to
	// Timeout, so Classify/Compose/Summarize/etc. keep exactly today's
	// "total retry time bounded by Timeout" behavior.
	//
	// gk chat is the one caller that sets this explicitly (to
	// ai.chat.round_timeout, chat.go's resolveChatProviderChain): a proxy
	// that occasionally 500s needs room for ~3 attempts + backoff, and
	// inflating Timeout itself to fit that (this field's predecessor)
	// meant a single SLOW attempt could consume the entire round budget
	// by itself (Timeout, via Client's http.Client.Timeout, no longer
	// bounded just one attempt — it bounded the round). Keeping Timeout
	// at its own small, independent default while RetryBudget alone
	// grows to match the round means one hung attempt gets cut off with
	// room left for the rest — see send()'s docstring.
	RetryBudget time.Duration
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
	var lastErr error
	for attempt := 0; attempt <= maxContentRetry; attempt++ {
		content, model, tokens, err := n.invoke(ctx, systemPrompt, userPrompt, true, classifyMaxTokens(len(in.Files)))
		if err != nil {
			return ClassifyResult{}, err
		}
		res, err := parseClassifyResponse([]byte(content), in.Files)
		if err == nil {
			res.Model = model
			res.TokensUsed = tokens
			return res, nil
		}
		lastErr = err
		if !isRetryableContentErr(err) || attempt == maxContentRetry {
			return ClassifyResult{}, err
		}
	}
	return ClassifyResult{}, lastErr
}

// Compose implements Provider.
func (n *Nvidia) Compose(ctx context.Context, in ComposeInput) (ComposeResult, error) {
	userPrompt := buildComposeUserPrompt(in)
	var lastErr error
	for attempt := 0; attempt <= maxContentRetry; attempt++ {
		content, model, tokens, err := n.invoke(ctx, systemPrompt, userPrompt, true, 0)
		if err != nil {
			return ComposeResult{}, err
		}
		// Strict JSON parse (no plain-text fallback): this path requests
		// response_format=json_object, so a prose reply is a provider misfire
		// worth retrying. parseComposeResponse's lenient fallback would
		// instead accept the prose's first line as a "valid" subject and
		// never consume the retry — the exact gap for the reported
		// "invalid character 'I'" failure on the Compose side.
		res, err := parseComposeJSON([]byte(content))
		if err == nil {
			res.Model = model
			res.TokensUsed = tokens
			return res, nil
		}
		lastErr = err
		if !isRetryableContentErr(err) || attempt == maxContentRetry {
			return ComposeResult{}, err
		}
	}
	return ComposeResult{}, lastErr
}

// maxContentRetry caps re-requests when the HTTP call succeeds but the
// model's content doesn't parse as the requested JSON shape (e.g. prose
// instead of JSON). This is a content-quality retry, distinct from
// send()'s transport-level 429/5xx retries — a re-roll of the same
// prompt often yields valid JSON on the next attempt.
const maxContentRetry = 1

// isRetryableContentErr reports whether a Classify/Compose parse failure
// is worth re-requesting. Truncated JSON is excluded: retrying with the
// same token budget almost always truncates again, so that case surfaces
// its actionable message (fewer files / gk commit --plan -) instead.
func isRetryableContentErr(err error) bool {
	return errors.Is(err, ErrProviderResponse) && !errors.Is(err, errTruncatedJSON)
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
	// Stream and StreamOptions request SSE streaming. Both omitempty —
	// every existing non-streaming caller's request body stays byte-
	// identical (zero values are dropped); only chatWithToolsStream
	// sets them.
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

// streamOptions requests usage accounting on the final streaming chunk —
// Chat Completions streaming otherwise omits `usage` entirely, unlike the
// non-streaming response.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
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

// ── Streaming (Chat Completions SSE chunks) ──────────────────────────

// chatStreamChunk is one `data: {...}` SSE chunk from a streaming Chat
// Completions request — the incremental counterpart to chatResponse.
type chatStreamChunk struct {
	Model   string             `json:"model"`
	Choices []chatStreamChoice `json:"choices"`
	Usage   *chatUsage         `json:"usage"`
	// Error carries an in-band error object some OpenAI-compatible gateways
	// emit as a well-formed `data: {"error":{...}}` chunk mid-stream (rate
	// limit hit, upstream failure) instead of a transport-level non-2xx.
	// Without this field the chunk parses cleanly, the error is dropped, and
	// a trailing `data: [DONE]` would confirm a truncated answer as success
	// — the Anthropic twin already guards this via its `event: error` case.
	Error *chatStreamError `json:"error"`
}

type chatStreamError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type chatStreamChoice struct {
	Delta        chatStreamDelta `json:"delta"`
	FinishReason string          `json:"finish_reason"`
}

// chatStreamDelta reuses chatToolCall for ToolCalls: OpenAI-compatible
// tool-call deltas arrive as fragments (an "index", sometimes an "id",
// sometimes only a partial "function.arguments" string) but every field
// chatToolCall declares is optional from JSON's perspective, so a
// fragment unmarshals cleanly — this adapter only needs to DETECT a
// tool_calls delta's presence, never assemble the fragments (that's the
// exact assembly problem streaming sidesteps by falling back instead).
type chatStreamDelta struct {
	Content   string         `json:"content"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
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
//
// The loop's own deadline is retryBudget(), NOT timeout() — timeout()
// still separately bounds each individual attempt via Client's
// http.Client.Timeout, but the two are independent (RetryBudget's
// docstring has the full rationale). For every caller that never sets
// RetryBudget, retryBudget() falls back to timeout() and this is exactly
// the single deadline that existed before that field did.
func (n *Nvidia) send(ctx context.Context, bodyBytes []byte) (chatResponse, error) {
	endpoint := n.endpoint()
	apiKey := n.apiKey()

	deadline := time.Now().Add(n.retryBudget())
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
				return chatResponse{}, sleepAbortErr(ctx, n.brand(), lastErr)
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
				return chatResponse{}, sleepAbortErr(ctx, n.brand(), lastErr)
			}
			continue

		case statusCode >= 500 && statusCode < 600:
			// 5xx: exponential backoff (1s, 2s, 4s).
			if attempt >= maxRetry {
				return chatResponse{}, lastErr
			}
			backoff := time.Duration(1<<uint(attempt)) * time.Second
			if !n.sleep(ctx, backoff) {
				return chatResponse{}, sleepAbortErr(ctx, n.brand(), lastErr)
			}
			continue

		default:
			// 4xx (non-429): no retry.
			return chatResponse{}, lastErr
		}
	}

	return chatResponse{}, lastErr
}

// sleepAbortErr wraps the last transport error with the context error that
// interrupted a retry backoff. Without the wrap, a round deadline expiring
// mid-backoff surfaces as the previous HTTP failure (e.g. a 500) and
// callers matching context.DeadlineExceeded — like gk chat's timeout hint
// — never fire.
func sleepAbortErr(ctx context.Context, brand string, lastErr error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s: %w (last error before deadline: %v)", brand, ctxErr, lastErr)
	}
	return lastErr
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
	if in.OnTextDelta != nil {
		// The streaming attempt gets its OWN shorter sub-deadline
		// (streamAttemptContext, stream.go) instead of the bare round
		// ctx: a hung/incomplete stream must not be able to burn the
		// entire round budget before the fallback below even starts — see
		// streamAttemptContext's docstring. ctx (the round's own,
		// untouched deadline) is what the fallback call further down
		// still uses.
		// Wrap OnTextDelta to learn whether the stream printed anything
		// before it was abandoned — only then is an OnStreamReset owed.
		streamedAny := false
		si := in
		if in.OnTextDelta != nil {
			si.OnTextDelta = func(s string) { streamedAny = true; in.OnTextDelta(s) }
		}
		streamCtx, cancel := streamAttemptContext(ctx)
		sres, ok := n.chatWithToolsStream(streamCtx, mdl, msgs, tools, si)
		cancel()
		if ok {
			return sres, nil
		}
		// Streaming didn't produce a definitive text-only answer (a
		// tool_calls delta was detected, a malformed/unparseable chunk
		// arrived, or the stream ended anywhere short of its terminal
		// "[DONE]" marker) — fall through to the ordinary non-stream
		// request below, whose send() call carries the real
		// retry/backoff policy. Same fallback contract as
		// Anthropic.chatWithToolsStream: never a splice of streamed-
		// then-abandoned text with the fallback's reply. Signal the reset
		// if text already reached the caller so it can void the partial.
		if streamedAny && in.OnStreamReset != nil {
			in.OnStreamReset()
		}
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

// chatWithToolsStream mirrors Anthropic.chatWithToolsStream for the
// OpenAI-compatible Chat Completions wire format — shared by nvidia, and
// by openai/groq through their delegation into this adapter. It attempts
// ONE streaming round-trip and returns (result, true) ONLY when the
// stream ran to its terminal "data: [DONE]" marker with no tool_calls
// delta anywhere in the response. Every other outcome — a tool_calls
// delta at any point, a malformed/unparseable chunk, a severed
// connection, a non-2xx status, or a stream that never reaches "[DONE]"
// — returns (ChatResult{}, false) so the caller re-sends the identical
// conversation through the ordinary non-streaming path, whose send()
// call owns the real retry/backoff policy. Like its Anthropic
// counterpart, this never returns a Go error: every failure mode here is
// meant to fall back silently.
func (n *Nvidia) chatWithToolsStream(ctx context.Context, mdl string, msgs []chatMessage, tools []chatToolDef, in ChatInput) (ChatResult, bool) {
	bodyBytes, mErr := json.Marshal(chatRequest{
		Model:         mdl,
		Messages:      msgs,
		MaxTokens:     in.MaxTokens,
		Tools:         tools,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	})
	if mErr != nil {
		return ChatResult{}, false
	}
	req, rErr := http.NewRequestWithContext(ctx, http.MethodPost, n.endpoint(), bytes.NewReader(bodyBytes))
	if rErr != nil {
		return ChatResult{}, false
	}
	req.Header.Set("Authorization", "Bearer "+n.apiKey())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, doErr := n.Client.Do(ctx, req)
	if doErr != nil {
		return ChatResult{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return ChatResult{}, false
	}

	var sb strings.Builder
	var model, finishReason string
	var usage *chatUsage
	var toolCallsSeen, doneSeen, malformedChunk, sawError bool

	scanErr := scanSSE(resp.Body, func(ev sseEvent) bool {
		data := strings.TrimSpace(ev.Data)
		if data == "" {
			// SSE-legal but content-free: a comment line (leading ':')
			// never reaches here at all — scanSSE drops those before
			// calling onEvent. This is an event whose only fields were
			// unknown ones (id:/retry:) or a blank "data:" line — there
			// is nothing to parse and nothing lost by skipping it.
			return false
		}
		if data == "[DONE]" {
			doneSeen = true
			return true
		}
		var chunk chatStreamChunk
		if json.Unmarshal([]byte(data), &chunk) != nil {
			// A NON-EMPTY data: payload that fails to parse as JSON is
			// not a benign SSE framing quirk — every real chunk on this
			// wire is JSON (or the literal "[DONE]" sentinel, already
			// handled above). This means the chunk is corrupted or was
			// cut off mid-write. Silently skipping it (the pre-fix
			// behavior) let scanning continue to a later, well-formed
			// "[DONE]" and confirm an answer that is missing whatever
			// content this chunk carried — an incomplete reply reported
			// as a complete success. Stop here instead, matching this
			// method's own documented contract (a malformed/unparseable
			// chunk returns (ChatResult{}, false)), so the caller falls
			// back to a full non-stream retry.
			malformedChunk = true
			return true
		}
		if chunk.Error != nil {
			// A well-formed in-band error chunk: abandon the stream so the
			// caller falls back to a full non-stream retry, exactly as the
			// Anthropic adapter does for its `event: error`.
			sawError = true
			return true
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, c := range chunk.Choices {
			if len(c.Delta.ToolCalls) > 0 {
				toolCallsSeen = true
				return true
			}
			if c.Delta.Content != "" {
				sb.WriteString(c.Delta.Content)
				in.OnTextDelta(c.Delta.Content)
			}
			if c.FinishReason != "" {
				finishReason = c.FinishReason
			}
		}
		return false
	})

	if malformedChunk || sawError || toolCallsSeen || scanErr != nil || !doneSeen {
		return ChatResult{}, false
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return ChatResult{}, false
	}
	out := ChatResult{
		Text:       text,
		Model:      model,
		StopReason: normalizeOpenAIStop(finishReason),
	}
	if usage != nil {
		out.TokensUsed = usage.TotalTokens
	}
	return out, true
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

// retryBudget bounds send()'s whole retry loop — see RetryBudget's
// docstring for why this is independent of timeout().
func (n *Nvidia) retryBudget() time.Duration {
	if n.RetryBudget > 0 {
		return n.RetryBudget
	}
	return n.timeout()
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
