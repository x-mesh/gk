package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/secrets"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "push [<remote>] [<branch>]",
		Short: "Guarded push: secret scan + protected-branch confirmation",
		Args:  cobra.MaximumNArgs(2),
		RunE:  runPush,
	}
	cmd.Flags().Bool("force", false, "allow non-fast-forward (uses --force-with-lease)")
	cmd.Flags().Bool("skip-scan", false, "skip the secret-pattern scan")
	cmd.Flags().Bool("yes", false, "skip interactive confirmations (for automation)")
	rootCmd.AddCommand(cmd)
}

func runPush(cmd *cobra.Command, args []string) error {
	cfg, _ := config.Load(cmd.Flags())
	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	ctx := cmd.Context()

	force, _ := cmd.Flags().GetBool("force")
	skipScan, _ := cmd.Flags().GetBool("skip-scan")
	yes, _ := cmd.Flags().GetBool("yes")

	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}
	branch := ""
	switch len(args) {
	case 1:
		remote = args[0]
	case 2:
		remote = args[0]
		branch = args[1]
	}
	if branch == "" {
		b, err := client.CurrentBranch(ctx)
		if err != nil {
			return err
		}
		branch = b
	}

	// Protected branch gate
	if isProtected(branch, cfg.Push.Protected) && force {
		if !cfg.Push.AllowForce {
			if !yes && !ui.IsTerminal() {
				return fmt.Errorf("refusing to force-push to protected branch %q in non-interactive mode", branch)
			}
			if !yes {
				fmt.Fprintf(cmd.ErrOrStderr(), "force-pushing to protected branch %q. type the branch name to confirm: ", branch)
				sc := bufio.NewScanner(cmd.InOrStdin())
				if !sc.Scan() {
					return fmt.Errorf("confirmation aborted")
				}
				if strings.TrimSpace(sc.Text()) != branch {
					return fmt.Errorf("confirmation did not match; aborting")
				}
			}
		}
	}

	Dbg("push: remote=%s branch=%s force=%v protected=%v", remote, branch, force, isProtected(branch, cfg.Push.Protected))

	// Secret scan
	if !skipScan {
		findings, err := scanCommitsToPush(ctx, runner, remote, branch)
		if err != nil {
			return fmt.Errorf("secret scan: %w", err)
		}
		Dbg("push: secret-scan — %d finding(s)", len(findings))
		if len(findings) > 0 {
			fmt.Fprintln(cmd.ErrOrStderr(), "potential secrets detected:")
			for _, f := range findings {
				fmt.Fprintf(cmd.ErrOrStderr(), "  [%s] line %d: %s\n", f.Kind, f.Line, f.Sample)
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "  use --skip-scan to override (not recommended)")
			return fmt.Errorf("aborting push")
		}
	} else {
		Dbg("push: secret-scan skipped (--skip-scan)")
	}

	// Actual push — auto-set upstream when the branch has none yet.
	hasUpstream := branchHasUpstream(ctx, runner, branch)
	gitArgs := []string{"push"}
	if force {
		gitArgs = append(gitArgs, "--force-with-lease")
	}
	if !hasUpstream {
		gitArgs = append(gitArgs, "--set-upstream")
	}
	gitArgs = append(gitArgs, remote, branch)
	Dbg("push: hasUpstream=%v argv=%v", hasUpstream, gitArgs)

	// Snapshot ahead-count and HEAD before push so we can render a
	// summary line afterwards. Best-effort — failure here just means
	// we fall back to whatever git's own output already conveys.
	ahead := pushAheadCount(ctx, runner, remote, branch, hasUpstream)
	short := pushHeadShort(ctx, runner, branch)

	// --verbose mode streams git's progress (objects/deltas/refs) into a
	// scrollable viewport so the user can watch the push proceed.
	if Verbose() && ui.IsTerminal() {
		args := []string{}
		if RepoFlag() != "" {
			args = append(args, "-C", RepoFlag())
		}
		args = append(args, gitArgs...)
		title := fmt.Sprintf("pushing %s → %s", branch, remote)
		if err := ui.RunCommandStreamTUI(ctx, title, "git", args...); err != nil {
			return err
		}
		return nil
	}

	stop := ui.StartBubbleSpinner(fmt.Sprintf("pushing %s → %s", branch, remote))
	stdout, stderr, err := runner.Run(ctx, gitArgs...)
	stop()
	if err != nil {
		fmt.Fprint(cmd.ErrOrStderr(), string(stderr))
		return err
	}

	// JSON mode: render structured result, suppress git's raw output.
	if JSONOut() {
		_ = stdout // git push writes nothing to stdout
		_ = stderr
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(pushResult{
			Remote: remote,
			Branch: branch,
			Ahead:  ahead,
			Head:   short,
		})
	}

	// Default: surface git's output (it's where users see the URL +
	// ref-update line) and append a one-line gk-style summary so the
	// flow has a clear "what just happened" signal.
	fmt.Fprint(cmd.OutOrStdout(), string(stdout))
	fmt.Fprint(cmd.ErrOrStderr(), string(stderr))
	if e := EasyEngine(); e != nil {
		if msg := e.PushSummaryHint(ahead, remote, branch, short); msg != "" {
			fmt.Fprintln(cmd.ErrOrStderr(), msg)
		}
	}
	return nil
}

