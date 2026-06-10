// Package update implements `gk update` — self-update with install-source
// detection. brew installs are forwarded to `brew upgrade`, manual installs
// (curl install.sh) self-replace via atomic rename, and `go install` builds
// surface a copy-pasteable upgrade command.
//
// The package is deliberately runtime-only: no GitHub auth, no signature
// verification beyond the published `checksums.txt`, no auto-cron — all of
// those are upstream responsibilities or out of scope for a CLI updater.
package update

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Source classifies how the running gk binary was installed. The classifier
// is intentionally permissive: when in doubt it returns SourceManual, which
// triggers the safest path (download + atomic rename).
type Source int

const (
	// SourceUnknown is the zero value. Indicates DetectInstall has not run.
	SourceUnknown Source = iota
	// SourceBrew means the binary lives under a Homebrew prefix and should
	// be upgraded via `brew upgrade x-mesh/tap/gk`.
	SourceBrew
	// SourceGoInstall means the binary lives under $GOPATH/bin or
	// $HOME/go/bin. We refuse to overwrite these and instead suggest
	// `go install ...@latest`.
	SourceGoInstall
	// SourceManual is everything else — typically /usr/local/bin/gk or
	// ~/.local/bin/gk from `install.sh`. Self-replace is allowed.
	SourceManual
)

// String returns a stable label for logs and human output.
func (s Source) String() string {
	switch s {
	case SourceBrew:
		return "brew"
	case SourceGoInstall:
		return "go-install"
	case SourceManual:
		return "manual"
	default:
		return "unknown"
	}
}

// BrewKind distinguishes the two Homebrew install shapes — the tap was
// migrated from a formula to a cask at v0.55+ and `brew upgrade` needs
// the `--cask` flag for the new shape. Empty for non-brew installs.
type BrewKind string

const (
	BrewKindNone    BrewKind = ""
	BrewKindFormula BrewKind = "formula"
	BrewKindCask    BrewKind = "cask"
)

// Install is the resolved environment for the running gk binary.
type Install struct {
	Source     Source
	BrewKind   BrewKind // formula/cask when Source == SourceBrew; empty otherwise
	BinaryPath string   // resolved absolute path of the running gk binary
	Dir        string   // filepath.Dir(BinaryPath) — where a sibling gk.new lands
	OS         string   // runtime.GOOS
	Arch       string   // runtime.GOARCH
}

// AssetName is the archive filename published in releases for this platform.
// Mirrors the `gk_<os>_<arch>.tar.gz` template in .goreleaser.yaml. Returns
// the empty string for unsupported platforms.
func (i *Install) AssetName() string {
	if i.OS == "" || i.Arch == "" {
		return ""
	}
	return fmt.Sprintf("gk_%s_%s.tar.gz", i.OS, i.Arch)
}

// AliasName returns the secondary command name to expose alongside the
// running binary, mirroring install.sh's `git-kit` link and the cask's
// `binary "gk", target: "git-kit"`. The suffix is preserved so a dev build
// keeps its own namespace:
//
//	gk      → git-kit
//	gk-dev  → git-kit-dev
//
// Returns "" when the binary name doesn't match the `gk[-suffix]` shape (e.g.
// a user renamed the binary), in which case callers skip alias creation
// rather than guess at an unrelated name.
func (i *Install) AliasName() string {
	return aliasFor(filepath.Base(i.BinaryPath))
}

// aliasFor maps a gk binary name to its git-kit counterpart, preserving any
// suffix. Package-private so tests can exercise it without a full Install.
func aliasFor(binName string) string {
	if binName == "gk" {
		return "git-kit"
	}
	if suffix, ok := strings.CutPrefix(binName, "gk-"); ok && suffix != "" {
		return "git-kit-" + suffix
	}
	return ""
}

// brewPrefixes lists path prefixes that, when a binary's resolved location
// lies underneath, identify the install as Homebrew-managed. Covers the
// three Homebrew layouts we expect to encounter:
//   - /opt/homebrew on Apple Silicon
//   - /usr/local on Intel macOS (and old layouts)
//   - /home/linuxbrew/.linuxbrew on Linux
//
// Symlinks under these prefixes resolve to a Cellar path that is also
// covered, so EvalSymlinks is not strictly required.
var brewPrefixes = []string{
	"/opt/homebrew/",
	"/usr/local/Cellar/",
	"/usr/local/Homebrew/",
	"/home/linuxbrew/.linuxbrew/",
}

// DetectInstall identifies how the running binary was installed.
//
// The classification is structural — it inspects the binary path returned by
// os.Executable, after symlink resolution — rather than asking `brew list`.
// Asking brew would shell out and depend on PATH, which is exactly what we
// want to avoid in a self-updater.
func DetectInstall() (*Install, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate running binary: %w", err)
	}
	// Resolve symlinks: brew installs gk to Cellar and symlinks bin/gk
	// to it; without resolution we'd misclassify by the symlink path.
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		// On EvalSymlinks failure (e.g. path no longer exists), fall back
		// to the raw exe path — better to ship a best-effort detection
		// than abort the whole command.
		resolved = exe
	}

	in := &Install{
		BinaryPath: resolved,
		Dir:        filepath.Dir(resolved),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
	}
	in.Source = classify(resolved)
	if in.Source == SourceBrew {
		in.BrewKind = classifyBrewKind(resolved)
	}
	return in, nil
}

// classify returns the Source for an absolute, symlink-resolved binary path.
// Exposed package-private for tests that don't want to fight os.Executable.
func classify(path string) Source {
	for _, p := range brewPrefixes {
		if strings.HasPrefix(path, p) {
			return SourceBrew
		}
	}
	if isGoInstallPath(path) {
		return SourceGoInstall
	}
	return SourceManual
}

// classifyBrewKind separates cask from formula by looking for the
// `/Caskroom/` segment in the resolved binary path. Both layouts live
// under the same brew prefix, but cask stages artifacts under
// `<prefix>/Caskroom/<name>/<version>/...` while formula stages under
// `<prefix>/Cellar/<name>/<version>/...`.
//
// Falls back to BrewKindFormula when neither marker is found —
// historically that was the only shape, and `brew upgrade` without
// `--cask` is the safer guess against an old install layout.
func classifyBrewKind(path string) BrewKind {
	if strings.Contains(path, "/Caskroom/") {
		return BrewKindCask
	}
	if strings.Contains(path, "/Cellar/") {
		return BrewKindFormula
	}
	return BrewKindFormula
}

func isGoInstallPath(path string) bool {
	// $GOPATH/bin — honour GOPATH if set, fall back to $HOME/go/bin which
	// is the documented Go default.
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		for _, gp := range strings.Split(gopath, string(os.PathListSeparator)) {
			if gp == "" {
				continue
			}
			if strings.HasPrefix(path, filepath.Join(gp, "bin")+string(os.PathSeparator)) {
				return true
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		if strings.HasPrefix(path, filepath.Join(home, "go", "bin")+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}
