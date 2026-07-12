package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// --- gk drivers: repo-local git driver wiring ---------------------------------
//
// Git's built-in language "userdiff" drivers give hunk headers accurate
// function contexts (a CSS hunk names its selector, a Python hunk its def),
// but they only activate when an attributes file maps extensions to them.
// Most repos never set that up, so everything that reads hunk function
// contexts — the live-feed symbols in `gk status --watch` / `gk fleet`,
// `gk diff --digest`, the conflict symbols in `gk context
// --include=conflict` — runs on git's weaker default heuristic.
//
// `gk drivers install` writes the mapping into `.git/info/attributes`: the
// repo-local attributes file git consults IN ADDITION to any versioned
// .gitattributes. It is not version-controlled, so this never dirties the
// working tree or imposes the choice on teammates (the same trick weave's
// `setup --local` uses for its merge driver). It lives in the common git
// dir, so every linked worktree of the repo benefits at once. Only git's
// built-in drivers are referenced — no config keys are written, and an
// existing per-extension mapping elsewhere still wins if it is more
// specific.

const (
	// driversMarkerPrefix identifies a gk-managed begin line regardless of the
	// version/hint suffix — the containment and strip probes both key on it.
	driversMarkerPrefix = "# gk:diff-drivers:begin"
	driversBeginMarker  = driversMarkerPrefix + " v1 — managed by `gk drivers install`; edit outside this block"
	driversEndMarker    = "# gk:diff-drivers:end"
)

// diffDriverRules maps extensions to git's BUILT-IN userdiff drivers only —
// nothing here needs a diff.<name>.xfuncname config entry. Languages whose
// default heuristic already works well (Go top-level funcs, C-likes) are
// still listed: the dedicated driver also understands their indented/nested
// definitions.
var diffDriverRules = []string{
	"*.py diff=python",
	"*.go diff=golang",
	"*.rs diff=rust",
	"*.java diff=java",
	"*.kt diff=kotlin",
	"*.kts diff=kotlin",
	"*.css diff=css",
	"*.scss diff=css",
	"*.less diff=css",
	"*.html diff=html",
	"*.htm diff=html",
	"*.php diff=php",
	"*.rb diff=ruby",
	"*.pl diff=perl",
	"*.pm diff=perl",
	"*.ex diff=elixir",
	"*.exs diff=elixir",
	"*.sh diff=bash",
	"*.bash diff=bash",
	"*.c diff=cpp",
	"*.h diff=cpp",
	"*.cc diff=cpp",
	"*.cpp diff=cpp",
	"*.hpp diff=cpp",
	"*.cxx diff=cpp",
	"*.cs diff=csharp",
	"*.m diff=objc",
	"*.f90 diff=fortran",
	"*.f95 diff=fortran",
	"*.md diff=markdown",
	"*.tex diff=tex",
}

func init() {
	cmd := &cobra.Command{
		Use:   "drivers",
		Short: "Wire git's built-in language diff drivers into this repo (.git/info/attributes)",
		Long: `Maps file extensions to git's BUILT-IN language diff drivers in
.git/info/attributes — the repo-local attributes file that is never
version-controlled, so nothing lands in the working tree and teammates are
unaffected. Linked worktrees share it automatically.

Why: git's hunk headers carry a function context ("@@ ... @@ def foo(...)"),
and everything in gk that names WHAT changed reads it — the live-feed symbols
of 'gk status --watch' and 'gk fleet --feed-stats', 'gk diff --digest', and
the conflict symbols of 'gk context --include=conflict'. Without a driver
mapping, git falls back to a generic heuristic that cannot read CSS selectors,
indented Python methods, and friends; with it, those names resolve correctly.

Only built-in git drivers are referenced (python, golang, rust, css, ...) —
no git config is written. The block is fenced with marker comments and
install/uninstall touch nothing outside it.`,
		Args: cobra.NoArgs,
	}
	install := &cobra.Command{
		Use:   "install",
		Short: "Write the gk diff-driver block into .git/info/attributes",
		Args:  cobra.NoArgs,
		RunE:  runDriversInstall,
	}
	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the gk diff-driver block from .git/info/attributes",
		Args:  cobra.NoArgs,
		RunE:  runDriversUninstall,
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Report whether the gk diff-driver block is installed",
		Args:  cobra.NoArgs,
		RunE:  runDriversStatus,
	}
	cmd.AddCommand(install, uninstall, status)
	rootCmd.AddCommand(cmd)
}

