package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/diff"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/secrets"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	cmd := &cobra.Command{
		Use:   "commit",
		Short: "Generate commit messages with AI and apply them",
		Long: `Analyzes current working-tree changes (staged + unstaged + untracked),
groups them into semantic commit plans via an AI CLI provider, and applies
the resulting Conventional Commit messages.

Provider resolution order:
  1. --provider flag
  2. ai.provider in .gk.yaml / config.yaml
  3. Auto-detect (gemini → qwen → kiro-cli)

Default behaviour is an interactive TUI review; pass --force/-f to skip
review.

  --dry-run shows the plan and per-group estimated token cost without
  making any LLM call. Useful before firing a large run, and works even
  when the daily token quota is exhausted.

Use --abort to restore HEAD to the backup ref created at the start of
the most recent run.

  --plan applies a deterministic, AI-free commit plan instead of
  classifying with a provider. The plan is JSON that groups working-tree
  files into commits with pre-written Conventional Commit messages:

    1. gk commit --plan-template            # current changes as a draft
    2. edit the message(s) / split into commits   # the judgment step
    3. gk commit --plan - < plan.json       # validate + apply

  gk validates the plan against the real working tree and commitlint
  rules (every named file must have a change, no file in two commits,
  every message must lint), runs the same commit-time guards as the AI
  path (gofmt advisory, secret scan), then applies behind a backup ref.
  Uncovered dirty files are left in the tree on purpose. --dry-run
  validates without committing; with --json (or GK_AGENT=1) the result
  is a machine contract: {result, commits:[{message,result,sha}],
  failed_at?, backup_ref?}.
`,
		RunE: runAICommit,
	}
	cmd.Flags().BoolP("force", "f", false, "apply commits without interactive review")
	cmd.Flags().Bool("dry-run", false, "preview groups + estimated token cost; no LLM calls")
	cmd.Flags().String("provider", "", "override ai.provider (gemini|qwen|kiro)")
	cmd.Flags().String("model", "", "override the model for this run (HTTP providers only)")
	cmd.Flags().String("lang", "", "override ai.lang (en|ko|...)")
	cmd.Flags().Bool("staged-only", false, "only consider already-staged changes")
	cmd.Flags().Bool("include-unstaged", false, "include unstaged + untracked changes (default true)")
	cmd.Flags().Bool("include-noise", false, "include build output / dependency / cache files normally excluded (node_modules, __pycache__, *.db, …); skips the .gitignore guard")
	cmd.Flags().StringSliceP("allow-secret-kind", "S", nil, "suppress secret findings of the given kind (repeatable); the special value 'all' bypasses every finding")
	cmd.Flags().BoolP("no-verify", "n", false, "bypass the noise + secret guards and the privacy-gate abort threshold (bypassed secrets are reported, then committed; payload redaction to remote AI still applies)")
	cmd.Flags().Bool("abort", false, "restore HEAD to the latest ai-commit backup ref and exit")
	cmd.Flags().String("plan", "", "JSON commit plan: a file path, or '-' for stdin (deterministic, no AI)")
	cmd.Flags().Bool("plan-template", false, "emit current working-tree changes as a commit-plan draft (JSON) and exit")
	cmd.Flags().BoolP("interactive", "i", false, "interactively group working-tree files into commits (TUI; builds a commit plan, no AI)")
	cmd.Flags().Bool("ci", false, "CI mode — require --force or --dry-run, never prompt")
	cmd.Flags().BoolP("yes", "y", false, "accept every prompt (alias for --force when non-TTY)")
	cmd.Flags().Bool("no-wip-unwrap", false, "skip detection/unwrap of WIP-like commits in HEAD chain")
	cmd.Flags().Bool("force-wip", false, "unwrap WIP chain even when some commits are already pushed (rewrites pushed history; requires force-push afterward)")

	rootCmd.AddCommand(cmd)
}

