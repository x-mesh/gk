package provider

import (
	"errors"
	"strings"
	"testing"
)

var parseTestFiles = []FileChange{
	{Path: "a.go", Status: "M"},
	{Path: "b.go", Status: "A"},
	{Path: "3", Status: "M"}, // literal file named like an index
}

// A complete JSON object wrapped in a ```json fence parses fine.
func TestParseClassify_Fenced(t *testing.T) {
	raw := "```json\n{\"groups\":[{\"type\":\"feat\",\"files\":[\"a.go\"]}]}\n```"
	res, err := parseClassifyResponse([]byte(raw), parseTestFiles)
	if err != nil {
		t.Fatalf("fenced JSON should parse: %v", err)
	}
	if len(res.Groups) != 1 || res.Groups[0].Type != "feat" {
		t.Fatalf("unexpected groups: %+v", res.Groups)
	}
}

// The index protocol: numeric entries resolve to the 1-based file, string
// paths keep working, and a literal file whose NAME looks like an index
// wins over the index reading.
func TestParseClassify_IndexProtocol(t *testing.T) {
	raw := `{"groups":[{"type":"feat","files":[1,"b.go","3"]}]}`
	res, err := parseClassifyResponse([]byte(raw), parseTestFiles)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Groups[0].Files
	want := []string{"a.go", "b.go", "3"}
	if len(got) != len(want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("files[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// Out-of-range indexes are dropped; a group left with no resolvable file
// is skipped rather than fabricating paths.
func TestParseClassify_OutOfRangeIndexDropped(t *testing.T) {
	raw := `{"groups":[{"type":"feat","files":[99]},{"type":"fix","files":[2]}]}`
	res, err := parseClassifyResponse([]byte(raw), parseTestFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 1 || res.Groups[0].Files[0] != "b.go" {
		t.Fatalf("unexpected groups: %+v", res.Groups)
	}
}

// A response cut off mid-object reports actionable truncation guidance,
// not a raw "invalid character" parser error — and the guidance must
// point at knobs that actually control the failure (the response cap and
// splitting), NOT ai.commit.max_tokens, which only bounds the INPUT
// payload (the misleading-hint bug found in the space-mesh incident).
func TestParseClassify_Truncated(t *testing.T) {
	raw := "```json\n{\"groups\":[{\"type\":\"feat\",\"files\":[\"a.go\""
	_, err := parseClassifyResponse([]byte(raw), parseTestFiles)
	if err == nil {
		t.Fatal("truncated JSON should error")
	}
	if !errors.Is(err, ErrProviderResponse) {
		t.Errorf("want ErrProviderResponse, got %v", err)
	}
	if !strings.Contains(err.Error(), "cut off") {
		t.Errorf("error should explain truncation, got: %v", err)
	}
	if strings.Contains(err.Error(), "ai.commit.max_tokens") {
		t.Errorf("hint must not point at ai.commit.max_tokens (input-only knob): %v", err)
	}
	if !strings.Contains(err.Error(), "gk commit --plan") {
		t.Errorf("hint should offer the deterministic --plan path: %v", err)
	}
}

// A CLI adapter that wraps its plain-text answer in a bare ``` fence must
// still yield the real subject. The plain-text fallback runs on the
// fence-stripped text, so the opening "```" never becomes the subject (which
// would drop the real subject line entirely and commit a fenced header).
func TestParseCompose_FencedPlainTextKeepsSubject(t *testing.T) {
	raw := "```\nfeat(sync): propagate directory deletions\n\nleaves first, then the parent\n```"
	res, err := parseComposeResponse([]byte(raw))
	if err != nil {
		t.Fatalf("fenced plain text should parse: %v", err)
	}
	if want := "feat(sync): propagate directory deletions"; res.Subject != want {
		t.Errorf("subject = %q, want %q", res.Subject, want)
	}
	if want := "leaves first, then the parent"; res.Body != want {
		t.Errorf("body = %q, want %q", res.Body, want)
	}
}

// The fence-stripping fallback must not disturb unfenced plain text.
func TestParseCompose_PlainTextUnchanged(t *testing.T) {
	res, err := parseComposeResponse([]byte("fix(api): reject empty scope\n\nwhy it matters"))
	if err != nil {
		t.Fatalf("plain text should parse: %v", err)
	}
	if res.Subject != "fix(api): reject empty scope" || res.Body != "why it matters" {
		t.Errorf("got subject=%q body=%q", res.Subject, res.Body)
	}
}

// tryJSONDecode flags a truncated object distinctly from other failures.
func TestTryJSONDecode_TruncatedSentinel(t *testing.T) {
	var v map[string]any
	err := tryJSONDecode("{\"a\":1", &v) // unbalanced
	if !errors.Is(err, errTruncatedJSON) {
		t.Errorf("want errTruncatedJSON, got %v", err)
	}
}
