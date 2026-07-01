package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
)

// Quality-gate hook for `gk worktree finish`. A gate runs an external review
// command (e.g. `xm panel {patch} --json`) against the exact patch that a
// worktree merge produces — before merging, after merging, or both — and
// blocks/pauses the finish on failure. gk owns only the deterministic git
// plumbing (lock the target, pin SHAs under the lock, build the patch, run the
// command, record an audit file); it never interprets the reviewer's verdict.
//
// The lock serializes concurrent finishes on the same target so the patch the
// gate approved is byte-for-byte the patch that merges: with the target lock
// held, no other finish can advance the target between "pin SHA" and "merge".

// gatePhase enumerates when the gate runs relative to the merge.
const (
	gatePhaseBefore = "before"
	gatePhaseAfter  = "after"
	gatePhaseBoth   = "both"
)

// gateSpec is the parsed --gate configuration. tokens is the pre-substitution
// argv (already split): each element becomes exactly one argv element after
// {token} substitution, so a substituted value containing spaces or shell
// metacharacters is never re-split — there is no shell, hence no injection.
type gateSpec struct {
	tokens    []string
	phase     string
	timeout   time.Duration
	keepPatch bool
}

// worktreeGateResultJSON is the machine-readable gate outcome embedded in
// worktreeFinishJSON.Gate. Fields are append-only.
type worktreeGateResultJSON struct {
	Phase   string      `json:"phase"`
	Before  string      `json:"before,omitempty"` // passed | failed | skipped
	After   string      `json:"after,omitempty"`  // passed | failed | skipped
	Paused  bool        `json:"paused,omitempty"`
	Merged  bool        `json:"merged,omitempty"`
	Patch   string      `json:"patch,omitempty"`   // kept patch path (--gate-keep-patch)
	Recover []errRemedy `json:"recover,omitempty"` // resume/abort commands when paused
	RunID   string      `json:"run_id,omitempty"`
}

// gateStateFile is the on-disk audit record for one gate run, written under
// <git-common-dir>/gk/worktree-gate/<run-id>.json so retries and audits can
// reconstruct what was reviewed. Shared across linked worktrees because it
// lives in the common dir, not a per-worktree .git.
type gateStateFile struct {
	RunID           string `json:"run_id"`
	Source          string `json:"source"`
	Target          string `json:"target"`
	Phase           string `json:"phase"`
	BaseSHA         string `json:"base_sha"`
	HeadSHA         string `json:"head_sha"`
	TargetBeforeSHA string `json:"target_before_sha"`
	TargetAfterSHA  string `json:"target_after_sha,omitempty"`
	Patch           string `json:"patch"`
	GateCommand     string `json:"gate_command"`
	GateExitCode    int    `json:"gate_exit_code"`
	StartedAt       string `json:"started_at"`
	FinishedAt      string `json:"finished_at"`
}

// parseGateSpec reads the gate flags off the finish command. Returns nil when
// no gate was requested, so the caller runs the unchanged (gate-free) path.
func parseGateSpec(gate string, gateArgs []string, panelReview bool, phase string, timeout time.Duration, keepPatch bool) (*gateSpec, error) {
	var tokens []string
	switch {
	case panelReview:
		if gate != "" || len(gateArgs) > 0 {
			return nil, fmt.Errorf("worktree finish: --panel-review cannot be combined with --gate/--gate-arg")
		}
		// --panel-review is an alias for the canonical panel template.
		tokens = []string{"xm", "panel", "{patch}", "--json"}
	case len(gateArgs) > 0:
		if gate != "" {
			return nil, fmt.Errorf("worktree finish: --gate and --gate-arg are mutually exclusive (use one or the other)")
		}
		// Each --gate-arg is one argv token verbatim; no whitespace split, so
		// the caller controls argv boundaries exactly.
		tokens = append(tokens, gateArgs...)
	case strings.TrimSpace(gate) != "":
		// --gate is the shorthand: whitespace-tokenize into argv. Each token
		// may carry {var} placeholders substituted 1:1 into a single element.
		tokens = strings.Fields(gate)
	default:
		return nil, nil
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("worktree finish: gate command is empty")
	}

	phase = strings.TrimSpace(phase)
	if phase == "" {
		phase = gatePhaseBefore
	}
	switch phase {
	case gatePhaseBefore, gatePhaseAfter, gatePhaseBoth:
	default:
		return nil, fmt.Errorf("worktree finish: invalid --gate-phase %q (want before|after|both)", phase)
	}
	return &gateSpec{tokens: tokens, phase: phase, timeout: timeout, keepPatch: keepPatch}, nil
}

