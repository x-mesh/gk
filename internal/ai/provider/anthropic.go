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
	content, model, tokens, err := a.invoke(ctx, systemPrompt, userPrompt)
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

func (a *Anthropic) Compose(ctx context.Context, in ComposeInput) (ComposeResult, error) {
	userPrompt := buildComposeUserPrompt(in)
	content, model, tokens, err := a.invoke(ctx, systemPrompt, userPrompt)
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
	content, model, tokens, err := a.invoke(ctx, summarizeSystemPrompt, userPrompt)
	if err != nil {
		return SummarizeResult{}, err
	}
	return SummarizeResult{Text: content, Model: model, TokensUsed: tokens, Provider: a.Name()}, nil
}

func (a *Anthropic) SuggestGitignore(ctx context.Context, projectInfo string) ([]string, error) {
	userPrompt := gitignoreUserPromptPrefix + projectInfo
	content, _, _, err := a.invoke(ctx, gitignoreSystemPrompt, userPrompt)
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

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicResponse struct {
	Model   string                  `json:"model"`
	Content []anthropicContentBlock `json:"content"`
	Usage   anthropicUsage          `json:"usage"`
	Type    string                  `json:"type"`
	Error   *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// invoke sends a Messages API request and returns the joined text from
// every content block plus model/token bookkeeping. Retries on
// 429/5xx using the same backoff strategy as Nvidia.invoke.
func (a *Anthropic) invoke(ctx context.Context, sys, user string) (content, model string, tokensUsed int, err error) {
	if a.apiKey() == "" {
		return "", "", 0, fmt.Errorf("%w: ANTHROPIC_API_KEY not set", ErrUnauthenticated)
	}
	body, mErr := json.Marshal(anthropicRequest{
		Model:     a.modelOrDefault(),
		MaxTokens: a.maxTokens(),
		System:    sys,
		Messages:  []anthropicMessage{{Role: "user", Content: user}},
	})
	if mErr != nil {
		return "", "", 0, fmt.Errorf("anthropic: marshal request: %w", mErr)
	}

	endpoint := a.endpoint()
	maxRetry := a.maxRetry()
	for attempt := 0; attempt <= maxRetry; attempt++ {
		req, rErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if rErr != nil {
			return "", "", 0, fmt.Errorf("anthropic: build request: %w", rErr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", a.apiKey())
		req.Header.Set("anthropic-version", a.version())

		resp, doErr := a.Client.Do(ctx, req)
		if doErr != nil {
			if attempt < maxRetry && a.sleep(ctx, time.Duration(1<<uint(attempt))*time.Second) {
				continue
			}
			return "", "", 0, fmt.Errorf("anthropic: http: %w", doErr)
		}
		body2, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		// Retry on transient failures.
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			if attempt < maxRetry && a.sleep(ctx, time.Duration(1<<uint(attempt))*time.Second) {
				continue
			}
			return "", "", 0, fmt.Errorf("%w: anthropic %d: %s", ErrProviderResponse, resp.StatusCode, truncateBody(body2))
		}
		if resp.StatusCode >= 400 {
			return "", "", 0, fmt.Errorf("%w: anthropic %d: %s", a.classifyStatus(resp.StatusCode), resp.StatusCode, truncateBody(body2))
		}

		var parsed anthropicResponse
		if pErr := json.Unmarshal(body2, &parsed); pErr != nil {
			return "", "", 0, fmt.Errorf("%w: decode: %v: %s", ErrProviderResponse, pErr, truncateBody(body2))
		}
		if parsed.Error != nil {
			return "", "", 0, fmt.Errorf("%w: %s: %s", ErrProviderResponse, parsed.Error.Type, parsed.Error.Message)
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
		tokens := parsed.Usage.InputTokens + parsed.Usage.OutputTokens
		return text, parsed.Model, tokens, nil
	}
	return "", "", 0, errors.New("anthropic: exhausted retries")
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
)
