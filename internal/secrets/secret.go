package secrets

import (
	"encoding/json"
	"fmt"
)

// Secret holds a secret value that a finding matched. A Finding or a
// Violation carries it, and it must not leak through them into logs, errors,
// or agent output: only Reveal returns the value.
//
// Where fmt can call its methods, it prints masked in every verb: formatted
// directly, through a pointer, or in an exported field. fmt cannot call them
// for %p, for a verb that it rejects for the value, or for a field that is not
// exported; there it prints what the struct holds. So the value is behind a
// pointer, which fmt prints as an address and does not follow. JSON gets the
// masked text.
type Secret struct{ v *string }

// NewSecret wraps a matched value. No value gives the zero Secret.
func NewSecret(v string) Secret {
	if v == "" {
		return Secret{}
	}
	return Secret{v: &v}
}

// Reveal returns the value. Callers print it only when the user asked to see
// it.
func (s Secret) Reveal() string {
	if s.v == nil {
		return ""
	}
	return *s.v
}

func (s Secret) String() string {
	if s.v == nil {
		return ""
	}
	return mask(*s.v)
}

func (s Secret) GoString() string { return s.String() }

// Format prints the masked text for every verb. The verbs for a string (%s,
// %q, %x, %X) apply to it. Any other verb prints it as %s does: without this
// method fmt uses String for %v only, and prints the fields of the struct for
// a verb such as %d.
func (s Secret) Format(f fmt.State, verb rune) {
	switch verb {
	case 's', 'q', 'x', 'X':
	default:
		verb = 's'
	}
	fmt.Fprintf(f, fmt.FormatString(f, verb), s.String())
}

// MarshalJSON keeps the value out of any encoded finding.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }
