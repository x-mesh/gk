package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// classifyJSON is the schema adapters ask the model to emit for
// Classify. Fields are all optional on the wire so a partial response
// is still parseable — callers re-validate downstream.
type classifyJSON struct {
	Groups []struct {
		Type      string    `json:"type"`
		Scope     string    `json:"scope"`
		Files     []fileRef `json:"files"`
		Rationale string    `json:"rationale"`
	} `json:"groups"`
}

// fileRef accepts both wire forms for a group's files entry: the 1-based
// index the prompt asks for (keeps the response size independent of path
// length), and a bare path string — smaller models still echo paths, and
// both must stay valid.
type fileRef struct {
	index int    // 1-based when > 0
	path  string // set when the wire carried a non-numeric string
}

func (r *fileRef) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		r.path = s
		// "3" — a stringified index; resolvePath prefers a literal file
		// named "3" when one exists, so recording both is safe.
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			r.index = n
		}
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	r.index = n
	return nil
}

// resolvePath turns a wire fileRef into a real path from the classify
// input, or "" when it cannot be resolved (invented paths and
// out-of-range indexes are dropped, matching the historical behavior for
// unknown paths).
func (r fileRef) resolvePath(files []FileChange, known map[string]bool) string {
	if r.path != "" && known[r.path] {
		return r.path
	}
	if r.index >= 1 && r.index <= len(files) {
		return files[r.index-1].Path
	}
	if r.path != "" {
		// Unknown path string: return it unchanged — the caller's
		// coverage guard decides, exactly as before the index protocol.
		return r.path
	}
	return ""
}

// composeJSON is the schema for Compose responses.
type composeJSON struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
	Footers []struct {
		Token string `json:"token"`
		Value string `json:"value"`
	} `json:"footers"`
}

// parseClassifyResponse extracts a ClassifyResult from raw bytes.
//
// Strategy:
//  1. Trim whitespace and try json.Unmarshal directly.
//  2. If that fails, strip ```json ... ``` fences and retry.
//  3. If that still fails, scan for the first "{" ... matching "}" block.
//
// Empty groups arrays surface as ErrProviderResponse — an "ok" parse
// with no content is almost always a provider misfire worth retrying
// at a higher level.
func parseClassifyResponse(raw []byte, files []FileChange) (ClassifyResult, error) {
	trimmed := stripFences(strings.TrimSpace(string(raw)))
	var parsed classifyJSON
	if err := tryJSONDecode(trimmed, &parsed); err != nil {
		if errors.Is(err, errTruncatedJSON) {
			return ClassifyResult{}, fmt.Errorf("%w: %w: the AI response was cut off mid-JSON — the provider hit its response token limit. Retry (gk scales the response cap with file count), or split the change: stage a subset, or group it yourself with `gk commit --plan -`", ErrProviderResponse, errTruncatedJSON)
		}
		return ClassifyResult{}, fmt.Errorf("%w: %v", ErrProviderResponse, err)
	}
	if len(parsed.Groups) == 0 {
		return ClassifyResult{}, fmt.Errorf("%w: empty groups", ErrProviderResponse)
	}
	known := make(map[string]bool, len(files))
	for _, f := range files {
		known[f.Path] = true
	}
	out := ClassifyResult{Groups: make([]Group, 0, len(parsed.Groups))}
	for _, g := range parsed.Groups {
		if g.Type == "" || len(g.Files) == 0 {
			continue
		}
		paths := make([]string, 0, len(g.Files))
		for _, ref := range g.Files {
			if p := ref.resolvePath(files, known); p != "" {
				paths = append(paths, p)
			}
		}
		if len(paths) == 0 {
			continue
		}
		out.Groups = append(out.Groups, Group{
			Type:      g.Type,
			Scope:     g.Scope,
			Files:     paths,
			Rationale: g.Rationale,
		})
	}
	if len(out.Groups) == 0 {
		return ClassifyResult{}, fmt.Errorf("%w: no well-formed groups", ErrProviderResponse)
	}
	return out, nil
}