func (g *gateSpec) runsBefore() bool { return g.phase == gatePhaseBefore || g.phase == gatePhaseBoth }
func (g *gateSpec) runsAfter() bool  { return g.phase == gatePhaseAfter || g.phase == gatePhaseBoth }

// commandTemplate renders the pre-substitution tokens back into a display
// string for the audit record and human output.
func (g *gateSpec) commandTemplate() string { return strings.Join(g.tokens, " ") }

// substituteGateArgv replaces {token} placeholders in each argv element with
// the concrete value from vars. Substitution is per-element: the whole element
// stays one argv slot regardless of what the value contains, so there is no
// word-splitting and no shell-injection path. Unknown {tokens} are left as-is.
func substituteGateArgv(tokens []string, vars map[string]string) []string {
	repl := make([]string, 0, len(vars)*2)
	for k, v := range vars {
		repl = append(repl, "{"+k+"}", v)
	}
	r := strings.NewReplacer(repl...)
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = r.Replace(t)
	}
	return out
}

// sanitizeBranchForPath makes a branch name safe as a single filename
// component while staying injective (feat/x and feat-x never collide), so a
// lock file / run-id maps back to exactly one branch.
func sanitizeBranchForPath(b string) string {
	return strings.NewReplacer("%", "%25", "/", "%2F", "\\", "%5C", ":", "%3A").Replace(b)
}

// acquireTargetLock takes the per-target serialization lock keyed by
// (common-dir + branch). Returns a release func (always safe to call, even on
// the degraded no-common-dir path). A live holder yields a blocked error; a
// stale holder (dead pid) is reclaimed. The lock file lives beside the gate
// state under the shared common dir so it is visible from every linked
// worktree.
func acquireTargetLock(commonDir, branch string) (func(), error) {
	if commonDir == "" {
		// No shared common dir (non-repo / detached plumbing). Degrade: run
		// the gate without cross-worktree serialization rather than fail.
		return func() {}, nil
	}
	dir := filepath.Join(commonDir, "gk", "locks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("worktree finish: create lock dir: %w", err)
	}
	lockPath := filepath.Join(dir, sanitizeBranchForPath(branch)+".lock")
	for range 2 {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "pid %d\n", os.Getpid())
			_ = f.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("worktree finish: acquire target lock: %w", err)
		}
		data, _ := os.ReadFile(lockPath)
		if pid := pidFromLockReason(string(data)); pid > 0 && pidAlive(pid) {
			return nil, WithBlocked(
				fmt.Errorf("target %q is locked by an active worktree finish (pid %d)", branch, pid),
				"worktree_gate_locked",
				"another finish is integrating this target; wait for it to release the lock",
				errRemedy{Command: selfCmd("worktree list --json"), Safety: "safe"},
			)
		}
		// Stale holder (no pid or dead pid): reclaim and retry once.
		_ = os.Remove(lockPath)
	}
	// The retry lost a race (another process recreated the lock between our
	// Remove and OpenFile). Report it as blocked-with-remedy, not a bare error,
	// so an agent treats it like any other contended target rather than a hard
	// failure it cannot act on.
	return nil, WithBlocked(
		fmt.Errorf("target %q lock is contended (racing acquisition)", branch),
		"worktree_gate_locked",
		"another process is racing to acquire the target lock; retry in a moment",
		errRemedy{Command: selfCmd("worktree list --json"), Safety: "safe"},
	)
}

// pinTargetSHA reads the target branch tip under the lock. refs/heads/<target>
// always names the target tip regardless of which worktree (if any) has it
// checked out — so this never reads the feature worktree's own HEAD (that is
// the SOURCE tip and would corrupt the patch baseline).
func pinTargetSHA(ctx context.Context, runner git.Runner, target string) (string, error) {
	return gitsafe.ResolveRef(ctx, runner, "refs/heads/"+target)
}

// writeGatePatch runs `git diff --binary <revRange>` and writes it to a temp
// file, returning the path. The caller removes it unless --gate-keep-patch.
func writeGatePatch(ctx context.Context, runner git.Runner, revRange string) (string, error) {
	out, stderr, err := runner.Run(ctx, "diff", "--binary", revRange)
	if err != nil {
		return "", fmt.Errorf("worktree finish: build gate patch (%s): %s: %w", revRange, strings.TrimSpace(string(stderr)), err)
	}
	f, err := os.CreateTemp("", "gk-gate-*.patch")
	if err != nil {
		return "", fmt.Errorf("worktree finish: create gate patch file: %w", err)
	}
	if _, werr := f.Write(out); werr != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("worktree finish: write gate patch: %w", werr)
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("worktree finish: close gate patch: %w", cerr)
	}
	return f.Name(), nil
}

