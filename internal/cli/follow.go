package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
)

// follow watches a REMOTE branch and, when it advances, hard-resets the local
// repo to the remote SHA (GitOps mirror) and runs a hook command once per
// change. It is a FOREGROUND process meant to be supervised externally
// (systemd/docker/k8s) — there is no built-in daemon manager.
//
// Update strategy is deliberately destructive: the local checkout is treated as
// disposable and reset --hard to the remote tip. The safety contract is the
// same wedge every gk verb that moves HEAD uses — a backup ref is written
// BEFORE the reset so the previous tip is always recoverable, and the reset is
// refused outright when the working tree carries uncommitted work (likely a
// human mistake) unless --discard-dirty is set.
//
// `gk follow` is intentionally NOT `gk watch`: `gk status --watch` already owns
// that name with the opposite, local-file-change semantics.

func init() {
	cmd := &cobra.Command{
		Use:   "follow <branch> [-- <hook> args...]",
		Short: "Watch a remote branch and mirror+run a hook each time it advances",
		Long: `Foreground watcher that polls a REMOTE branch and, when it advances,
hard-resets the local repo to the remote SHA and runs a hook command once
per change ("git-sync + watchexec", zero-infra, single binary).

The update is a GitOps mirror: the local checkout is disposable and is
reset --hard to the remote tip. This is DESTRUCTIVE — but recoverable:
a backup ref of the current HEAD is written BEFORE every reset, so the
previous tip can always be restored with

  git reset --hard <backup-ref>

Safety gates (per cycle, before any reset):
  - if the working tree has UNCOMMITTED changes the reset is REFUSED and
    the cycle reports an error, unless --discard-dirty is passed;
  - the backup ref is written first, then git fetch, then git reset --hard.

The hook runs synchronously inside the loop, so runs never overlap. On a
non-zero hook exit the poll interval backs off exponentially (interval ..
10×interval) so a broken commit cannot thrash; a clean run resets it.

Hook command precedence: a trailing "-- <cmd> args..." wins over --run.
With --run the string is executed via "sh -c". With neither, the cycle
still mirrors but runs no hook.

Designed to be supervised: SIGINT/SIGTERM stop the loop cleanly (exit 0).
Use --once to run exactly one cycle and exit (cron / tests).`,
		Args: cobra.MinimumNArgs(1),
		RunE: runFollow,
	}
	cmd.Flags().String("remote", "", "remote to watch (default: config remote, else origin)")
	cmd.Flags().Var(watchIntervalValue{d: &followInterval}, "interval", "poll interval in seconds (e.g. 30) or a duration (500ms, 1m)")
	cmd.Flags().String("run", "", "hook command run via `sh -c` on each change (a trailing `-- cmd...` overrides this)")
	cmd.Flags().Bool("discard-dirty", false, "allow the hard reset to discard uncommitted local changes (DESTRUCTIVE)")
	cmd.Flags().Bool("once", false, "run exactly one cycle (check, maybe update+hook) then exit")
	// watchIntervalValue.String() reports status --watch's 2s default; follow's
	// runtime default is 30s (enforced in runFollow). Correct the help text so
	// --help doesn't advertise the wrong default.
	if f := cmd.Flags().Lookup("interval"); f != nil {
		f.DefValue = "30s"
	}
	rootCmd.AddCommand(cmd)
}

// followInterval backs the --interval flag (bare-seconds-or-duration, shared
// with status --watch-interval). Defaults to 30s when the flag is untouched.
var followInterval time.Duration

// followOpts is the resolved configuration for one follow loop. Splitting it
// out (and the loop/cycle below) from the cobra wiring keeps the watch logic
// runner- and hook-injectable, so tests drive it with a FakeRunner and a stub
// hook instead of spawning git and sh.
type followOpts struct {
	remote       string
	branch       string
	interval     time.Duration
	discardDirty bool
	once         bool
	// hook runs the configured hook command and returns its exit code; nil
	// means "no hook configured" (mirror only). A non-nil error is a spawn
	// failure (binary missing), distinct from a non-zero exit code.
	hook func(ctx context.Context) (exitCode int, err error)
	// agent reports whether to emit one JSON envelope per cycle (GK_AGENT)
	// vs. concise human progress lines.
	agent bool
	now   func() time.Time
}

