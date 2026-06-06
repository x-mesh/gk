package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

func TestPullHasUnmergedPaths(t *testing.T) {
	withConflict := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --name-only --diff-filter=U": {Stdout: "a.txt\nb.txt\n"},
		},
	}
	if !pullHasUnmergedPaths(context.Background(), withConflict) {
		t.Error("expected unmerged paths to be detected")
	}

	clean := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --name-only --diff-filter=U": {Stdout: ""},
		},
	}
	if pullHasUnmergedPaths(context.Background(), clean) {
		t.Error("expected no unmerged paths on a clean index")
	}
}

// newPullFlagSet builds a cobra.Command with the pull flags relevant to
// validation, so runPullCore's early flag checks can be exercised.
func newPullFlagSet() *cobra.Command {
	cmd := &cobra.Command{Use: "pull"}
	cmd.Flags().String("base", "", "")
	cmd.Flags().String("strategy", "", "")
	cmd.Flags().Bool("rebase", false, "")
	cmd.Flags().Bool("merge", false, "")
	cmd.Flags().Bool("fetch-only", false, "")
	cmd.Flags().Bool("no-rebase", false, "")
	cmd.Flags().Bool("autostash", false, "")
	cmd.Flags().Bool("ai", false, "")
	cmd.Flags().String("repo", "", "")
	return cmd
}

func TestPull_AIWithFetchOnly_Rejected(t *testing.T) {
	cmd := newPullFlagSet()
	_ = cmd.Flags().Set("ai", "true")
	_ = cmd.Flags().Set("fetch-only", "true")

	err := runPullCore(cmd)
	if err == nil || !strings.Contains(err.Error(), "nothing to resolve") {
		t.Fatalf("expected --ai/--fetch-only rejection, got: %v", err)
	}
}
