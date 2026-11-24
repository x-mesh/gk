package policy

import (
	"fmt"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/secrets"
)

// A Violation carries the value of a secret under --reveal. A report, a log
// line, or an error that formats the violation must not show the value, in
// any verb.
func TestViolationFormatsItsSecretMasked(t *testing.T) {
	raw := "ghp_4fT9kL2mQ8wX1zR7vB3nC6yH0pJ5sD8gA2eW"
	tail := raw[len(raw)-12:]
	v := Violation{RuleID: "secret_patterns", Message: "m", Secret: secrets.NewSecret(raw)}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d", "%t", "%p"} {
		for name, arg := range map[string]any{"a violation": v, "a pointer": &v, "violations": []Violation{v}} {
			out := fmt.Sprintf(verb, arg)
			if strings.Contains(out, tail) || strings.Contains(out, fmt.Sprintf("%x", tail)) {
				t.Errorf("%s with %s prints the value: %s", name, verb, out)
			}
		}
	}
	if out := fmt.Sprintf("%+v", v); !strings.Contains(out, "Secret:"+v.Secret.String()) || v.Secret.String() == "" {
		t.Errorf("a violation does not print its secret masked: %s", out)
	}
}
