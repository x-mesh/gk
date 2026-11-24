package secrets

import "encoding/json"

// Secret holds a secret value that a finding matched. It formats masked in
// every fmt verb and in JSON, so a Finding or a Violation can carry it
// without leaking it into logs, errors, or agent output; only Reveal returns
// the value.
type Secret struct{ v string }

// NewSecret wraps a matched value.
func NewSecret(v string) Secret { return Secret{v: v} }

// Reveal returns the value. Callers print it only when the user asked to see
// it.
func (s Secret) Reveal() string { return s.v }

func (s Secret) String() string {
	if s.v == "" {
		return ""
	}
	return mask(s.v)
}

func (s Secret) GoString() string { return s.String() }

// MarshalJSON keeps the value out of any encoded finding.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }
