package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/x-mesh/gk/internal/config"
)

func TestScoreWithJevBuildsBoundedScoreRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Errorf("authorization = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var request struct {
			State     map[string]any              `json:"state"`
			Model     string                      `json:"model"`
			Questions map[string]jevScoreQuestion `json:"questions"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "test-model" || len(request.Questions) != 2 {
			t.Fatalf("request = %+v", request)
		}
		if !strings.Contains(request.Questions["a"].Instructions, "state.candidates[0]") {
			t.Fatalf("question instructions = %+v", request.Questions)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"test-model","answers":{"a":{"type":"score","score":2,"confidence":0.9,"probabilities":{"0":0.01,"1":0.09,"2":0.9}},"b":{"type":"score","score":0.5,"confidence":0.8,"probabilities":{"0":0.5,"1":0.4,"2":0.1}}}}`)
	}))
	defer server.Close()
	scores, info, err := scoreWithJev(context.Background(), config.JevConfig{
		Endpoint: server.URL, APIKey: "test-secret", Model: "test-model",
	}, "test", map[string]any{"candidates": []any{"one", "two"}}, []jevCandidate{{ID: "a"}, {ID: "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if scores["a"] != 2 || scores["b"] != 0.5 || info.Evaluated != 2 {
		t.Fatalf("scores=%v info=%+v", scores, info)
	}
}

func TestScoreWithJevRejectsUnsafeEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"http://example.com/systemone",
		"https://example.com/systemone?token=secret",
		"https://user@example.com/systemone",
	} {
		_, _, err := scoreWithJev(context.Background(), config.JevConfig{
			Endpoint: endpoint, APIKey: "key", Model: "model",
		}, "test", nil, []jevCandidate{{ID: "a"}})
		if err == nil {
			t.Errorf("endpoint %q must fail", endpoint)
		}
	}
}

func TestScoreWithJevRejectsDuplicateOrUnknownAnswers(t *testing.T) {
	for _, response := range []string{
		`{"model":"m","answers":{"a":{"type":"score","score":1,"confidence":1,"probabilities":{"0":0.2,"1":0.6,"2":0.2}},"a":{"type":"score","score":1,"confidence":1,"probabilities":{"0":0.2,"1":0.6,"2":0.2}}}}`,
		`{"model":"m","answers":{"b":{"type":"score","score":1,"confidence":1,"probabilities":{"0":0.2,"1":0.6,"2":0.2}}}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, response)
		}))
		_, _, err := scoreWithJev(context.Background(), config.JevConfig{Endpoint: server.URL, APIKey: "key", Model: "m"}, "test", nil, []jevCandidate{{ID: "a"}})
		server.Close()
		if err == nil {
			t.Errorf("response %q must fail", response)
		}
	}
}

func TestScoreWithJevAllowsRoundedProbabilitySum(t *testing.T) {
	for _, tc := range []struct {
		name          string
		probabilities map[string]float64
		wantError     bool
	}{
		{name: "rounded values", probabilities: map[string]float64{"0": 0.3333, "1": 0.3333, "2": 0.3333}},
		{name: "invalid sum", probabilities: map[string]float64{"0": 0.32, "1": 0.32, "2": 0.32}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				response := jevResponse{
					Model: "test-model",
					Answers: map[string]jevScoreAnswer{
						"a": {Type: "score", Score: 0.9999, Confidence: 0.5, Probabilities: tc.probabilities},
					},
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			_, _, err := scoreWithJev(context.Background(), config.JevConfig{
				Endpoint: server.URL,
				APIKey:   "test-key",
				Model:    "test-model",
			}, "find", map[string]string{"query": "stop"}, []jevCandidate{{ID: "a"}})
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError %v", err, tc.wantError)
			}
		})
	}
}

func TestScoreWithJevDebugShowsResponseAnswers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"model":"jev-1.13.0","answers":{"candidate":{"type":"score","score":1.25,"confidence":0.6,"probabilities":{"0":0.1,"1":0.45,"2":0.45}}}}`)
	}))
	defer server.Close()

	var output bytes.Buffer
	previousWriter := SetDebugWriter(&output)
	previousDebug := flagDebug
	previousNoColor := color.NoColor
	flagDebug = true
	color.NoColor = true
	t.Cleanup(func() {
		flagDebug = previousDebug
		color.NoColor = previousNoColor
		SetDebugWriter(previousWriter)
	})

	_, _, err := scoreWithJev(context.Background(), config.JevConfig{
		Endpoint: server.URL,
		APIKey:   "test-key",
		Model:    "test-model",
	}, "find", nil, []jevCandidate{{ID: "candidate"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`jev find response: model="jev-1.13.0" answers=1`,
		`id="candidate" type="score" score=1.250000 confidence=0.600000`,
		`probabilities=[0.100000, 0.450000, 0.450000] sum=1.000000`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("debug output does not contain %q:\n%s", want, output.String())
		}
	}
}

func TestScoreWithJevDebugShowsRedactedHTTPError(t *testing.T) {
	const secret = "sk-abcdefghijklmnopqrstuvwxyz123456"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"question limit exceeded","api_key":"`+secret+`"}`)
	}))
	defer server.Close()

	var output bytes.Buffer
	previousWriter := SetDebugWriter(&output)
	previousDebug := flagDebug
	previousNoColor := color.NoColor
	flagDebug = true
	color.NoColor = true
	t.Cleanup(func() {
		flagDebug = previousDebug
		color.NoColor = previousNoColor
		SetDebugWriter(previousWriter)
	})

	_, _, err := scoreWithJev(context.Background(), config.JevConfig{
		Endpoint: server.URL,
		APIKey:   "test-key",
		Model:    "test-model",
	}, "suggest", nil, []jevCandidate{{ID: "candidate"}})
	if err == nil || !strings.Contains(err.Error(), "HTTP status 400") {
		t.Fatalf("error = %v, want HTTP 400", err)
	}
	got := output.String()
	for _, want := range []string{"questions=1", "HTTP 400", "question limit exceeded"} {
		if !strings.Contains(got, want) {
			t.Errorf("debug output does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, secret) {
		t.Errorf("debug output leaked response secret: %s", got)
	}
}

func TestUTF8LimitPreservesRuneBoundary(t *testing.T) {
	got, truncated := utf8Limit("가나다라마바사", 3)
	if got != "가나다" || !truncated {
		t.Fatalf("got %q truncated=%v", got, truncated)
	}
}
