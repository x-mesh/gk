package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:   "precheck <target>",
		Short: "Simulate a merge into <target> and report conflicts without touching the working tree",
		Long: `Runs git merge-tree between HEAD and <target> and prints any files that
would conflict. Working tree, index, and refs are not modified.

Exit codes:
  0  merge would be clean
  2  invalid input (unknown ref, bad arguments)
  3  conflicts detected

Requires git >= 2.40 (for --name-only). Falls back to marker parsing on older git.`,
		Args: cobra.ExactArgs(1),
		RunE: runPrecheck,
	}
	cmd.Flags().String("base", "", "explicit merge-base (overrides git merge-base HEAD <target>)")
	cmd.Flags().Bool("json", false, "emit machine-readable JSON")
	rootCmd.AddCommand(cmd)
}

// precheckResult is the JSON shape emitted by --json.
type precheckResult struct {
	Target    string   `json:"target"`
	Base      string   `json:"base"`
	Clean     bool     `json:"clean"`
	Conflicts []string `json:"conflicts"`
}

func runPrecheck(cmd *cobra.Command, args []string) error {
	err := runPrecheckCore(cmd, args)
	var ce *ConflictError
	if errors.As(err, &ce) {
		os.Exit(ce.Code)
	}
	return err
}

func runPrecheckCore(cmd *cobra.Command, args []string) error {
	target := args[0]
	if err := guardRef(target); err != nil {
		return fmt.Errorf("invalid target: %w", err)
	}

	baseOverride, _ := cmd.Flags().GetString("base")
	asJSON, _ := cmd.Flags().GetBool("json")

	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()

	// Resolve the target to a concrete commit so error messages are actionable.
	if _, _, err := runner.Run(ctx, "rev-parse", "--verify", target+"^{commit}"); err != nil {
		return WithHint(
			fmt.Errorf("unknown target %q: not a commit", target),
			"run `git fetch` if the ref lives on a remote, or spell-check the branch name",
		)
	}

	base := strings.TrimSpace(baseOverride)
	if base == "" {
		mb, _, mbErr := runner.Run(ctx, "merge-base", "HEAD", target)
		if mbErr != nil {
			return fmt.Errorf("cannot find merge-base between HEAD and %s", target)
		}
		base = strings.TrimSpace(string(mb))
	} else {
		if err := guardRef(base); err != nil {
			return fmt.Errorf("invalid base: %w", err)
		}
		resolved, _, rerr := runner.Run(ctx, "rev-parse", "--verify", base+"^{commit}")
		if rerr != nil {
			return fmt.Errorf("unknown base %q: not a commit", base)
		}
		base = strings.TrimSpace(string(resolved))
	}

	conflicts, serr := scanMergeConflicts(ctx, runner, base, "HEAD", target)
	if serr != nil {
		return fmt.Errorf("merge-tree scan: %w", serr)
	}
	if conflicts == nil {
		conflicts = []string{}
	}

	res := precheckResult{
		Target:    target,
		Base:      base,
		Clean:     len(conflicts) == 0,
		Conflicts: conflicts,
	}

	w := cmd.OutOrStdout()
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			return err
		}
	} else {
		writePrecheckHuman(w, res, useColor(cmd))
	}

	if !res.Clean {
		return &ConflictError{Code: 3}
	}
	return nil
}

// writePrecheckHuman emits the human-readable report.
func writePrecheckHuman(w io.Writer, res precheckResult, color bool) {
	if res.Clean {
		if color {
			fmt.Fprintf(w, "\033[32m✓\033[0m clean merge: HEAD → %s\n", res.Target)
		} else {
			fmt.Fprintf(w, "✓ clean merge: HEAD → %s\n", res.Target)
		}
		return
	}
	if color {
		fmt.Fprintf(w, "\033[31m✗\033[0m %d conflict(s) merging HEAD into %s:\n", len(res.Conflicts), res.Target)
	} else {
		fmt.Fprintf(w, "✗ %d conflict(s) merging HEAD into %s:\n", len(res.Conflicts), res.Target)
	}
	for _, p := range res.Conflicts {
		fmt.Fprintf(w, "  %s\n", p)
	}
	fmt.Fprintln(w, "\nnext: resolve locally via")
	fmt.Fprintf(w, "  git merge %s    # then gk edit-conflict\n", res.Target)
}

