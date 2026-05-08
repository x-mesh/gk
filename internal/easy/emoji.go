package easy

// defaultEmojis maps status keys to their visual marker. Despite the
// historical name (kept for compatibility) the values are now plain
// Unicode symbols that render uniformly across terminals — emoji
// renderers vary in width and several environments box-substitute the
// glyphs entirely. The marker set below is a one-cell, monochrome
// alternative chosen for legibility.
var defaultEmojis = map[string]string{
	"success":  "✓",
	"warning":  "⚠",
	"error":    "✗",
	"conflict": "‼",
	"new":      "?",
	"modified": "~",
	"deleted":  "−",
	"staged":   "+",
	"push":     "↑",
	"pull":     "↓",
	"branch":   "▸",
	"merge":    "↕",
	"hint":     "→",
}

// EmojiMapper manages status-to-emoji mappings. When disabled, all
// methods return empty strings so callers don't need conditional logic.
type EmojiMapper struct {
	enabled bool
	emojis  map[string]string
}

// NewEmojiMapper creates an EmojiMapper pre-loaded with the default
// emoji set. When enabled is false, Get and Prefix always return "".
func NewEmojiMapper(enabled bool) *EmojiMapper {
	m := &EmojiMapper{
		enabled: enabled,
		emojis:  make(map[string]string, len(defaultEmojis)),
	}
	for k, v := range defaultEmojis {
		m.emojis[k] = v
	}
	return m
}

// Get returns the emoji for the given status key.
// Returns "" if the mapper is disabled or the key is unknown.
func (m *EmojiMapper) Get(key string) string {
	if m == nil || !m.enabled {
		return ""
	}
	return m.emojis[key]
}

// Prefix returns the emoji followed by a space for the given key.
// Returns "" if the mapper is disabled or the key is unknown.
func (m *EmojiMapper) Prefix(key string) string {
	e := m.Get(key)
	if e == "" {
		return ""
	}
	return e + " "
}
