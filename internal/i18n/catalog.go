// Package i18n provides a key-value message catalogue with per-language,
// per-mode (normal / easy) message storage and a deterministic fallback
// chain.
package i18n

import "fmt"

// Mode represents a message variant.
type Mode string

const (
	// ModeNormal is the standard technical message variant.
	ModeNormal Mode = "normal"
	// ModeEasy is the beginner-friendly message variant.
	ModeEasy Mode = "easy"
)

// registry is the package-level store populated by RegisterMessages.
// Structure: lang → key → mode → message.
var registry = map[string]map[string]map[Mode]string{}

// RegisterMessages adds (or merges) messages for the given language into
// the global registry. Callers typically invoke this from init() in
// per-language source files (e.g. ko.go, en.go).
func RegisterMessages(lang string, msgs map[string]map[Mode]string) {
	if registry[lang] == nil {
		registry[lang] = make(map[string]map[Mode]string, len(msgs))
	}
	for k, v := range msgs {
		registry[lang][k] = v
	}
}

// Catalog is a per-language, per-mode message store with an optional
// fallback catalogue (typically the "en" catalogue).
type Catalog struct {
	lang     string
	mode     Mode
	messages map[string]map[Mode]string // key → mode → message
	fallback *Catalog                   // fallback catalogue (en)
}

// New creates a Catalog for the given language and mode.
// Messages are looked up from the global registry. If lang is not "en",
// an English fallback catalogue is attached automatically.
func New(lang string, mode Mode) *Catalog {
	c := &Catalog{
		lang:     lang,
		mode:     mode,
		messages: registry[lang], // may be nil if lang not registered
	}

	// Attach an English fallback when the requested language is not
	// English itself.
	if lang != "en" {
		c.fallback = &Catalog{
			lang:     "en",
			mode:     mode,
			messages: registry["en"],
		}
	}

	return c
}

// Get returns the message for key following the fallback chain:
//  1. Current lang, requested mode (e.g. easy)
//  2. Current lang, normal mode
//  3. Fallback lang (en), requested mode
//  4. Fallback lang (en), normal mode
//  5. The key itself
func (c *Catalog) Get(key string) string {
	// Step 1: current lang, requested mode
	if msg := lookup(c.messages, key, c.mode); msg != "" {
		return msg
	}

	// Step 2: current lang, normal mode
	if c.mode != ModeNormal {
		if msg := lookup(c.messages, key, ModeNormal); msg != "" {
			return msg
		}
	}

	// Steps 3 & 4: fallback catalogue (en)
	if c.fallback != nil {
		// Step 3: fallback lang, requested mode
		if msg := lookup(c.fallback.messages, key, c.fallback.mode); msg != "" {
			return msg
		}
		// Step 4: fallback lang, normal mode
		if c.fallback.mode != ModeNormal {
			if msg := lookup(c.fallback.messages, key, ModeNormal); msg != "" {
				return msg
			}
		}
	}

	// Step 5: return the key itself
	return key
}

// Getf is a convenience wrapper that calls Get and then applies
// fmt.Sprintf with the supplied arguments.
func (c *Catalog) Getf(key string, args ...interface{}) string {
	msg := c.Get(key)
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

// Has reports whether key exists in the current catalogue's messages
// (regardless of mode). It does NOT check the fallback catalogue.
func (c *Catalog) Has(key string) bool {
	if c.messages == nil {
		return false
	}
	_, ok := c.messages[key]
	return ok
}

// lookup is a nil-safe helper that retrieves a message from a messages
// map for the given key and mode. Returns "" when not found.
func lookup(msgs map[string]map[Mode]string, key string, mode Mode) string {
	if msgs == nil {
		return ""
	}
	modes, ok := msgs[key]
	if !ok {
		return ""
	}
	return modes[mode]
}
