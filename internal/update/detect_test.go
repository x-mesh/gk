package update

import (
	"path/filepath"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		path string
		want Source
	}{
		{"apple-silicon brew", "/opt/homebrew/Cellar/gk/0.29.1/bin/gk", SourceBrew},
		{"intel brew Cellar", "/usr/local/Cellar/gk/0.29.1/bin/gk", SourceBrew},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/bin/gk", SourceBrew},
		{"install.sh /usr/local/bin", "/usr/local/bin/gk", SourceManual},
		{"install.sh ~/.local/bin", "/home/ubuntu/.local/bin/gk", SourceManual},
		{"random path", "/opt/myapps/gk", SourceManual},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.path); got != tc.want {
				t.Errorf("classify(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestClassifyGoInstallGOPATH(t *testing.T) {
	gopath := t.TempDir()
	t.Setenv("GOPATH", gopath)
	binPath := filepath.Join(gopath, "bin", "gk")
	if got := classify(binPath); got != SourceGoInstall {
		t.Errorf("classify(%q) = %v, want SourceGoInstall", binPath, got)
	}
}

func TestClassifyGoInstallHomeFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GOPATH", "")
	binPath := filepath.Join(home, "go", "bin", "gk")
	if got := classify(binPath); got != SourceGoInstall {
		t.Errorf("classify(%q) = %v, want SourceGoInstall", binPath, got)
	}
}

func TestSourceString(t *testing.T) {
	for _, tc := range []struct {
		s    Source
		want string
	}{
		{SourceBrew, "brew"},
		{SourceGoInstall, "go-install"},
		{SourceManual, "manual"},
		{SourceUnknown, "unknown"},
	} {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("Source(%d).String() = %q, want %q", tc.s, got, tc.want)
		}
	}
}

func TestClassifyBrewKind(t *testing.T) {
	cases := []struct {
		name string
		path string
		want BrewKind
	}{
		// Cask paths — must dispatch `brew upgrade --cask` from
		// `gk update`. Tap migrated formula → cask at v0.55.
		{"apple-silicon cask", "/opt/homebrew/Caskroom/gk/0.57.1/gk", BrewKindCask},
		{"linuxbrew cask", "/home/linuxbrew/.linuxbrew/Caskroom/gk/0.57.1/gk", BrewKindCask},
		// Formula paths — legacy install layout still on the tap as
		// the deprecated Formula/gk.rb. `brew upgrade x-mesh/tap/gk`
		// (no --cask) is correct for these.
		{"apple-silicon formula", "/opt/homebrew/Cellar/gk/0.54.0/bin/gk", BrewKindFormula},
		{"intel formula", "/usr/local/Cellar/gk/0.54.0/bin/gk", BrewKindFormula},
		// Edge: brew prefix but no Caskroom/Cellar token. Fall back to
		// formula — historically the only shape, and `brew upgrade`
		// without --cask is the safer guess.
		{"brew bin shim", "/opt/homebrew/bin/gk", BrewKindFormula},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyBrewKind(tc.path); got != tc.want {
				t.Errorf("classifyBrewKind(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestAssetName(t *testing.T) {
	in := &Install{OS: "linux", Arch: "amd64"}
	if got, want := in.AssetName(), "gk_linux_amd64.tar.gz"; got != want {
		t.Errorf("AssetName() = %q, want %q", got, want)
	}
	empty := &Install{}
	if empty.AssetName() != "" {
		t.Errorf("empty Install.AssetName() = %q, want empty", empty.AssetName())
	}
}

func TestAliasFor(t *testing.T) {
	cases := []struct {
		bin  string
		want string
	}{
		// Canonical install.sh / cask name → the bare git-kit alias.
		{"gk", "git-kit"},
		// Dev builds keep their suffix so they never collide with the
		// Homebrew-owned git-kit (mirrors `make install INSTALL_NAME=gk-dev`).
		{"gk-dev", "git-kit-dev"},
		{"gk-nightly", "git-kit-nightly"},
		// Already an alias, an empty suffix, or an unrelated name → no alias,
		// so callers skip linking rather than invent a target.
		{"git-kit", ""},
		{"gk-", ""},
		{"mygk", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.bin, func(t *testing.T) {
			if got := aliasFor(tc.bin); got != tc.want {
				t.Errorf("aliasFor(%q) = %q, want %q", tc.bin, got, tc.want)
			}
		})
	}
}

func TestInstallAliasName(t *testing.T) {
	// AliasName derives from the binary basename, not the full path.
	if got := (&Install{BinaryPath: "/home/u/.local/bin/gk"}).AliasName(); got != "git-kit" {
		t.Errorf("AliasName(gk) = %q, want git-kit", got)
	}
	if got := (&Install{BinaryPath: "/home/u/.local/bin/gk-dev"}).AliasName(); got != "git-kit-dev" {
		t.Errorf("AliasName(gk-dev) = %q, want git-kit-dev", got)
	}
}
