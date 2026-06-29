package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

func init() {
	c := &cobra.Command{
		Use:   "bisect --good <ref> --bad <ref> -- <command>",
		Short: "Find the commit that introduced a regression by binary search",
		Long: `Binary-search the commit range between a known-good and known-bad ref to
find the first commit where <command> starts failing — the culprit.

The search runs in a throwaway detached worktree, so your working tree and HEAD
are never touched (unlike raw 'git bisect', which is stateful and checks out each
candidate in place). The command after '--' classifies each commit: exit 0 means
good, non-zero means bad.

  gk bisect --good v1.2.0 --bad HEAD -- go test ./pkg/auth/...
  gk bisect --good abc123 --bad main -- sh -c 'make build && ./check.sh'

Without a command, manual mode steps interactively: it pauses on each candidate
for you to test, then advance with 'gk bisect good|bad|skip' ('gk bisect reset'
ends it). Under --json (or GK_AGENT=1) the automatic result is {culprit:{sha,
subject,author,date}, good, bad, tested}; each manual step emits a paused contract.`,
		Args: cobra.ArbitraryArgs,
		RunE: runBisect,
	}
	c.Flags().String("good", "", "a ref where the regression is absent (required)")
	c.Flags().String("bad", "", "a ref where the regression is present (default: HEAD)")
	c.AddCommand(
		&cobra.Command{Use: "good", Short: "Mark the current bisect commit good (manual mode)", Args: cobra.NoArgs, RunE: runBisectStep("good")},
		&cobra.Command{Use: "bad", Short: "Mark the current bisect commit bad (manual mode)", Args: cobra.NoArgs, RunE: runBisectStep("bad")},
		&cobra.Command{Use: "skip", Short: "Skip the current bisect commit (manual mode)", Args: cobra.NoArgs, RunE: runBisectStep("skip")},
		&cobra.Command{Use: "reset", Short: "End the in-progress bisect and clean up its worktree", Args: cobra.NoArgs, RunE: runBisectReset},
	)
	rootCmd.AddCommand(c)
}

// bisectCommitJSON is the culprit commit in the bisect result.
type bisectCommitJSON struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

// bisectResultJSON is the `gk bisect` contract: the first bad commit plus the
// range it was found in.
type bisectResultJSON struct {
	Culprit bisectCommitJSON `json:"culprit"`
	Good    string           `json:"good"`
	Bad     string           `json:"bad"`
	Tested  int              `json:"tested,omitempty"`
}

func runBisect(cmd *cobra.Command, args []string) error {
	good, _ := cmd.Flags().GetString("good")
	bad, _ := cmd.Flags().GetString("bad")
	if bad == "" {
		bad = "HEAD"
	}
	if good == "" {
		return WithHint(
			fmt.Errorf("bisect: --good <ref> is required"),
			"usage: gk bisect --good <ref> --bad <ref> -- <command>",
		)
	}

	dash := cmd.ArgsLenAtDash()
	var runCmd []string
	if dash >= 0 {
		runCmd = args[dash:]
	}
	if len(runCmd) == 0 {
		return runBisectManualStart(cmd, good, bad)
	}
	return runBisectAuto(cmd, good, bad, runCmd)
}

// runBisectAuto drives `git bisect run` inside a throwaway detached worktree so
// the user's tree/HEAD stay untouched, then reports the first bad commit.
func runBisectAuto(cmd *cobra.Command, good, bad string, runCmd []string) error {
	ctx := cmd.Context()
	runner := &git.ExecRunner{Dir: RepoFlag()}

	for _, ref := range []string{good, bad} {
		if _, _, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
			return fmt.Errorf("bisect: %q is not a valid commit ref", ref)
		}
	}

	tmp, err := os.MkdirTemp("", "gk-bisect-")
	if err != nil {
		return fmt.Errorf("bisect: create temp worktree dir: %w", err)
	}
	if _, stderr, e := runner.Run(ctx, "worktree", "add", "--detach", tmp, bad); e != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("bisect: worktree add: %s: %w", strings.TrimSpace(string(stderr)), e)
	}
	wt := &git.ExecRunner{Dir: tmp}
	defer func() {
		// Use a fresh context: cmd.Context() is cancelled on SIGINT, which would
		// skip cleanup and leak the registered worktree.
		clean := context.Background()
		_, _, _ = wt.Run(clean, "bisect", "reset")
		_, _, _ = runner.Run(clean, "worktree", "remove", "--force", tmp)
		_ = os.RemoveAll(tmp)
	}()

	if _, stderr, e := wt.Run(ctx, "bisect", "start", bad, good); e != nil {
		return fmt.Errorf("bisect: start: %s: %w", strings.TrimSpace(string(stderr)), e)
	}

	stdout, stderr, runErr := wt.Run(ctx, append([]string{"bisect", "run"}, runCmd...)...)
	combined := string(stdout) + "\n" + string(stderr)

	culprit := parseBisectCulprit(combined)
	// Only fall back to HEAD when the run actually converged (runErr == nil) but
	// the "first bad commit" line was not matched. If the run itself failed —
	// non-zero classifier in a way git could not bisect, bad/good order, missing
	// command — HEAD is NOT the culprit, so report the failure instead of a
	// confident wrong answer.
	if culprit == "" && runErr == nil {
		if hs, _, e := wt.Run(ctx, "rev-parse", "HEAD"); e == nil {
			culprit = strings.TrimSpace(string(hs))
		}
	}
	if culprit == "" {
		reason := "did not converge on a first bad commit"
		if runErr != nil {
			reason = fmt.Sprintf("git bisect run failed (%v)", runErr)
		}
		return fmt.Errorf("bisect: %s:\n%s", reason, strings.TrimSpace(combined))
	}

	info := bisectShowCommit(ctx, wt, culprit)
	if info.SHA == "" {
		info.SHA = culprit
	}

	res := bisectResultJSON{Culprit: info, Good: good, Bad: bad, Tested: countBisectSteps(combined)}
	w := cmd.OutOrStdout()
	if JSONOut() {
		return emitAgentResult(w, res)
	}
	fmt.Fprintf(w, "first bad commit: %s %s\n", shortSHA(info.SHA), info.Subject)
	if info.Author != "" {
		fmt.Fprintf(w, "  %s · %s\n", info.Author, info.Date)
	}
	return nil
}

