package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/config"
)

func TestBumpVersionByFormat(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		before  string
		want    string
		wantErr bool
	}{
		{
			name:   "VERSION whole file",
			base:   "VERSION",
			before: "0.5.0\n",
			want:   "1.0.0\n",
		},
		{
			name:   "package.json version field",
			base:   "package.json",
			before: `{"name":"x","version":"0.5.0","deps":{}}`,
			want:   `{"name":"x","version":"1.0.0","deps":{}}`,
		},
		{
			name: "pyproject PEP 621 leaves dependency versions alone",
			base: "pyproject.toml",
			before: "[project]\n" +
				"name = \"x\"\n" +
				"version = \"0.5.0\"\n" +
				"dependencies = []\n\n" +
				"[tool.poetry.dependencies]\n" +
				"requests = { version = \"2.0.0\" }\n",
			want: "[project]\n" +
				"name = \"x\"\n" +
				"version = \"1.0.0\"\n" +
				"dependencies = []\n\n" +
				"[tool.poetry.dependencies]\n" +
				"requests = { version = \"2.0.0\" }\n",
		},
		{
			name: "poetry table version",
			base: "pyproject.toml",
			before: "[tool.poetry]\n" +
				"version = \"0.5.0\"\n",
			want: "[tool.poetry]\n" +
				"version = \"1.0.0\"\n",
		},
		{
			name: "Cargo package version, not dependency",
			base: "Cargo.toml",
			before: "[package]\n" +
				"name = \"x\"\n" +
				"version = \"0.5.0\"\n\n" +
				"[dependencies]\n" +
				"serde = { version = \"1.0\" }\n",
			want: "[package]\n" +
				"name = \"x\"\n" +
				"version = \"1.0.0\"\n\n" +
				"[dependencies]\n" +
				"serde = { version = \"1.0\" }\n",
		},
		{
			name:   "python __version__ double quotes",
			base:   "__init__.py",
			before: "__version__ = \"0.5.0\"\n",
			want:   "__version__ = \"1.0.0\"\n",
		},
		{
			name:   "python __version__ single quotes",
			base:   "version.py",
			before: "__version__ = '0.5.0'\n",
			want:   "__version__ = '1.0.0'\n",
		},
		{
			name:   "pubspec top-level version",
			base:   "pubspec.yaml",
			before: "name: x\nversion: 0.5.0\nenvironment:\n  sdk: '>=3.0.0'\n",
			want:   "name: x\nversion: 1.0.0\nenvironment:\n  sdk: '>=3.0.0'\n",
		},
		{
			name:    "unsupported format errors",
			base:    "build.gradle",
			before:  "version '0.5.0'\n",
			wantErr: true,
		},
		{
			name:    "pyproject without version key errors",
			base:    "pyproject.toml",
			before:  "[project]\nname = \"x\"\n",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bumpVersionByFormat(tc.before, tc.base, "1.0.0")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestBumpVersionByPattern(t *testing.T) {
	got, err := bumpVersionByPattern(`__version__ = "0.5.0"  # release`, `__version__ = "{version}"`, "1.0.0", "x.py")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if want := `__version__ = "1.0.0"  # release`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	if _, err := bumpVersionByPattern("x", "no placeholder", "1.0.0", "x"); err == nil {
		t.Error("expected error for pattern without {version}")
	}
	if _, err := bumpVersionByPattern("x = 0.1", `{version} {version}`, "1.0.0", "x"); err == nil {
		t.Error("expected error for two placeholders")
	}
	if _, err := bumpVersionByPattern("nothing here", `v={version}`, "1.0.0", "x"); err == nil {
		t.Error("expected error when pattern not found")
	}
}

func TestBumpVersionByYAMLKey(t *testing.T) {
	before := "# chart\nname: app\nversion: 0.5.0\nappVersion: 0.5.0  # keep me\n"
	got, err := bumpVersionByYAMLKey(before, "appVersion", "1.0.0", "Chart.yaml")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(got, "appVersion: 1.0.0") {
		t.Errorf("appVersion not bumped:\n%s", got)
	}
	if !strings.Contains(got, "version: 0.5.0") {
		t.Errorf("sibling version should be untouched:\n%s", got)
	}
	if !strings.Contains(got, "# chart") || !strings.Contains(got, "keep me") {
		t.Errorf("comments should be preserved:\n%s", got)
	}

	// dotted path
	nested := "tool:\n  poetry:\n    version: 0.5.0\n"
	got, err = bumpVersionByYAMLKey(nested, "tool.poetry.version", "1.0.0", "x.yaml")
	if err != nil {
		t.Fatalf("nested err: %v", err)
	}
	if !strings.Contains(got, "version: 1.0.0") {
		t.Errorf("nested version not bumped:\n%s", got)
	}

	if _, err := bumpVersionByYAMLKey("name: x\n", "missing", "1.0.0", "x.yaml"); err == nil {
		t.Error("expected error for missing key")
	}
}

// bumpShipVersionFile end-to-end: write, bump, read back, plus the dispatch
// between native/pattern/key strategies and the no-op case.
func TestBumpShipVersionFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	native := write("VERSION", "0.5.0\n")
	changed, err := bumpShipVersionFile(config.VersionFile{Path: native}, "1.0.0")
	if err != nil || !changed {
		t.Fatalf("native: changed=%v err=%v", changed, err)
	}
	if b, _ := os.ReadFile(native); string(b) != "1.0.0\n" {
		t.Errorf("VERSION = %q", b)
	}

	// no-op: bumping to the same version reports no change
	changed, err = bumpShipVersionFile(config.VersionFile{Path: native}, "1.0.0")
	if err != nil || changed {
		t.Fatalf("no-op: changed=%v err=%v", changed, err)
	}

	pat := write("ver.txt", "release = <<0.5.0>>\n")
	changed, err = bumpShipVersionFile(config.VersionFile{Path: pat, Pattern: "release = <<{version}>>"}, "1.0.0")
	if err != nil || !changed {
		t.Fatalf("pattern: changed=%v err=%v", changed, err)
	}
	if b, _ := os.ReadFile(pat); string(b) != "release = <<1.0.0>>\n" {
		t.Errorf("ver.txt = %q", b)
	}

	key := write("c.yaml", "appVersion: 0.5.0\n")
	changed, err = bumpShipVersionFile(config.VersionFile{Path: key, Key: "appVersion"}, "1.0.0")
	if err != nil || !changed {
		t.Fatalf("key: changed=%v err=%v", changed, err)
	}

	// unsupported native format surfaces an error instead of silently skipping
	bad := write("weird.conf", "v=0.5.0\n")
	if _, err := bumpShipVersionFile(config.VersionFile{Path: bad}, "1.0.0"); err == nil {
		t.Error("expected error for unsupported format")
	}
}
