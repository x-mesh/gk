package cli

import (
	"strings"
	"testing"
)

func TestCountSquashDebt(t *testing.T) {
	isWIP := func(s string) bool {
		return strings.HasPrefix(s, "WIP") || s == "save" || s == "tmp"
	}

	fixup, wip := countSquashDebt(strings.Join([]string{
		"fixup! feat: login",
		"squash! refactor",
		"WIP: form validation",
		"save",
		"feat: real work", // not debt
		"",                // blank lines tolerated
	}, "\n"), isWIP)
	if fixup != 2 || wip != 2 {
		t.Errorf("countSquashDebt = (%d fixup, %d wip), want (2, 2)", fixup, wip)
	}

	// fixup wins over WIP when a subject is both — counted once.
	fixup, wip = countSquashDebt("fixup! WIP: thing", isWIP)
	if fixup != 1 || wip != 0 {
		t.Errorf("dual-match = (%d, %d), want (1, 0)", fixup, wip)
	}

	if fixup, wip = countSquashDebt("", isWIP); fixup+wip != 0 {
		t.Errorf("empty input must count zero, got (%d, %d)", fixup, wip)
	}
}

func TestPorcelainPathOverlap(t *testing.T) {
	dirty := map[string]bool{
		"app.go":     true,
		"lib.go":     true,
		"new-name.go": true,
		"a b.txt":    true,
	}
	porcelain := strings.Join([]string{
		" M app.go",            // overlaps
		"?? other.go",          // not in dirty set
		"R  old.go -> new-name.go", // rename: new side overlaps
		` M "a b.txt"`,         // quoted path
		"M  lib.go",
		"",
	}, "\n")

	got := porcelainPathOverlap(porcelain, dirty)
	want := []string{"a b.txt", "app.go", "lib.go", "new-name.go"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("overlap = %v, want %v", got, want)
	}

	if got := porcelainPathOverlap("?? clean.go\n", dirty); got != nil {
		t.Errorf("no overlap must return nil, got %v", got)
	}
}

func TestFormatCollisionLine(t *testing.T) {
	line := formatCollisionLine("develop", []string{"a.go", "b.go"})
	for _, want := range []string{"⊠", "2 files", "develop", "a.go, b.go"} {
		if !strings.Contains(line, want) {
			t.Errorf("collision line missing %q:\n%s", want, line)
		}
	}

	// Singular + truncation past three names.
	if line := formatCollisionLine("wt", []string{"only.go"}); !strings.Contains(line, "1 file also") {
		t.Errorf("singular form broken:\n%s", line)
	}
	line = formatCollisionLine("wt", []string{"a", "b", "c", "d", "e"})
	if !strings.Contains(line, "+2") || strings.Contains(line, "d, e") {
		t.Errorf("name list must truncate to 3 (+N):\n%s", line)
	}
}