// scanMergeConflicts returns the list of conflicted paths when merging `theirs`
// into `ours`. If `base` is non-empty it is passed as --merge-base; otherwise
// git computes the merge-base itself. Empty slice means the merge would be clean.
//
// Exit semantics (git 2.38+): exit 0 = clean merge, exit 1 = conflicts, other = error.
// We treat exit 1 with parseable output as conflicts (not an error).
//
// Prefers `git merge-tree --name-only` (git >= 2.40). Falls back to parsing
// `<<<<<<<` markers for older git, where paths cannot be enumerated.
func scanMergeConflicts(ctx context.Context, r git.Runner, base, ours, theirs string) ([]string, error) {
	stdout, stderr, err := runMergeTree(ctx, r, base, ours, theirs, true /*nameOnly*/)
	if err == nil {
		return parseMergeTreeNames(stdout), nil
	}

	// Git returns non-zero on conflicts too — if we got a parseable tree line
	// in stdout, treat it as a conflict report rather than an error.
	if looksLikeTreeOID(stdout) {
		return parseMergeTreeNames(stdout), nil
	}

	stderrStr := string(stderr)
	unsupported := strings.Contains(stderrStr, "unknown option") ||
		strings.Contains(stderrStr, "unknown switch")
	if !unsupported {
		trimmed := strings.TrimSpace(stderrStr)
		if trimmed == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%s", trimmed)
	}

	// Fallback: git 2.38/2.39 — no --name-only; parse markers instead.
	stdout2, stderr2, err2 := runMergeTree(ctx, r, base, ours, theirs, false /*nameOnly*/)
	if err2 != nil && !looksLikeTreeOID(stdout2) {
		trimmed := strings.TrimSpace(string(stderr2))
		if trimmed == "" {
			return nil, err2
		}
		return nil, fmt.Errorf("%s", trimmed)
	}
	if strings.Contains(string(stdout2), "<<<<<<<") {
		return []string{"(git <2.40: paths not enumerable)"}, nil
	}
	return nil, nil
}

// runMergeTree issues a single `git merge-tree` call with the given options.
func runMergeTree(ctx context.Context, r git.Runner, base, ours, theirs string, nameOnly bool) (stdout, stderr []byte, err error) {
	args := []string{"merge-tree", "--write-tree", "--no-messages"}
	if nameOnly {
		args = append(args, "--name-only")
	}
	if base != "" {
		args = append(args, "--merge-base", base)
	}
	args = append(args, ours, theirs)
	return r.Run(ctx, args...)
}

// looksLikeTreeOID reports whether the first line of stdout is a 40-char hex
// (SHA-1) or 64-char hex (SHA-256) tree OID — indicating merge-tree actually
// ran and produced output, not a usage dump.
func looksLikeTreeOID(out []byte) bool {
	s := string(out)
	nl := strings.IndexByte(s, '\n')
	if nl < 0 {
		nl = len(s)
	}
	first := strings.TrimSpace(s[:nl])
	if len(first) != 40 && len(first) != 64 {
		return false
	}
	for _, ch := range first {
		isHex := (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')
		if !isHex {
			return false
		}
	}
	return true
}

// parseMergeTreeNames parses the output of `git merge-tree --write-tree --name-only`.
// The first line is the merge tree OID; subsequent lines are conflicted paths.
// Duplicates (one path per conflict stage) are collapsed.
func parseMergeTreeNames(out []byte) []string {
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 2 {
		return nil
	}
	seen := make(map[string]bool, len(lines)-1)
	conflicts := make([]string, 0, len(lines)-1)
	for _, ln := range lines[1:] {
		ln = strings.TrimSpace(ln)
		if ln == "" || seen[ln] {
			continue
		}
		seen[ln] = true
		conflicts = append(conflicts, ln)
	}
	return conflicts
}

// guardRef rejects obviously unsafe ref inputs. It does not attempt full
// RFC validation — `git rev-parse --verify` is authoritative. The goal here
// is to prevent argv injection and empty inputs from slipping through.
func guardRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("ref is empty")
	}
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("refs may not start with '-' (got %q)", ref)
	}
	return nil
}

// useColor returns true when stdout is a TTY and --no-color is not set.
func useColor(cmd *cobra.Command) bool {
	if flagNoColor {
		return false
	}
	f, ok := cmd.OutOrStdout().(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
