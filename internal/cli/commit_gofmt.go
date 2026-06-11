package cli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/x-mesh/gk/internal/aicommit"
)

// guardGofmt warns — advisory, never blocking — when any .go file in the
// commit scope is not gofmt-clean. A gofmt violation that slips into a
// commit only surfaces later in the release preflight (golangci-lint),
// costing a fixup commit + re-run; catching it here lets the author run
// `gofmt -w` before the commit lands.
//
// The guard is silent unless it has something to say: it self-skips when
// the repo is not a Go module (no go.mod at repoRoot) or when no gofmt
// binary is on PATH. Generated sources (*.pb.go, *_gen.go, zz_generated*)
// and deleted files (no longer on disk) are excluded — the former are
// machine-written, the latter cannot be reformatted.
//
// The signature takes only a files slice (no cobra.Command) so the same
// gate can run from the --plan flow, which assembles its own scope.
func guardGofmt(ctx context.Context, out io.Writer, repoRoot string, files []aicommit.FileChange) {
	// Go-module gate: no go.mod → not our concern, stay silent.
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
		return
	}
	// gofmt absent (rare outside CI) → skip rather than error.
	if _, err := exec.LookPath("gofmt"); err != nil {
		return
	}

	var targets []string
	for _, f := range files {
		if !isGofmtTarget(f) {
			continue
		}
		// Deleted paths are not on disk — gofmt would error on them.
		if _, err := os.Stat(f.Path); err != nil {
			continue
		}
		targets = append(targets, f.Path)
	}
	if len(targets) == 0 {
		return
	}

	// `gofmt -l` lists files whose formatting differs; clean files print
	// nothing. A non-zero exit (e.g. a syntax error gofmt can't parse) is
	// treated as "nothing to advise" — the compiler/linter owns that case,
	// not a formatting nudge.
	cmd := exec.CommandContext(ctx, "gofmt", append([]string{"-l"}, targets...)...)
	stdout, err := cmd.Output()
	if err != nil {
		return
	}
	var unformatted []string
	for _, line := range strings.Split(strings.TrimSpace(string(stdout)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			unformatted = append(unformatted, line)
		}
	}
	if len(unformatted) == 0 {
		return
	}

	lines := []string{
		"gofmt: the following Go file(s) are not formatted (committing anyway):",
	}
	for _, p := range unformatted {
		lines = append(lines, "  "+p)
	}
	lines = append(lines, "fix with: gofmt -w "+strings.Join(unformatted, " "))
	printNote(out, lines...)
}

// isGofmtTarget reports whether a FileChange should be checked by gofmt:
// a .go source file that is neither generated nor an excluded variant.
func isGofmtTarget(f aicommit.FileChange) bool {
	if f.DeniedBy != "" {
		return false
	}
	p := filepath.ToSlash(f.Path)
	if !strings.HasSuffix(p, ".go") {
		return false
	}
	return !isGeneratedGoFile(p)
}

// isGeneratedGoFile matches the conventional names for machine-generated
// Go sources, which should never be hand-formatted: protobuf output
// (*.pb.go), code-gen output (*_gen.go), and controller-gen / deepcopy
// output (zz_generated*).
func isGeneratedGoFile(p string) bool {
	base := filepath.Base(p)
	switch {
	case strings.HasSuffix(base, ".pb.go"):
		return true
	case strings.HasSuffix(base, "_gen.go"):
		return true
	case strings.HasPrefix(base, "zz_generated"):
		return true
	}
	return false
}
