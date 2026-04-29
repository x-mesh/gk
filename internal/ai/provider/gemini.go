package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Gemini adapts the Google Gemini CLI (`gemini`, github.com/google-gemini/gemini-cli).
//
// Invocation contract (v0.38+):
//
//	gemini -p <prompt> -o json --approval-mode plan
//	gemini <prompt>      -o json --approval-mode plan   # positional form
//	echo <diff> | gemini -p <prompt> -o json            # stdin concat
//
// -o json yields {session_id, response, stats} — we extract .response
// and feed it to parse{Classify,Compose}Response. Gemini sometimes
// emits "[STARTUP]" lines on stderr which we ignore. Exit code is NOT
// a reliable success signal; we always try to parse stdout first.
type Gemini struct {
	Runner    CommandRunner
	Binary    string // override path for tests; defaults to "gemini"
	MinVer    string // advisory only; empty skips the check
	EnvLookup func(string) string
}

// NewGemini returns a Gemini adapter with sensible defaults.
func NewGemini() *Gemini {
	return &Gemini{Runner: ExecRunner{}, Binary: "gemini", EnvLookup: os.Getenv}
}

// Name implements Provider.
func (g *Gemini) Name() string { return "gemini" }

// Locality implements Provider. Gemini uploads prompts to Google.
func (g *Gemini) Locality() Locality { return LocalityRemote }

// Available verifies the binary is on PATH and an auth signal is present.
func (g *Gemini) Available(_ context.Context) error {
	bin := g.binary()
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("%w: %s not found on PATH", ErrNotInstalled, bin)
	}
	// Cheap auth probe: GEMINI_API_KEY / GOOGLE_API_KEY / google-adc env.
	// Interactive OAuth is detected by the binary at runtime — we only
	// flag _missing_ auth when no env var is set; false-negatives here
	// turn into runtime errors with an adequate message.
	lookup := g.EnvLookup
	if lookup == nil {
		lookup = os.Getenv
	}
	for _, key := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		if lookup(key) != "" {
			return nil
		}
	}
	// Fall through: let the real CLI decide. OAuth-cached sessions are
	// common and we can't inspect them without invoking the binary.
	return nil
}

// Classify implements Provider.
func (g *Gemini) Classify(ctx context.Context, in ClassifyInput) (ClassifyResult, error) {
	prompt := buildClassifyUserPrompt(in, string(concatFileDiffs(in.Files)))
	raw, err := g.invoke(ctx, prompt, concatFileDiffs(in.Files))
	if err != nil {
		return ClassifyResult{}, err
	}
	res, err := parseClassifyResponse(raw.responseText())
	if err != nil {
		return ClassifyResult{}, err
	}
	res.Model = raw.Model
	res.TokensUsed = raw.Tokens
	return res, nil
}

// Compose implements Provider.
func (g *Gemini) Compose(ctx context.Context, in ComposeInput) (ComposeResult, error) {
	prompt := buildComposeUserPrompt(in)
	raw, err := g.invoke(ctx, prompt, []byte(in.Diff))
	if err != nil {
		return ComposeResult{}, err
	}
	res, err := parseComposeResponse(raw.responseText())
	if err != nil {
		return ComposeResult{}, err
	}
	res.Model = raw.Model
	res.TokensUsed = raw.Tokens
	return res, nil
}

// Summarize implements Summarizer.
func (g *Gemini) Summarize(ctx context.Context, in SummarizeInput) (SummarizeResult, error) {
	prompt := summarizeSystemPrompt + "\n\n" + buildSummarizeUserPrompt(in)
	raw, err := g.invoke(ctx, prompt, nil)
	if err != nil {
		return SummarizeResult{}, err
	}
	return SummarizeResult{
		Text:       stripANSI(string(raw.responseText())),
		Model:      raw.Model,
		TokensUsed: raw.Tokens,
		Provider:   g.Name(),
	}, nil
}

