package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGeminiClassifySuccess(t *testing.T) {
	runner := &FakeCommandRunner{
		Responses: []FakeCommandResponse{{
			Stdout: []byte(`{"session_id":"abc","response":"{\"groups\":[{\"type\":\"feat\",\"files\":[\"a.go\"],\"rationale\":\"new file\"}]}","stats":{"models":{"gemini-3-flash-preview":{"tokens":{"total":42}}}}}`),
		}},
	}
	g := &Gemini{Runner: runner, Binary: "gemini", EnvLookup: func(string) string { return "" }}
	res, err := g.Classify(context.Background(), ClassifyInput{
		Files:        []FileChange{{Path: "a.go", Status: "added", DiffHint: "+package main\n"}},
		AllowedTypes: []string{"feat", "fix"},
		Lang:         "en",
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if len(res.Groups) != 1 || res.Groups[0].Type != "feat" {
		t.Errorf("groups: %+v", res.Groups)
	}
	if res.Model != "gemini-3-flash-preview" {
		t.Errorf("model: got %q", res.Model)
	}
	if res.TokensUsed != 42 {
		t.Errorf("tokens: got %d", res.TokensUsed)
	}
	if len(runner.Calls) != 1 {
		t.Fatalf("runner calls: %d", len(runner.Calls))
	}
	args := runner.Calls[0].Args
	if args[0] != "-p" || args[2] != "-o" || args[3] != "json" {
		t.Errorf("args missing -p / -o json: %v", args)
	}
	if !strings.Contains(string(runner.Calls[0].Stdin), "package main") {
		t.Errorf("diff not forwarded on stdin")
	}
}

func TestGeminiComposeSuccessMarkdownFence(t *testing.T) {
	// Gemini sometimes wraps the JSON in ```json ... ```.
	inner := "{\"subject\":\"add config loader\",\"body\":\"reads XDG first\"}"
	wrapped := "```json\n" + inner + "\n```"
	// Still inside gemini envelope:
	envelope := `{"response":` + jsonQuote(wrapped) + `}`
	runner := &FakeCommandRunner{Responses: []FakeCommandResponse{{Stdout: []byte(envelope)}}}
	g := &Gemini{Runner: runner, Binary: "gemini"}
	res, err := g.Compose(context.Background(), ComposeInput{
		Group:            Group{Type: "feat", Files: []string{"c.go"}},
		Lang:             "en",
		AllowedTypes:     []string{"feat"},
		MaxSubjectLength: 72,
		Diff:             "+x\n",
	})
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if res.Subject != "add config loader" {
		t.Errorf("subject: %q", res.Subject)
	}
	if res.Body != "reads XDG first" {
		t.Errorf("body: %q", res.Body)
	}
}

func TestGeminiEmptyResponseIsError(t *testing.T) {
	runner := &FakeCommandRunner{
		Responses: []FakeCommandResponse{{Stdout: []byte(`{"response":""}`)}},
	}
	g := &Gemini{Runner: runner, Binary: "gemini"}
	_, err := g.Compose(context.Background(), ComposeInput{
		Group:            Group{Type: "feat", Files: []string{"a.go"}},
		MaxSubjectLength: 72,
	})
	if !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("want ErrProviderResponse, got %v", err)
	}
}

func TestGeminiRunnerErrorWithEmptyStdoutPropagates(t *testing.T) {
	runner := &FakeCommandRunner{
		Responses: []FakeCommandResponse{{
			Err: &ExecError{Code: 1, Name: "gemini", Stderr: "authentication failed"},
		}},
	}
	g := &Gemini{Runner: runner, Binary: "gemini"}
	_, err := g.Classify(context.Background(), ClassifyInput{
		Files:        []FileChange{{Path: "a.go", Status: "added", DiffHint: "+a\n"}},
		AllowedTypes: []string{"feat"},
	})
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("error should include stderr: %v", err)
	}
}

func TestFilterGeminiStderrDropsStartupLines(t *testing.T) {
	in := []byte("[STARTUP] loading config\nreal warning\n[STARTUP] done\n")
	out := string(filterGeminiStderr(in))
	if strings.Contains(out, "STARTUP") {
		t.Errorf("still contains STARTUP: %q", out)
	}
	if !strings.Contains(out, "real warning") {
		t.Errorf("dropped the real warning: %q", out)
	}
}

// jsonQuote returns s wrapped in JSON-safe double quotes.
func jsonQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
