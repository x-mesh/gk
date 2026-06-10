package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/fatih/color"
)

// TestMain forces NoColor for the whole package so tests that scan
// captured stdout for substrings don't have to deal with ANSI bleeding
// in from production code that adopts cell_color helpers. Individual
// tests that need to assert *with* color still set color.NoColor=false
// + Cleanup back to true (see merge_test, errhint_test, log_test).
//
// Without this, tests like TestRunInitConfigWritesToCustomOut run fine
// in isolation but fail under the full package run because a sibling
// test toggled NoColor without restoring it, leaking ANSI into later
// substring assertions.
func TestMain(m *testing.M) {
	// Helper-process mode for rebase integration tests: gk resolves its own
	// binary via os.Executable(), which inside `go test` is this test binary.
	// When git invokes it as GIT_SEQUENCE_EDITOR (`<bin> rebase-todo-editor
	// <todo>`), this branch performs the todo rewrite with full fidelity —
	// same env contract, same copy code — and exits before any test runs.
	if os.Getenv("GK_TEST_SEQUENCE_EDITOR") == "1" {
		if err := rewriteRebaseTodo(os.Args[len(os.Args)-1], os.Getenv(rebaseTodoEnv)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	color.NoColor = true
	flagNoColor = true
	os.Exit(m.Run())
}
