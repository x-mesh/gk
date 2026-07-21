package aicommit

import (
	"reflect"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
)

func TestPairRustModuleGroupsMovesAddedModuleToDeclaration(t *testing.T) {
	groups := []provider.Group{
		{Type: "feat", Files: []string{"crates/core/src/lib.rs"}},
		{Type: "chore", Files: []string{"crates/core/src/proc.rs"}},
	}
	files := []FileChange{
		{Path: "crates/core/src/lib.rs", Status: "modified"},
		{Path: "crates/core/src/proc.rs", Status: "added"},
	}
	diff := "diff --git a/crates/core/src/lib.rs b/crates/core/src/lib.rs\n" +
		"--- a/crates/core/src/lib.rs\n+++ b/crates/core/src/lib.rs\n@@ -1 +1,2 @@\n pub mod old;\n+pub mod proc;\n"

	got := PairRustModuleGroups(groups, files, diff)
	if len(got) != 1 {
		t.Fatalf("groups = %+v, want one cohesive group", got)
	}
	if !reflect.DeepEqual(got[0].Files, []string{"crates/core/src/lib.rs", "crates/core/src/proc.rs"}) {
		t.Fatalf("files = %v", got[0].Files)
	}
}

func TestPairRustModuleGroupsLeavesUnrelatedNewRustFileAlone(t *testing.T) {
	groups := []provider.Group{
		{Type: "feat", Files: []string{"crates/core/src/lib.rs"}},
		{Type: "chore", Files: []string{"crates/core/src/other.rs"}},
	}
	files := []FileChange{
		{Path: "crates/core/src/lib.rs", Status: "modified"},
		{Path: "crates/core/src/other.rs", Status: "added"},
	}
	diff := "diff --git a/crates/core/src/lib.rs b/crates/core/src/lib.rs\n--- a/crates/core/src/lib.rs\n+++ b/crates/core/src/lib.rs\n@@ -1 +1,2 @@\n pub mod old;\n+pub mod proc;\n"

	got := PairRustModuleGroups(groups, files, diff)
	if !reflect.DeepEqual(got, groups) {
		t.Fatalf("unrelated file moved: %+v", got)
	}
}