// followResult is the per-cycle outcome. In agent mode one of these is wrapped
// in the success envelope each cycle; field set matches the command's contract.
type followResult struct {
	Branch    string `json:"branch"`
	Remote    string `json:"remote"`
	RemoteSHA string `json:"remote_sha"`
	Changed   bool   `json:"changed"`
	Updated   bool   `json:"updated"`
	BackupRef string `json:"backup_ref,omitempty"`
	Ran       bool   `json:"ran"`
	ExitCode  int    `json:"exit_code"`
	Backoff   string `json:"backoff,omitempty"`
}

func runFollow(cmd *cobra.Command, args []string) error {
	branch := args[0]
	if err := guardRef(branch); err != nil {
		return fmt.Errorf("invalid branch: %w", err)
	}

	cfg, _ := config.Load(cmd.Flags())
	remote, _ := cmd.Flags().GetString("remote")
	if remote == "" {
		remote = cfg.Remote
	}
	if remote == "" {
		remote = "origin"
	}
	if err := guardRef(remote); err != nil {
		return fmt.Errorf("invalid remote: %w", err)
	}

	interval := followInterval
	if !cmd.Flags().Changed("interval") {
		interval = 30 * time.Second
	}

	discardDirty, _ := cmd.Flags().GetBool("discard-dirty")
	once, _ := cmd.Flags().GetBool("once")
	runStr, _ := cmd.Flags().GetString("run")

	runner := &git.ExecRunner{Dir: RepoFlag()}

	// Hook precedence: a trailing `-- cmd args...` wins over --run. cobra
	// hands us everything after `--` as args past the branch.
	var hook func(ctx context.Context) (int, error)
	if len(args) > 1 {
		hookArgs := args[1:]
		hook = func(ctx context.Context) (int, error) {
			return runHookExec(ctx, RepoFlag(), hookArgs[0], hookArgs[1:]...)
		}
	} else if strings.TrimSpace(runStr) != "" {
		hook = func(ctx context.Context) (int, error) {
			return runHookExec(ctx, RepoFlag(), "sh", "-c", runStr)
		}
	}

	// Graceful shutdown: SIGINT/SIGTERM cancel the loop's context. The wait
	// between cycles is interrupted immediately; a hook running at that moment
	// inherits this context and is cancelled too (exec sends SIGKILL once ctx is
	// done), so a supervised follow stops promptly instead of hanging on a hook.
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	opts := followOpts{
		remote:       remote,
		branch:       branch,
		interval:     interval,
		discardDirty: discardDirty,
		once:         once,
		hook:         hook,
		agent:        AgentOut(),
		now:          time.Now,
	}
	return followLoop(ctx, runner, cmd.OutOrStdout(), opts)
}

// runHookExec runs <name args...> in dir, streaming stdout/stderr through to
// the parent's, and returns its exit code. A nil error with a non-zero code is
// the normal "hook failed" path; a non-nil error is a spawn failure. The hook
// inherits ctx (exec.CommandContext): a shutdown signal arriving mid-run
// cancels it, so a supervised follow stops promptly rather than blocking on a
// long-running hook.
func runHookExec(ctx context.Context, dir, name string, args ...string) (int, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	err := c.Run()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return -1, err
}

// followLoop runs the poll/mirror/hook cycle until ctx is cancelled (signal) or
// opts.once short-circuits it. It owns the backoff state and the sleep between
// cycles; the actual per-cycle work lives in followCycle so it stays testable
// in isolation.
func followLoop(ctx context.Context, runner git.Runner, w io.Writer, opts followOpts) error {
	if opts.now == nil {
		opts.now = time.Now
	}
	// lastApplied is the remote SHA we have already mirrored to. Empty means
	// "nothing applied yet", so the first observed SHA counts as a change and
	// triggers the initial mirror — the watcher converges the local checkout
	// to the remote on startup rather than waiting for the next push.
	lastApplied := ""
	backoff := time.Duration(0)
	maxBackoff := 10 * opts.interval

	for {
		res, cerr := followCycle(ctx, runner, opts, &lastApplied)

		// A hook that exited non-zero (or a cycle error like a refused dirty
		// reset) trips backoff; a clean cycle resets it.
		failed := cerr != nil || (res.Ran && res.ExitCode != 0)
		if failed {
			backoff = nextBackoff(backoff, opts.interval, maxBackoff)
		} else {
			backoff = 0
		}

		// --once behaves like a normal one-shot command: a cycle error
		// propagates to main (which renders the single error envelope on stderr
		// and sets the exit code); success prints one result envelope here. This
		// avoids the double-render that emitting inside the cycle would cause.
		if opts.once {
			if cerr != nil {
				return cerr
			}
			followEmit(w, opts, res)
			return nil
		}

		// Continuous mode owns ALL per-cycle output — main never sees these
		// errors (the loop folds them into backoff), so each poll renders inline:
		// exactly one envelope/line per cycle.
		if cerr != nil {
			followEmitErr(w, opts, cerr)
		} else {
			followEmit(w, opts, res)
		}

		wait := opts.interval
		if backoff > 0 {
			wait = backoff
		}
		if !sleepCtx(ctx, wait) {
			// ctx cancelled (SIGINT/SIGTERM) — clean shutdown.
			if !opts.agent {
				fmt.Fprintln(w, "follow: stopped")
			}
			return nil
		}
	}
}