// parseComposeResponse extracts a ComposeResult from raw bytes, falling
// back to a plain-text interpretation (first non-empty line = subject,
// rest = body) when the payload is not JSON. CLI adapters (gemini / qwen /
// kiro) that emit markdown or prose by default rely on this fallback.
//
// JSON-mode HTTP providers must NOT use this — see parseComposeJSON: the
// plain-text fallback would accept a prose misfire ("I'm sorry, …") as a
// one-line subject, masking a broken response that should be retried.
func parseComposeResponse(raw []byte) (ComposeResult, error) {
	trimmed := stripFences(strings.TrimSpace(string(raw)))
	var parsed composeJSON
	if err := tryJSONDecode(trimmed, &parsed); err != nil {
		// Second-chance: plain-text fallback.
		subject, body := splitSubjectBody(strings.TrimSpace(string(raw)))
		if subject == "" {
			return ComposeResult{}, fmt.Errorf("%w: %v", ErrProviderResponse, err)
		}
		return ComposeResult{Subject: subject, Body: body}, nil
	}
	return composeFromJSON(parsed)
}

// parseComposeJSON parses the strict JSON compose shape with NO plain-text
// fallback: any non-JSON payload (or JSON with an empty subject) is an
// ErrProviderResponse. Providers that request response_format=json_object
// (nvidia / openai / groq) use this so a prose reply is retryable instead
// of being silently accepted as a subject by parseComposeResponse.
func parseComposeJSON(raw []byte) (ComposeResult, error) {
	trimmed := stripFences(strings.TrimSpace(string(raw)))
	var parsed composeJSON
	if err := tryJSONDecode(trimmed, &parsed); err != nil {
		return ComposeResult{}, fmt.Errorf("%w: %v", ErrProviderResponse, err)
	}
	return composeFromJSON(parsed)
}

// composeFromJSON validates a decoded composeJSON and projects it onto a
// ComposeResult. Shared by parseComposeResponse and parseComposeJSON so
// the empty-subject check and footer filtering stay identical.
func composeFromJSON(parsed composeJSON) (ComposeResult, error) {
	if parsed.Subject == "" {
		return ComposeResult{}, fmt.Errorf("%w: empty subject", ErrProviderResponse)
	}
	out := ComposeResult{Subject: parsed.Subject, Body: parsed.Body}
	for _, f := range parsed.Footers {
		if f.Token == "" || f.Value == "" {
			continue
		}
		out.Footers = append(out.Footers, Footer{Token: f.Token, Value: f.Value})
	}
	return out, nil
}

// errTruncatedJSON marks a response that started a JSON object but never
// closed it — almost always the model hit its output token limit. Callers
// turn this into actionable guidance (fewer files / higher max_tokens)
// instead of a cryptic "invalid character" message.
var errTruncatedJSON = fmt.Errorf("incomplete JSON object (response likely truncated)")

// tryJSONDecode unmarshals s into v; if that fails it scans for the first
// balanced JSON object and retries. When the text opens a "{" but no
// balanced object is found, it reports errTruncatedJSON so the caller can
// explain the likely cause (truncated output) rather than echoing a raw
// "invalid character '`'"-style parser error.
func tryJSONDecode(s string, v any) error {
	if err := json.Unmarshal([]byte(s), v); err == nil {
		return nil
	}
	if block := firstJSONObject(s); block != "" {
		return json.Unmarshal([]byte(block), v)
	}
	// A "{" with no balanced close means the object was cut off mid-stream.
	if strings.IndexByte(s, '{') >= 0 {
		return errTruncatedJSON
	}
	return json.Unmarshal([]byte(s), v)
}

// fenceRE matches optional ```json ... ``` or ``` ... ``` wrappers.
var fenceRE = regexp.MustCompile("^```(?:json|JSON)?\\s*([\\s\\S]*?)\\s*```$")

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// stripFences removes a surrounding markdown code fence when present.
func stripFences(s string) string {
	if m := fenceRE.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return s
}

func stripANSI(s string) string {
	return ansiRE.ReplaceAllString(s, "")
}

// firstJSONObject returns the first balanced {...} block in s.
// Strings and escaped braces inside strings are respected.
func firstJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' && inStr {
			escape = true
			continue
		}
		if c == '"' {
			inStr = !inStr
			continue
		}
		if inStr {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// splitSubjectBody splits plain text on the first blank line.
func splitSubjectBody(s string) (string, string) {
	// Subject is the first non-empty line; body is everything after a
	// blank-line separator.
	lines := strings.Split(s, "\n")
	subject := ""
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		subject = strings.TrimSpace(line)
		// Find blank-line separator after the subject.
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "" {
				body := strings.TrimSpace(strings.Join(lines[j+1:], "\n"))
				return subject, body
			}
		}
		return subject, ""
	}
	return "", ""
}
