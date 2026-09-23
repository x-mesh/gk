package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestUTF8LimitPreservesRuneBoundary(t *testing.T) {
	got, truncated := utf8Limit("가나다라마바사", 3)
	if got != "가나다" || !truncated {
		t.Fatalf("got %q truncated=%v", got, truncated)
	}
}
