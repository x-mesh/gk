package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/commitlint"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:   "preflight",
		Short: "Run a configured sequence of checks before pushing",
		Long: `Runs cfg.preflight.steps in order. Each step is a shell command or a
built-in alias: commit-lint, branch-check, no-conflict.

Built-in aliases:
  commit-lint   — lint the HEAD commit message against Conventional Commits rules
  branch-check  — validate the current branch name against configured patterns
  no-conflict   — pre-scan for merge conflicts vs the base branch`,
		RunE: runPreflight,
	}
	cmd.Flags().Bool("dry-run", false, "print steps without executing")
	cmd.Flags().Bool("continue-on-failure", false, "keep running after a step fails")
	cmd.Flags().StringSlice("skip", nil, "step names to skip (comma-separated)")
	rootCmd.AddCommand(cmd)
}

type preflightResult struct {
	Name     string
	Status   string // "pass" | "fail" | "skip"
	Duration time.Duration
	Message  string
}

func runPreflight(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load(cmd.Flags())
	if err != nil || cfg == nil {
		d := config.Defaults()
		cfg = &d
	}

	if len(cfg.Preflight.Steps) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no preflight steps configured")
		return nil
	}

	dry, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("continue-on-failure")
	skip, _ := cmd.Flags().GetStringSlice("skip")

	skipSet := make(map[string]bool, len(skip))
	for _, s := range skip {
		skipSet[strings.TrimSpace(s)] = true
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	w := cmd.OutOrStdout()

	var results []preflightResult
	failed := 0

	for _, step := range cfg.Preflight.Steps {
		name := step.Name
		if name == "" {
			name = step.Command
		}

		if skipSet[name] {
			fmt.Fprintf(w, "- %-22s skipped\n", name)
			results = append(results, preflightResult{Name: name, Status: "skip"})
			continue
		}

		if dry {
			fmt.Fprintf(w, "  %-22s %s\n", name, resolveDescription(step.Command))
			continue
		}

		start := time.Now()
		stepErr := runStep(ctx, runner, cfg, step)
		dur := time.Since(start)

		if stepErr == nil {
			fmt.Fprintf(w, "ok %-22s (%s)\n", name, dur.Round(time.Millisecond))
			results = append(results, preflightResult{Name: name, Status: "pass", Duration: dur})
			continue
		}

		failed++
		fmt.Fprintf(w, "!! %-22s (%s) — %s\n", name, dur.Round(time.Millisecond), stepErr)
		results = append(results, preflightResult{Name: name, Status: "fail", Duration: dur, Message: stepErr.Error()})

		if !force && !step.ContinueOnFailure {
			return fmt.Errorf("preflight failed at step %q: %w", name, stepErr)
		}
	}

	// suppress "results unused" when dry-run produces no results
	_ = results

	if failed > 0 {
		return fmt.Errorf("preflight: %d step(s) failed", failed)
	}
	return nil
}

// runStep dispatches to the built-in handler or runs a shell command.
func runStep(ctx context.Context, r git.Runner, cfg *config.Config, step config.PreflightStep) error {
	switch step.Command {
	case "commit-lint":
		return runBuiltinCommitLint(ctx, r, cfg)
	case "branch-check":
		return runBuiltinBranchCheck(ctx, r, cfg)
	case "no-conflict":
		return runBuiltinNoConflict(ctx, r, cfg)
	case "gofmt":
		return runBuiltinGofmt(ctx, r)
	default:
		return runShellStep(ctx, step.Command)
	}
}

// resolveDescription returns a human-readable description for dry-run output.
func resolveDescription(cmd string) string {
	switch cmd {
	case "commit-lint":
		return "[builtin] lint HEAD commit message"
	case "branch-check":
		return "[builtin] validate branch name against patterns"
	case "no-conflict":
		return "[builtin] pre-merge conflict scan vs base"
	case "gofmt":
		return "[builtin] gofmt -l on tracked .go files"
	default:
		return "[shell] " + cmd
	}
}

