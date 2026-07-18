package cli

import "testing"

// TestInboxSearchQuery covers the type-narrowing and mutual-exclusion logic
// of `gk inbox` without needing a token or network.
func TestInboxSearchQuery(t *testing.T) {
	if _, err := inboxSearchQuery(true, true, "open"); err == nil {
		t.Error("--pr and --issue together must be an error")
	}
	cases := []struct {
		name              string
		onlyPR, onlyIssue bool
		state, want       string
	}{
		{"pr only", true, false, "open", "involves:@me is:pr is:open"},
		{"issue only", false, true, "open", "involves:@me is:issue is:open"},
		{"untyped open", false, false, "open", "involves:@me is:open"},
		{"untyped all", false, false, "all", "involves:@me"},
	}
	for _, tc := range cases {
		got, err := inboxSearchQuery(tc.onlyPR, tc.onlyIssue, tc.state)
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: query = %q, want %q", tc.name, got, tc.want)
		}
	}
}
