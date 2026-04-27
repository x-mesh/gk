package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// Kiro adapts the Kiro headless CLI (`kiro-cli`, kiro.dev/docs/cli/headless).
//
// Important: the IDE launcher binary `kiro` is DIFFERENT from the
// headless chat binary `kiro-cli`. Invoking `kiro chat` opens a
// VS Code window; we explicitly target `kiro-cli` and surface a
// descriptive error when only the IDE launcher is present.
//
// Invocation contract (v2.0+):
//
//	kiro-cli chat --no-interactive --trust-tools= "<prompt>"
//	echo <diff> | kiro-cli chat --no-interactive --trust-tools= "<prompt>"
//
// --trust-tools= (empty list) disables tool calls so the model returns
// pure text. Responses are plain text / markdown; JSON mode exists but
// only for --list-* queries.
type Kiro struct {
	Runner    CommandRunner
	Binary    string
	EnvLookup func(string) string
}

// NewKiro returns a Kiro adapter with sensible defaults.
func NewKiro() *Kiro {
	return &Kiro{Runner: ExecRunner{}, Binary: "kiro-cli", EnvLookup: os.Getenv}
}

// Name implements Provider.
func (k *Kiro) Name() string { return "kiro" }

// Locality implements Provider.
func (k *Kiro) Locality() Locality { return LocalityRemote }

// Available differentiates the headless CLI from the IDE launcher.
//
// Behaviour:
//   - `kiro-cli` on PATH: OK.
//   - Only `kiro` on PATH: ErrNotInstalled wrapped with "IDE launcher
//     detected, install kiro-cli" so CLI output can point users at
//     kiro.dev/docs/cli/installation.
func (k *Kiro) Available(_ context.Context) error {
	bin := k.binary()
	if _, err := exec.LookPath(bin); err == nil {
		return nil
	}
	if _, err := exec.LookPath("kiro"); err == nil {
		return fmt.Errorf("%w: found `kiro` (IDE launcher) but not `kiro-cli` (headless); install kiro-cli from https://kiro.dev/docs/cli/installation",
			ErrNotInstalled)
	}
	return fmt.Errorf("%w: kiro-cli not found on PATH", ErrNotInstalled)
}

// Classify implements Provider.
func (k *Kiro) Classify(ctx context.Context, in ClassifyInput) (ClassifyResult, error) {
	prompt := buildClassifyUserPrompt(in, string(concatFileDiffs(in.Files)))
	raw, err := k.invoke(ctx, prompt, concatFileDiffs(in.Files))
	if err != nil {
		return ClassifyResult{}, err
	}
	return parseClassifyResponse(raw)
}

// Compose implements Provider.
func (k *Kiro) Compose(ctx context.Context, in ComposeInput) (ComposeResult, error) {
	prompt := buildComposeUserPrompt(in)
	raw, err := k.invoke(ctx, prompt, []byte(in.Diff))
	if err != nil {
		return ComposeResult{}, err
	}
	return parseComposeResponse(raw)
}

// Summarize implements Summarizer.
func (k *Kiro) Summarize(ctx context.Context, in SummarizeInput) (SummarizeResult, error) {
	prompt := summarizeSystemPrompt + "\n\n" + buildSummarizeUserPrompt(in)
	raw, err := k.invoke(ctx, prompt, nil)
	if err != nil {
		return SummarizeResult{}, err
	}
	return SummarizeResult{Text: stripANSI(string(raw)), Provider: k.Name()}, nil
}

func (k *Kiro) invoke(ctx context.Context, userPrompt string, stdinExtra []byte) ([]byte, error) {
	args := []string{
		"chat",
		"--no-interactive",
		"--trust-tools=",
		userPrompt,
	}
	stdout, stderr, err := k.Runner.Run(ctx, k.binary(), args, stdinExtra, nil)
	if err != nil && len(stdout) == 0 {
		return nil, fmt.Errorf("kiro-cli: %w (stderr=%s)", err, string(stderr))
	}
	if len(stdout) == 0 {
		return nil, fmt.Errorf("%w: empty response", ErrProviderResponse)
	}
	return stdout, nil
}

func (k *Kiro) binary() string {
	if k.Binary == "" {
		return "kiro-cli"
	}
	return k.Binary
}

var _ Provider = (*Kiro)(nil)
var _ Summarizer = (*Kiro)(nil)

// SuggestGitignore implements GitignoreSuggester.
func (k *Kiro) SuggestGitignore(ctx context.Context, projectInfo string) ([]string, error) {
	prompt := gitignoreSystemPrompt + "\n\n" + gitignoreUserPromptPrefix + projectInfo
	raw, err := k.invoke(ctx, prompt, nil)
	if err != nil {
		return nil, err
	}
	return parseGitignoreLines(string(raw)), nil
}
