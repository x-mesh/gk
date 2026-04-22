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

	SetVersionInfo("v9.9.9", "deadbee", "2026-01-01T00:00:00Z")

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
