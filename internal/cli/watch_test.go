package cli

import (
	"testing"
)

// TestWatchCommandRegistered: `gk watch` (alias `gk w`) resolves, and carries
// the full fleet flag set so the shared resolve helpers find their flags.
func TestWatchCommandRegistered(t *testing.T) {
	for _, name := range []string{"watch", "w"} {
		cmd, _, err := rootCmd.Find([]string{name})
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		if cmd.Name() != "watch" {
			t.Errorf("%q resolved to %q, want watch", name, cmd.Name())
		}
	}

	cmd, _, _ := rootCmd.Find([]string{"watch"})
	for _, flag := range []string{"interval", "repos", "scan", "all", "depth", "feed-stats", "events"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("watch is missing fleet flag --%s", flag)
		}
	}
}
