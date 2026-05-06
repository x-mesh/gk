package forget

import (
	"slices"
	"testing"
)

func TestFilterKeptDirectMatch(t *testing.T) {
	got, err := FilterKept(
		[]string{"db/data.sqlite", "db/keep/seed.sql", "secrets.json"},
		[]string{"db/keep/*"},
	)
	if err != nil {
		t.Fatalf("FilterKept: %v", err)
	}
	want := []string{"db/data.sqlite", "secrets.json"}
	if !slices.Equal(got, want) {
		t.Errorf("FilterKept = %v, want %v", got, want)
	}
}

func TestFilterKeptDirectoryPrefixMatch(t *testing.T) {
	// Pattern names a directory; entries underneath should be excluded too,
	// matching how a user would expect "keep db/keep" to work.
	got, err := FilterKept(
		[]string{"db/keep/seed.sql", "db/keep/sub/more.sql", "db/data.sqlite"},
		[]string{"db/keep"},
	)
	if err != nil {
		t.Fatalf("FilterKept: %v", err)
	}
	want := []string{"db/data.sqlite"}
	if !slices.Equal(got, want) {
		t.Errorf("FilterKept = %v, want %v", got, want)
	}
}

func TestFilterKeptNoPatterns(t *testing.T) {
	in := []string{"a", "b", "c"}
	got, err := FilterKept(in, nil)
	if err != nil {
		t.Fatalf("FilterKept: %v", err)
	}
	if !slices.Equal(got, in) {
		t.Errorf("FilterKept(no patterns) = %v, want input back", got)
	}
}

func TestFilterKeptMultiplePatterns(t *testing.T) {
	got, err := FilterKept(
		[]string{"db/data", "db/keep/x", "secrets.json", "logs/keep.log"},
		[]string{"db/keep/*", "secrets.json"},
	)
	if err != nil {
		t.Fatalf("FilterKept: %v", err)
	}
	want := []string{"db/data", "logs/keep.log"}
	if !slices.Equal(got, want) {
		t.Errorf("FilterKept = %v, want %v", got, want)
	}
}

func TestFilterKeptInvalidPattern(t *testing.T) {
	// filepath.Match returns ErrBadPattern for unbalanced "[".
	_, err := FilterKept([]string{"db/data"}, []string{"db/[invalid"})
	if err == nil {
		t.Fatal("expected error for invalid glob pattern")
	}
}
