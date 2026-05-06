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