// gateRun holds one gate invocation's outcome.
type gateRun struct {
	exitCode int
	timedOut bool
	started  time.Time
	finished time.Time
}

// runGate executes the substituted argv with the feature worktree as the
// working directory and no shell. In JSON/agent mode the gate's stdout is
// redirected to stderr so gk's stdout carries only the envelope. A start
// failure (command not found) is returned as err; a non-zero exit is reported
// via gateRun.exitCode, not err.
func runGate(ctx context.Context, spec *gateSpec, argv []string, workdir string, jsonMode bool) (gateRun, error) {
	runCtx := ctx
	if spec.timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, spec.timeout)
		defer cancel()
	}
	started := time.Now().UTC()
	c := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	c.Dir = workdir
	c.Stdin = os.Stdin
	c.Stderr = os.Stderr
	if jsonMode {
		c.Stdout = os.Stderr
	} else {
		c.Stdout = os.Stdout
	}
	c.Env = os.Environ()
	err := c.Run()
	finished := time.Now().UTC()
	timedOut := spec.timeout > 0 && runCtx.Err() == context.DeadlineExceeded
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return gateRun{exitCode: ee.ExitCode(), timedOut: timedOut, started: started, finished: finished}, nil
		}
		// Could not start (e.g. not on PATH).
		return gateRun{exitCode: -1, timedOut: timedOut, started: started, finished: finished}, err
	}
	return gateRun{exitCode: 0, timedOut: timedOut, started: started, finished: finished}, nil
}

