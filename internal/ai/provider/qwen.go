package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Qwen adapts the Qwen Code CLI (`qwen`, github.com/QwenLM/qwen-code).
//
// Invocation contract (v0.15+):
//
//	qwen "<prompt>" -o json --system-prompt <sys> --approval-mode plan
//	echo <diff> | qwen "<prompt>" -o json
//
// Qwen is the trickiest of the three: it often exits with code 0 even
// when auth is missing, and embeds the error inside the JSON payload
// as is_error=true. We honour that by scanning response events.
type Qwen struct {
	Runner    CommandRunner
	Binary    string
	EnvLookup func(string) string
}

// NewQwen returns a Qwen adapter with sensible defaults.
func NewQwen() *Qwen {
	return &Qwen{Runner: ExecRunner{}, Binary: "qwen", EnvLookup: os.Getenv}
}

// Name implements Provider.
func (q *Qwen) Name() string { return "qwen" }

// Locality implements Provider.
func (q *Qwen) Locality() Locality { return LocalityRemote }

// Available checks the binary exists and at least one supported auth
// env var is set. We can't call `qwen auth status` here — that spawns
// a real subprocess — so we sniff env vars and fall through to the
// runtime error path when nothing is configured.
func (q *Qwen) Available(_ context.Context) error {
	bin := q.binary()
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("%w: %s not found on PATH", ErrNotInstalled, bin)
	}
	lookup := q.EnvLookup
	if lookup == nil {
		lookup = os.Getenv
	}
	keys := []string{
		"DASHSCOPE_API_KEY",
		"OPENAI_API_KEY",
		"ANTHROPIC_API_KEY",
		"GEMINI_API_KEY",
		"BAILIAN_CODING_PLAN_API_KEY",
	}
	for _, k := range keys {
		if lookup(k) != "" {
			return nil
		}
	}
	// Neither env nor OAuth check wired in — qwen may have an OAuth
	// session; surface ErrUnauthenticated so the CLI wiring can print a
	// friendly hint.
	return fmt.Errorf("%w: set one of %v or run `qwen auth qwen-oauth`",
		ErrUnauthenticated, keys)
}

// Classify implements Provider.
func (q *Qwen) Classify(ctx context.Context, in ClassifyInput) (ClassifyResult, error) {
	prompt := buildClassifyUserPrompt(in, string(concatFileDiffs(in.Files)))
	raw, err := q.invoke(ctx, prompt, concatFileDiffs(in.Files))
	if err != nil {
		return ClassifyResult{}, err
	}
	res, err := parseClassifyResponse([]byte(raw.text))
	if err != nil {
		return ClassifyResult{}, err
	}
	res.Model = raw.model
	return res, nil
}

// Compose implements Provider.
func (q *Qwen) Compose(ctx context.Context, in ComposeInput) (ComposeResult, error) {
	prompt := buildComposeUserPrompt(in)
	raw, err := q.invoke(ctx, prompt, []byte(in.Diff))
	if err != nil {
		return ComposeResult{}, err
	}
	res, err := parseComposeResponse([]byte(raw.text))
	if err != nil {
		return ComposeResult{}, err
	}
	res.Model = raw.model
	return res, nil
}

// qwenEvent models one element of the stream-json array qwen emits.
// Only fields we care about are listed; extras are skipped.
type qwenEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Error   struct {
		Message string `json:"message"`
	} `json:"error"`
	Content string `json:"content"`
	Text    string `json:"text"`
	Model   string `json:"model"`
}

type qwenParsed struct {
	text  string
	model string
}

func (q *Qwen) invoke(ctx context.Context, userPrompt string, stdinExtra []byte) (qwenParsed, error) {
	args := []string{
		userPrompt,
		"-o", "json",
		"--system-prompt", systemPrompt,
	}
	stdout, stderr, err := q.Runner.Run(ctx, q.binary(), args, stdinExtra, nil)
	if err != nil && len(stdout) == 0 {
		return qwenParsed{}, fmt.Errorf("qwen: %w (stderr=%s)", err, string(stderr))
	}

	// qwen -o json emits a JSON array of events. Inspect each.
	var events []qwenEvent
	if jerr := json.Unmarshal(stdout, &events); jerr != nil {
		// Some versions emit a single object; try that path.
		var single qwenEvent
		if serr := json.Unmarshal(stdout, &single); serr != nil {
			// Still not JSON — treat stdout as literal response text.
			return qwenParsed{text: string(stdout)}, nil
		}
		events = []qwenEvent{single}
	}

	var (
		buf   strings.Builder
		model string
	)
	for _, ev := range events {
		if ev.IsError {
			return qwenParsed{}, fmt.Errorf("%w: qwen error: %s",
				ErrProviderResponse, ev.Error.Message)
		}
		if ev.Model != "" {
			model = ev.Model
		}
		if ev.Text != "" {
			buf.WriteString(ev.Text)
		}
		if ev.Content != "" {
			buf.WriteString(ev.Content)
		}
	}
	if strings.TrimSpace(buf.String()) == "" {
		return qwenParsed{}, fmt.Errorf("%w: empty response", ErrProviderResponse)
	}
	return qwenParsed{text: buf.String(), model: model}, nil
}

func (q *Qwen) binary() string {
	if q.Binary == "" {
		return "qwen"
	}
	return q.Binary
}

var _ Provider = (*Qwen)(nil)

// SuggestGitignore implements GitignoreSuggester.
func (q *Qwen) SuggestGitignore(ctx context.Context, projectInfo string) ([]string, error) {
	prompt := gitignoreSystemPrompt + "\n\n" + gitignoreUserPromptPrefix + projectInfo
	raw, err := q.invoke(ctx, prompt, nil)
	if err != nil {
		return nil, err
	}
	return parseGitignoreLines(raw.text), nil
}
