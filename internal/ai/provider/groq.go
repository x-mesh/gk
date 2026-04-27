package provider

import (
	"context"
	"fmt"
	"os"
	"time"
)

const (
	defaultGroqEndpoint = "https://api.groq.com/openai/v1/chat/completions"
	defaultGroqModel    = "llama-3.3-70b-versatile"
	defaultGroqTimeout  = 60 * time.Second
	defaultGroqMaxRetry = 3
)

// Groq adapts the Groq Chat Completions API (OpenAI-compatible).
// Like Nvidia, it talks HTTP directly — no external binary needed.
type Groq struct {
	Client    HTTPClient
	Endpoint  string
	Model     string
	APIKey    string
	Timeout   time.Duration
	MaxRetry  int
	EnvLookup func(string) string
	SleepFn   func(ctx context.Context, d time.Duration) bool

	// embed Nvidia for HTTP invoke reuse
	nv *Nvidia
}

// NewGroq returns a Groq adapter with sensible defaults.
func NewGroq() *Groq {
	g := &Groq{
		Client:    NewDefaultHTTPClient(defaultGroqTimeout),
		Endpoint:  defaultGroqEndpoint,
		Model:     defaultGroqModel,
		Timeout:   defaultGroqTimeout,
		MaxRetry:  defaultGroqMaxRetry,
		EnvLookup: os.Getenv,
	}
	g.nv = g.toNvidia()
	return g
}

func (g *Groq) Name() string       { return "groq" }
func (g *Groq) Locality() Locality { return LocalityRemote }

func (g *Groq) Available(_ context.Context) error {
	if g.apiKey() == "" {
		return fmt.Errorf("%w: GROQ_API_KEY not set", ErrUnauthenticated)
	}
	return nil
}

func (g *Groq) Classify(ctx context.Context, in ClassifyInput) (ClassifyResult, error) {
	return g.nv.Classify(ctx, in)
}

func (g *Groq) Compose(ctx context.Context, in ComposeInput) (ComposeResult, error) {
	return g.nv.Compose(ctx, in)
}

func (g *Groq) Summarize(ctx context.Context, in SummarizeInput) (SummarizeResult, error) {
	res, err := g.nv.Summarize(ctx, in)
	if err != nil {
		return SummarizeResult{}, err
	}
	res.Provider = g.Name()
	return res, nil
}

func (g *Groq) SuggestGitignore(ctx context.Context, projectInfo string) ([]string, error) {
	return g.nv.SuggestGitignore(ctx, projectInfo)
}

func (g *Groq) apiKey() string {
	if g.APIKey != "" {
		return g.APIKey
	}
	lookup := g.EnvLookup
	if lookup == nil {
		lookup = os.Getenv
	}
	return lookup("GROQ_API_KEY")
}

// toNvidia creates an Nvidia adapter configured with Groq's settings.
// Groq's API is OpenAI-compatible, so we reuse Nvidia's HTTP invoke logic.
func (g *Groq) toNvidia() *Nvidia {
	return &Nvidia{
		Client:   g.Client,
		Endpoint: g.Endpoint,
		Model:    g.Model,
		APIKey:   g.apiKey(),
		Timeout:  g.Timeout,
		MaxRetry: g.MaxRetry,
		SleepFn:  g.SleepFn,
		EnvLookup: func(key string) string {
			// Nvidia의 apiKey()가 NVIDIA_API_KEY를 찾으므로,
			// Groq의 API key를 NVIDIA_API_KEY로 매핑
			if key == "NVIDIA_API_KEY" {
				return g.apiKey()
			}
			lookup := g.EnvLookup
			if lookup == nil {
				lookup = os.Getenv
			}
			return lookup(key)
		},
	}
}

var (
	_ Provider           = (*Groq)(nil)
	_ Summarizer         = (*Groq)(nil)
	_ GitignoreSuggester = (*Groq)(nil)
)
