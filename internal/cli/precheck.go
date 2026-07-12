package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:     "precheck [target]",
		Aliases: []string{"forecast"},
		Short:   "Forecast conflicts before integrating — merge-tree simulation, nothing touched",
		Long: `Runs git merge-tree between HEAD and the target and prints any files that
would conflict. Working tree, index, and refs are not modified.

Without a target the upstream (@{u}) is checked — "will my next pull
conflict?" — falling back to the remote base branch when no upstream is
configured. The simulation is a merge; a rebase replays commits one by one,
so its conflicts can differ in detail, but the file set is the practical
forecast either way: a clean report means start the integration, a conflict
list means pick a strategy first instead of the try→abort loop.

Exit codes:
  0  merge would be clean
  2  invalid input (unknown ref, bad arguments)
  3  conflicts detected

Requires git >= 2.40 (for --name-only). Falls back to marker parsing on older git.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runPrecheck,
	}
	cmd.Flags().String("base", "", "explicit merge-base (overrides git merge-base HEAD <target>)")
	cmd.Flags().Bool("json", false, "emit machine-readable JSON")
	rootCmd.AddCommand(cmd)
}

// precheckResult is the JSON shape emitted by --json / agent mode.
// Fields are append-only.
type precheckResult struct {
	Ours      string                   `json:"ours"`
	Target    string                   `json:"target"`
	Base      string                   `json:"base"`
	Clean     bool                     `json:"clean"`
	Conflicts []string                 `json:"conflicts"`
	Details   []precheckConflictDetail `json:"details,omitempty"`
}

// precheckConflictDetail names the enclosing definitions a conflicted path
// fights over — "which function", not just "which file". Populated only when
// the merge-tree result tree is available (git >= 2.40) and the blob's symbols
// could be read; absent otherwise, which is why Details is omitempty rather
// than always mirroring Conflicts.
type precheckConflictDetail struct {
	Path    string   `json:"path"`
	Symbols []string `json:"symbols"`
}

// symbolsFor returns the enclosing-definition names forecast for a conflicted
// path, or nil when none were resolved for it.
func (r precheckResult) symbolsFor(path string) []string {
	for _, d := range r.Details {
		if d.Path == path {
			return d.Symbols
		}
	}
	return nil
}

func runPrecheck(cmd *cobra.Command, args []string) error {
	err := runPrecheckCore(cmd, args)
	var ce *ConflictError
	if errors.As(err, &ce) {
		// Precheck's own JSON (clean/conflicts) was already emitted by the
		// core; the non-zero exit alone signals "conflicts predicted".
		os.Exit(ce.Code)
	}
	return err
}

func runPrecheckCore(cmd *cobra.Command, args []string) error {
	baseOverride, _ := cmd.Flags().GetString("base")
	asJSON, _ := cmd.Flags().GetBool("json")
	asJSON = asJSON || JSONOut()

	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()

	// No target → forecast the next integration: upstream first, base as
	// the fallback — the question an agent asks before every pull.
	target := ""
	if len(args) == 1 {
		target = args[0]
	} else {
		cfg, _ := config.Load(cmd.Flags())
		resolved, err := precheckDefaultTarget(ctx, runner, cfg)
		if err != nil {
			return err
		}
		target = resolved
	}

	res, err := collectPrecheck(ctx, runner, target, baseOverride)
	if err != nil {
		return err
	}

	w := cmd.OutOrStdout()
	if asJSON {
		if err := emitAgentResult(w, res); err != nil {
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

// collectPrecheck runs the merge-tree forecast for a resolved target and
// returns the result document. Factored out of the command path so other
// one-call surfaces (gk context --include=precheck) can embed the same
// forecast without re-implementing target/base resolution semantics.
func collectPrecheck(ctx context.Context, runner *git.ExecRunner, target, baseOverride string) (precheckResult, error) {
	var res precheckResult
	if err := guardRef(target); err != nil {
		return res, fmt.Errorf("invalid target: %w", err)
	}

	// Resolve the target to a concrete commit so error messages are actionable.
	if _, _, err := runner.Run(ctx, "rev-parse", "--verify", target+"^{commit}"); err != nil {
		return res, WithHint(
			fmt.Errorf("unknown target %q: not a commit", target),
			"run `git fetch` if the ref lives on a remote, or spell-check the branch name",
		)
	}

	base := strings.TrimSpace(baseOverride)
	if base == "" {
		mb, _, mbErr := runner.Run(ctx, "merge-base", "HEAD", target)
		if mbErr != nil {
			return res, fmt.Errorf("cannot find merge-base between HEAD and %s", target)
		}
		base = strings.TrimSpace(string(mb))
	} else {
		if err := guardRef(base); err != nil {
			return res, fmt.Errorf("invalid base: %w", err)
		}
		resolved, _, rerr := runner.Run(ctx, "rev-parse", "--verify", base+"^{commit}")
		if rerr != nil {
			return res, fmt.Errorf("unknown base %q: not a commit", base)
		}
		base = strings.TrimSpace(string(resolved))
	}

	scan, serr := scanMergeConflictsTree(ctx, runner, base, "HEAD", target)
	if serr != nil {
		return res, fmt.Errorf("merge-tree scan: %w", serr)
	}
	conflicts := scan.conflicts
	if conflicts == nil {
		conflicts = []string{}
	}

	out := precheckResult{
		Ours:      "HEAD",
		Target:    target,
		Base:      base,
		Clean:     len(conflicts) == 0,
		Conflicts: conflicts,
	}
	// The merged tree's conflicted blobs already carry git's `<<<<<<<` markers,
	// so we can name the fighting functions without synthesizing a merge — but
	// only when merge-tree handed back a tree OID (git >= 2.40).
	if len(conflicts) > 0 && scan.treeOID != "" {
		out.Details = precheckConflictDetails(ctx, runner, scan.treeOID, conflicts)
	}
	return out, nil
}

// precheckDefaultTarget resolves what "the next integration" means when no
// target is given, in the SAME order gk pull resolves its upstream — a
// forecast that predicts a different ref than the pull would fetch is worse
// than none: ① @{u}; ② tracking config whose remote-tracking ref exists
// locally (precheck is read-only, so a missing cache ref can't be fetched —
// it errors with the fetch remedy instead of silently forecasting the
// wrong branch); ③ the same-name remote ref; ④ the remote base branch.
func precheckDefaultTarget(ctx context.Context, runner *git.ExecRunner, cfg *config.Config) (string, error) {
	if upstream, _, _, ok := tryTrackingUpstream(ctx, runner); ok {
		return upstream, nil
	}
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}
	client := git.NewClient(runner)
	current, _ := client.CurrentBranch(ctx)

	if cfgRemote, cfgBranch, ok := trackingFromConfig(ctx, runner, current); ok {
		candidate := cfgRemote + "/" + cfgBranch
		if git.RefExists(ctx, runner, candidate) {
			return candidate, nil
		}
		return "", WithRemedy(
			fmt.Errorf("precheck: %q tracks %s but its remote-tracking ref is missing locally", current, candidate),
			"fetch it first, then forecast again",
			errRemedy{Command: fmt.Sprintf("git fetch %s %s", cfgRemote, cfgBranch), Safety: "safe"},
		)
	}
	if current != "" && git.RefExists(ctx, runner, remote+"/"+current) {
		return remote + "/" + current, nil
	}

	base := cfg.BaseBranch
	if base == "" {
		detected, err := client.DefaultBranch(ctx, remote)
		if err != nil {
			return "", WithHint(
				fmt.Errorf("precheck: no upstream and no base branch to forecast against"),
				"name the target explicitly: gk precheck <branch>",
			)
		}
		base = detected
	}
	candidate := remote + "/" + base
	if git.RefExists(ctx, runner, candidate) {
		return candidate, nil
	}
	return base, nil
}

// writePrecheckHuman emits the human-readable report. The explicit
// `color` flag is honored over `color.NoColor` because callers may
// have stripped color separately (--no-color flag, captured output).
// ANSI sequences below use the cell-safe FG-only reset (`\x1b[39m`) so
// they compose with table backgrounds the way cell_color.go helpers do.
func writePrecheckHuman(w io.Writer, res precheckResult, color bool) {
	tick, cross := "✓", "✗"
	hintLabel, hintCmd := "next:", fmt.Sprintf("git merge %s    # then gk edit-conflict", res.Target)
	if color {
		tick = ansiBold + ansiFgGreen + tick + ansiResetFg + ansiResetBold
		cross = ansiBold + ansiFgRed + cross + ansiResetFg + ansiResetBold
		hintLabel = ansiFaint + hintLabel + ansiResetBold
		hintCmd = ansiFgCyan + hintCmd + ansiResetFg
	}
	if res.Clean {
		fmt.Fprintf(w, "%s clean merge: HEAD → %s\n", tick, res.Target)
		return
	}
	fmt.Fprintf(w, "%s %d conflict(s) merging HEAD into %s:\n", cross, len(res.Conflicts), res.Target)
	for _, p := range res.Conflicts {
		line := "  " + p
		if syms := res.symbolsFor(p); len(syms) > 0 {
			suffix := "  · in " + strings.Join(syms, ", ")
			if color {
				suffix = ansiFaint + suffix + ansiResetBold
			}
			line += suffix
		}
		fmt.Fprintf(w, "%s\n", line)
	}
	fmt.Fprintf(w, "\n%s resolve locally via\n", hintLabel)
	fmt.Fprintf(w, "  %s\n", hintCmd)
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
	scan, err := scanMergeConflictsTree(ctx, r, base, ours, theirs)
	return scan.conflicts, err
}

// mergeScan carries a merge-tree scan's results: the conflicted paths and, on
// git >= 2.40, the OID of the merged tree. That tree's conflicted blobs already
// contain git's `<<<<<<<` markers, so a caller can read a path back with
// `git show <treeOID>:<path>` and inspect the fight without synthesizing a
// merge. treeOID is empty on the 2.38/2.39 marker-parsing fallback.
type mergeScan struct {
	conflicts []string
	treeOID   string
}

// scanMergeConflictsTree is scanMergeConflicts plus the merged-tree OID. See
// scanMergeConflicts for the exit-code and fallback semantics — this is the
// same logic, only also surfacing the tree line merge-tree emits first.
func scanMergeConflictsTree(ctx context.Context, r git.Runner, base, ours, theirs string) (mergeScan, error) {
	stdout, stderr, err := runMergeTree(ctx, r, base, ours, theirs, true /*nameOnly*/)
	if err == nil {
		return mergeScan{conflicts: parseMergeTreeNames(stdout), treeOID: parseMergeTreeOID(stdout)}, nil
	}

	// Git returns non-zero on conflicts too — if we got a parseable tree line
	// in stdout, treat it as a conflict report rather than an error.
	if looksLikeTreeOID(stdout) {
		return mergeScan{conflicts: parseMergeTreeNames(stdout), treeOID: parseMergeTreeOID(stdout)}, nil
	}

	stderrStr := string(stderr)
	unsupported := strings.Contains(stderrStr, "unknown option") ||
		strings.Contains(stderrStr, "unknown switch")
	if !unsupported {
		trimmed := strings.TrimSpace(stderrStr)
		if trimmed == "" {
			return mergeScan{}, err
		}
		return mergeScan{}, fmt.Errorf("%s", trimmed)
	}

	// Fallback: git 2.38/2.39 — no --name-only; parse markers instead. The tree
	// OID is left empty: this path can't enumerate paths, so the per-path blob
	// reads that need it aren't possible here anyway.
	stdout2, stderr2, err2 := runMergeTree(ctx, r, base, ours, theirs, false /*nameOnly*/)
	if err2 != nil && !looksLikeTreeOID(stdout2) {
		trimmed := strings.TrimSpace(string(stderr2))
		if trimmed == "" {
			return mergeScan{}, err2
		}
		return mergeScan{}, fmt.Errorf("%s", trimmed)
	}
	if strings.Contains(string(stdout2), "<<<<<<<") {
		return mergeScan{conflicts: []string{"(git <2.40: paths not enumerable)"}}, nil
	}
	return mergeScan{}, nil
}

// precheckConflictDetailMaxFiles caps how many conflicted files we crack open
// to name the fighting definitions — a forecast is a glance, not a full merge
// review, so past a couple dozen files the honest signal is "lots conflicts".
const precheckConflictDetailMaxFiles = 20

// precheckConflictDetails names the enclosing definitions for each conflicted
// path by reading its blob out of the merged tree (git show <treeOID>:<path>)
// and scanning the `<<<<<<<` markers merge-tree already wrote there. Every
// per-path failure — missing path, binary, oversized, unreadable, or no symbol
// under a marker — is silently skipped: the file list alone stays a valid
// forecast, so a detail we can't produce simply isn't emitted.
func precheckConflictDetails(ctx context.Context, r git.Runner, treeOID string, conflicts []string) []precheckConflictDetail {
	var details []precheckConflictDetail
	for i, path := range conflicts {
		if i >= precheckConflictDetailMaxFiles {
			break
		}
		syms := precheckPathSymbols(ctx, r, treeOID, path)
		if len(syms) == 0 {
			continue
		}
		details = append(details, precheckConflictDetail{Path: path, Symbols: syms})
	}
	return details
}

// precheckPathSymbols reads one conflicted path's marker-bearing blob from the
// merged tree and returns its enclosing-definition names. Returns nil on any
// failure or when the blob is oversized or binary — the same content cap and
// text sniff the live feeds' symbol scan uses.
func precheckPathSymbols(ctx context.Context, r git.Runner, treeOID, path string) []string {
	// treeOID is git-emitted hex and path is git-emitted output, so
	// "<oid>:<path>" can neither be empty nor look like a flag.
	out, _, err := r.Run(ctx, "show", treeOID+":"+path)
	if err != nil {
		return nil
	}
	if len(out) > untrackedProfileMaxBytes || bytes.IndexByte(out, 0) >= 0 {
		return nil
	}
	return conflictSymbolsFromContent(path, string(out))
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
	return looksLikeHexOID(firstLine(string(out)))
}

// parseMergeTreeOID returns the merged-tree OID from merge-tree output — its
// first line — or "" when that line is not an OID (e.g. a usage dump), so a
// caller can tell "no tree to read blobs from" from a real OID.
func parseMergeTreeOID(out []byte) string {
	if first := firstLine(string(out)); looksLikeHexOID(first) {
		return first
	}
	return ""
}

// looksLikeHexOID reports whether s is a 40-char (SHA-1) or 64-char (SHA-256)
// lowercase-hex object ID.
func looksLikeHexOID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, ch := range s {
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
