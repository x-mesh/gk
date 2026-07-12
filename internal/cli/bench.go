package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

// gk bench — internal dogfooding benchmarks, hidden from user-facing help.
//
// apply-sanity replays history through the same apply ladder `gk apply`
// uses and asks weave's real-world question: "when the ladder reports
// success, is the result also CORRECT?" For each commit C it rebuilds
// C from its parent by applying `git diff C^ C` onto a throwaway index
// seeded at C^, then compares the written tree against C's real tree.
// A tree match is a pass; a mismatch is a regression. The corpus is the
// project's own history, and each commit's real tree is the answer key —
// no hand-labelled fixtures needed. The working tree, real index, and
// HEAD are never touched: every mutation lands in a temp GIT_INDEX_FILE.

const benchApplySanityDefaultLimit = 200

const benchApplySanitySchema = 1

// Case outcomes. mismatch is the one that matters — it means the ladder
// applied a patch but produced the wrong tree (a silent regression).
const (
	benchOutcomePass        = "pass"
	benchOutcomeMismatch    = "mismatch" // regression: applied but wrong tree
	benchOutcomeApplyFailed = "apply-failed"
	benchOutcomeSkipped     = "skipped"
)

func init() {
	rootCmd.AddCommand(newBenchCmd())
}

// newBenchCmd builds the hidden `bench` parent and its subcommands. bench
// is a dogfooding tool with no user-facing documentation, so the parent is
// Hidden — it stays out of help output while remaining runnable.
func newBenchCmd() *cobra.Command {
	bench := &cobra.Command{
		Use:    "bench",
		Short:  "Internal dogfooding benchmarks (hidden)",
		Hidden: true,
	}
	applySanity := &cobra.Command{
		Use:   "apply-sanity",
		Short: "Replay history through the apply ladder and measure regressions",
		Long: `Replay the project's own history through the gk apply ladder.

For each non-merge commit C, rebuild C by applying git diff C^ C onto a
throwaway index seeded at C^, then compare the written tree to C's real
tree. A match is a pass; a mismatch is a regression (the ladder applied
a patch but produced the wrong tree). The working tree, real index, and
HEAD are never touched — every write goes to a temp GIT_INDEX_FILE.

Per-case records are appended to ~/.gk/bench/apply-sanity-<ts>.jsonl.`,
		Args: cobra.NoArgs,
		RunE: runBenchApplySanity,
	}
	applySanity.Flags().Int("limit", benchApplySanityDefaultLimit, "number of commits to replay back from HEAD")
	bench.AddCommand(applySanity)
	return bench
}

// benchCase is one replayed commit's result — appended as a JSONL line.
type benchCase struct {
	Commit   string `json:"commit"`
	Parent   string `json:"parent,omitempty"`
	Outcome  string `json:"outcome"`
	Rung     string `json:"rung,omitempty"`   // succeeding ladder strategy, on pass
	Reason   string `json:"reason,omitempty"` // skip reason, or apply/mismatch detail
	WantTree string `json:"want_tree,omitempty"`
	GotTree  string `json:"got_tree,omitempty"`
}

// benchApplySanityResult is the run summary — the envelope result payload.
type benchApplySanityResult struct {
	Schema      int            `json:"schema"`
	Total       int            `json:"total"`
	Pass        int            `json:"pass"`
	Regressions int            `json:"regressions"`
	ApplyFailed int            `json:"apply_failed"`
	Skipped     int            `json:"skipped"`
	Rungs       map[string]int `json:"rungs"`
	Corpus      benchCorpus    `json:"corpus"`
	CasesFile   string         `json:"cases_file"`
}

type benchCorpus struct {
	Head  string `json:"head"`
	Limit int    `json:"limit"`
	Repo  string `json:"repo"`
}

func runBenchApplySanity(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	limit, _ := cmd.Flags().GetInt("limit")
	if limit <= 0 {
		limit = benchApplySanityDefaultLimit
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("bench apply-sanity: locate home dir: %w", err)
	}

	res, _, err := runApplySanity(ctx, RepoFlag(), home, limit, time.Now().Unix())
	if err != nil {
		return err
	}

	w := cmd.OutOrStdout()
	if JSONOut() {
		return emitAgentResult(w, res)
	}
	writeBenchSummaryHuman(w, res)
	return nil
}