// runBuiltinGofmt is the `gofmt` preflight step: it fails when any tracked,
// non-generated .go file in the repo is not gofmt-clean — the release-gate
// counterpart to commit's advisory guardGofmt, fast enough to run before the
// slower golangci-lint so a formatting slip is caught early, not mid-release.
// Self-skips (passes) when the repo is not a Go module or gofmt is absent.
func runBuiltinGofmt(ctx context.Context, r git.Runner) error {
	// Resolve the worktree root through the same runner that enumerates files,
	// so root and file list always refer to the same repo (honors --repo).
	topOut, terr := func() (string, error) {
		out, _, e := r.Run(ctx, "rev-parse", "--show-toplevel")
		return strings.TrimSpace(string(out)), e
	}()
	root := topOut
	if terr != nil || root == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return nil // not a Go module — not our concern
	}
	if _, err := exec.LookPath("gofmt"); err != nil {
		return nil // gofmt absent (rare outside CI) — skip rather than fail
	}

	out, _, err := r.Run(ctx, "ls-files", "-z", "--", "*.go")
	if err != nil {
		return nil // can't enumerate — let golangci-lint own it
	}
	var targets []string
	for _, p := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if p == "" || isGeneratedGoFile(p) {
			continue
		}
		abs := filepath.Join(root, p)
		if _, e := os.Stat(abs); e != nil {
			continue // deleted/missing on disk
		}
		targets = append(targets, abs)
	}
	if len(targets) == 0 {
		return nil
	}

	// `gofmt -l` lists files whose formatting differs. A non-zero exit (e.g. a
	// syntax error gofmt can't parse) is left to the compiler/linter, not a
	// formatting gate.
	cmd := exec.CommandContext(ctx, "gofmt", append([]string{"-l"}, targets...)...)
	stdout, gerr := cmd.Output()
	if gerr != nil {
		return nil
	}
	var bad []string
	for _, line := range strings.Split(strings.TrimSpace(string(stdout)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if rel, e := filepath.Rel(root, line); e == nil {
				line = rel
			}
			bad = append(bad, line)
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return WithHint(
		fmt.Errorf("gofmt: %d file(s) not formatted: %s", len(bad), strings.Join(bad, ", ")),
		"fix with: gofmt -w "+strings.Join(bad, " "),
	)
}

// runBuiltinCommitLint lints the HEAD commit message.
func runBuiltinCommitLint(ctx context.Context, r git.Runner, cfg *config.Config) error {
	stdout, stderr, err := r.Run(ctx, "log", "-1", "--format=%B", "HEAD")
	if err != nil {
		return fmt.Errorf("git log: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	msg := commitlint.Parse(cleanRawMessage(string(stdout)))
	rules := commitlint.Rules{
		AllowedTypes:     cfg.Commit.Types,
		ScopeRequired:    cfg.Commit.ScopeRequired,
		MaxSubjectLength: cfg.Commit.MaxSubjectLength,
	}
	issues := commitlint.Lint(msg, rules)
	if len(issues) == 0 {
		return nil
	}
	parts := make([]string, 0, len(issues))
	for _, iss := range issues {
		parts = append(parts, fmt.Sprintf("[%s] %s", iss.Code, iss.Message))
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

// runBuiltinBranchCheck validates the current branch name.
func runBuiltinBranchCheck(ctx context.Context, r git.Runner, cfg *config.Config) error {
	client := git.NewClient(r)
	branch, err := client.CurrentBranch(ctx)
	if err != nil {
		return err
	}
	res := checkBranch(branch, cfg.Branch.Patterns, cfg.Branch.Protected)
	if res.Matched || res.Skipped {
		return nil
	}
	return fmt.Errorf("branch %q did not match any configured pattern", branch)
}

// runBuiltinNoConflict performs a pre-merge conflict scan against the base branch.
// Thin wrapper over scanMergeConflicts; see precheck.go for the shared helper.
func runBuiltinNoConflict(ctx context.Context, r git.Runner, cfg *config.Config) error {
	client := git.NewClient(r)
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}
	base := cfg.BaseBranch
	if base == "" {
		b, err := client.DefaultBranch(ctx, remote)
		if err != nil {
			// Cannot determine base — inconclusive, pass with no error.
			return nil
		}
		base = b
	}

	target := remote + "/" + base
	mb, _, err := r.Run(ctx, "merge-base", "HEAD", target)
	if err != nil {
		// No upstream ref yet (e.g. fresh branch not pushed) — treat as pass.
		return nil
	}
	mergeBase := strings.TrimSpace(string(mb))

	conflicts, serr := scanMergeConflicts(ctx, r, mergeBase, "HEAD", target)
	if serr != nil {
		return fmt.Errorf("merge-tree: %w", serr)
	}
	if len(conflicts) > 0 {
		return fmt.Errorf("conflicts detected merging HEAD into %s (%d path(s))", target, len(conflicts))
	}
	return nil
}

// envWithoutAgentMode returns the current environment with GK_AGENT stripped,
// for preflight subprocess steps. GK_AGENT switches gk's OWN output to the
// agent envelope; a preflight step is a child process (the project's
// test/lint command) and must never inherit that mode. This matters when gk
// drives a project that itself reads GK_AGENT — most visibly gk's own test
// suite: a `gk ship` launched in an `export GK_AGENT=1` shell (as the gk
// agents contract instructs) would otherwise pass GK_AGENT=1 into
// `go test ./...`, flipping the gk-under-test to envelope output and breaking
// its bare-output assertions. The release that follows preflight reads
// nothing from the step's stdout (only its exit code), so stripping the flag
// changes no contract.
func envWithoutAgentMode() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		if strings.HasPrefix(kv, "GK_AGENT=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// runShellStep executes an arbitrary shell command via `sh -c`.
func runShellStep(ctx context.Context, command string) error {
	c := exec.CommandContext(ctx, "sh", "-c", command)
	c.Env = envWithoutAgentMode()
	out, err := c.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, trimmed)
	}
	return nil
}
