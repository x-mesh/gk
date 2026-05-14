package cli

import "testing"

// TestParseDiffRefs covers the ref-label extraction shipped with the
// `gk diff` "◀/▶ ref" UX. The mapping mirrors git diff's positional
// semantics — see parseDiffRefs's doc comment for the table.
func TestParseDiffRefs(t *testing.T) {
	cases := []struct {
		name      string
		staged    bool
		args      []string
		wantLeft  string
		wantRight string
	}{
		{"no args", false, nil, "index", "working tree"},
		{"staged", true, nil, "HEAD", "index"},
		{"staged wins over args", true, []string{"foo"}, "HEAD", "index"},
		{"single ref", false, []string{"main"}, "main", "working tree"},
		{"two-dot range", false, []string{"develop..origin/main"}, "develop", "origin/main"},
		{"three-dot range", false, []string{"main...feat"}, "main...", "feat"},
		{"two refs", false, []string{"main", "feat"}, "main", "feat"},
		{"flag is skipped", false, []string{"-w", "main"}, "main", "working tree"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotLeft, gotRight := parseDiffRefs(tc.staged, tc.args)
			if gotLeft != tc.wantLeft || gotRight != tc.wantRight {
				t.Fatalf("parseDiffRefs(staged=%v, %v) = (%q, %q); want (%q, %q)",
					tc.staged, tc.args, gotLeft, gotRight, tc.wantLeft, tc.wantRight)
			}
		})
	}
}