func runAICommit(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return fmt.Errorf("commit: load config: %w", err)
	}

	flags, err := readAICommitFlags(cmd)
	if err != nil {
		return err
	}
	// --no-verify is the "bypass every commit guard" switch, so fold in the
	// privacy gate's abort threshold too — a payload with more than
	// max_secrets findings must not re-block a commit the user already chose
	// to force through. Redaction is untouched: applyPrivacyGate still scrubs
	// the payload, so the remote LLM never sees a raw secret. This is exactly
	// what --skip-privacy does; we set it implicitly so `gk commit -n` is a
	// single, complete escape hatch instead of needing `-n --skip-privacy`.
	if flags.noVerify {
		_ = cmd.Flags().Set("skip-privacy", "true")
	}
	ai := applyAICommitFlagsToConfig(cfg.AI, flags)

	runner := &git.ExecRunner{Dir: RepoFlag()}

	if flags.abort {
		return runAICommitAbort(ctx, cmd, runner)
	}

	// Declarative (AI-free) paths branch BEFORE any provider is constructed —
	// a plan needs no LLM, so spinning one up (and failing when none is
	// configured) would be wrong. --plan-template wins over --plan when both
	// are set, matching batch/rebase (template is a pure read-and-exit).
	if flags.planTemplate {
		return runAICommitPlanTemplate(cmd, ctx, runner, ai)
	}
	if flags.plan != "" {
		return runAICommitPlan(cmd, ctx, runner, cfg, ai, flags)
	}
	// --interactive is also AI-free: the human groups files and writes the
	// messages in a TUI, which builds the same commit plan the --plan path
	// applies. Branch before any provider is constructed.
	if flags.interactive {
		return runAICommitInteractive(cmd, ctx, runner, cfg, ai, flags)
	}

	// Resolve provider first so Preflight can query Locality / Available.
	// Fallback Chain when no explicit provider; single provider otherwise.
	var prov provider.Provider
	if ai.Provider == "" {
		fc, fcErr := buildFallbackChain(nil, provider.ExecRunner{})
		if fcErr != nil {
			return fmt.Errorf("commit: %w", fcErr)
		}
		prov = fc
	} else {
		opts := aiFactoryOptionsFromAI(ai)
		// ai.commit.model, when set, overrides ai.<provider>.model for
		// commit only — a small/fast model handles message generation well
		// while chat/advice commands keep the larger default. --model
		// (folded onto Commit.Model by applyAICommitFlagsToConfig) wins.
		if ai.Commit.Model != "" {
			opts.Model = ai.Commit.Model
		}
		// ai.commit.timeout, when set, is the per-call HTTP timeout for the
		// commit provider (overrides the per-provider default). Previously
		// this config field was never read.
		if d := parseDurationOrDefault(ai.Commit.Timeout, 0); d > 0 {
			opts.Timeout = d
		}
		p, pErr := provider.NewProvider(ctx, opts)
		if pErr != nil {
			return fmt.Errorf("commit: provider: %w", pErr)
		}
		prov = p
	}
	Dbg("commit: provider=%s model=%s lang=%s scope=%s", prov.Name(), providerModel(prov), ai.Lang, ai.Commit.DenyPaths)

	if err := aicommit.Preflight(ctx, aicommit.PreflightInput{
		Runner:      runner,
		WorkDir:     RepoFlag(),
		AI:          ai,
		Provider:    prov,
		AllowRemote: ai.Commit.AllowRemote,
	}); err != nil {
		return fmt.Errorf("commit: preflight: %w", err)
	}
	Dbg("commit: preflight ok")

	wipDisabled := flags.noWIPUnwrap || !ai.Commit.WIPEnabled
	wipCommit, err := inspectWIPCommitForAICommit(ctx, runner, ai.Commit, wipDisabled, flags.forceWIP)
	if err != nil {
		return err
	}
	// `cfg.Branch.Protected` was previously consumed here as a branch-name
	// veto on WIP unwrap, which silently disabled the feature on develop /
	// main. The per-commit push gate inside DetectWIPChain is sufficient,
	// so the list is intentionally no longer read in this path.
	_ = cfg.Branch.Protected

	// Gather WIP.
	scope := aicommit.ScopeAll
	switch {
	case flags.stagedOnly:
		scope = aicommit.ScopeStagedOnly
	case flags.includeUnstaged:
		// explicit flag, same as default — noop
	}
	files, err := aicommit.GatherWIP(ctx, runner, aicommit.GatherOptions{
		Scope:     scope,
		DenyPaths: ai.Commit.DenyPaths,
	})
	if err != nil {
		return err
	}
	files = appendWIPCommitFiles(files, wipCommit.Files)
	if len(files) == 0 {
		// A detected chain with zero net files means the WIP commits
		// cancel each other out (a later WIP reverted an earlier one).
		// There is nothing to classify, but the chain itself is the
		// noise the user asked gk commit to fold — unwrap it behind
		// the usual backup ref and finish OK.
		if wipCommit.Present {
			return unwrapNetZeroWIPChain(ctx, cmd, runner, wipCommit, flags)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "commit: no working-tree changes to commit")
		if hint := wipChainSkipHint(wipDisabled, wipCommit); hint != "" {
			fmt.Fprintln(cmd.OutOrStdout(), stylizeHintLine(hint))
		}
		return nil
	}
	if wipCommit.Present && wipCommit.ForcePushBypass {
		fmt.Fprintln(cmd.ErrOrStderr(),
			"⚠ commit: --force-wip set — chain may include already-pushed commits; rerun `git push --force-with-lease` after the rewrite")
	}
	// Drop well-known non-source artifacts (node_modules, __pycache__, *.db,
	// …) before they reach the classifier — a missing .gitignore otherwise
	// floods the scope and the AI response gets truncated. Offers to add
	// them to .gitignore on a TTY. --include-noise opts out (commit them as-is).
	if !flags.includeNoise && !flags.noVerify {
		files = guardNoiseFiles(ctx, cmd, runner, files)
		if len(files) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "commit: nothing to commit after excluding non-source files (use --include-noise to keep them)")
			return nil
		}
	}
	// gofmt advisory gate — same guard chain as the noise filter. Warns
	// (never blocks) when a .go file in scope is unformatted, so the
	// violation is caught here instead of in the release preflight. Skips
	// silently in non-Go repos / when gofmt is absent, and is bypassed by
	// --no-verify alongside the noise + secret guards.
	if !flags.noVerify {
		guardGofmt(ctx, cmd.ErrOrStderr(), RepoFlag(), files)
	}
	Dbg("commit: gather ok — %d file(s) in scope=%v", len(files), scope)

	// Secret gate. gitleaks + internal/secrets can take a noticeable
	// beat on large diffs; show a spinner so the user knows gk is
	// actively guarding the payload rather than hung.
	stopGate := ui.StartBubbleSpinner("scanning payload for secrets...")
	payload := summariseForSecretScan(files)
	findings, err := aicommit.ScanPayload(ctx, payload, aicommit.SecretGateOptions{
		AllowKinds:  flags.allowSecretKinds,
		RunGitleaks: true,
	}, nil)
	stopGate()
	if err != nil {
		return err
	}
	if len(findings) > 0 {
		// Two bypass paths converge here, both deliberately loud: --no-verify
		// (turn off the commit guards wholesale) and --allow-secret-kind all
		// (the secret-specific escape hatch). Either way we still PRINT every
		// finding and shout that it's entering history — bypassing a real
		// credential is a rotate-now event, not a silent one. Naming a kind
		// explicitly (`--allow-secret-kind github-token`) is the quiet path:
		// it's filtered out upstream in ScanPayload and never reaches here.
		if secretBypass(flags.noVerify, flags.allowSecretKinds) {
			via := "--allow-secret-kind all"
			if flags.noVerify {
				via = "--no-verify"
			}
			renderFindings(cmd.ErrOrStderr(), findings)
			fmt.Fprintf(cmd.ErrOrStderr(),
				"⚠️  commit: %d secret finding(s) BYPASSED via %s — these WILL be written into git history. Rotate any real credential.\n",
				len(findings), via)
		} else {
			renderFindings(cmd.ErrOrStderr(), findings)
			return fmt.Errorf("commit: aborted due to %d secret finding(s); fix, allow a kind with --allow-secret-kind <kind>, or bypass everything with --allow-secret-kind all / --no-verify",
				len(findings))
		}
	} else {
		Dbg("commit: secret-gate clean")
	}

	// Privacy Gate: redact payload for remote providers.
	redactedPayload, pgFindings, pgErr := applyPrivacyGate(cmd, prov, payload, ai)
	if pgErr != nil {
		renderPrivacyFindings(cmd.ErrOrStderr(), pgFindings)
		return fmt.Errorf("commit: privacy gate: %w", pgErr)
	}

	// --show-prompt: display redacted payload.
	showPromptIfRequested(cmd, redactedPayload)

	// --dry-run is a cost preview: heuristic classify + token estimate,
	// no LLM calls. Useful before firing a large commit run, and works
	// even when the daily TPD quota is exhausted (the original 429
	// scenario). Exits before Classify so no remote call is issued.
	if flags.dryRun {
		return runCommitDryRunPreview(cmd, runner, ctx, prov, files, wipCommit, *cfg, ai)
	}

	// Classify — the first provider call. The spinner advertises we are waiting
	// on the AI CLI, not stuck, and counts down against the classify timeout so
	// an imminent deadline (the cause of "context deadline exceeded") is visible
	// rather than waiting blind.
	fmt.Fprintf(cmd.ErrOrStderr(), "commit: classifying %d file(s) via %s...\n", len(files), prov.Name())
	classifyBudget := parseDurationOrDefault(ai.Commit.Timeout, 0)
	stopClassify := ui.StartBubbleSpinnerWithBudget(fmt.Sprintf("classify — %s", prov.Name()), classifyBudget)
	classifyStart := time.Now()
	res, err := aicommit.Classify(ctx, prov, files, aicommit.ClassifyOptions{
		AllowedTypes:    cfg.Commit.Types,
		AllowedScopes:   allowedScopesFromFiles(files),
		Lang:            ai.Lang,
		HybridFileLimit: 5,
		ScopeRequired:   cfg.Commit.ScopeRequired,
	})
	stopClassify()
	if err != nil {
		return fmt.Errorf("commit: classify: %w", err)
	}
	groups := res.Groups
	Dbg("commit: classify ok — %d group(s) in %s", len(groups), time.Since(classifyStart).Round(time.Millisecond))
	// Mirror the compose timing line below: surface the classify wall-clock +
	// grouping on the human stream so the two LLM phases each report their cost.
	// The model/token tail is appended when the provider reports them (the
	// heuristic short-circuit shows "heuristic" with no token count).
	classifyLine := fmt.Sprintf("commit: classified %d file(s) into %d group(s) in %s",
		len(files), len(groups), time.Since(classifyStart).Round(time.Millisecond))
	if res.Model != "" {
		classifyLine += " · " + res.Model
	}
	if res.TokensUsed > 0 {
		classifyLine += " · " + formatTokens(res.TokensUsed)
	}
	fmt.Fprintln(cmd.ErrOrStderr(), classifyLine)
	if len(groups) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "commit: nothing to commit after filtering")
		return nil
	}

	// Pair Go test/prod files split across groups so Compose sees the
	// full picture for one commit (e.g. switch.go + switch_test.go →
	// one feat/chore commit instead of two).
	groups = aicommit.PairTestProdGroups(groups)

	// Compose (per group) with commitlint retry.
	diffs, err := collectGroupDiffs(ctx, runner, groups, wipCommit)
	if err != nil {
		return err
	}
	// Privacy gate the ACTUAL compose payload. collectGroupDiffs returns
	// raw `git diff` output; the earlier applyPrivacyGate call only redacted
	// the secret-scan summary used for --show-prompt, so without this the
	// real diff reached a remote provider un-redacted. Apply per group; the
	// gate is a no-op for local providers (Locality check inside).
	for k, d := range diffs {
		red, pgFindings, pgErr := applyPrivacyGate(cmd, prov, d, ai)
		if pgErr != nil {
			renderPrivacyFindings(cmd.ErrOrStderr(), pgFindings)
			return fmt.Errorf("commit: privacy gate (diff): %w", pgErr)
		}
		diffs[k] = red
	}
	heuristicN := aicommit.CountHeuristicGroups(groups, ai.Lang)
	llmN := len(groups) - heuristicN
	// WarmCache is opt-in (ai.commit.warm_cache, default false) AND only
	// meaningful for a prompt-caching provider. Measurement showed the
	// warm-up serialises one round-trip (~1.8× slower on a cache-less
	// provider) with no proven offsetting saving, so it stays off unless
	// the operator measured a net win on Anthropic and enabled it.
	warmCache := ai.Commit.WarmCache && providerCachesPrompt(prov)
	// Dispatch label makes the concurrency setting observable: it names
	// whether the LLM groups run single-shot, parallel ×N (with the
	// effective worker count, so ai.commit.concurrency is visible), or
	// not at all (all heuristic). Without this the parallelism — and
	// whether the config knob took effect — was invisible.
	dispatch := composeDispatchLabel(llmN, ai.Commit.Concurrency, warmCache)
	if heuristicN > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"commit: composing %d message(s) (%d via heuristic, %d via %s; %s)...\n",
			len(groups), heuristicN, llmN, prov.Name(), dispatch)
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"commit: composing %d message(s) via %s (%s)...\n",
			len(groups), prov.Name(), dispatch)
	}
	stopCompose := ui.StartBubbleSpinner(fmt.Sprintf("compose — %d group(s) via %s", len(groups), prov.Name()))
	composeStart := time.Now()
	messages, err := aicommit.ComposeAll(ctx, prov, groups, diffs, aicommit.ComposeOptions{
		MaxAttempts:      3,
		AllowedTypes:     cfg.Commit.Types,
		ScopeRequired:    cfg.Commit.ScopeRequired,
		MaxSubjectLength: cfg.Commit.MaxSubjectLength,
		Lang:             ai.Lang,
		Concurrency:      ai.Commit.Concurrency,
		WarmCache:        warmCache,
	})
	stopCompose()
	if err != nil {
		return fmt.Errorf("commit: compose: %w", err)
	}
	// Wall-clock + dispatch on the human stream (not just -d): with a
	// parallel dispatch this is where the speedup shows; with single-shot
	// it surfaces a slow model round-trip that parallelism can't fix. Skip
	// only the all-heuristic case, which is instant and makes no LLM call.
	if llmN >= 1 {
		fmt.Fprintf(cmd.ErrOrStderr(), "commit: composed %d message(s) in %s (%s)\n",
			len(messages), time.Since(composeStart).Round(time.Millisecond), dispatch)
	}
	Dbg("commit: compose ok — %d message(s) in %s", len(messages), time.Since(composeStart).Round(time.Millisecond))

	// Review.
	reviewOpts := aicommit.ReviewOptions{
		Out:            cmd.OutOrStdout(),
		Force:          flags.force || flags.yes,
		NonInteractive: flags.ci && !ui.IsTerminal(),
		FileStats:      commitDisplayStats(ctx, runner, files, wipCommit),
	}
	decisions, err := aicommit.ReviewPlan(messages, reviewOpts)
	if err != nil {
		if errors.Is(err, aicommit.ErrReviewAborted) {
			fmt.Fprintln(cmd.ErrOrStderr(), "commit: review aborted")
			return nil
		}
		return fmt.Errorf("commit: review: %w", err)
	}
	kept := filterKept(messages, decisions)
	if len(kept) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "commit: no groups were kept after review")
		return nil
	}

	// Snapshot the backup ref BEFORE the unwrap rewrites HEAD —
	// otherwise --abort would only roll back to the chain ancestor,
	// not the original WIP-tipped state.
	var preUnwrapBackup string
	if wipCommit.Present && !flags.dryRun {
		b, bErr := aicommit.EnsureBackupRef(ctx, runner)
		if bErr != nil {
			return fmt.Errorf("commit: backup ref: %w", bErr)
		}
		preUnwrapBackup = b
	}

	if err := unwrapWIPCommitBeforeApply(ctx, runner, wipCommit, flags, cmd.OutOrStdout()); err != nil {
		return err
	}

	// Apply.
	applyOpts := aicommit.ApplyOptions{DryRun: flags.dryRun, PrecapturedBackupRef: preUnwrapBackup}
	if ai.Commit.Trailer {
		applyOpts.Trailer = fmt.Sprintf("%s@%s", prov.Name(), readProviderVersion(ctx, prov))
	}
	result, err := aicommit.ApplyMessages(ctx, runner, kept, applyOpts)
	if err != nil {
		printBackupHint(cmd.ErrOrStderr(), result.BackupRef)
		return fmt.Errorf("commit: apply: %w", err)
	}

	// Audit (opt-in).
	if ai.Commit.Audit && !flags.dryRun {
		_ = writeAuditEntries(ctx, runner, prov, kept, result)
	}

	printApplySummary(cmd.OutOrStdout(), kept, result, flags.dryRun)
	return nil
}

