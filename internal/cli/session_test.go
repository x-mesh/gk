package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRunSessionAudit_HumanOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	line := `{"payload":{"arguments":"{\"cmd\":\"git status --short && git log --oneline -5\"}"}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	prevJSON := flagJSON
	flagJSON = false
	t.Cleanup(func() { flagJSON = prevJSON })

	cmd := &cobra.Command{}
	cmd.Flags().Int("max-files", 200, "")
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := runSessionAudit(cmd, []string{path}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"session audit: 1 files", "raw-context-probes", "git-kit context"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}
