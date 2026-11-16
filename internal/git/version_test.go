package git

import "testing"

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in      string
		wantMaj int
		wantMin int
	}{
		{"git version 2.54.0", 2, 54},
		{"git version 2.36.1", 2, 36},
		{"git version 2.35.8", 2, 35},
		{"", 0, 0},
		{"git version", 0, 0},
	}
	for _, c := range cases {
		maj, min := ParseVersion(c.in)
		if maj != c.wantMaj || min != c.wantMin {
			t.Errorf("ParseVersion(%q) = %d.%d, want %d.%d", c.in, maj, min, c.wantMaj, c.wantMin)
		}
	}
}

func TestHistoryLogMergeDiffFlag(t *testing.T) {
	if got := HistoryLogMergeDiffFlag(2, 36); got != "--remerge-diff" {
		t.Errorf("2.36 = %q, want --remerge-diff", got)
	}
	if got := HistoryLogMergeDiffFlag(2, 35); got != "--cc" {
		t.Errorf("2.35 = %q, want --cc", got)
	}
	if got := HistoryLogMergeDiffFlag(3, 0); got != "--remerge-diff" {
		t.Errorf("3.0 = %q, want --remerge-diff", got)
	}
}
