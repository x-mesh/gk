package provider

import (
	"context"
	"fmt"
	"os"
	"time"
)

const (
	defaultOpenAIEndpoint = "https://api.openai.com/v1/chat/completions"
	defaultOpenAIModel    = "gpt-4o-mini"
	defaultOpenAITimeout  = 60 * time.Second
	defaultOpenAIMaxRetry = 3
)

// OpenAI adapts the OpenAI Chat Completions API. Like Groq it's wire-
// compatible with the Nvidia adapter's HTTP invoke, so the heavy lifting
// (request shape, retries, response parsing) is reused.
type OpenAI struct {
	Client    HTTPClient
	Endpoint  string
	Model     string
	APIKey    string
	Timeout   time.Duration
	MaxRetry  int
	EnvLookup func(string) string
	SleepFn   func(ctx context.Context, d time.Duration) bool

	nv *Nvidia
}

// NewOpenAI returns an OpenAI adapter with sensible defaults
// (gpt-4o-mini, 60s timeout, 3 retries).
func NewOpenAI() *OpenAI {
	o := &OpenAI{
		Client:    NewDefaultHTTPClient(defaultOpenAITimeout),
		Endpoint:  defaultOpenAIEndpoint,
		Model:     defaultOpenAIModel,
		Timeout:   defaultOpenAITimeout,
		MaxRetry:  defaultOpenAIMaxRetry,
		EnvLookup: os.Getenv,
	}
	o.nv = o.toNvidia()
	return o
}

func (o *OpenAI) Name() string       { return "openai" }
func (o *OpenAI) Locality() Locality { return LocalityRemote }

func (o *OpenAI) Available(_ context.Context) error {
	if o.apiKey() == "" {
		return fmt.Errorf("%w: OPENAI_API_KEY not set", ErrUnauthenticated)
	}
	return nil
}

func (o *OpenAI) Classify(ctx context.Context, in ClassifyInput) (ClassifyResult, error) {
	return o.nv.Classify(ctx, in)
}

func (o *OpenAI) Compose(ctx context.Context, in ComposeInput) (ComposeResult, error) {
	return o.nv.Compose(ctx, in)
}

func (o *OpenAI) Summarize(ctx context.Context, in SummarizeInput) (SummarizeResult, error) {
	res, err := o.nv.Summarize(ctx, in)
	if err != nil {
		return SummarizeResult{}, err
	}
	res.Provider = o.Name()
	return res, nil
}

func (o *OpenAI) SuggestGitignore(ctx context.Context, projectInfo string) ([]string, error) {
	return o.nv.SuggestGitignore(ctx, projectInfo)
}

func (o *OpenAI) apiKey() string {
	if o.APIKey != "" {
		return o.APIKey
	}
	lookup := o.EnvLookup
	if lookup == nil {
		lookup = os.Getenv
	}
	return lookup("OPENAI_API_KEY")
}

// toNvidia mirrors Groq's pattern — OpenAI's API is the canonical
// OpenAI Chat Completions shape that Nvidia's adapter already speaks,
// so we configure a Nvidia adapter with our endpoint/model and let it
// drive the HTTP exchange.
func (o *OpenAI) toNvidia() *Nvidia {
	return &Nvidia{
		Client:   o.Client,
		Endpoint: o.Endpoint,
		Model:    o.Model,
		APIKey:   o.apiKey(),
		Timeout:  o.Timeout,
		MaxRetry: o.MaxRetry,
		SleepFn:  o.SleepFn,
		Brand:    "openai",
		EnvLookup: func(key string) string {
			if key == "NVIDIA_API_KEY" {
				return o.apiKey()
			}
			lookup := o.EnvLookup
			if lookup == nil {
				lookup = os.Getenv
			}
			return lookup(key)
		},
	}
}

var (
	_ Provider           = (*OpenAI)(nil)
	_ Summarizer         = (*OpenAI)(nil)
	_ GitignoreSuggester = (*OpenAI)(nil)
)