type wipCommitForAICommit struct {
	Present bool
	// ChainLen is how many recent commits will be unwrapped after
	// Apply. >= 1 when Present, 0 otherwise. Backwards-compatible
	// with the prior single-WIP shape (ChainLen=1).
	ChainLen int
	// HeadSHA captures HEAD at detection time. Verified again before
	// `git reset HEAD~N` — if HEAD moved during the (potentially long)
	// classify/compose/review phases, the unwrap is refused so we
	// don't reset against a stale offset.
	HeadSHA string
	Files   []aicommit.FileChange
	// StopReason explains why the chain walk ended. Set on every call,
	// regardless of Present, so the CLI can render an actionable hint
	// when the chain came back empty.
	StopReason aicommit.StopReason
	// ForcePushBypass is true when the user passed --force-wip and the
	// chain may include commits already on a remote — drives a one-line
	// stderr warning before reset.
	ForcePushBypass bool
}

// runCommitDryRunPreview produces a cost-aware plan without making any
// LLM call. Classify uses the path heuristic (HeuristicOnly) so the
// preview is free even when the provider's daily quota is exhausted —
// exactly the scenario that motivated this command shape.
//
// Output: per-group file count, classification rationale, and an
// estimated Compose token cost. Heuristic-bypassed groups
// (lockfile-only build, CI-only) report 0 tokens. The estimate is
// approximate (4 chars per token) — accurate enough to flag a
// 50K-token blowup before it happens.
func runCommitDryRunPreview(
	cmd *cobra.Command,
	runner git.Runner,
	ctx context.Context,
	prov provider.Provider,
	files []aicommit.FileChange,
	wipCommit wipCommitForAICommit,
	cfg config.Config,
	ai config.AIConfig,
) error {
	out := cmd.OutOrStdout()

	res, err := aicommit.Classify(ctx, prov, files, aicommit.ClassifyOptions{
		HeuristicOnly:   true,
		AllowedTypes:    cfg.Commit.Types,
		AllowedScopes:   allowedScopesFromFiles(files),
		Lang:            ai.Lang,
		HybridFileLimit: 5,
		ScopeRequired:   cfg.Commit.ScopeRequired,
	})
	if err != nil {
		return fmt.Errorf("commit: dry-run classify: %w", err)
	}
	groups := res.Groups
	if len(groups) == 0 {
		fmt.Fprintln(out, "commit: dry-run — nothing to commit after filtering")
		return nil
	}

	// Mirror the merge pass that the real run will perform — keeps
	// preview aligned with actual output for paired Go test/prod files.
	groups = aicommit.PairTestProdGroups(groups)

	diffs, err := collectGroupDiffs(ctx, runner, groups, wipCommit)
	if err != nil {
		return err
	}

	classifyTok := aicommit.EstimateClassifyTokens(files)
	deniedN := countDenied(files)

	fmt.Fprintln(out, "commit: dry-run — cost preview (no LLM call made)")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Provider:  %s\n", prov.Name())
	fmt.Fprintf(out, "Language:  %s\n", fallbackStr(ai.Lang, "en"))
	fmt.Fprintf(out, "Files:     %d in scope", len(files))
	if deniedN > 0 {
		fmt.Fprintf(out, " (%d denied by deny_paths)", deniedN)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Groups:    %d (heuristic-classified — actual run may regroup via LLM)\n", len(groups))
	fmt.Fprintln(out)

	totalCompose := 0
	for _, g := range groups {
		key := groupKeyLocal(g)
		est := aicommit.EstimateComposeTokens(g, diffs[key], ai.Lang)
		totalCompose += est
		bypass := ""
		if est == 0 {
			bypass = "  [heuristic — no LLM]"
		}
		fmt.Fprintf(out, "  %s — %d file(s) — ~%d tokens%s\n",
			groupLabel(g), len(g.Files), est, bypass)
		for _, f := range g.Files {
			fmt.Fprintf(out, "      %s\n", f)
		}
	}

	total := classifyTok + totalCompose
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Estimated tokens:\n")
	fmt.Fprintf(out, "  classify   ~%d\n", classifyTok)
	fmt.Fprintf(out, "  compose    ~%d  (sum of per-group estimates above)\n", totalCompose)
	fmt.Fprintf(out, "  TOTAL      ~%d\n", total)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Run without --dry-run to execute.")
	return nil
}

func groupLabel(g provider.Group) string {
	if g.Scope != "" {
		return fmt.Sprintf("[%s(%s)]", g.Type, g.Scope)
	}
	return fmt.Sprintf("[%s]", g.Type)
}

func countDenied(files []aicommit.FileChange) int {
	n := 0
	for _, f := range files {
		if f.DeniedBy != "" {
			n++
		}
	}
	return n
}

func fallbackStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// inspectWIPCommitForAICommit walks HEAD backward as long as each
// commit's subject matches a WIP pattern (defaults + user-config
// additions), so a stack of save-point commits can be folded into one
// AI plan. cfg.WIPMaxChain caps the walk. Already-pushed commits stop
// it by default; --force-wip (forceWIP=true) bypasses that gate. When
// --no-wip-unwrap is set the function returns immediately without
// touching git — the caller distinguishes the disabled case via the
// StopReason being empty.
func inspectWIPCommitForAICommit(ctx context.Context, runner git.Runner, cfg config.AICommitConfig, disabled, forceWIP bool) (wipCommitForAICommit, error) {
	if disabled {
		return wipCommitForAICommit{}, nil
	}
	patterns, err := aicommit.CompileWIPPatterns(cfg.WIPPatterns)
	if err != nil {
		return wipCommitForAICommit{}, fmt.Errorf("commit: wip patterns: %w", err)
	}
	chain, reason, err := aicommit.DetectWIPChain(ctx, runner, aicommit.DetectWIPChainOptions{
		MaxChain:    cfg.WIPMaxChain,
		Patterns:    patterns,
		AllowPushed: forceWIP,
	})
	if err != nil {
		return wipCommitForAICommit{}, fmt.Errorf("commit: detect WIP chain: %w", err)
	}
	if len(chain) == 0 {
		return wipCommitForAICommit{StopReason: reason}, nil
	}
	headOut, _, _ := runner.Run(ctx, "rev-parse", "HEAD")
	// The chain's file list is the real HEAD~N→HEAD diff, not a
	// per-commit union: a WIP that reverts an earlier WIP must vanish
	// from the plan, or apply later commits against a clean tree.
	netFiles, err := aicommit.ChainNetFiles(ctx, runner, len(chain))
	if err != nil {
		return wipCommitForAICommit{}, fmt.Errorf("commit: %w", err)
	}
	return wipCommitForAICommit{
		Present:         true,
		ChainLen:        len(chain),
		HeadSHA:         strings.TrimSpace(string(headOut)),
		Files:           netFiles,
		StopReason:      reason,
		ForcePushBypass: forceWIP,
	}, nil
}

// wipChainSkipHint returns a one-line stdout hint when DetectWIPChain
// found nothing actionable. It is printed right after the
// "no working-tree changes to commit" line so users on a protected /
// post-push state stop seeing the feature as silently dead.
//
// Returns "" when nothing useful can be said (chain was simply
// non-WIP, or stop reason was a benign sentinel).
func wipChainSkipHint(disabled bool, wc wipCommitForAICommit) string {
	if disabled {
		return "hint: WIP chain auto-unwrap is disabled (--no-wip-unwrap or ai.commit.wip_enabled=false)"
	}
	if wc.Present {
		// Chain was non-empty and processed (or about to be); no skip
		// hint needed — unwrap path emits its own line later.
		return ""
	}
	switch wc.StopReason {
	case aicommit.StopReasonPushed:
		return "hint: WIP commit(s) at HEAD are already pushed — rerun with --force-wip to unwrap them anyway (rewrites pushed history)"
	case aicommit.StopReasonDetachedHEAD:
		return "hint: WIP chain unwrap is disabled on detached HEAD — check out a branch first"
	case aicommit.StopReasonMergeCommit:
		return "hint: WIP unwrap stopped at a merge commit (multi-parent unwrap is unsafe)"
	default:
		// non-WIP head, shallow history, root commit with non-WIP subject,
		// etc. — silence keeps everyday output clean.
		return ""
	}
}

func appendWIPCommitFiles(files, wipFiles []aicommit.FileChange) []aicommit.FileChange {
	if len(wipFiles) == 0 {
		return files
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.Path] = true
	}
	for _, f := range wipFiles {
		if seen[f.Path] {
			continue
		}
		files = append(files, f)
	}
	return files
}

