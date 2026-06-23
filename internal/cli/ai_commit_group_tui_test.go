package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/commitlint"
	"github.com/x-mesh/gk/internal/ui"
)

func TestRemoveByPath(t *testing.T) {
	files := []aicommit.FileChange{{Path: "a"}, {Path: "b"}, {Path: "c"}}

	got := removeByPath(files, []string{"b"})
	if len(got) != 2 || got[0].Path != "a" || got[1].Path != "c" {
		t.Errorf("removeByPath drop b = %+v, want [a c]", got)
	}
	// Input must not be mutated.
	if len(files) != 3 || files[1].Path != "b" {
		t.Errorf("input slice was mutated: %+v", files)
	}

	if got := removeByPath(files, []string{"a", "b", "c"}); len(got) != 0 {
		t.Errorf("removing all = %+v, want empty", got)
	}
	if got := removeByPath(files, []string{"zzz"}); len(got) != 3 {
		t.Errorf("removing nonexistent = %+v, want 3", got)
	}
}

// TestRunCommitGroupTUI_NonTTY guards that the grouping loop surfaces the
// no-TTY error rather than hanging or panicking when there is no terminal —
// the picker (MultiSelectPreviewTUI) returns ui.ErrNonInteractive, which the
// loop propagates so the caller (runAICommitInteractive) can guard on it.
func TestRunCommitGroupTUI_NonTTY(t *testing.T) {
	_, err := runCommitGroupTUI(
		context.Background(),
		[]aicommit.FileChange{{Path: "a.go", Status: "M"}},
		commitlint.Rules{},
	)
	if !errors.Is(err, ui.ErrNonInteractive) {
		t.Fatalf("non-TTY runCommitGroupTUI err = %v, want ErrNonInteractive", err)
	}
}