// infoAttributesPath resolves <common git dir>/info/attributes for the repo
// containing dir — the common dir, so linked worktrees share one file.
func infoAttributesPath(ctx context.Context, dir string) (string, error) {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	_, common, ok := repoRootAndCommonDir(ctx, dir)
	if !ok {
		return "", WithHint(
			fmt.Errorf("gk drivers: git 저장소가 아닙니다"),
			"git 저장소 안에서 실행하세요",
		)
	}
	return filepath.Join(common, "info", "attributes"), nil
}

// driversBlock renders the fenced managed block.
func driversBlock() string {
	return driversBeginMarker + "\n" + strings.Join(diffDriverRules, "\n") + "\n" + driversEndMarker
}

// stripDriversBlock removes the gk-managed fenced block, leaving every other
// line byte-for-byte intact. A dangling begin marker without its end strips
// to EOF rather than leaving half a block behind.
func stripDriversBlock(content string) string {
	lines := strings.Split(content, "\n")
	var out []string
	inBlock := false
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, driversMarkerPrefix):
			inBlock = true
		case inBlock && strings.HasPrefix(l, driversEndMarker):
			inBlock = false
		case !inBlock:
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

type driversResultJSON struct {
	Schema    int    `json:"schema"`
	Action    string `json:"action"` // installed | updated | unchanged | removed | absent | dry-run
	Path      string `json:"path"`
	Installed bool   `json:"installed"`
	Rules     int    `json:"rules,omitempty"`
}

func runDriversInstall(cmd *cobra.Command, _ []string) error {
	path, err := infoAttributesPath(cmd.Context(), RepoFlag())
	if err != nil {
		return err
	}
	existing := ""
	if data, rerr := os.ReadFile(path); rerr == nil {
		existing = string(data)
	}
	kept := strings.TrimRight(stripDriversBlock(existing), "\n")
	next := driversBlock() + "\n"
	if kept != "" {
		next = kept + "\n\n" + next
	}

	action := "installed"
	switch {
	case existing == next:
		action = "unchanged"
	case strings.Contains(existing, driversMarkerPrefix):
		action = "updated"
	}
	if DryRun() {
		return emitDriversResult(cmd, driversResultJSON{
			Schema: 1, Action: "dry-run", Path: path, Installed: true, Rules: len(diffDriverRules),
		}, fmt.Sprintf("dry-run: would write %d rules to %s", len(diffDriverRules), path))
	}
	if action != "unchanged" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
			return err
		}
	}
	return emitDriversResult(cmd, driversResultJSON{
		Schema: 1, Action: action, Path: path, Installed: true, Rules: len(diffDriverRules),
	}, fmt.Sprintf("%s: %d diff-driver rules → %s", action, len(diffDriverRules), path))
}

func runDriversUninstall(cmd *cobra.Command, _ []string) error {
	path, err := infoAttributesPath(cmd.Context(), RepoFlag())
	if err != nil {
		return err
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		return emitDriversResult(cmd, driversResultJSON{Schema: 1, Action: "absent", Path: path}, "nothing installed at "+path)
	}
	if !strings.Contains(string(data), driversMarkerPrefix) {
		return emitDriversResult(cmd, driversResultJSON{Schema: 1, Action: "absent", Path: path}, "no gk block in "+path)
	}
	if DryRun() {
		return emitDriversResult(cmd, driversResultJSON{Schema: 1, Action: "dry-run", Path: path}, "dry-run: would remove the gk block from "+path)
	}
	kept := strings.TrimRight(stripDriversBlock(string(data)), "\n")
	if strings.TrimSpace(kept) == "" {
		// The file held only our block: remove it entirely so uninstall
		// round-trips to the pre-install state.
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if err := os.WriteFile(path, []byte(kept+"\n"), 0o644); err != nil {
		return err
	}
	return emitDriversResult(cmd, driversResultJSON{Schema: 1, Action: "removed", Path: path}, "removed the gk block from "+path)
}

func runDriversStatus(cmd *cobra.Command, _ []string) error {
	path, err := infoAttributesPath(cmd.Context(), RepoFlag())
	if err != nil {
		return err
	}
	installed := false
	if data, rerr := os.ReadFile(path); rerr == nil {
		installed = strings.Contains(string(data), driversMarkerPrefix)
	}
	msg := "not installed — run `" + selfCmd("drivers install") + "`"
	if installed {
		msg = "installed: " + path
	}
	return emitDriversResult(cmd, driversResultJSON{Schema: 1, Action: "status", Path: path, Installed: installed}, msg)
}

func emitDriversResult(cmd *cobra.Command, res driversResultJSON, human string) error {
	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), res)
	}
	fmt.Fprintln(cmd.OutOrStdout(), human)
	return nil
}
