package ui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/x-mesh/gk/internal/easy"
)

// ansiPattern matches ANSI escape sequences used for terminal styling.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// ANSI escape codes for bold text.
const (
	ansiBold  = "\033[1m"
	ansiReset = "\033[0m"
)

// EasyFormatter provides consistent formatting for Easy Mode output.
// It handles emoji insertion, bold section headers, command boxes,
// and error formatting while respecting --no-color mode.
type EasyFormatter struct {
	emoji   *easy.EmojiMapper
	noColor bool
}

// NewEasyFormatter creates an EasyFormatter with the given emoji mapper
// and noColor setting. When noColor is true, ANSI escape sequences are
// stripped from all output while preserving emoji and text structure.
func NewEasyFormatter(emoji *easy.EmojiMapper, noColor bool) *EasyFormatter {
	return &EasyFormatter{
		emoji:   emoji,
		noColor: noColor,
	}
}

// SectionHeader returns a formatted section header with an emoji prefix
// and bold text. Format: "{emoji} {bold}{text}{reset}".
// When noColor is true, ANSI codes are removed: "{emoji} {text}".
func (f *EasyFormatter) SectionHeader(emojiKey, text string) string {
	prefix := ""
	if f.emoji != nil {
		prefix = f.emoji.Prefix(emojiKey)
	}

	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString(ansiBold)
	b.WriteString(text)
	b.WriteString(ansiReset)

	return f.stripIfNoColor(b.String())
}

// CommandBox returns a command suggestion formatted as an indented block.
// Format:
//
//	→ {cmd}
//	  {description}
//
// The description line is omitted when description is empty.
func (f *EasyFormatter) CommandBox(cmd, description string) string {
	var b strings.Builder
	b.WriteString("  → ")
	b.WriteString(cmd)
	if description != "" {
		b.WriteByte('\n')
		b.WriteString("    ")
		b.WriteString(description)
	}
	return f.stripIfNoColor(b.String())
}

// FormatError formats an error with easy language and a resolution hint.
// Format:
//
//	{error emoji} {error message}
//	{hint emoji} {hint}
//
// The hint line is omitted when hint is empty.
func (f *EasyFormatter) FormatError(err error, hint string) string {
	errPrefix := ""
	if f.emoji != nil {
		errPrefix = f.emoji.Prefix("error")
	}

	var b strings.Builder
	b.WriteString(errPrefix)
	b.WriteString(err.Error())

	if hint != "" {
		hintPrefix := ""
		if f.emoji != nil {
			hintPrefix = f.emoji.Prefix("hint")
		}
		b.WriteByte('\n')
		b.WriteString(hintPrefix)
		b.WriteString(hint)
	}

	return f.stripIfNoColor(b.String())
}

// stripIfNoColor removes ANSI escape sequences from s when noColor is true.
// Emoji and text structure are preserved.
func (f *EasyFormatter) stripIfNoColor(s string) string {
	if !f.noColor {
		return s
	}
	return stripANSI(s)
}

// stripANSI removes all ANSI escape sequences from the given string.
func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}

// FormatJSON is a no-op passthrough for --json mode. When JSON mode is
// active, callers should use this instead of other formatting methods
// to ensure no Easy Mode formatting is applied.
func FormatJSON(data string) string {
	return data
}

// IsJSONMode checks whether the given format string indicates JSON output.
// Callers can use this to decide whether to apply Easy Mode formatting.
func IsJSONMode(format string) bool {
	return strings.EqualFold(format, "json")
}

// StripANSI is the exported version of stripANSI for use by other packages.
func StripANSI(s string) string {
	return stripANSI(s)
}

// Bold wraps text in ANSI bold escape codes.
// Returns plain text when noColor is true.
func Bold(text string, noColor bool) string {
	if noColor {
		return text
	}
	return fmt.Sprintf("%s%s%s", ansiBold, text, ansiReset)
}
