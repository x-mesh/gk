package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/easy"
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
	// --from names the branch to push regardless of the current checkout —
	// the mirror image of `gk merge --into <branch>`. In a bare/worktree
	// layout you merge into main from another worktree, then push that main
	// without switching to it: `gk push --from main`.
	cmd.Flags().String("from", "", "branch to push, regardless of the current checkout (defaults to the current branch)")
	cmd.Flags().Bool("force", false, "allow non-fast-forward (uses --force-with-lease)")
	cmd.Flags().Bool("skip-scan", false, "skip the secret-pattern scan")
	// --no-verify is the alias-friendly spelling that matches `gk commit -n`;
	// note `git push -n` means --dry-run, but gk push has no dry-run, so there
	// is no clash. Both flags do the same thing: skip the secret scan.
	cmd.Flags().BoolP("no-verify", "n", false, "skip the secret-pattern scan (same as --skip-scan; matches 'gk commit -n')")
	cmd.Flags().Bool("yes", false, "skip interactive confirmations (for automation)")
	// -v shows ±1 source line of masked context around each secret-scan hit
	// (also enabled persistently via `gk config set push.scan_context true`).
	// A local flag, mirroring pull/status, so it never clashes with the global
	// persistent --verbose.
	cmd.Flags().BoolP("verbose", "v", false, "show ±1 source line of context around each secret-scan hit")
	// --scan-context is the verbose secret context WITHOUT the verbose
	// git-progress stream. `gk land` forwards this (not --verbose) so a child
	// push shows ±1 context but never opens the streaming TUI viewport, which
	// would flash and vanish inside land's own step output.
	cmd.Flags().Bool("scan-context", false, "show secret-scan context only, without the git-progress stream (used by gk land)")
	_ = cmd.Flags().MarkHidden("scan-context")
	rootCmd.AddCommand(cmd)
}

func runPush(cmd *cobra.Command, args []string) error {
	cfg, _ := config.Load(cmd.Flags())
	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	ctx := cmd.Context()

	force, _ := cmd.Flags().GetBool("force")
	skipScan, _ := cmd.Flags().GetBool("skip-scan")
	if nv, _ := cmd.Flags().GetBool("no-verify"); nv {
		skipScan = true
	}
	yes, _ := cmd.Flags().GetBool("yes")
	// The local -v shadows the global persistent --verbose for this command, so
	// fold both in: a user can write `gk push -v` or `gk --verbose push`. verbose
	// drives BOTH the git-progress stream and the secret-scan context. The
	// hidden --scan-context asks for the context ALONE (no stream) — gk land
	// forwards it so a child push doesn't flash a streaming viewport.
	verbose, _ := cmd.Flags().GetBool("verbose")
	verbose = verbose || Verbose()
	scanCtx, _ := cmd.Flags().GetBool("scan-context")
	showScanContext := verbose || scanCtx || cfg.Push.ScanContext

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
	from, _ := cmd.Flags().GetString("from")
	branch, err := resolvePushBranch(branch, from)
	if err != nil {
		return err
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
		base := resolveBaseForStatus(ctx, runner, client, cfg).Resolved
		cmp := resolveScanCmp(ctx, runner, remote, branch, base)
		findings, err := scanCommitsToPush(ctx, runner, cmp)
		if err != nil {
			return fmt.Errorf("secret scan: %w", err)
		}
		Dbg("push: secret-scan — %d finding(s)", len(findings))
		if len(findings) > 0 {
			renderScanFindings(cmd.ErrOrStderr(), findings, showScanContext)
			fmt.Fprintln(cmd.ErrOrStderr(), "  use --no-verify (or --skip-scan) to override (not recommended)")
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
	if verbose && ui.IsTerminal() {
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

	// JSON mode: render structured result, suppress git's raw output. The
	// reported count is reportedPushCount(stderr, ahead), NOT the raw pre-push
	// ahead — so the agent envelope agrees with the human summary when git
	// reports a no-op against a stale remote-tracking ref (see pushReport).
	if JSONOut() {
		_ = stdout // git push writes nothing to stdout
		return emitAgentResult(cmd.OutOrStdout(), pushResult{
			Remote: remote,
			Branch: branch,
			Ahead:  reportedPushCount(string(stderr), ahead),
			Head:   short,
		})
	}

	// Default: surface git's output (it's where users see the URL +
	// ref-update line) and append a one-line gk-style summary so the
	// flow has a clear "what just happened" signal.
	fmt.Fprint(cmd.OutOrStdout(), string(stdout))
	gitOut, summary := pushReport(string(stderr), ahead, EasyEngine(), remote, branch, short)
	fmt.Fprint(cmd.ErrOrStderr(), gitOut)
	if summary != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), summary)
	}
	return nil
}