func unwrapWIPCommitBeforeApply(ctx context.Context, runner git.Runner, wipCommit wipCommitForAICommit, flags aiCommitFlags, out io.Writer) error {
	if !wipCommit.Present || flags.dryRun {
		return nil
	}
	depth := wipCommit.ChainLen
	if depth <= 0 {
		depth = 1 // legacy callers / safety
	}
	// Verify HEAD hasn't moved since detection — classify+compose+
	// review can take many seconds and another shell could land a
	// commit in the meantime, in which case `HEAD~depth` would point
	// somewhere different than what the AI plan was built against.
	if wipCommit.HeadSHA != "" {
		cur, _, err := runner.Run(ctx, "rev-parse", "HEAD")
		if err != nil {
			return fmt.Errorf("commit: unwrap WIP chain: verify HEAD: %w", err)
		}
		curSHA := strings.TrimSpace(string(cur))
		if curSHA != wipCommit.HeadSHA {
			return fmt.Errorf("commit: unwrap WIP chain: HEAD moved during plan (was %s, now %s); refusing reset",
				shortSHA(wipCommit.HeadSHA), shortSHA(curSHA))
		}
	}
	target := fmt.Sprintf("HEAD~%d", depth)
	if _, stderr, err := runner.Run(ctx, "reset", target); err != nil {
		return fmt.Errorf("commit: unwrap WIP chain (%d commit(s)): %s: %w", depth, strings.TrimSpace(string(stderr)), err)
	}
	if out != nil {
		if depth == 1 {
			fmt.Fprintln(out, "commit: unwrapped WIP commit after AI plan; rewriting it as regular commit(s)")
		} else {
			fmt.Fprintf(out, "commit: unwrapped %d WIP commits after AI plan; rewriting as regular commit(s)\n", depth)
		}
	}
	return nil
}