// writeGateState best-effort records the audit file under the shared common
// dir. A write failure never fails the finish — the state file is diagnosis,
// not control flow. No-op when commonDir is empty (degrade).
func writeGateState(commonDir string, st gateStateFile) {
	if commonDir == "" {
		return
	}
	dir := filepath.Join(commonDir, "gk", "worktree-gate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	// Include the phase in the filename so a `--gate-phase both` run keeps BOTH
	// the before and after audit records instead of the after overwriting the
	// before (same run id, two phases).
	name := st.RunID + ".json"
	if st.Phase != "" {
		name = st.RunID + "-" + st.Phase + ".json"
	}
	_ = os.WriteFile(filepath.Join(dir, name), b, 0o644)
}

// gateRunID builds a sortable, branch-tagged run id.
func gateRunID(now time.Time, branch string) string {
	return now.UTC().Format("20060102-150405") + "-" + sanitizeBranchForPath(branch)
}

// gateRecoveryRemedies builds the resume/abort pair offered when the after
// gate fails. The merge already succeeded, so the finish is paused awaiting an
// accept-or-revert decision:
//   - resume-forward: accept the merge and run cleanup now.
//   - abort: rewind the target to the pinned pre-merge SHA. When the target is
//     checked out in a worktree a bare update-ref would be refused, so reset
//     --hard in that worktree is offered instead.
func gateRecoveryRemedies(ctx context.Context, runner git.Runner, target, beforeSHA, afterSHA string, cleanup, deleteBranch bool) []errRemedy {
	accept := fmt.Sprintf("worktree finish --to %s --resume-accept", target)
	// Preserve the cleanup intent from the original invocation so accepting
	// completes exactly the finish the operator asked for.
	if cleanup {
		accept += " --cleanup"
	}
	if deleteBranch {
		accept += " --delete-branch"
	}
	remedies := []errRemedy{{
		Command: selfCmd(accept),
		Safety:  "safe",
	}}
	if entry, err := findWorktreeForBranch(ctx, runner, target); err == nil && entry != nil {
		// %q shell-quotes the path so a worktree path with spaces survives
		// copy-paste. reset --hard is unconditional, so if the target advanced
		// while paused it also discards those commits — hence the destructive tag
		// (an agent must check safety before running it).
		remedies = append(remedies, errRemedy{
			Command: fmt.Sprintf("git -C %q reset --hard %s", entry.Path, beforeSHA),
			Safety:  "destructive",
		})
	} else {
		remedies = append(remedies, errRemedy{
			Command: fmt.Sprintf("git update-ref refs/heads/%s %s %s", target, beforeSHA, afterSHA),
			Safety:  "destructive",
		})
	}
	return remedies
}

// gateExitError formats a gate failure message including the exit code / timeout.
func gateFailureMessage(phase string, run gateRun) string {
	if run.timedOut {
		return fmt.Sprintf("gate timed out %s merge", phaseWord(phase))
	}
	if run.exitCode < 0 {
		return fmt.Sprintf("gate command could not be started %s merge", phaseWord(phase))
	}
	return fmt.Sprintf("gate failed %s merge (exit %d)", phaseWord(phase), run.exitCode)
}

func phaseWord(phase string) string {
	if phase == gatePhaseAfter {
		return "after"
	}
	return "before"
}

// mergeBaseSHA resolves merge-base(source, target); best-effort ("" on error)
// since it only feeds the {base_sha} template var and the audit record.
func mergeBaseSHA(ctx context.Context, runner git.Runner, target string) string {
	out, _, err := runner.Run(ctx, "merge-base", "refs/heads/"+target, "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// resolveGateTarget turns the finish target into a concrete branch the lock,
// SHA pin, and patch can name. A branch or "base" is already concrete
// (finishChildArgs resolved "base"); "parent" is resolved here the same way
// promote picks its one hop — the branch's gk-parent, else the base.
func resolveGateTarget(ctx context.Context, runner git.Runner, cfg *config.Config, effectiveTo, branch string) (string, error) {
	if effectiveTo != "" && effectiveTo != "parent" {
		return effectiveTo, nil
	}
	base := finishBaseBranch(ctx, runner, cfg)
	target := cleanupMergeTarget(ctx, runner, base, branch)
	if target == "" || target == branch {
		return "", WithBlocked(
			fmt.Errorf("worktree finish: cannot resolve the gate target for %q", branch),
			"worktree_gate_no_target",
			"record a parent (gk branch set-parent) or pass --to <branch>",
		)
	}
	return target, nil
}

// executeGatedFinish runs the full gated finish under the target lock: pin the
// pre-merge SHA, run the before gate, merge, run the after gate. It returns the
// embeddable gate result plus an error. A non-nil error means the finish
// stopped without a completed merge (a live lock, a dirty tree, or a
// before-gate rejection — all shaped as WithBlocked so the envelope reports
// state:"blocked"). A nil error with gateRes.Paused==true means the merge
// stands but the after gate failed and the caller must hold cleanup.
func executeGatedFinish(ctx context.Context, runner *git.ExecRunner, spec *gateSpec, gkPath, path, branch, target, mode string, jsonMode, cleanup, deleteBranch bool, childArgs []string) (*worktreeGateResultJSON, error) {
	commonDir := gitCommonDir(ctx, runner)
	release, err := acquireTargetLock(commonDir, target)
	if err != nil {
		return nil, err
	}
	defer release()

	// The gate must review exactly what merges. promote/land commits a dirty
	// tree AFTER the before gate would have run, so gating a dirty worktree
	// would approve less than what lands — block and direct to commit first.
	if dirty := worktreeDirtyAt(ctx, path); dirty != nil {
		return nil, WithBlocked(
			fmt.Errorf("worktree finish: uncommitted changes in %s", path),
			"worktree_gate_dirty",
			"commit or stash before gating so the gate reviews exactly what merges",
			errRemedy{Command: selfCmd("commit"), Safety: "safe"},
		)
	}

	beforeSHA, err := pinTargetSHA(ctx, runner, target)
	if err != nil {
		return nil, fmt.Errorf("worktree finish: pin target %q: %w", target, err)
	}
	baseSHA := mergeBaseSHA(ctx, runner, target)
	head, _ := headSHA(ctx, runner)

	gateRes := &worktreeGateResultJSON{Phase: spec.phase, RunID: gateRunID(time.Now(), branch)}
	buildVars := func(phase, afterSHA, patch string) map[string]string {
		return map[string]string{
			"patch": patch, "source": branch, "target": target,
			"base_sha": baseSHA, "head_sha": head,
			"target_before_sha": beforeSHA, "target_after_sha": afterSHA,
			"phase": phase,
		}
	}
	recordState := func(phase, patch, afterSHA string, run gateRun) {
		writeGateState(commonDir, gateStateFile{
			RunID: gateRes.RunID, Source: branch, Target: target, Phase: phase,
			BaseSHA: baseSHA, HeadSHA: head, TargetBeforeSHA: beforeSHA, TargetAfterSHA: afterSHA,
			Patch: patch, GateCommand: spec.commandTemplate(), GateExitCode: run.exitCode,
			StartedAt: run.started.Format(time.RFC3339), FinishedAt: run.finished.Format(time.RFC3339),
		})
	}

	// ---- before gate ----
	if spec.runsBefore() {
		patch, perr := writeGatePatch(ctx, runner, beforeSHA+"...HEAD")
		if perr != nil {
			return gateRes, perr
		}
		argv := substituteGateArgv(spec.tokens, buildVars(gatePhaseBefore, "", patch))
		run, rerr := runGate(ctx, spec, argv, path, jsonMode)
		recordState(gatePhaseBefore, patch, "", run)
		if rerr != nil || run.exitCode != 0 || run.timedOut {
			gateRes.Before = "failed"
			// Retain the patch (regardless of --gate-keep-patch) so the blocked
			// remedy — re-run the gate on it — is executable.
			return gateRes, WithBlocked(
				fmt.Errorf("worktree finish: %s", gateFailureMessage(gatePhaseBefore, run)),
				"worktree_gate_before_failed",
				"the gate rejected the patch; nothing merged (target unchanged)",
				errRemedy{Command: strings.Join(argv, " "), Safety: "safe"},
			)
		}
		gateRes.Before = "passed"
		if spec.keepPatch {
			gateRes.Patch = patch
		} else {
			_ = os.Remove(patch)
		}
	} else {
		gateRes.Before = "skipped"
	}

	// ---- merge (existing promote/land self-exec) ----
	if err := landRunChild(ctx, gkPath, path, jsonMode, childArgs...); err != nil {
		return gateRes, fmt.Errorf("worktree finish: %s failed: %w", mode, err)
	}
	gateRes.Merged = true

	// ---- after gate ----
	if spec.runsAfter() {
		afterSHA, aerr := pinTargetSHA(ctx, runner, target)
		if aerr != nil {
			return gateRes, fmt.Errorf("worktree finish: pin target after merge: %w", aerr)
		}
		patch, perr := writeGatePatch(ctx, runner, beforeSHA+".."+afterSHA)
		if perr != nil {
			return gateRes, perr
		}
		argv := substituteGateArgv(spec.tokens, buildVars(gatePhaseAfter, afterSHA, patch))
		run, rerr := runGate(ctx, spec, argv, path, jsonMode)
		recordState(gatePhaseAfter, patch, afterSHA, run)
		if rerr != nil || run.exitCode != 0 || run.timedOut {
			gateRes.After = "failed"
			gateRes.Paused = true
			gateRes.Patch = patch // keep as evidence for the review
			gateRes.Recover = gateRecoveryRemedies(ctx, runner, target, beforeSHA, afterSHA, cleanup, deleteBranch)
			return gateRes, nil
		}
		gateRes.After = "passed"
		if spec.keepPatch {
			gateRes.Patch = patch
		} else {
			_ = os.Remove(patch)
		}
	} else {
		gateRes.After = "skipped"
	}
	return gateRes, nil
}

// finishCleanup runs the worktree removal (and optional branch delete),
// mirroring the pre-gate inline logic: a removal that succeeds but whose
// branch delete fails is reported as such, not swallowed.
func finishCleanup(ctx context.Context, runner *git.ExecRunner, res *worktreeFinishJSON, path, branch string, deleteBranch bool) error {
	removed, branchDeleted, cerr := cleanupFinishedWorktree(ctx, runner, path, branch, deleteBranch)
	res.Removed = removed
	res.BranchDeleted = branchDeleted
	if cerr != nil {
		if removed {
			return fmt.Errorf("%w (worktree already removed at %s)", cerr, path)
		}
		return cerr
	}
	return nil
}

// finishEmitOK writes the success result — the envelope in JSON/agent mode, the
// human summary otherwise.
func finishEmitOK(cmd *cobra.Command, res worktreeFinishJSON) error {
	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), res)
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "worktree finished: %s -> %s\n", res.Branch, res.To)
	if res.Gate != nil {
		fmt.Fprintf(w, "gate: before=%s after=%s\n", res.Gate.Before, res.Gate.After)
	}
	if res.Removed {
		fmt.Fprintf(w, "removed worktree %s\n", res.Path)
	}
	if res.BranchDeleted {
		fmt.Fprintf(w, "deleted branch %s\n", res.Branch)
	}
	return nil
}

// renderFinishPaused prints the human after-gate paused summary: the merge
// stands, cleanup is held, and the resume/abort commands are listed.
func renderFinishPaused(cmd *cobra.Command, res worktreeFinishJSON) {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "worktree finish PAUSED: %s -> %s merged, but the after gate failed.\n", res.Branch, res.To)
	fmt.Fprintln(w, "the merge stands; cleanup was held. resolve with one of:")
	if res.Gate != nil {
		for _, r := range res.Gate.Recover {
			fmt.Fprintf(w, "  [%s] %s\n", r.Safety, r.Command)
		}
		if res.Gate.Patch != "" {
			fmt.Fprintf(w, "integration patch kept at %s\n", res.Gate.Patch)
		}
	}
}