var bisectCulpritRe = regexp.MustCompile(`(?m)^([0-9a-f]{7,40}) is the first bad commit`)

func parseBisectCulprit(out string) string {
	if m := bisectCulpritRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// countBisectSteps counts how many commits `git bisect` actually tested, from
// its "Bisecting: N revisions left to test" progress lines (best-effort).
func countBisectSteps(out string) int {
	return strings.Count(out, "Bisecting:")
}

var bisectRemainingRe = regexp.MustCompile(`Bisecting:\s+(\d+)\s+revisions?\s+left`)

// parseBisectRemaining reads "Bisecting: N revisions left to test" (best-effort).
func parseBisectRemaining(out string) int {
	if m := bisectRemainingRe.FindStringSubmatch(out); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// bisectShowCommit loads a commit's display fields from a worktree, degrading to
// a bare SHA on any error.
func bisectShowCommit(ctx context.Context, wt *git.ExecRunner, ref string) bisectCommitJSON {
	info := bisectCommitJSON{}
	if out, _, e := wt.Run(ctx, "show", "-s", "--format=%H%x1f%s%x1f%an%x1f%aI", ref); e == nil {
		parts := strings.Split(strings.TrimSpace(string(out)), "\x1f")
		if len(parts) == 4 {
			info = bisectCommitJSON{SHA: parts[0], Subject: parts[1], Author: parts[2], Date: parts[3]}
		}
	}
	return info
}

// --- manual mode (Phase 2) ---

// bisectState is the persisted manual-bisect session: the throwaway worktree the
// candidate commits are checked out in, plus the bounds. Lives at
// <git-common-dir>/gk/bisect.json so it survives across separate gk invocations.
type bisectState struct {
	Worktree string `json:"worktree"`
	Good     string `json:"good"`
	Bad      string `json:"bad"`
}

// bisectPausedJSON is the manual-mode paused contract: which commit to test next
// and how to advance. agentState() makes the envelope state:"paused".
type bisectPausedJSON struct {
	Worktree  string           `json:"worktree"`
	Current   bisectCommitJSON `json:"current"`
	Remaining int              `json:"remaining,omitempty"`
	Resume    []string         `json:"resume"`
}

func (bisectPausedJSON) agentState() string { return envStatePaused }

func bisectMetaPath(ctx context.Context, runner *git.ExecRunner) string {
	cd := gitCommonDir(ctx, runner)
	if cd == "" {
		return ""
	}
	return filepath.Join(cd, "gk", "bisect.json")
}

func loadBisectState(path string) (*bisectState, bool) {
	if path == "" {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var st bisectState
	if json.Unmarshal(b, &st) != nil || st.Worktree == "" {
		return nil, false
	}
	return &st, true
}

func saveBisectState(path string, st *bisectState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(path, b, 0o644)
}

func cleanupBisect(ctx context.Context, runner, wt *git.ExecRunner, wtPath, metaPath string) {
	_, _, _ = wt.Run(ctx, "bisect", "reset")
	_, _, _ = runner.Run(ctx, "worktree", "remove", "--force", wtPath)
	_ = os.Remove(metaPath)
}

// runBisectManualStart begins an interactive bisect in a persistent throwaway
// worktree and pauses on the first candidate. The user/agent tests that worktree
// and advances with `gk bisect good|bad|skip`.
func runBisectManualStart(cmd *cobra.Command, good, bad string) error {
	ctx := cmd.Context()
	runner := &git.ExecRunner{Dir: RepoFlag()}
	metaPath := bisectMetaPath(ctx, runner)
	if metaPath == "" {
		return fmt.Errorf("bisect: not a git repository")
	}
	if _, ok := loadBisectState(metaPath); ok {
		return WithHint(
			fmt.Errorf("bisect: a bisect is already in progress"),
			"advance it with gk bisect good|bad|skip, or end it with gk bisect reset",
		)
	}
	for _, ref := range []string{good, bad} {
		if _, _, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
			return fmt.Errorf("bisect: %q is not a valid commit ref", ref)
		}
	}

	wtPath := filepath.Join(filepath.Dir(metaPath), "bisect-wt")
	// Clear a stale worktree from a previously crashed session (meta gone but the
	// dir/registration lingering at the fixed path) so a fresh start does not
	// hard-fail on "already exists".
	_, _, _ = runner.Run(ctx, "worktree", "remove", "--force", wtPath)
	_ = os.RemoveAll(wtPath)
	if _, stderr, e := runner.Run(ctx, "worktree", "add", "--detach", wtPath, bad); e != nil {
		return fmt.Errorf("bisect: worktree add: %s: %w", strings.TrimSpace(string(stderr)), e)
	}
	wt := &git.ExecRunner{Dir: wtPath}
	out, stderr, e := wt.Run(ctx, "bisect", "start", bad, good)
	if e != nil {
		_, _, _ = runner.Run(ctx, "worktree", "remove", "--force", wtPath)
		return fmt.Errorf("bisect: start: %s: %w", strings.TrimSpace(string(stderr)), e)
	}
	if err := saveBisectState(metaPath, &bisectState{Worktree: wtPath, Good: good, Bad: bad}); err != nil {
		cleanupBisect(ctx, runner, wt, wtPath, metaPath)
		return fmt.Errorf("bisect: save state: %w", err)
	}
	return emitBisectPaused(cmd, wt, wtPath, string(out)+string(stderr))
}

// runBisectStep advances a manual bisect by classifying the current commit. On
// the first bad commit it reports the culprit and cleans up; otherwise it pauses
// on the next candidate.
func runBisectStep(verb string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		runner := &git.ExecRunner{Dir: RepoFlag()}
		metaPath := bisectMetaPath(ctx, runner)
		st, ok := loadBisectState(metaPath)
		if !ok {
			return WithHint(
				fmt.Errorf("bisect: no bisect in progress"),
				"start one with gk bisect --good <ref> --bad <ref>",
			)
		}
		wt := &git.ExecRunner{Dir: st.Worktree}
		out, stderr, e := wt.Run(ctx, "bisect", verb)
		combined := string(out) + string(stderr)

		// Converged: report the culprit and clean up (the verb that finds it still
		// exits 0, so check the output before treating a non-zero exit as failure).
		if culprit := parseBisectCulprit(combined); culprit != "" {
			info := bisectShowCommit(ctx, wt, culprit)
			cleanupBisect(ctx, runner, wt, st.Worktree, metaPath)
			res := bisectResultJSON{Culprit: info, Good: st.Good, Bad: st.Bad}
			w := cmd.OutOrStdout()
			if JSONOut() {
				return emitAgentResult(w, res)
			}
			fmt.Fprintf(w, "first bad commit: %s %s\n", shortSHA(info.SHA), info.Subject)
			return nil
		}
		// A non-zero `git bisect <verb>` that did not converge is a real failure
		// (e.g. corrupt bisect state) — surface it instead of pausing on a bogus
		// "next" commit.
		if e != nil {
			return fmt.Errorf("bisect %s: %s: %w", verb, strings.TrimSpace(string(stderr)), e)
		}
		return emitBisectPaused(cmd, wt, st.Worktree, combined)
	}
}

func runBisectReset(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	runner := &git.ExecRunner{Dir: RepoFlag()}
	metaPath := bisectMetaPath(ctx, runner)
	st, ok := loadBisectState(metaPath)
	if !ok {
		return WithHint(fmt.Errorf("bisect: no bisect in progress"), "nothing to reset")
	}
	wt := &git.ExecRunner{Dir: st.Worktree}
	cleanupBisect(ctx, runner, wt, st.Worktree, metaPath)
	w := cmd.OutOrStdout()
	if JSONOut() {
		return emitAgentResult(w, map[string]string{"reset": st.Worktree})
	}
	fmt.Fprintln(w, "bisect reset — worktree removed")
	return nil
}

// emitBisectPaused reports the next commit to test and how to advance.
func emitBisectPaused(cmd *cobra.Command, wt *git.ExecRunner, wtPath, bisectOut string) error {
	cur := bisectShowCommit(cmd.Context(), wt, "HEAD")
	p := bisectPausedJSON{
		Worktree:  wtPath,
		Current:   cur,
		Remaining: parseBisectRemaining(bisectOut),
		Resume:    []string{selfCmd("bisect good"), selfCmd("bisect bad"), selfCmd("bisect skip")},
	}
	w := cmd.OutOrStdout()
	if JSONOut() {
		if err := emitAgentResult(w, p); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(w, "bisect paused — test %s\n  at %s %s\n  then: %s | %s | %s\n",
			wtPath, shortSHA(cur.SHA), cur.Subject,
			selfCmd("bisect good"), selfCmd("bisect bad"), selfCmd("bisect skip"))
	}
	// Paused is exit 3 (in both output modes) so batch/land detect the stop.
	return pausedExitIf(p)
}
