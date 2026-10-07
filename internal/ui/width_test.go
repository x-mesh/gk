package ui

import (
	"os"
	"testing"

	"github.com/mattn/go-runewidth"
)

// Run under LANG=ko_KR.UTF-8 this is the regression: without the pin,
// go-runewidth reads the locale and sizes the ellipsis as two cells.
func TestAmbiguousRunesAreNarrowByDefault(t *testing.T) {
	if os.Getenv("RUNEWIDTH_EASTASIAN") != "" {
		t.Skip("RUNEWIDTH_EASTASIAN overrides the default on purpose")
	}
	for _, r := range "…▸→◌●" {
		if w := runewidth.RuneWidth(r); w != 1 {
			t.Errorf("RuneWidth(%q) = %d, want 1", r, w)
		}
	}
	if w := runewidth.RuneWidth('한'); w != 2 {
		t.Errorf("RuneWidth('한') = %d, want 2: Hangul stays wide", w)
	}
}