// pushReport decides what to print after a successful push, given git's stderr
// and the pre-push ahead count. Returns the (possibly emptied) git output to
// echo and the localized one-line summary to append. The reported commit count
// is delegated to reportedPushCount; on a no-op push git's duplicate English
// "Everything up-to-date" line is dropped in favor of the localized summary,
// while a real push's stderr (URL + ref-update lines) is always kept.
func pushReport(gitStderr string, ahead int, eng *easy.Engine, remote, branch, short string) (gitOut, summary string) {
	upToDate := strings.TrimSpace(gitStderr) == "Everything up-to-date"
	if eng != nil {
		summary = eng.PushSummaryHint(reportedPushCount(gitStderr, ahead), remote, branch, short)
	}
	if summary != "" && upToDate {
		return "", summary
	}
	return gitStderr, summary
}

// reportedPushCount is the commit count to report after a push. git's
// "Everything up-to-date" (emitted under the LC_ALL=C guardEnv) is authoritative:
// when git sent nothing we report 0 even if the pre-push `ahead` is stale and
// positive — commits already on the real remote still count against a stale
// remote-tracking ref. Otherwise the pre-push ahead count stands. Shared by the
// human summary and the --json/agent envelope so the two never disagree.
func reportedPushCount(gitStderr string, ahead int) int {
	if strings.TrimSpace(gitStderr) == "Everything up-to-date" {
		return 0
	}
	return ahead
}

// resolvePushBranch decides which branch `gk push` targets, given the
// positional [<branch>] argument and the --from flag. --from is the mirror
// of `gk merge --into`: it names the branch to push regardless of the current
// checkout (e.g. push main from another worktree without switching to it).
// When both are set they must agree. An empty return means "fall back to the
// current branch".
func resolvePushBranch(posBranch, from string) (string, error) {
	if from != "" {
		if posBranch != "" && posBranch != from {
			return "", fmt.Errorf("conflicting branch: positional %q vs --from %q", posBranch, from)
		}
		return from, nil
	}
	return posBranch, nil
}

// pushResult is the JSON shape emitted when --json is active on push.
type pushResult struct {
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Ahead  int    `json:"ahead"`
	Head   string `json:"head"`
}

