package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"ai.commit.model", true},
		{"log.limit", true},
		{"ai.commit.audit", true},
		{"status.density", true},
		{"ai.providers.myhost.model", true}, // dynamic map prefix
		{"ai.commit.nope", false},
		{"bogus", false},
		{"ai", false},        // section, not a leaf
		{"ai.commit", false}, // section, not a leaf
	}
	for _, c := range cases {
		if got := ValidKey(c.key); got != c.want {
			t.Errorf("ValidKey(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

func TestSetValue_NewFileAndTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if _, err := SetValue(path, "ai.commit.model", "kiro/claude-haiku-4.5"); err != nil {
		t.Fatalf("SetValue model: %v", err)
	}
	if _, err := SetValue(path, "ai.commit.audit", "true"); err != nil {
		t.Fatalf("SetValue audit: %v", err)
	}
	if _, err := SetValue(path, "log.limit", "50"); err != nil {
		t.Fatalf("SetValue limit: %v", err)
	}

	got := readFile(t, path)
	// bool/int unquoted, string as-is; nested mappings created once.
	for _, want := range []string{
		"model: kiro/claude-haiku-4.5",
		"audit: true",
		"limit: 50",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "ai:") != 1 || strings.Count(got, "commit:") != 1 {
		t.Errorf("expected single ai/commit section, got:\n%s", got)
	}
}

func TestSetValue_PreservesComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	seed := "# top comment\nai:\n    commit:\n        # keep me\n        model: old\n"
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SetValue(path, "ai.commit.model", "new"); err != nil {
		t.Fatalf("SetValue: %v", err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "# top comment") || !strings.Contains(got, "# keep me") {
		t.Errorf("comments not preserved:\n%s", got)
	}
	if !strings.Contains(got, "model: new") || strings.Contains(got, "model: old") {
		t.Errorf("value not updated:\n%s", got)
	}
}

func TestSetValue_UnknownAndNotScalar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if _, err := SetValue(path, "ai.commit.nope", "x"); err == nil {
		t.Error("expected ErrUnknownKey for ai.commit.nope")
	}
	if _, err := SetValue(path, "ai.commit.deny_paths", "foo"); err == nil {
		t.Error("expected ErrNotScalar for a slice key")
	}
}

func TestSetValue_QuotesAmbiguousStrings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// remote is a string field; setting it to "true" must round-trip as a
	// string, not a bool.
	if _, err := SetValue(path, "remote", "true"); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, `remote: "true"`) {
		t.Errorf("ambiguous string not quoted:\n%s", got)
	}
}

func TestUnsetValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if _, err := SetValue(path, "status.density", "compact"); err != nil {
		t.Fatal(err)
	}
	if _, err := SetValue(path, "ai.commit.model", "haiku"); err != nil {
		t.Fatal(err)
	}

	existed, err := UnsetValue(path, "status.density")
	if err != nil || !existed {
		t.Fatalf("UnsetValue existed=%v err=%v", existed, err)
	}
	got := readFile(t, path)
	if strings.Contains(got, "status:") {
		t.Errorf("emptied section not cleaned up:\n%s", got)
	}
	if !strings.Contains(got, "model: haiku") {
		t.Errorf("unrelated key lost:\n%s", got)
	}

	// Unsetting an absent key is a no-op, not an error.
	existed, err = UnsetValue(path, "log.limit")
	if err != nil || existed {
		t.Fatalf("UnsetValue absent: existed=%v err=%v", existed, err)
	}
}

func TestUnsetValue_MissingFile(t *testing.T) {
	existed, err := UnsetValue(filepath.Join(t.TempDir(), "none.yaml"), "log.limit")
	if err != nil || existed {
		t.Fatalf("UnsetValue missing file: existed=%v err=%v", existed, err)
	}
}

func TestUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "" +
		"ai:\n" +
		"  commit:\n" +
		"    model: ok\n" +
		"    notakey: typo\n" + // not in schema → unknown
		"  providers:\n" +
		"    myhost:\n" +
		"      model: x\n" + // dynamic map → allowed
		"refresh:\n" + // present-but-empty section → allowed
		"stauts:\n" + // typo section
		"  density: y\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := UnknownKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"ai.commit.notakey": true, "stauts.density": true}
	if len(got) != len(want) {
		t.Fatalf("UnknownKeys = %v, want keys %v", got, want)
	}
	for _, k := range got {
		if !want[k] {
			t.Errorf("unexpected unknown key %q (got %v)", k, got)
		}
	}
}

func TestUnknownKeys_MissingFile(t *testing.T) {
	got, err := UnknownKeys(filepath.Join(t.TempDir(), "none.yaml"))
	if err != nil || got != nil {
		t.Fatalf("UnknownKeys missing: got=%v err=%v", got, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