// nextBackoff advances the exponential backoff: the first failure waits one
// interval, each subsequent failure doubles it, capped at max. A clean cycle
// resets it to 0 (handled by the caller). Pulled out as a pure function so the
// doubling/cap math is unit-testable without real timers.
func nextBackoff(cur, interval, max time.Duration) time.Duration {
	if cur == 0 {
		cur = interval
	} else {
		cur *= 2
	}
	if cur > max {
		cur = max
	}
	return cur
}

// sleepCtx waits d, returning false if ctx is cancelled first. The watcher
// sleeps between cycles here so a shutdown signal interrupts the wait instead
// of being deferred to the end of a full interval.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// followCycle performs one poll: read the remote SHA cheaply, and if it changed
// since lastApplied, run the safety-gated mirror (backup → fetch → reset) and
// the hook. It returns the cycle result and error WITHOUT emitting anything —
// the caller (followLoop) owns all output, so the emission contract ("one
// envelope per cycle") lives in exactly one place.
//
// lastApplied is updated in place to the freshly-mirrored SHA once a mirror
// completes, so a transient hook failure does NOT re-mirror the same SHA on the
// next cycle — only the hook is retried (under backoff).
func followCycle(ctx context.Context, runner git.Runner, opts followOpts, lastApplied *string) (followResult, error) {
	res := followResult{Branch: opts.branch, Remote: opts.remote}

	// a. cheap remote read — no fetch unless the SHA actually moved.
	sha, err := lsRemoteSHA(ctx, runner, opts.remote, opts.branch)
	if err != nil {
		return res, err
	}
	res.RemoteSHA = sha

	// b. unchanged → idle.
	if sha == *lastApplied {
		return res, nil
	}
	res.Changed = true

	// c. safety gate — refuse to reset over uncommitted work unless told to.
	if !opts.discardDirty {
		dirty, derr := followWorkingTreeDirty(ctx, runner)
		if derr != nil {
			return res, fmt.Errorf("follow: cannot read working tree status: %w", derr)
		}
		if dirty {
			return res, WithRemedy(
				fmt.Errorf("follow: remote %s/%s advanced but the working tree has uncommitted changes — refusing to hard-reset over them", opts.remote, opts.branch),
				"commit/stash the changes, or pass --discard-dirty to mirror anyway (DESTRUCTIVE)",
				errRemedy{Command: "git stash", Safety: "safe"},
				errRemedy{Command: fmt.Sprintf("gk follow %s --discard-dirty", opts.branch), Safety: "destructive"},
			)
		}
	}

	// backup BEFORE the reset — the recovery anchor. Skipping it is ONLY safe
	// when the repo genuinely has no commits yet (nothing to lose). If HEAD is
	// unreadable but the repo HAS history, a reset with no recovery anchor is
	// the one thing we must never do — abort the cycle instead.
	head, herr := gitsafe.ResolveRef(ctx, runner, "HEAD")
	switch {
	case herr == nil && head != "":
		ref := gitsafe.BackupRefName("follow", opts.branch, opts.now())
		if _, stderr, berr := runner.Run(ctx, "update-ref", ref, head); berr != nil {
			return res, fmt.Errorf("follow: write backup ref: %s: %w", strings.TrimSpace(string(stderr)), berr)
		}
		res.BackupRef = ref
	default:
		empty, eerr := repoHasNoCommits(ctx, runner)
		if eerr != nil || !empty {
			return res, fmt.Errorf("follow: cannot resolve HEAD to back up before the reset; refusing a destructive reset with no recovery anchor (HEAD: %v)", herr)
		}
		// genuinely empty repo (no commits) → nothing to back up; mirror on.
	}

	// fetch the advanced ref, then hard-reset the mirror to the remote SHA.
	if err := git.NewClient(runner).Fetch(ctx, opts.remote, opts.branch, false); err != nil {
		return res, fmt.Errorf("follow: fetch %s %s: %w", opts.remote, opts.branch, err)
	}
	if _, stderr, rerr := runner.Run(ctx, "reset", "--hard", sha); rerr != nil {
		return res, fmt.Errorf("follow: reset --hard %s: %s: %w", sha, strings.TrimSpace(string(stderr)), rerr)
	}
	res.Updated = true
	// Record the applied SHA now: even if the hook fails below, the mirror is
	// done — we must not re-reset the same SHA on the retry cycle.
	*lastApplied = sha

	// d. run the hook synchronously (non-overlapping by construction).
	if opts.hook != nil {
		code, hkerr := opts.hook(ctx)
		res.Ran = true
		if hkerr != nil {
			res.ExitCode = -1
			return res, fmt.Errorf("follow: hook failed to start: %w", hkerr)
		}
		res.ExitCode = code
	}

	return res, nil
}

