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
	// The JSON of a secret is its masked text, not only something else than
	// the value.
	if want, _ := json.Marshal(mask(raw)); string(sj) != string(want) {
		t.Errorf("json secret = %s, want %s", sj, want)
	}
	if NewSecret("").String() != "" || NewSecret("").Reveal() != "" {
		t.Errorf("the zero secret prints %q", NewSecret("").String())
	}
}

// secretHolder keeps a secret, and findings that hold one, in fields that are
// not exported: fmt can call no method on those and prints what the fields
// hold.
type secretHolder struct {
	s  Secret
	f  Finding
	fs []Finding
}

// TestSecretNeverFormatsItsValue covers every way fmt can print a secret.
// fmt calls String or Format for some verbs and paths only. %p, and a verb it
// rejects, print the fields of the struct with the methods off, and a field
// that is not exported has no method fmt can call. So the value must not be
// where fmt prints it.
func TestSecretNeverFormatsItsValue(t *testing.T) {
	raw := "ghp_4fT9kL2mQ8wX1zR7vB3nC6yH0pJ5sD8gA2eW"
	masked := mask(raw)
	s := NewSecret(raw)
	f := Finding{Kind: "github-pat", File: "config.env", FileLine: 3, Sample: masked, Secret: s}
	args := map[string]any{
		"a secret":             s,
		"a pointer":            &s,
		"a finding":            f,
		"a pointer to finding": &f,
		"findings":             []Finding{f},
		"a map":                map[string]Secret{"k": s},
		"a slice of any":       []any{s, f},
		"a holder":             secretHolder{s: s, f: f, fs: []Finding{f}},
		"a pointer to holder":  &secretHolder{s: s, f: f, fs: []Finding{f}},
	}
	verbs := []string{
		"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%t", "%c", "%e", "%g", "%o", "%b", "%U", "%p", "%T",
		"% x", "%#x", "%+q", "%#q", "%40v", "%-40d", "%.3s",
	}
	// The value, its tail after the prefix that the mask keeps, and the hex
	// form that %x gives a string.
	tail := raw[len(raw)-12:]
	hexTail := fmt.Sprintf("%x", tail)
	leaks := func(out string) bool {
		flat := strings.ToLower(strings.ReplaceAll(out, " ", ""))
		return strings.Contains(out, tail) || strings.Contains(flat, hexTail) || strings.Contains(flat, strings.ToLower(tail))
	}
	for name, arg := range args {
		for _, verb := range verbs {
			if out := fmt.Sprintf(verb, arg); leaks(out) {
				t.Errorf("%s with %s prints the value: %s", name, verb, out)
			}
		}
		for how, out := range map[string]string{"Sprint": fmt.Sprint(arg), "Sprintln": fmt.Sprintln(arg)} {
			if leaks(out) {
				t.Errorf("%s with %s prints the value: %s", name, how, out)
			}
		}
	}

	// Where fmt can call the methods, the secret prints as its masked text
	// for every verb, not as an address or as the fields of the struct.
	for verb, want := range map[string]string{
		"%v": masked, "%+v": masked, "%#v": masked, "%s": masked, "%d": masked, "%t": masked, "%c": masked, "%e": masked, "%U": masked,
		"%q":   fmt.Sprintf("%q", masked),
		"%x":   fmt.Sprintf("%x", masked),
		"%X":   fmt.Sprintf("%X", masked),
		"%16v": fmt.Sprintf("%16s", masked),
		"%.3s": masked[:3],
	} {
		if got := fmt.Sprintf(verb, s); got != want {
			t.Errorf("a secret with %s = %q, want %q", verb, got, want)
		}
		if got := fmt.Sprintf(verb, &s); got != want {
			t.Errorf("a pointer to a secret with %s = %q, want %q", verb, got, want)
		}
	}
	if got := fmt.Sprint(s) + fmt.Sprintln(s); got != masked+masked+"\n" {
		t.Errorf("Sprint and Sprintln = %q", got)
	}
	// In an exported field too, for a verb fmt rejects for a struct as well.
	for _, verb := range []string{"%v", "%+v", "%d", "%t"} {
		for name, arg := range map[string]any{"a finding": f, "a pointer to finding": &f, "findings": []Finding{f}} {
			if out := fmt.Sprintf(verb, arg); !strings.Contains(out, masked) {
				t.Errorf("%s with %s lacks the masked text %q: %s", name, verb, masked, out)
			}
		}
	}

	// The zero secret prints nothing in any verb.
	for _, verb := range []string{"%v", "%d", "%s", "%x"} {
		if got := fmt.Sprintf(verb, Secret{}); got != "" {
			t.Errorf("the zero secret with %s = %q", verb, got)
		}
	}
	if NewSecret("") != (Secret{}) {
		t.Errorf("a secret of no value is not the zero secret")
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
