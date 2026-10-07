package ui

import (
	"os"

	"github.com/mattn/go-runewidth"
)

// go-runewidth sizes East Asian ambiguous runes (…, ▸, →, ◌, ●) as two cells
// whenever LANG is a CJK locale, while lipgloss sizes them as one and common
// terminals draw them as one. Mixing the two misaligns tables and truncation
// by a cell per ambiguous rune under ko_KR/ja_JP/zh_CN. Pin the narrow width
// unless the user asked for wide via runewidth's own RUNEWIDTH_EASTASIAN=1,
// for terminals configured to draw ambiguous runes wide.
func init() {
	if os.Getenv("RUNEWIDTH_EASTASIAN") != "" {
		return
	}
	runewidth.EastAsianWidth = false
	runewidth.DefaultCondition.EastAsianWidth = false
}