// runApplySanity is the cobra-free core: given a repo dir (empty = cwd), a
// home dir for the cases file, a corpus limit, and a run timestamp, it
// replays history and returns the summary plus the per-case records. Split
// out so tests drive it with an injected t.TempDir() home and repo.
func runApplySanity(ctx context.Context, repoDir, home string, limit int, ts int64) (benchApplySanityResult, []benchCase, error) {
	runner := &git.ExecRunner{Dir: repoDir}

	headOut, stderr, err := runner.Run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return benchApplySanityResult{}, nil, WithHint(
			fmt.Errorf("bench apply-sanity: resolve HEAD: %s", firstNonEmptyLine(string(stderr), err.Error())),
			"run inside a git repository with at least one commit")
	}
	head := strings.TrimSpace(string(headOut))

	repoPath := repoDir
	if top, _, terr := runner.Run(ctx, "rev-parse", "--show-toplevel"); terr == nil {
		repoPath = strings.TrimSpace(string(top))
	}

	listOut, stderr, err := runner.Run(ctx, "rev-list", "--no-merges", "-n", strconv.Itoa(limit), "HEAD")
	if err != nil {
		return benchApplySanityResult{}, nil, fmt.Errorf("bench apply-sanity: list corpus: %s",
			firstNonEmptyLine(string(stderr), err.Error()))
	}
	commits := splitNonEmptyLines(string(listOut))

	// The ladder is built once in --staged/--cached mode: the replay
	// applies every patch to a temp index, never the working tree.
	rungs := applyRungs(ctx, runner, true)

	tempDir, err := os.MkdirTemp("", "gk-bench-apply-sanity-")
	if err != nil {
		return benchApplySanityResult{}, nil, fmt.Errorf("bench apply-sanity: temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	res := benchApplySanityResult{
		Schema: benchApplySanitySchema,
		Rungs:  map[string]int{},
		Corpus: benchCorpus{Head: head, Limit: limit, Repo: repoPath},
	}
	cases := make([]benchCase, 0, len(commits))
	for _, commit := range commits {
		c := replayBenchCommit(ctx, runner, tempDir, commit, rungs)
		cases = append(cases, c)
		res.Total++
		switch c.Outcome {
		case benchOutcomePass:
			res.Pass++
			res.Rungs[c.Rung]++
		case benchOutcomeMismatch:
			res.Regressions++
		case benchOutcomeApplyFailed:
			res.ApplyFailed++
		default:
			res.Skipped++
		}
	}

	casesFile := benchCasesPath(home, ts)
	if err := writeBenchCases(casesFile, cases); err != nil {
		return benchApplySanityResult{}, nil, fmt.Errorf("bench apply-sanity: write cases: %w", err)
	}
	res.CasesFile = casesFile
	return res, cases, nil
}

// replayBenchCommit rebuilds one commit from its first parent through the
// apply ladder against a throwaway index, and classifies the outcome. It
// never touches the real index, working tree, or HEAD — the seeded index
// and applied patch live in per-commit temp files under tempDir.
func replayBenchCommit(ctx context.Context, runner *git.ExecRunner, tempDir, commit string, rungs []applyRung) benchCase {
	// First parent. Root commits (and shallow-clipped ones) have none —
	// there is nothing to replay against, so they are skipped.
	parentOut, _, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", commit+"^")
	parent := strings.TrimSpace(string(parentOut))
	if err != nil || parent == "" {
		return benchCase{Commit: commit, Outcome: benchOutcomeSkipped, Reason: "no parent (root or shallow)"}
	}

	wantOut, wantErr, err := runner.Run(ctx, "rev-parse", "--verify", commit+"^{tree}")
	if err != nil {
		return benchCase{Commit: commit, Parent: parent, Outcome: benchOutcomeSkipped,
			Reason: firstNonEmptyLine(string(wantErr), "cannot resolve target tree")}
	}
	wantTree := strings.TrimSpace(string(wantOut))

	// --binary (implies --full-index) so binary hunks and 3-way blob ids
	// survive; an empty diff has nothing to replay.
	patchOut, diffErr, err := runner.Run(ctx, "diff", "--binary", parent, commit)
	if err != nil {
		return benchCase{Commit: commit, Parent: parent, Outcome: benchOutcomeSkipped,
			Reason: firstNonEmptyLine(string(diffErr), "cannot compute diff")}
	}
	if len(bytes.TrimSpace(patchOut)) == 0 {
		return benchCase{Commit: commit, Parent: parent, Outcome: benchOutcomeSkipped, Reason: "empty diff"}
	}

	patchPath, err := writeBenchTemp(tempDir, "patch-", patchOut)
	if err != nil {
		return benchCase{Commit: commit, Parent: parent, Outcome: benchOutcomeSkipped,
			Reason: "temp patch: " + err.Error()}
	}
	defer os.Remove(patchPath)

	idxPath, err := reserveBenchIndex(tempDir)
	if err != nil {
		return benchCase{Commit: commit, Parent: parent, Outcome: benchOutcomeSkipped,
			Reason: "temp index: " + err.Error()}
	}
	defer os.Remove(idxPath)

	// All index work routes through the temp GIT_INDEX_FILE, so the real
	// index at .git/index is never opened for writing.
	idxRunner := &git.ExecRunner{Dir: runner.Dir, ExtraEnv: []string{"GIT_INDEX_FILE=" + idxPath}}
	if _, stderr, e := idxRunner.Run(ctx, "read-tree", parent); e != nil {
		return benchCase{Commit: commit, Parent: parent, Outcome: benchOutcomeSkipped,
			Reason: "seed index: " + firstNonEmptyLine(string(stderr), e.Error())}
	}

	strategy, applyErr := applyPatchLadder(ctx, idxRunner, []string{"--cached"}, rungs, patchPath, false)

	var gotTree string
	if applyErr == nil {
		gotOut, stderr, e := idxRunner.Run(ctx, "write-tree")
		if e != nil {
			return benchCase{Commit: commit, Parent: parent, Outcome: benchOutcomeSkipped,
				Reason: "write-tree: " + firstNonEmptyLine(string(stderr), e.Error())}
		}
		gotTree = strings.TrimSpace(string(gotOut))
	}

	outcome, reason := classifyBenchCase(applyErr, wantTree, gotTree)
	c := benchCase{
		Commit:   commit,
		Parent:   parent,
		Outcome:  outcome,
		Reason:   reason,
		WantTree: wantTree,
		GotTree:  gotTree,
	}
	if outcome == benchOutcomePass {
		c.Rung = strategy
	}
	return c
}