// unwrapNetZeroWIPChain handles a WIP chain whose net diff is empty:
// the chain is "rewritten as regular commits" in the degenerate sense —
// zero of them. The chain is unwrapped behind the standard backup ref
// (restorable with `gk commit --abort`) and the run ends successfully.
// Before this path existed, the per-commit file union planned a commit
// for the cancelled path, the unwrap reset HEAD, and apply died on
// `git commit` finding a clean tree.
func unwrapNetZeroWIPChain(ctx context.Context, cmd *cobra.Command, runner git.Runner, wipCommit wipCommitForAICommit, flags aiCommitFlags) error {
	out := cmd.OutOrStdout()
	if flags.dryRun {
		fmt.Fprintf(out, "commit: WIP chain (%d commit(s)) nets to zero — dry-run; a real run unwraps the chain and commits nothing\n", wipCommit.ChainLen)
		return nil
	}
	backup, err := aicommit.EnsureBackupRef(ctx, runner)
	if err != nil {
		return fmt.Errorf("commit: backup ref: %w", err)
	}
	if err := unwrapWIPCommitBeforeApply(ctx, runner, wipCommit, flags, nil); err != nil {
		return err
	}
	fmt.Fprintf(out, "commit: WIP chain (%d commit(s)) nets to zero — unwrapped; nothing to commit\n", wipCommit.ChainLen)
	printBackupHint(cmd.ErrOrStderr(), backup)
	return nil
}

// runAICommitAbort implements `gk commit --abort`.
func runAICommitAbort(ctx context.Context, cmd *cobra.Command, runner git.Runner) error {
	latest, err := latestAICommitBackupRef(ctx, runner)
	if err != nil {
		return err
	}
	if latest == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "commit: no backup ref found — nothing to abort")
		return nil
	}
	if err := aicommit.AbortRestore(ctx, runner, latest); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), successLinef("commit: restored HEAD", "to %s", latest))
	return nil
}

