package cli

import (
	"strings"
	"testing"
)

// TestSetVersionInfo_PrependsHeader verifies that the help page's Long
// description includes the version line after SetVersionInfo is called.
func TestSetVersionInfo_PrependsHeader(t *testing.T) {
	origVersion := rootCmd.Version
	origLong := rootCmd.Long
	t.Cleanup(func() {
		rootCmd.Version = origVersion
		rootCmd.Long = origLong
	})

	SetVersionInfo("v9.9.9", "deadbee", "2026-01-01T00:00:00Z", "", "")

	if !strings.Contains(rootCmd.Version, "v9.9.9") {
		t.Errorf("Version missing tag: %q", rootCmd.Version)
	}
	if !strings.HasPrefix(rootCmd.Long, "gk v9.9.9 (commit deadbee, built 2026-01-01T00:00:00Z)") {
		t.Errorf("Long missing version header:\n%s", rootCmd.Long)
	}
	if !strings.Contains(rootCmd.Long, rootLongDesc) {
		t.Errorf("Long missing base description:\n%s", rootCmd.Long)
	}
}

// TestResolveInvocationName checks that only the names gk actually ships
// under are echoed back, and that everything else — crucially the *.test
// binary that drives this very package — falls back to the canonical "gk"
// so help output stays stable under `go test`.
func TestResolveInvocationName(t *testing.T) {
	cases := map[string]string{
		"/usr/local/bin/gk":          "gk",
		"/opt/homebrew/bin/git-kit":  "git-kit",
		"git-kit":                    "git-kit",
		"git-kit.exe":                "git-kit",
		"/Users/x/.local/bin/gk-dev": "gk-dev",
		"/tmp/go-build123/cli.test":  "gk", // test binary → stable fallback
		"gitk":                       "gk", // not us — never claim it
		"":                           "gk",
	}
	for arg0, want := range cases {
		if got := resolveInvocationName(arg0); got != want {
			t.Errorf("resolveInvocationName(%q) = %q, want %q", arg0, got, want)
		}
	}
}

// TestRenderRootLong_UsesInvocationName guards P3: the root --help header
// must follow the invocation name, so `git-kit --help` reads "git-kit vX …"
// rather than the "gk vX …" baked in by SetVersionInfo at startup.
func TestRenderRootLong_UsesInvocationName(t *testing.T) {
	long := renderRootLong("git-kit", "v9.9.9", "deadbee", "2026-01-01T00:00:00Z", "")
	if !strings.HasPrefix(long, "git-kit v9.9.9 (commit deadbee, built 2026-01-01T00:00:00Z)") {
		t.Errorf("header not rendered under invocation name:\n%s", long)
	}
	if !strings.Contains(long, rootLongDesc) {
		t.Errorf("missing base description:\n%s", long)
	}
}