// pushAheadCount returns how many commits the local branch will push: counted
// against its upstream when one exists, else the commits not yet on any of the
// remote's refs (the first-push case). Returning 0 for a no-upstream branch
// would render a false "already up-to-date" summary for a genuine first push,
// so that case counts `<branch> --not --remotes=<remote>` instead. Returns 0
// when git fails — best-effort, callers must tolerate a 0 on transient failures.
func pushAheadCount(ctx context.Context, r git.Runner, remote, branch string, hasUpstream bool) int {
	args := []string{"rev-list", "--count", remote + "/" + branch + ".." + branch}
	if !hasUpstream {
		args = []string{"rev-list", "--count", branch, "--not", "--remotes=" + remote}
	}
	out, _, err := r.Run(ctx, args...)
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

// resolveScanCmp picks the ref to diff HEAD against for the pre-push secret
// scan: the branch's own upstream (remote/branch) when it exists, else the
// base branch's remote ref, else a local base, else "" (no comparison point —
// scan the whole history). Anchoring the scan to a real base does two things:
// it scopes the scan to what THIS push adds (content already on the remote is
// not re-flagged), and — because scanCommitsToPush diffs base...HEAD with a
// 3-dot net diff — it numbers hunks against the current HEAD file, so reported
// line numbers match what the user sees in their editor. (A plain
// "remote/branch..HEAD" log -p numbered hunks per-commit, so a token added in
// an early commit was reported at its line in THAT commit, drifting from the
// final file once later commits inserted lines above it.)
func resolveScanCmp(ctx context.Context, r git.Runner, remote, branch, base string) string {
	hasCommit := func(ref string) bool {
		_, _, err := r.Run(ctx, "rev-parse", "--verify", ref+"^{commit}")
		return err == nil
	}
	if up := remote + "/" + branch; hasCommit(up) {
		return up
	}
	if base != "" {
		if rb := remote + "/" + base; hasCommit(rb) {
			return rb
		}
		if hasCommit(base) {
			return base
		}
	}
	return ""
}

// scanCommitsToPush scans the additions this push would publish. cmp is the ref
// to compare HEAD against (from resolveScanCmp); "" means no base is known.
//
// Only added (`+`) lines from non-test files are scanned. This mirrors
// what gitleaks-style scanners do — removals already exist in the base
// branch (so flagging them again is noise), and test files routinely
// contain intentional fake secrets used to verify detection logic.
// Without these filters every fixture cleanup commit becomes a ship
// blocker, which we hit immediately after tightening the privacy gate.
func scanCommitsToPush(ctx context.Context, r git.Runner, cmp string) ([]secrets.Finding, error) {
	if cmp != "" {
		// Net 3-dot diff: what HEAD adds over its merge-base with cmp, with
		// hunk line numbers anchored to the current HEAD file.
		stdout, stderr, err := r.Run(ctx, "diff", "--no-color", cmp+"...HEAD")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", strings.TrimSpace(string(stderr)), err)
		}
		return scanDiffAdditions(string(stdout)), nil
	}
	// No comparison point (first push of a brand-new history): scan the whole
	// HEAD. log -p numbers hunks per-commit so lines may drift, but with no
	// base there is nothing to anchor to.
	stdout, stderr, err := r.Run(ctx, "log", "-p", "--no-color", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", strings.TrimSpace(string(stderr)), err)
	}
	return scanDiffAdditions(string(stdout)), nil
}