// aiCommitFlags captures every CLI flag in one struct for easy passing.
type aiCommitFlags struct {
	force            bool
	dryRun           bool
	provider         string
	model            string
	lang             string
	stagedOnly       bool
	includeUnstaged  bool
	includeNoise     bool
	allowSecretKinds []string
	noVerify         bool
	abort            bool
	ci               bool
	yes              bool
	noWIPUnwrap      bool
	forceWIP         bool
	plan             string
	planTemplate     bool
	interactive      bool
}

// secretBypass reports whether secret findings should be waved through
// (reported on stderr, then committed) instead of aborting the commit.
// True for the two explicit, loud escape hatches: --no-verify and the
// special --allow-secret-kind all. Naming a concrete kind is NOT a bypass
// here — those are filtered inside aicommit.ScanPayload and never reach
// this decision, so they stay silent.
func secretBypass(noVerify bool, allowKinds []string) bool {
	return noVerify || slices.Contains(allowKinds, "all")
}

func readAICommitFlags(cmd *cobra.Command) (aiCommitFlags, error) {
	var f aiCommitFlags
	f.force, _ = cmd.Flags().GetBool("force")
	f.dryRun, _ = cmd.Flags().GetBool("dry-run")
	f.provider, _ = cmd.Flags().GetString("provider")
	f.model, _ = cmd.Flags().GetString("model")
	f.lang, _ = cmd.Flags().GetString("lang")
	f.stagedOnly, _ = cmd.Flags().GetBool("staged-only")
	f.includeUnstaged, _ = cmd.Flags().GetBool("include-unstaged")
	f.includeNoise, _ = cmd.Flags().GetBool("include-noise")
	f.allowSecretKinds, _ = cmd.Flags().GetStringSlice("allow-secret-kind")
	f.noVerify, _ = cmd.Flags().GetBool("no-verify")
	f.abort, _ = cmd.Flags().GetBool("abort")
	f.ci, _ = cmd.Flags().GetBool("ci")
	f.yes, _ = cmd.Flags().GetBool("yes")
	f.noWIPUnwrap, _ = cmd.Flags().GetBool("no-wip-unwrap")
	f.forceWIP, _ = cmd.Flags().GetBool("force-wip")
	f.plan, _ = cmd.Flags().GetString("plan")
	f.planTemplate, _ = cmd.Flags().GetBool("plan-template")
	f.interactive, _ = cmd.Flags().GetBool("interactive")
	if f.stagedOnly && f.includeUnstaged {
		return f, fmt.Errorf("--staged-only and --include-unstaged are mutually exclusive")
	}
	return f, nil
}

// applyAICommitFlagsToConfig merges CLI flags onto the loaded config —
// flags win. Returns an updated AIConfig without mutating the input.
func applyAICommitFlagsToConfig(ai config.AIConfig, f aiCommitFlags) config.AIConfig {
	out := ai
	if f.provider != "" {
		out.Provider = f.provider
	}
	if f.model != "" {
		// --model is a one-shot override of ai.commit.model (which itself
		// overrides ai.<provider>.model). Folding it onto Commit.Model here
		// keeps a single resolution point downstream.
		out.Commit.Model = f.model
	}
	// Language resolution for commit, lowest-to-highest precedence:
	//   output.lang → ai.lang (both folded into ai.Lang by config.Load) →
	//   ai.commit.lang (commit-only) → --lang flag (one-shot). Folding the
	//   winner onto out.Lang keeps a single resolution point downstream.
	if out.Commit.Lang != "" {
		out.Lang = out.Commit.Lang
	}
	if f.lang != "" {
		out.Lang = f.lang
	}
	switch {
	case f.force:
		out.Commit.Mode = "force"
	case f.dryRun:
		out.Commit.Mode = "dry-run"
	}
	return out
}

// summariseForSecretScan builds the text blob the secret gate runs on.
// We prefer file content over diff here — diffs include context lines
// and hunk headers that inflate false positives, while whole-file
// snapshots map 1:1 to what users see.
func summariseForSecretScan(files []aicommit.FileChange) string {
	var b strings.Builder
	for _, f := range files {
		if f.DeniedBy != "" || f.IsBinary {
			continue
		}
		// Skip test files — they contain intentional fake secrets for
		// scanner unit tests. These files are still included in the AI
		// classification; only the secret scan skips them.
		if isTestFile(f.Path) {
			continue
		}
		content, err := os.ReadFile(filepath.Clean(f.Path))
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "%s\n", secrets.PayloadFileHeader(f.Path))
		b.Write(content)
		b.WriteString("\n")
	}
	return b.String()
}

// isTestFile returns true for files that are test sources and may
// contain intentional fake secrets (e.g. _test.go, .test.ts).
func isTestFile(path string) bool {
	base := filepath.Base(path)
	lower := strings.ToLower(base)

	// 언어별 테스트 파일 suffix
	if strings.HasSuffix(base, "_test.go") ||
		strings.HasSuffix(base, ".test.ts") ||
		strings.HasSuffix(base, ".test.js") ||
		strings.HasSuffix(base, ".test.tsx") ||
		strings.HasSuffix(base, ".test.jsx") ||
		strings.HasSuffix(base, ".spec.ts") ||
		strings.HasSuffix(base, ".spec.js") ||
		strings.HasSuffix(base, "_test.rs") ||
		strings.HasSuffix(base, "_test.py") ||
		strings.HasSuffix(base, "_spec.rb") {
		return true
	}

	// 테스트/mock/fixture/example 관련 파일명 패턴
	for _, kw := range []string{"test", "mock", "fake", "fixture", "example", "redact", "sample", "stub", "dummy"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}

	// testdata/, tests/, __tests__/ 등 테스트 디렉토리 내 파일
	dir := strings.ToLower(filepath.Dir(path))
	for _, seg := range []string{"testdata", "tests", "__tests__", "test_fixtures", "fixtures"} {
		if strings.Contains(dir, seg) {
			return true
		}
	}

	return false
}

func renderFindings(out interface{ Write(p []byte) (int, error) }, findings []aicommit.SecretFinding) {
	fmt.Fprintln(out, "commit: secret findings detected — aborting:")
	for _, f := range findings {
		loc := fmt.Sprintf("%s:%d", f.File, f.Line)
		if f.File == "" {
			// Header parse failed — at least tell the user the payload-line so
			// they can grep their staged content rather than chase a phantom path.
			loc = fmt.Sprintf("(unknown file, payload line %d)", f.Line)
		}
		fmt.Fprintf(out, "  [%s] %s @ %s — %s\n", f.Source, f.Kind, loc, f.Sample)
	}
}

// allowedScopesFromFiles returns the unique top-level dirs so the
// provider's scope suggestions are bounded to existing modules.
func allowedScopesFromFiles(files []aicommit.FileChange) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		td := topLevelDirSimple(f.Path)
		if td == "." || seen[td] {
			continue
		}
		seen[td] = true
		out = append(out, td)
	}
	return out
}

