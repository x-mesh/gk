package provider

import (
	"errors"
	"strings"
	"testing"
)

// A complete JSON object wrapped in a ```json fence parses fine.
func TestParseClassify_Fenced(t *testing.T) {
	raw := "```json\n{\"groups\":[{\"type\":\"feat\",\"files\":[\"a.go\"]}]}\n```"
	res, err := parseClassifyResponse([]byte(raw))
	if err != nil {
		t.Fatalf("fenced JSON should parse: %v", err)
	}
	if len(res.Groups) != 1 || res.Groups[0].Type != "feat" {
		t.Fatalf("unexpected groups: %+v", res.Groups)
	}
}

// A response cut off mid-object reports actionable truncation guidance,
// not a raw "invalid character" parser error.
func TestParseClassify_Truncated(t *testing.T) {
	raw := "```json\n{\"groups\":[{\"type\":\"feat\",\"files\":[\"a.go\""
	_, err := parseClassifyResponse([]byte(raw))
	if err == nil {
		t.Fatal("truncated JSON should error")
	}
	if !errors.Is(err, ErrProviderResponse) {
		t.Errorf("want ErrProviderResponse, got %v", err)
	}
	if !strings.Contains(err.Error(), "cut off") {
		t.Errorf("error should explain truncation, got: %v", err)
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