// classifyBenchCase maps an apply result and the tree comparison onto an
// outcome. Kept separate from the replay plumbing so the mismatch branch —
// which a well-behaved ladder never produces on real history — is unit
// testable in isolation.
func classifyBenchCase(applyErr error, wantTree, gotTree string) (outcome, reason string) {
	if applyErr != nil {
		return benchOutcomeApplyFailed, firstNonEmptyLine(applyErr.Error(), "apply failed")
	}
	if wantTree != gotTree {
		return benchOutcomeMismatch, fmt.Sprintf("want %s got %s", shortBenchOID(wantTree), shortBenchOID(gotTree))
	}
	return benchOutcomePass, ""
}

// --- temp files --------------------------------------------------------------

// writeBenchTemp writes data to a fresh temp file under dir and returns its
// absolute path. The patch path must be absolute because git apply runs with
// its CWD set to the repo, not gk's.
func writeBenchTemp(dir, prefix string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, prefix)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// reserveBenchIndex reserves a unique path for a throwaway index and removes
// the placeholder so git's read-tree creates a fresh index there (git refuses
// to treat a pre-existing empty file as a valid index).
func reserveBenchIndex(dir string) (string, error) {
	f, err := os.CreateTemp(dir, "index-")
	if err != nil {
		return "", err
	}
	path := f.Name()
	f.Close()
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

// --- cases JSONL -------------------------------------------------------------

// benchCasesPath is where a run's per-case records land. Like the session
// audit history it is global (under the home, not a repo's .gk) because the
// bench is a project-level dogfooding tool, one file per run.
func benchCasesPath(home string, ts int64) string {
	return filepath.Join(home, ".gk", "bench", fmt.Sprintf("apply-sanity-%d.jsonl", ts))
}

// writeBenchCases appends each case as a JSON line, creating the directory
// and file as needed.
func writeBenchCases(path string, cases []benchCase) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, c := range cases {
		line, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// readBenchCases parses a cases JSONL file back into records, in file order.
func readBenchCases(path string) ([]benchCase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []benchCase
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c benchCase
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, sc.Err()
}

// --- rendering ---------------------------------------------------------------

func writeBenchSummaryHuman(w io.Writer, res benchApplySanityResult) {
	fmt.Fprintf(w, "apply-sanity replay — %s (%s)\n", shortBenchOID(res.Corpus.Head), res.Corpus.Repo)
	fmt.Fprintf(w, "  corpus        %d commits (limit %d)\n", res.Total, res.Corpus.Limit)
	fmt.Fprintf(w, "  pass          %d\n", res.Pass)
	fmt.Fprintf(w, "  regressions   %d\n", res.Regressions)
	fmt.Fprintf(w, "  apply-failed  %d\n", res.ApplyFailed)
	fmt.Fprintf(w, "  skipped       %d\n", res.Skipped)
	if len(res.Rungs) > 0 {
		fmt.Fprintf(w, "  rungs         %s\n", formatBenchRungs(res.Rungs))
	}
	fmt.Fprintf(w, "  cases         %s\n", res.CasesFile)
}

// formatBenchRungs renders the rung histogram in stable order for the human
// summary, e.g. "plain=42 recount=3".
func formatBenchRungs(rungs map[string]int) string {
	keys := make([]string, 0, len(rungs))
	for k := range rungs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, rungs[k]))
	}
	return strings.Join(parts, " ")
}

// --- small helpers -----------------------------------------------------------

func shortBenchOID(oid string) string {
	if len(oid) > 12 {
		return oid[:12]
	}
	return oid
}