func topLevelDirSimple(p string) string {
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}

// composeDiffContextLines is the unified-diff context (-U) used for the
// compose payload. git's default is 3; we drop to 1 because the model
// needs only enough surrounding lines to anchor a hunk, not three on each
// side. Measured over 30 real commits this trims the aggregate diff ~15%
// (315KB → 267KB) — a direct token saving on every compose round-trip —
// while keeping one context line as a safer floor than the -U0 that
// `gk diff --digest` forces for symbol extraction (see internal/cli/diff.go).
//
// NOTE: this lower context is applied ONLY to the compose payload, not to
// commitDisplayStats: fewer context lines change git's hunk-header funcname
// pick (measured: 18/30 real commits differ), which would shift the
// preview's symbol column. The stat pass keeps its own default context so
// preview numbers stay exact.
const composeDiffContextLines = 1

// collectGroupDiffs runs `git diff --cached` / `git diff` per group
// file set. Binary files fall back to `--stat`-only output so the
// provider sees a stub instead of junk bytes.
//
// Per-group diffs are capped to aicommit.DefaultComposeDiffByteCap
// before they leave this function — protects against a single huge
// diff (typical: a generated lockfile that escaped the heuristic
// bypass) blowing through the daily token budget.
func collectGroupDiffs(ctx context.Context, runner git.Runner, groups []provider.Group, wipCommit wipCommitForAICommit) (map[string]string, error) {
	uFlag := fmt.Sprintf("-U%d", composeDiffContextLines)
	diffs := map[string]string{}
	for _, g := range groups {
		var b strings.Builder
		if wipCommit.Present {
			depth := wipCommit.ChainLen
			if depth <= 0 {
				depth = 1
			}
			rangeRef := fmt.Sprintf("HEAD~%d..HEAD", depth)
			args := append([]string{"diff", uFlag, rangeRef, "--"}, g.Files...)
			wipDiff, _, _ := runner.Run(ctx, args...)
			b.Write(wipDiff)
			if len(wipDiff) > 0 && !strings.HasSuffix(string(wipDiff), "\n") {
				b.WriteByte('\n')
			}
		}
		args := append([]string{"diff", uFlag, "--cached", "--"}, g.Files...)
		cached, _, _ := runner.Run(ctx, args...)
		unstagedArgs := append([]string{"diff", uFlag, "--"}, g.Files...)
		unstaged, _, _ := runner.Run(ctx, unstagedArgs...)
		b.Write(cached)
		if len(cached) > 0 && !strings.HasSuffix(string(cached), "\n") {
			b.WriteByte('\n')
		}
		b.Write(unstaged)
		diffs[groupKeyLocal(g)] = aicommit.TruncateDiff(b.String(), aicommit.DefaultComposeDiffByteCap)
	}
	return diffs, nil
}

// commitDisplayStats digests the same cached+unstaged(+WIP) diff the
// commit is about to apply and returns a path → FileStat map for the
// plan preview: change kind, line delta, and touched symbols, mirroring
// `gk diff --digest`. Unlike collectGroupDiffs (per-group, truncated for
// the LLM payload) this is a single untruncated pass over every in-scope
// file so the preview totals are exact. Any failure returns nil so the
// preview falls back to the plain file list rather than blocking commit.
func commitDisplayStats(ctx context.Context, runner git.Runner, files []aicommit.FileChange, wipCommit wipCommitForAICommit) map[string]aicommit.FileStat {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	if len(paths) == 0 {
		return nil
	}

	var b strings.Builder
	if wipCommit.Present {
		depth := wipCommit.ChainLen
		if depth <= 0 {
			depth = 1
		}
		args := append([]string{"diff", fmt.Sprintf("HEAD~%d..HEAD", depth), "--"}, paths...)
		if d, _, err := runner.Run(ctx, args...); err == nil {
			b.Write(d)
			if len(d) > 0 && !strings.HasSuffix(string(d), "\n") {
				b.WriteByte('\n')
			}
		}
	}
	cached, _, _ := runner.Run(ctx, append([]string{"diff", "--cached", "--"}, paths...)...)
	unstaged, _, _ := runner.Run(ctx, append([]string{"diff", "--"}, paths...)...)
	b.Write(cached)
	if len(cached) > 0 && !strings.HasSuffix(string(cached), "\n") {
		b.WriteByte('\n')
	}
	b.Write(unstaged)

	res, err := diff.ParseUnifiedDiff(strings.NewReader(b.String()))
	if err != nil || res == nil {
		return nil
	}
	d := diff.BuildDigest(res)
	if len(d.Files) == 0 {
		return nil
	}
	stats := make(map[string]aicommit.FileStat, len(d.Files))
	for _, fd := range d.Files {
		// Suppress docs/build "symbols" — git's generic hunk-header
		// heuristic catches body text there, matching the digest contract.
		names := fd.Symbols
		if kind := aicommit.FileKind(fd.Path); kind == "docs" || kind == "build" {
			names = nil
		}
		fs := aicommit.FileStat{
			Glyph:   statusGlyph(fd.Status),
			Added:   fd.Added,
			Deleted: fd.Deleted,
			Symbols: joinSymbolNames(names, 3),
		}
		stats[fd.Path] = fs
		// A rename's group file may carry the old path — key both so the
		// preview lookup hits regardless of which side classification used.
		if fd.OldPath != "" {
			stats[fd.OldPath] = fs
		}
	}
	return stats
}

// groupKeyLocal mirrors internal/aicommit.groupKey; we duplicate rather
// than export the package-internal helper.
func groupKeyLocal(g provider.Group) string {
	key := g.Type + "|"
	for i, f := range g.Files {
		if i > 0 {
			key += ","
		}
		key += f
	}
	return key
}

// filterKept returns the subset of messages that survived review.
// EditedSubject / EditedBody overrides from the review TUI are applied
// in place so the committed message reflects the user's edits.
func filterKept(messages []aicommit.Message, decisions []aicommit.ReviewDecision) []aicommit.Message {
	out := messages[:0:0]
	for i, d := range decisions {
		if i >= len(messages) {
			break
		}
		if !d.Keep {
			continue
		}
		m := messages[i]
		if d.EditedSubject != "" {
			m.Subject = d.EditedSubject
		}
		if d.EditedBody != "" {
			m.Body = d.EditedBody
		}
		out = append(out, m)
	}
	return out
}

