package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// classifyJSON is the schema adapters ask the model to emit for
// Classify. Fields are all optional on the wire so a partial response
// is still parseable — callers re-validate downstream.
type classifyJSON struct {
	Groups []struct {
		Type      string   `json:"type"`
		Scope     string   `json:"scope"`
		Files     []string `json:"files"`
		Rationale string   `json:"rationale"`
	} `json:"groups"`
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
func parseClassifyResponse(raw []byte) (ClassifyResult, error) {
	trimmed := stripFences(strings.TrimSpace(string(raw)))
	var parsed classifyJSON
	if err := tryJSONDecode(trimmed, &parsed); err != nil {
		return ClassifyResult{}, fmt.Errorf("%w: %v", ErrProviderResponse, err)
	}
	if len(parsed.Groups) == 0 {
		return ClassifyResult{}, fmt.Errorf("%w: empty groups", ErrProviderResponse)
	}
	out := ClassifyResult{Groups: make([]Group, 0, len(parsed.Groups))}
	for _, g := range parsed.Groups {
		if g.Type == "" || len(g.Files) == 0 {
			continue
		}
		out.Groups = append(out.Groups, Group{
			Type:      g.Type,
			Scope:     g.Scope,
			Files:     append([]string(nil), g.Files...),
			Rationale: g.Rationale,
		})
	}
	if len(out.Groups) == 0 {
		return ClassifyResult{}, fmt.Errorf("%w: no well-formed groups", ErrProviderResponse)
	}
	return out, nil
}

// parseComposeResponse extracts a ComposeResult from raw bytes.
func parseComposeResponse(raw []byte) (ComposeResult, error) {
	trimmed := stripFences(strings.TrimSpace(string(raw)))
	var parsed composeJSON
	if err := tryJSONDecode(trimmed, &parsed); err != nil {
		// Second-chance: plain-text fallback — treat the first non-empty
		// line as the subject, rest as body. Adapters that can't emit
		// JSON (or return markdown by default) lean on this.
		subject, body := splitSubjectBody(strings.TrimSpace(string(raw)))
		if subject == "" {
			return ComposeResult{}, fmt.Errorf("%w: %v", ErrProviderResponse, err)
		}
		return ComposeResult{Subject: subject, Body: body}, nil
	}
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

// tryJSONDecode unmarshals s into v; if that fails, scans for the
// first JSON object in the string and retries.
func tryJSONDecode(s string, v any) error {
	if err := json.Unmarshal([]byte(s), v); err == nil {
		return nil
	}
	if block := firstJSONObject(s); block != "" {
		if err := json.Unmarshal([]byte(block), v); err == nil {
			return nil
		}
	}
	return json.Unmarshal([]byte(s), v)
}

// fenceRE matches optional ```json ... ``` or ``` ... ``` wrappers.
var fenceRE = regexp.MustCompile("^```(?:json|JSON)?\\s*([\\s\\S]*?)\\s*```$")

// stripFences removes a surrounding markdown code fence when present.
func stripFences(s string) string {
	if m := fenceRE.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return s
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