// geminiResponse models the -o json envelope. Unknown fields are dropped.
type geminiResponse struct {
	SessionID string `json:"session_id"`
	Response  string `json:"response"`
	Stats     struct {
		Models map[string]struct {
			Tokens struct {
				Total int `json:"total"`
			} `json:"tokens"`
		} `json:"models"`
	} `json:"stats"`
	Model  string
	Tokens int
}

// responseText returns the text payload, which may be either the
// envelope .response or (fallback) the raw stdout. Callers parse it
// as JSON/text downstream.
func (r *geminiResponse) responseText() []byte { return []byte(r.Response) }

// invoke runs the gemini binary with systemPrompt on stdin alongside the diff.
// Gemini's --prompt can grow large so we pass the prompt as an argument
// and the diff as stdin (the binary concatenates them).
func (g *Gemini) invoke(ctx context.Context, userPrompt string, stdinExtra []byte) (*geminiResponse, error) {
	args := []string{
		"-p", userPrompt,
		"-o", "json",
		"--approval-mode", "plan",
	}
	stdout, stderr, err := g.Runner.Run(ctx, g.binary(), args, stdinExtra, nil)
	if err != nil && len(stdout) == 0 {
		return nil, fmt.Errorf("gemini: %w (stderr=%s)", err, string(stderr))
	}
	// Strip stderr [STARTUP] noise — we don't use stderr here but this
	// anchors the design intent.
	_ = filterGeminiStderr(stderr)

	var env geminiResponse
	if jerr := json.Unmarshal(stdout, &env); jerr != nil {
		// Not JSON — treat whole stdout as the response text so caller's
		// parse fallback can try.
		env.Response = string(stdout)
	}
	if env.Response == "" {
		env.Response = string(stdout)
	}
	if len(env.Stats.Models) > 0 {
		for name, m := range env.Stats.Models {
			env.Model = name
			env.Tokens = m.Tokens.Total
			break
		}
	}
	// Synthesize prompt-content error if the envelope was valid but empty.
	if strings.TrimSpace(env.Response) == "" {
		return nil, fmt.Errorf("%w: empty response", ErrProviderResponse)
	}
	return &env, nil
}

func (g *Gemini) binary() string {
	if g.Binary == "" {
		return "gemini"
	}
	return g.Binary
}

// filterGeminiStderr returns stderr with [STARTUP] diagnostic lines removed.
// Exposed (unexported) so tests can assert noise handling independently.
func filterGeminiStderr(stderr []byte) []byte {
	if len(stderr) == 0 {
		return stderr
	}
	out := make([]byte, 0, len(stderr))
	for _, line := range strings.Split(string(stderr), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[STARTUP]") {
			continue
		}
		out = append(out, []byte(line)...)
		out = append(out, '\n')
	}
	return out
}

// concatFileDiffs stitches per-file DiffHints into a single payload.
// Skips binary files defensively — gather.go is the source of truth for
// IsBinary, but enforcing it here means a buggy gather pipeline can never
// silently leak binary blobs into an LLM prompt.
func concatFileDiffs(files []FileChange) []byte {
	var b strings.Builder
	for _, f := range files {
		if f.IsBinary || f.DiffHint == "" {
			continue
		}
		if f.OrigPath != "" {
			fmt.Fprintf(&b, "--- %s (%s from %s)\n", f.Path, f.Status, f.OrigPath)
		} else {
			fmt.Fprintf(&b, "--- %s (%s)\n", f.Path, f.Status)
		}
		b.WriteString(f.DiffHint)
		if !strings.HasSuffix(f.DiffHint, "\n") {
			b.WriteByte('\n')
		}
	}
	return []byte(b.String())
}

var _ Summarizer = (*Gemini)(nil)

var _ Provider = (*Gemini)(nil)

// SuggestGitignore implements GitignoreSuggester.
func (g *Gemini) SuggestGitignore(ctx context.Context, projectInfo string) ([]string, error) {
	prompt := gitignoreSystemPrompt + "\n\n" + gitignoreUserPromptPrefix + projectInfo
	raw, err := g.invoke(ctx, prompt, nil)
	if err != nil {
		return nil, err
	}
	return parseGitignoreLines(string(raw.responseText())), nil
}