// providerModel returns the model identifier for debug logging across the
// HTTP adapters (so `-d` shows the effective model, including a
// commit.model / --model override). CLI providers return "n/a" — they own
// their model selection. FallbackChain reports its first provider.
func providerModel(p provider.Provider) string {
	switch v := p.(type) {
	case *provider.Nvidia:
		if v.Model != "" {
			return v.Model
		}
		return "meta/llama-3.1-8b-instruct"
	case *provider.OpenAI:
		if v.Model != "" {
			return v.Model
		}
	case *provider.Groq:
		if v.Model != "" {
			return v.Model
		}
	case *provider.Anthropic:
		if v.Model != "" {
			return v.Model
		}
	case *provider.FallbackChain:
		if len(v.Providers) > 0 {
			return providerModel(v.Providers[0])
		}
	}
	return "n/a"
}

// composeDispatchLabel describes how the LLM groups will be composed,
// so the concurrency knob is observable in the normal output:
//
//   - 0 LLM groups        → "no LLM calls" (everything heuristic)
//   - 1 LLM group         → "single-shot" (fast-path, no fan-out)
//   - N>1 LLM groups       → "parallel ×K" (K = effective worker count)
//     or "warm+parallel ×K" when the first call
//     primes a prompt cache before the rest fan out
//
// K mirrors ComposeAll's own math: the first group runs synchronously
// when warming, so only llmN-1 groups fan out in that case.
func composeDispatchLabel(llmN, configured int, warm bool) string {
	switch {
	case llmN <= 0:
		return "no LLM calls"
	case llmN == 1:
		return "single-shot"
	default:
		fanOut := llmN
		prefix := ""
		if warm {
			fanOut = llmN - 1
			prefix = "warm+"
		}
		limit := configured
		if limit <= 0 {
			limit = aicommit.DefaultComposeConcurrency
		}
		if limit > fanOut {
			limit = fanOut
		}
		return fmt.Sprintf("%sparallel ×%d", prefix, limit)
	}
}

// providerCachesPrompt reports whether the provider benefits from
// priming a shared prompt prefix before the parallel Compose fan-out.
// Anthropic caches the system-prompt block (cache_control: ephemeral),
// so one synchronous warm-up call turns the siblings' full-price input
// tokens into cheap cache reads. Adapters without such a cache gain
// nothing from warming (it would only serialise the first call), so
// they report false and Compose stays fully parallel. A FallbackChain
// defers to whichever provider it would actually use first.
func providerCachesPrompt(p provider.Provider) bool {
	switch v := p.(type) {
	case *provider.Anthropic:
		return true
	case *provider.FallbackChain:
		if len(v.Providers) > 0 {
			return providerCachesPrompt(v.Providers[0])
		}
	}
	return false
}

// readProviderVersion tries `<provider> --version`; returns "unknown"
// on failure so the trailer still records _something_.
func readProviderVersion(_ context.Context, p provider.Provider) string {
	// v1 is intentionally lightweight — full version probing lives in
	// `gk doctor`. Attempts here would double the startup cost.
	return "unknown"
}

// printBackupHint is printed on apply failure so users know how to
// recover. Empty backup is a no-op.
func printBackupHint(out interface{ Write(p []byte) (int, error) }, backup string) {
	if backup == "" {
		return
	}
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, stylizeHintLine(fmt.Sprintf("hint: run `gk commit --abort` to restore HEAD to %s", backup)))
}

// printApplySummary prints the final commit list (or dry-run plan).
func printApplySummary(out interface{ Write(p []byte) (int, error) }, kept []aicommit.Message, res aicommit.ApplyResult, dryRun bool) {
	if dryRun {
		fmt.Fprintf(out, "commit: dry-run — %d commit(s) would be made (backup ref: %s)\n", len(kept), res.BackupRef)
		return
	}
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, successLinef("commit: created", "%d commit(s)", len(kept)))
	for i, m := range kept {
		sha := ""
		if i < len(res.CommitShas) {
			sha = res.CommitShas[i]
		}
		fmt.Fprintf(out, "  %s  %s\n", cellFaint(sha), m.Header())
	}
	// Undo affordance front-and-center; the raw backup ref is demoted to
	// dim metadata so the actionable command reads first.
	if res.BackupRef != "" {
		fmt.Fprintln(out, "  "+stylizeHintLine(
			fmt.Sprintf("hint: gk commit --abort (undo · backup ref %s)", res.BackupRef)))
	}
}

// writeAuditEntries appends one AuditEntry per applied commit. Swallows
// IO errors — audit is best-effort, never blocks a successful commit.
func writeAuditEntries(ctx context.Context, runner git.Runner, prov provider.Provider, kept []aicommit.Message, res aicommit.ApplyResult) error {
	out, _, err := runner.Run(ctx, "rev-parse", "--git-dir")
	if err != nil {
		return err
	}
	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(RepoFlag(), gitDir)
	}
	w, err := aicommit.OpenAuditLog(gitDir)
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()

	runID := newRunID()
	for i, m := range kept {
		sha := ""
		if i < len(res.CommitShas) {
			sha = res.CommitShas[i]
		}
		_ = w.Write(aicommit.AuditEntry{
			TS:         time.Now().UTC(),
			RunID:      runID,
			Provider:   prov.Name(),
			Model:      m.Model,
			CommitSha:  sha,
			GroupType:  m.Group.Type,
			GroupScope: m.Group.Scope,
			Files:      m.Group.Files,
			Subject:    m.Subject,
			Attempts:   m.Attempts,
			BackupRef:  res.BackupRef,
		})
	}
	return nil
}

// latestAICommitBackupRef returns the most recent
// refs/gk/ai-commit-backup/* ref by unix-timestamp suffix. Empty when
// none exist.
func latestAICommitBackupRef(ctx context.Context, runner git.Runner) (string, error) {
	out, _, err := runner.Run(ctx, "for-each-ref", "--format=%(refname)", "refs/gk/ai-commit-backup/")
	if err != nil {
		return "", fmt.Errorf("list backup refs: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	best, bestTS := "", int64(-1)
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		parts := strings.Split(l, "/")
		tsStr := parts[len(parts)-1]
		ts, err := parseUnixTS(tsStr)
		if err != nil {
			continue
		}
		if ts > bestTS {
			bestTS = ts
			best = l
		}
	}
	return best, nil
}

// newRunID returns a short hex id that groups AuditEntries from one
// `gk commit` invocation. Not a real UUID — just enough entropy to
// avoid collisions across concurrent runs on the same machine.
func newRunID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Fall back to time-based id if /dev/urandom is unavailable.
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

func parseUnixTS(s string) (int64, error) {
	// Simple non-negative integer parse — avoids strconv import churn.
	if s == "" {
		return 0, fmt.Errorf("empty ts")
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("bad ts %q", s)
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

// formatTokens renders a provider token count for the classify/compose timing
// lines: a compact "1.2k tok" above a thousand, the bare count below it.
func formatTokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk tok", float64(n)/1000)
	}
	return fmt.Sprintf("%d tok", n)
}
