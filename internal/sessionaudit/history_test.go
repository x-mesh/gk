package sessionaudit

import (
	"path/filepath"
	"testing"
)

func TestHistory_AppendReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "audit-history.jsonl")

	// Missing file reads as empty, not an error.
	if got, err := ReadHistory(path); err != nil || len(got) != 0 {
		t.Fatalf("empty read = %v, %v", got, err)
	}

	e1 := HistoryEntry{Timestamp: "2026-06-30T08:00:00Z", Files: 100, GitTurns: 50, EstimatedTurnsSaved: 8, Rate: 0.16, AdoptionRate: 0.6}
	e2 := HistoryEntry{Timestamp: "2026-06-30T09:00:00Z", Files: 100, GitTurns: 59, EstimatedTurnsSaved: 4, Rate: 0.07, AdoptionRate: 0.61, ByGroup: map[string]int{"context": 2, "diff": 2}}
	if err := AppendHistory(path, e1); err != nil {
		t.Fatal(err)
	}
	if err := AppendHistory(path, e2); err != nil {
		t.Fatal(err)
	}

	got, err := ReadHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2", len(got))
	}
	if got[0].EstimatedTurnsSaved != 8 || got[1].EstimatedTurnsSaved != 4 {
		t.Fatalf("order/values wrong: %+v", got)
	}
	if got[1].ByGroup["diff"] != 2 {
		t.Fatalf("by_group not round-tripped: %+v", got[1])
	}
}

func TestSparkline(t *testing.T) {
	if s := Sparkline(nil); s != "" {
		t.Errorf("empty series = %q, want empty", s)
	}
	// flat series → all lowest block.
	if s := Sparkline([]float64{5, 5, 5}); s != "▁▁▁" {
		t.Errorf("flat = %q, want ▁▁▁", s)
	}
	// monotonic rising: first is lowest block, last is highest.
	s := Sparkline([]float64{0, 1, 2, 3})
	r := []rune(s)
	if len(r) != 4 {
		t.Fatalf("len = %d, want 4", len(r))
	}
	if r[0] != '▁' || r[3] != '█' {
		t.Errorf("rising = %q, want ▁..█", s)
	}
}