// scanDiffAdditions parses a unified diff (e.g. `git log -p` output) and
// runs secrets.Scan over the added content of each file, after dropping
// test files. Returns findings whose Line refers to the relevant blob
// position so existing renderers keep working.
//
// FileLine is corrected from the diff's hunk headers: secrets.Scan derives a
// file-relative line by counting blob rows after a PayloadFileHeader, but a
// diff only carries the changed slice of a file, so that count is the offset
// within the diff — not the line in the post-image file. We track each hunk's
// `@@ … +newStart,… @@` and overwrite FileLine with the true post-image line,
// so output reads e.g. "src/foo.rs:218" instead of a meaningless diff offset.
func scanDiffAdditions(diff string) []secrets.Finding {
	var b strings.Builder
	currentFile := ""
	skip := false
	// fileLineOf maps a 1-based blob line (what secrets.Scan reports as
	// Finding.Line) to the real post-image file line, for added ('+') rows.
	fileLineOf := map[int]int{}
	blobLine := 0 // 1-based line of the blob handed to secrets.Scan
	newLine := 0  // next post-image file line, from the active @@ hunk (0 = none)
	write := func(s string) {
		b.WriteString(s)
		b.WriteString("\n")
		blobLine++
	}
	// Verbose-context capture: for each scanned ('+') blob line, remember the
	// source line itself plus the displayable line immediately above/below it
	// in the post-image. "Displayable" = added or context rows; removals are
	// skipped (absent from the post-image) and hunk/file boundaries reset the
	// window so context never crosses a gap.
	lineText := map[int]string{}
	ctxBefore := map[int]string{}
	ctxAfter := map[int]string{}
	prevDisp := ""         // last displayed source line in the current hunk
	var pendingAfter []int // added blob lines still awaiting their following line
	resetCtx := func() { prevDisp = ""; pendingAfter = pendingAfter[:0] }
	display := func(text string, addedBlob int) {
		for _, bl := range pendingAfter {
			ctxAfter[bl] = text
		}
		pendingAfter = pendingAfter[:0]
		if addedBlob > 0 {
			lineText[addedBlob] = text
			ctxBefore[addedBlob] = prevDisp
			pendingAfter = append(pendingAfter, addedBlob)
		}
		prevDisp = text
	}
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
			newLine = 0
			resetCtx()
			write(secrets.PayloadFileHeader(currentFile))
		case strings.HasPrefix(line, "@@ "):
			// "@@ -a,b +c,d @@" — c is the first post-image line of the hunk.
			newLine = parseHunkNewStart(line)
			resetCtx()
			write("")
		case strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "--- "),
			strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "new file mode"), strings.HasPrefix(line, "deleted file mode"),
			strings.HasPrefix(line, "similarity index"), strings.HasPrefix(line, "rename "),
			strings.HasPrefix(line, "Binary files"):
			// Diff metadata — skip but keep blob in step.
			write("")
		case strings.HasPrefix(line, "+"):
			text := strings.TrimPrefix(line, "+")
			if skip {
				write("")
			} else {
				write(text)
				if newLine > 0 {
					fileLineOf[blobLine] = newLine
				}
				display(text, blobLine)
			}
			if newLine > 0 {
				newLine++ // an added line advances the post-image counter
			}
		case strings.HasPrefix(line, "-"):
			// Removal: present in the pre-image only; the secret is not new
			// content and does not advance the post-image counter.
			write("")
		default:
			// Context lines (" ...") advance the post-image counter but are
			// never scanned; commit-log headers and blank separators carry no
			// leading space and collapse to empty rows. Blob line numbers
			// still line up with `git log -p` output (handy for debugging).
			write("")
			if newLine > 0 && strings.HasPrefix(line, " ") {
				display(strings.TrimPrefix(line, " "), 0)
				newLine++
			}
		}
	}
	findings := secrets.Scan(b.String(), nil)
	for i := range findings {
		bl := findings[i].Line
		if fl, ok := fileLineOf[bl]; ok {
			findings[i].FileLine = fl
		}
		if t, ok := lineText[bl]; ok {
			findings[i].LineText = secrets.MaskLine(t)
			findings[i].ContextBefore = secrets.MaskLine(ctxBefore[bl])
			findings[i].ContextAfter = secrets.MaskLine(ctxAfter[bl])
		}
	}
	return findings
}

// renderScanFindings prints the "potential secrets detected" block shared by
// push and ship. With verbose set, each hit is followed by its masked ±1 source
// context (when the diff-aware scanner captured it) so a reviewer can judge a
// false positive in place without leaving the terminal.
func renderScanFindings(w io.Writer, findings []secrets.Finding, verbose bool) {
	fmt.Fprintln(w, "potential secrets detected:")
	for _, f := range findings {
		fmt.Fprintf(w, "  [%s] %s: %s\n", f.Kind, f.Location(), f.Sample)
		if verbose && f.LineText != "" {
			if f.ContextBefore != "" {
				fmt.Fprintf(w, "      %d │ %s\n", f.FileLine-1, f.ContextBefore)
			}
			fmt.Fprintf(w, "    > %d │ %s\n", f.FileLine, f.LineText)
			if f.ContextAfter != "" {
				fmt.Fprintf(w, "      %d │ %s\n", f.FileLine+1, f.ContextAfter)
			}
		}
	}
}

// parseHunkNewStart extracts c from a "@@ -a,b +c,d @@" hunk header — the
// 1-based first line of the hunk in the post-image (new) file. Returns 0 on a
// malformed header, leaving the file-line mapping untouched for that hunk.
func parseHunkNewStart(line string) int {
	plus := strings.Index(line, "+")
	if plus < 0 {
		return 0
	}
	rest := line[plus+1:]
	if end := strings.IndexAny(rest, ", @"); end >= 0 {
		rest = rest[:end]
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return 0
	}
	return n
}
