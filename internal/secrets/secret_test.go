package secrets

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A Secret prints masked in every format; only Reveal returns the value.
func TestSecretFormatsMasked(t *testing.T) {
	raw := "ghp_4fT9kL2mQ8wX1zR7vB3nC6yH0pJ5sD8gA2eW"
	s := NewSecret(raw)
	if s.Reveal() != raw {
		t.Fatalf("Reveal = %q", s.Reveal())
	}
	if s.String() != mask(raw) {
		t.Errorf("String = %q, want %q", s.String(), mask(raw))
	}
	f := Finding{Kind: "github-pat", File: "config.env", FileLine: 3, Sample: mask(raw), Secret: s}
	js, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	sj, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{
		"%v": fmt.Sprint(s), "%+v finding": fmt.Sprintf("%+v", f), "%#v finding": fmt.Sprintf("%#v", f),
		"%v finding": fmt.Sprintf("%v", f), "json finding": string(js), "json secret": string(sj),
	} {
		if strings.Contains(out, raw) || strings.Contains(out, raw[4:]) {
			t.Errorf("%s prints the value: %s", name, out)
		}
	}
	if NewSecret("").String() != "" || NewSecret("").Reveal() != "" {
		t.Errorf("the zero secret prints %q", NewSecret("").String())
	}
}

// The gk patterns keep the value they mask.
func TestScanKeepsTheMaskedValue(t *testing.T) {
	fs := Scan(`token = "ghp_4fT9kL2mQ8wX1zR7vB3nC6yH0pJ5sD8gA2eW"`, nil)
	if len(fs) == 0 {
		t.Fatal("no finding")
	}
	for _, f := range fs {
		if f.Secret.Reveal() == "" || mask(f.Secret.Reveal()) != f.Sample {
			t.Errorf("%s: value %q does not mask to %q", f.Kind, f.Secret.Reveal(), f.Sample)
		}
	}
}
