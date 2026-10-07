package cli

import (
	"slices"
	"testing"

	"github.com/spf13/cobra"
)

func TestCompleteConfigSetKey(t *testing.T) {
	keys, dir := completeConfigSetKey(nil, nil, "")
	if dir != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want NoFileComp", dir)
	}
	if !slices.Contains(keys, "github.owner") {
		t.Errorf("completion missing github.owner")
	}
	if keys, _ := completeConfigSetKey(nil, []string{"github.owner"}, ""); len(keys) != 0 {
		t.Errorf("value position offered %d candidates, want none", len(keys))
	}
}