// repoHasNoCommits reports whether the repository has no commits on any ref yet
// (a freshly `git init`'d tree). Used by followCycle to tell a genuinely empty
// repo — where skipping the pre-reset backup is harmless — apart from an
// unreadable-HEAD failure, where it must refuse the destructive reset.
func repoHasNoCommits(ctx context.Context, runner git.Runner) (bool, error) {
	out, _, err := runner.Run(ctx, "rev-list", "-n", "1", "--all")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "", nil
}

// lsRemoteSHA reads the tip of refs/heads/<branch> on <remote> via a single
// `git ls-remote` — the cheap network probe that lets the loop avoid a fetch
// unless the SHA actually moved. Empty output (branch not on the remote) is an
// actionable error, not a silent "no change".
func lsRemoteSHA(ctx context.Context, runner git.Runner, remote, branch string) (string, error) {
	out, stderr, err := runner.Run(ctx, "ls-remote", remote, "refs/heads/"+branch)
	if err != nil {
		return "", fmt.Errorf("follow: ls-remote %s %s: %s: %w", remote, branch, strings.TrimSpace(string(stderr)), err)
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", fmt.Errorf("follow: remote %s has no branch %q", remote, branch)
	}
	// Format: "<sha>\trefs/heads/<branch>" — take the first field of the first line.
	fields := strings.Fields(line)
	if len(fields) == 0 || len(fields[0]) < 7 {
		return "", fmt.Errorf("follow: unexpected ls-remote output: %q", line)
	}
	return fields[0], nil
}

// followWorkingTreeDirty reports whether the working tree carries any
// uncommitted change — tracked OR untracked. The gate uses the broader
// porcelain (untracked included) rather than IsDirty's tracked-only view
// because a hard reset would also clobber untracked files a human just created.
func followWorkingTreeDirty(ctx context.Context, runner git.Runner) (bool, error) {
	out, _, err := runner.Run(ctx, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// followEmit writes the per-cycle success output: one JSON envelope in agent
// mode, a concise human line otherwise.
func followEmit(w io.Writer, opts followOpts, res followResult) {
	if opts.agent {
		_ = emitAgentResult(w, res)
		return
	}
	switch {
	case res.Updated && res.Ran:
		fmt.Fprintf(w, "follow: %s/%s → %s  mirrored, hook exit %d\n", res.Remote, res.Branch, shortFollowSHA(res.RemoteSHA), res.ExitCode)
	case res.Updated:
		fmt.Fprintf(w, "follow: %s/%s → %s  mirrored (no hook)\n", res.Remote, res.Branch, shortFollowSHA(res.RemoteSHA))
	default:
		fmt.Fprintf(w, "follow: %s/%s at %s  no change\n", res.Remote, res.Branch, shortFollowSHA(res.RemoteSHA))
	}
}

// followEmitErr renders a cycle failure for the continuous loop — the agent
// envelope on stdout, or a human line — and returns nothing: the caller already
// holds the error (it folds it into backoff). The watcher never crashes on a
// cycle error; it logs and keeps polling.
func followEmitErr(w io.Writer, opts followOpts, err error) {
	if opts.agent {
		fmt.Fprintln(w, FormatErrorJSON(err))
	} else {
		fmt.Fprintf(w, "follow: %v\n", err)
	}
}

func shortFollowSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