// pushResult is the JSON shape emitted when --json is active on push.
type pushResult struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Ahead  int    `json:"ahead"`
	Head   string `json:"head"`
}

// pushAheadCount returns how many commits the local branch is ahead of
// its upstream counterpart, or 0 when no upstream exists / git fails.
// Best-effort: callers must tolerate a 0 result on transient failures.
func pushAheadCount(ctx context.Context, r git.Runner, remote, branch string, hasUpstream bool) int {
	if !hasUpstream {
		return 0
	}
	out, _, err := r.Run(ctx, "rev-list", "--count", remote+"/"+branch+".."+branch)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0
	}
	return n
}

// pushHeadShort returns the abbreviated SHA at the tip of branch, or ""
// when git fails. Used purely for display.
func pushHeadShort(ctx context.Context, r git.Runner, branch string) string {
	out, _, err := r.Run(ctx, "rev-parse", "--short", branch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// branchHasUpstream reports whether <branch>@{upstream} resolves.
// Uses rev-parse; returns false if branch has no configured upstream.
func branchHasUpstream(ctx context.Context, r git.Runner, branch string) bool {
	_, _, err := r.Run(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", branch+"@{upstream}")
	return err == nil
}

// isProtected reports whether branch is in the protected list.
func isProtected(branch string, list []string) bool {
	for _, p := range list {
		if p == branch {
			return true
		}
	}
	return false
}

// scanCommitsToPush fetches the range "remote/branch..HEAD" diff and scans it.
// If the upstream ref is missing, scans all commits reachable from HEAD.
//
// Only added (`+`) lines from non-test files are scanned. This mirrors
// what gitleaks-style scanners do — removals already exist in the base
// branch (so flagging them again is noise), and test files routinely
// contain intentional fake secrets used to verify detection logic.
// Without these filters every fixture cleanup commit becomes a ship
// blocker, which we hit immediately after tightening the privacy gate.
func scanCommitsToPush(ctx context.Context, r git.Runner, remote, branch string) ([]secrets.Finding, error) {
	ref := remote + "/" + branch
	_, _, err := r.Run(ctx, "rev-parse", "--verify", ref+"^{commit}")
	rng := "HEAD"
	if err == nil {
		rng = ref + "..HEAD"
	}
	stdout, stderr, lerr := r.Run(ctx, "log", "-p", "--no-color", rng)
	if lerr != nil {
		return nil, fmt.Errorf("%s: %w", strings.TrimSpace(string(stderr)), lerr)
	}
	return scanDiffAdditions(string(stdout)), nil
}

// scanDiffAdditions parses a unified diff (e.g. `git log -p` output) and
// runs secrets.Scan over the added content of each file, after dropping
// test files. Returns findings whose Line refers to the relevant blob
// position so existing renderers keep working.
func scanDiffAdditions(diff string) []secrets.Finding {
	var b strings.Builder
	currentFile := ""
	skip := false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			// "diff --git a/path b/path" — extract the b/ path.
			if idx := strings.Index(line, " b/"); idx > 0 {
				currentFile = line[idx+len(" b/"):]
			} else {
				currentFile = ""
			}
			skip = isTestFile(currentFile)
			b.WriteString(secrets.PayloadFileHeader(currentFile) + "\n")
		case strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "--- "),
			strings.HasPrefix(line, "@@ "), strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "new file mode"), strings.HasPrefix(line, "deleted file mode"),
			strings.HasPrefix(line, "similarity index"), strings.HasPrefix(line, "rename "),
			strings.HasPrefix(line, "Binary files"):
			// Diff metadata — skip but keep blob in step.
			b.WriteString("\n")
		case strings.HasPrefix(line, "+"):
			if skip {
				b.WriteString("\n")
				continue
			}
			b.WriteString(strings.TrimPrefix(line, "+") + "\n")
		default:
			// Commit log header lines, removal lines (`-...`), context
			// lines (` ...`), and blank separators all collapse to empty
			// rows so secrets.Scan never sees them but blob line numbers
			// still line up with `git log -p` output (handy for debugging
			// regressions like the one this code path was added to fix).
			b.WriteString("\n")
		}
	}
	return secrets.Scan(b.String(), nil)
}
