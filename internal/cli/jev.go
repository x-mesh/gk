package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/config"
)

const (
	jevTimeout          = 10 * time.Second
	jevMaxRequestBytes  = 256 << 10
	jevMaxResponseBytes = 1 << 20
	jevMaxTextRunes     = 2048
)

type jevCandidate struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type jevScoreQuestion struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Criteria     []string `json:"criteria"`
}

type jevRequest struct {
	State     any                         `json:"state"`
	Model     string                      `json:"model"`
	Questions map[string]jevScoreQuestion `json:"questions"`
}

type jevScoreAnswer struct {
	Type          string             `json:"type"`
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type jevResponse struct {
	Model   string                    `json:"model"`
	Answers map[string]jevScoreAnswer `json:"answers"`
}

type jevCallInfo struct {
	Model     string
	Requested int
	Evaluated int
	Truncated bool
}

func scoreWithJev(ctx context.Context, cfg config.JevConfig, feature string, state any, candidates []jevCandidate) (map[string]float64, jevCallInfo, error) {
	if err := validateJevConfig(cfg, feature); err != nil {
		return nil, jevCallInfo{}, err
	}
	if len(candidates) == 0 {
		return map[string]float64{}, jevCallInfo{Model: cfg.Model}, nil
	}
	questions := make(map[string]jevScoreQuestion, len(candidates))
	for i, candidate := range candidates {
		questions[candidate.ID] = jevScoreQuestion{
			Type:         "score",
			Instructions: fmt.Sprintf("Evaluate state.candidates[%d] for how directly it answers the user's request.", i),
			Criteria:     []string{"The candidate is unrelated to the request.", "The candidate is partly related to the request.", "The candidate directly solves the request."},
		}
	}
	payload := jevRequest{State: state, Model: cfg.Model, Questions: questions}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, jevCallInfo{}, fmt.Errorf("jev %s: encode request: %w", feature, err)
	}
	redacted, _, err := aicommit.Redact(string(body), aicommit.PrivacyGateOptions{
		DenyPaths:      config.DefaultDenyPaths(),
		SecretPatterns: vendorSecretPatterns,
		MaxSecrets:     -1,
	})
	if err != nil {
		return nil, jevCallInfo{}, fmt.Errorf("jev %s: redact request: %w", feature, err)
	}
	body = []byte(redacted)
	if len(body) > jevMaxRequestBytes {
		return nil, jevCallInfo{}, fmt.Errorf("jev %s: request exceeds %d bytes", feature, jevMaxRequestBytes)
	}
	endpoint, err := jevEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, jevCallInfo{}, fmt.Errorf("jev %s: %w", feature, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, jevTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, jevCallInfo{}, fmt.Errorf("jev %s: create request: %w", feature, err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(req)
	if err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return nil, jevCallInfo{}, fmt.Errorf("jev %s: request timed out", feature)
		}
		if errors.Is(callCtx.Err(), context.Canceled) {
			return nil, jevCallInfo{}, fmt.Errorf("jev %s: request canceled", feature)
		}
		return nil, jevCallInfo{}, fmt.Errorf("jev %s: request failed", feature)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, jevCallInfo{}, fmt.Errorf("jev %s: HTTP status %d", feature, resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, jevMaxResponseBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return nil, jevCallInfo{}, fmt.Errorf("jev %s: read response", feature)
	}
	if len(responseBody) > jevMaxResponseBytes {
		return nil, jevCallInfo{}, fmt.Errorf("jev %s: response exceeds %d bytes", feature, jevMaxResponseBytes)
	}
	if err := decodeJSONUnique(responseBody, &struct{}{}); err != nil {
		return nil, jevCallInfo{}, fmt.Errorf("jev %s: invalid response JSON", feature)
	}
	var parsed jevResponse
	if err := json.Unmarshal(responseBody, &parsed); err != nil {
		return nil, jevCallInfo{}, fmt.Errorf("jev %s: invalid response", feature)
	}
	if strings.TrimSpace(parsed.Model) == "" {
		return nil, jevCallInfo{}, fmt.Errorf("jev %s: response model is empty", feature)
	}
	if len(parsed.Answers) != len(candidates) {
		return nil, jevCallInfo{}, fmt.Errorf("jev %s: response answers do not match requested candidates", feature)
	}
	requested := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		requested[candidate.ID] = true
	}
	result := make(map[string]float64, len(parsed.Answers))
	for id, answer := range parsed.Answers {
		if !requested[id] {
			return nil, jevCallInfo{}, fmt.Errorf("jev %s: response contains an unknown candidate", feature)
		}
		if answer.Type != "score" || !finiteRange(answer.Score, 0, 2) || !finiteRange(answer.Confidence, 0, 1) {
			return nil, jevCallInfo{}, fmt.Errorf("jev %s: invalid score answer", feature)
		}
		if len(answer.Probabilities) != 3 {
			return nil, jevCallInfo{}, fmt.Errorf("jev %s: invalid score probabilities", feature)
		}
		sum := 0.0
		for _, level := range []string{"0", "1", "2"} {
			probability, ok := answer.Probabilities[level]
			if !ok || !finiteRange(probability, 0, 1) {
				return nil, jevCallInfo{}, fmt.Errorf("jev %s: invalid score probabilities", feature)
			}
			sum += probability
		}
		if math.Abs(sum-1) > 1e-6 {
			return nil, jevCallInfo{}, fmt.Errorf("jev %s: score probabilities do not sum to one", feature)
		}
		result[id] = answer.Score
	}
	return result, jevCallInfo{Model: parsed.Model, Requested: len(candidates), Evaluated: len(result)}, nil
}

func validateJevConfig(cfg config.JevConfig, feature string) error {
	if strings.TrimSpace(cfg.Endpoint) == "" || strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.Model) == "" {
		return fmt.Errorf("jev %s: endpoint, api_key, and model are required", feature)
	}
	return nil
}

func jevEndpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("endpoint must be an HTTPS URL without userinfo, query, or fragment")
	}
	if u.Scheme == "https" {
		return u.String(), nil
	}
	if u.Scheme == "http" && isLoopbackHost(u.Hostname()) {
		return u.String(), nil
	}
	return "", errors.New("endpoint must use HTTPS, except for loopback HTTP")
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func finiteRange(value, min, max float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= min && value <= max
}

func utf8Limit(s string, max int) (string, bool) {
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	runes := []rune(s)
	return string(runes[:max]), true
}

func decodeJSONUnique(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if _, err := readJSONValue(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return json.Unmarshal(data, target)
}

func readJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				if _, exists := object[key]; exists {
					return nil, errors.New("duplicate JSON object key")
				}
				value, err := readJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return object, nil
		case '[':
			var values []any
			for decoder.More() {
				value, err := readJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				values = append(values, value)
			}
			if _, err := decoder.Token(); err != nil {
				return nil, err
			}
			return values, nil
		}
	}
	return token, nil
}
