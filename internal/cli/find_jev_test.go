package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/config"
)

func TestRerankFindResultBeforeFinalLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Questions map[string]json.RawMessage `json:"questions"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		answers := make(map[string]any, len(request.Questions))
		for id := range request.Questions {
			score := 0.0
			if id == "b" {
				score = 2
			}
			answers[id] = map[string]any{
				"type": "score", "score": score, "confidence": 0.9,
				"probabilities": map[string]float64{"0": 0.1, "1": 0.1, "2": 0.8},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "test-model", "answers": answers})
	}))
	defer server.Close()
	first := time.Now()
	res := findResult{Matches: []findMatch{
		{Hash: "a", Subject: "older", when: first},
		{Hash: "b", Subject: "direct answer", when: first.Add(-time.Second)},
		{Hash: "c", Subject: "tail", when: first.Add(-2 * time.Second)},
	}, allMatches: []findMatch{
		{Hash: "a", Subject: "older", when: first},
		{Hash: "b", Subject: "direct answer", when: first.Add(-time.Second)},
		{Hash: "c", Subject: "tail", when: first.Add(-2 * time.Second)},
	}}
	err := rerankFindResult(context.Background(), &res, findQuery{query: "answer", limit: 1}, config.JevConfig{Endpoint: server.URL, APIKey: "key", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 1 || res.Matches[0].Hash != "b" {
		t.Fatalf("matches = %+v", res.Matches)
	}
	if res.Ranking == nil || res.Ranking.Evaluated != 3 || res.Ranking.CandidateCount != 3 {
		t.Fatalf("ranking = %+v", res.Ranking)
	}
}

func TestRerankFindResultSkipsPathOnly(t *testing.T) {
	res := findResult{Matches: []findMatch{{Hash: "a"}}}
	err := rerankFindResult(context.Background(), &res, findQuery{path: "file.go", limit: 1}, config.JevConfig{})
	if err != nil || res.Ranking == nil || res.Ranking.Skipped != "path-only search" {
		t.Fatalf("err=%v ranking=%+v", err, res.Ranking)
	}
}

func TestRerankFindResultCapsAtFifty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Questions map[string]json.RawMessage `json:"questions"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		answers := make(map[string]any, len(request.Questions))
		for id := range request.Questions {
			answers[id] = map[string]any{"type": "score", "score": 1, "confidence": 1, "probabilities": map[string]float64{"0": 0, "1": 1, "2": 0}}
		}
		_, _ = fmt.Fprintf(w, `{"model":"m","answers":%s}`, mustJSON(t, answers))
	}))
	defer server.Close()
	all := make([]findMatch, 51)
	for i := range all {
		all[i] = findMatch{Hash: fmt.Sprintf("%02d", i), Subject: "subject"}
	}
	res := findResult{Matches: all[:1], allMatches: all}
	if err := rerankFindResult(context.Background(), &res, findQuery{query: "q", limit: 51}, config.JevConfig{Endpoint: server.URL, APIKey: "key", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if res.Ranking == nil || res.Ranking.Evaluated != 50 || !res.Ranking.Limited {
		t.Fatalf("ranking = %+v", res.Ranking)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Before ranking existed gk find never read the config, so a config that fails
// to load must not fail a search that never asked for ranking. The skip is
// reported in res.Ranking, where --json callers can see it.
func TestApplyFindRankingSkipsWhenConfigFailsToLoad(t *testing.T) {
	res := findResult{Query: "ship", Matches: []findMatch{{Hash: "a"}, {Hash: "b"}}, Count: 2}
	err := applyFindRanking(context.Background(), &res, findQuery{query: "ship", limit: 20}, &config.Config{}, errors.New("decoding failed"))
	if err != nil {
		t.Fatalf("applyFindRanking returned %v, want the search to succeed", err)
	}
	if res.Ranking == nil || !strings.Contains(res.Ranking.Skipped, "decoding failed") {
		t.Fatalf("Ranking = %+v, want a skip naming the config error", res.Ranking)
	}
	if res.Count != 2 {
		t.Fatalf("Count = %d, want the unranked matches kept", res.Count)
	}
}

func TestApplyFindRankingIsSilentWhenRerankIsOff(t *testing.T) {
	res := findResult{Query: "ship", Matches: []findMatch{{Hash: "a"}, {Hash: "b"}}, Count: 2}
	if err := applyFindRanking(context.Background(), &res, findQuery{query: "ship", limit: 20}, &config.Config{}, nil); err != nil {
		t.Fatal(err)
	}
	if res.Ranking != nil {
		t.Fatalf("Ranking = %+v, want nil when find_rerank is off", res.Ranking)
	}
}
