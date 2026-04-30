package easy

import (
	"github.com/x-mesh/gk/internal/i18n"
)

// HintLevel controls the verbosity of contextual hints.
type HintLevel string

const (
	// HintVerbose includes full command descriptions and examples.
	HintVerbose HintLevel = "verbose"
	// HintMinimal shows only the command, brief.
	HintMinimal HintLevel = "minimal"
	// HintOff suppresses all hints.
	HintOff HintLevel = "off"
)

// HintGenerator produces contextual next-action hints based on the
// current hint level, message catalog, and emoji mapper.
type HintGenerator struct {
	level   HintLevel
	catalog *i18n.Catalog
	emoji   *EmojiMapper
}

// NewHintGenerator creates a HintGenerator with the given verbosity
// level, message catalog, and emoji mapper.
func NewHintGenerator(level HintLevel, catalog *i18n.Catalog, emoji *EmojiMapper) *HintGenerator {
	return &HintGenerator{
		level:   level,
		catalog: catalog,
		emoji:   emoji,
	}
}

// Generate looks up a hint key from the catalog and formats it with
// the supplied arguments. Returns "" when the level is HintOff or the
// generator/catalog is nil.
func (g *HintGenerator) Generate(key string, args ...interface{}) string {
	if g == nil || g.level == HintOff || g.catalog == nil {
		return ""
	}

	// For minimal mode, try the ".minimal" suffixed key first.
	if g.level == HintMinimal {
		minKey := key + ".minimal"
		if g.catalog.Has(minKey) {
			return g.catalog.Getf(minKey, args...)
		}
		// Fall through to the regular key if no minimal variant exists.
	}

	return g.catalog.Getf(key, args...)
}

// statusHintKeys defines the priority order for status hints.
// Conflict has the highest priority, then staged, unstaged, untracked.
var statusHintKeys = []struct {
	flag bool   // placeholder — replaced at call time
	key  string // catalog key for the hint
}{
	{key: "hint.status.has_conflict"},
	{key: "hint.status.has_staged"},
	{key: "hint.status.has_unstaged"},
	{key: "hint.status.has_untracked"},
}

// StatusHint generates a contextual next-step hint based on the
// current git working tree state. Priority order:
// conflict > staged > unstaged > untracked.
//
// Returns "" when the level is HintOff, no flags are true, or the
// generator is nil.
func (g *HintGenerator) StatusHint(hasStaged, hasUnstaged, hasUntracked, hasConflict bool) string {
	if g == nil || g.level == HintOff {
		return ""
	}

	flags := []bool{hasConflict, hasStaged, hasUnstaged, hasUntracked}

	for i, f := range flags {
		if f {
			return g.Generate(statusHintKeys[i].key)
		}
	}

	return ""
}

// Level returns the current hint verbosity level.
func (g *HintGenerator) Level() HintLevel {
	if g == nil {
		return HintOff
	}
	return g.level
}
