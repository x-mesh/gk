package cli

import (
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
	color.NoColor = true
	flagNoColor = true
	os.Exit(m.Run())
}
