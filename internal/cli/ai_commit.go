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
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/config"
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
`,
		RunE: runAICommit,
	}
	cmd.Flags().BoolP("force", "f", false, "apply commits without interactive review")
	cmd.Flags().Bool("dry-run", false, "preview groups + estimated token cost; no LLM calls")
	cmd.Flags().String("provider", "", "override ai.provider (gemini|qwen|kiro)")
	cmd.Flags().String("lang", "", "override ai.lang (en|ko|...)")
	cmd.Flags().Bool("staged-only", false, "only consider already-staged changes")
	cmd.Flags().Bool("include-unstaged", false, "include unstaged + untracked changes (default true)")
	cmd.Flags().StringSlice("allow-secret-kind", nil, "suppress secret findings of the given kind (repeatable)")
	cmd.Flags().Bool("abort", false, "restore HEAD to the latest ai-commit backup ref and exit")
	cmd.Flags().Bool("ci", false, "CI mode — require --force or --dry-run, never prompt")
	cmd.Flags().BoolP("yes", "y", false, "accept every prompt (alias for --force when non-TTY)")
	cmd.Flags().Bool("no-wip-unwrap", false, "skip detection/unwrap of WIP-like commits in HEAD chain")

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
	ai := applyAICommitFlagsToConfig(cfg.AI, flags)

	runner := &git.ExecRunner{Dir: RepoFlag()}

	if flags.abort {
		return runAICommitAbort(ctx, cmd, runner)
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
		p, pErr := provider.NewProvider(ctx, aiFactoryOptionsFromAI(ai))
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
	wipCommit, err := inspectWIPCommitForAICommit(ctx, runner, ai.Commit, cfg.Branch.Protected, wipDisabled)
	if err != nil {
		return err
	}

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
		fmt.Fprintln(cmd.OutOrStdout(), "commit: no working-tree changes to commit")
		return nil
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
		renderFindings(cmd.ErrOrStderr(), findings)
		return fmt.Errorf("commit: aborted due to %d secret finding(s); fix or --allow-secret-kind <kind>",
			len(findings))
	}
	Dbg("commit: secret-gate clean")

	// Privacy Gate: redact payload for remote providers.
	redactedPayload, pgFindings, pgErr := applyPrivacyGate(prov, payload, ai)
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

	// Classify — the first provider call. Spinner advertises we are
	// waiting on the AI CLI, not stuck.
	fmt.Fprintf(cmd.ErrOrStderr(), "commit: classifying %d file(s) via %s...\n", len(files), prov.Name())
	stopClassify := ui.StartBubbleSpinner(fmt.Sprintf("classify — %s", prov.Name()))
	classifyStart := time.Now()
	groups, err := aicommit.Classify(ctx, prov, files, aicommit.ClassifyOptions{
		AllowedTypes:    cfg.Commit.Types,
		AllowedScopes:   allowedScopesFromFiles(files),
		Lang:            ai.Lang,
		HybridFileLimit: 5,
	})
	stopClassify()
	if err != nil {
		return fmt.Errorf("commit: classify: %w", err)
	}
	Dbg("commit: classify ok — %d group(s) in %s", len(groups), time.Since(classifyStart).Round(time.Millisecond))
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
	heuristicN := aicommit.CountHeuristicGroups(groups, ai.Lang)
	llmN := len(groups) - heuristicN
	if heuristicN > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"commit: composing %d message(s) (%d via heuristic, %d via %s)...\n",
			len(groups), heuristicN, llmN, prov.Name())
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "commit: composing %d message(s)...\n", len(groups))
	}
	stopCompose := ui.StartBubbleSpinner(fmt.Sprintf("compose — %d group(s) via %s", len(groups), prov.Name()))
	composeStart := time.Now()
	messages, err := aicommit.ComposeAll(ctx, prov, groups, diffs, aicommit.ComposeOptions{
		MaxAttempts:      3,
		AllowedTypes:     cfg.Commit.Types,
		ScopeRequired:    cfg.Commit.ScopeRequired,
		MaxSubjectLength: cfg.Commit.MaxSubjectLength,
		Lang:             ai.Lang,
	})
	stopCompose()
	if err != nil {
		return fmt.Errorf("commit: compose: %w", err)
	}
	Dbg("commit: compose ok — %d message(s) in %s", len(messages), time.Since(composeStart).Round(time.Millisecond))

	// Review.
	reviewOpts := aicommit.ReviewOptions{
		Out:            cmd.OutOrStdout(),
		Force:          flags.force || flags.yes,
		NonInteractive: flags.ci && !ui.IsTerminal(),
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

	groups, err := aicommit.Classify(ctx, prov, files, aicommit.ClassifyOptions{
		HeuristicOnly:   true,
		AllowedTypes:    cfg.Commit.Types,
		AllowedScopes:   allowedScopesFromFiles(files),
		Lang:            ai.Lang,
		HybridFileLimit: 5,
	})
	if err != nil {
		return fmt.Errorf("commit: dry-run classify: %w", err)
	}
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
// AI plan. cfg.WIPMaxChain caps the walk; protected branches and
// already-pushed commits stop it for safety. When --no-wip-unwrap is
// set the function returns immediately without touching git.
func inspectWIPCommitForAICommit(ctx context.Context, runner git.Runner, cfg config.AICommitConfig, branchProtected []string, disabled bool) (wipCommitForAICommit, error) {
	if disabled {
		return wipCommitForAICommit{}, nil
	}
	patterns, err := aicommit.CompileWIPPatterns(cfg.WIPPatterns)
	if err != nil {
		return wipCommitForAICommit{}, fmt.Errorf("commit: wip patterns: %w", err)
	}
	chain, err := aicommit.DetectWIPChain(ctx, runner, aicommit.DetectWIPChainOptions{
		MaxChain:          cfg.WIPMaxChain,
		Patterns:          patterns,
		ProtectedBranches: branchProtected,
	})
	if err != nil {
		return wipCommitForAICommit{}, fmt.Errorf("commit: detect WIP chain: %w", err)
	}
	if len(chain) == 0 {
		return wipCommitForAICommit{}, nil
	}
	headOut, _, _ := runner.Run(ctx, "rev-parse", "HEAD")
	return wipCommitForAICommit{
		Present:  true,
		ChainLen: len(chain),
		HeadSHA:  strings.TrimSpace(string(headOut)),
		Files:    aicommit.MergeChainFiles(chain),
	}, nil
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
	fmt.Fprintf(cmd.OutOrStdout(), "commit: restored HEAD to %s\n", latest)
	return nil
}

// aiCommitFlags captures every CLI flag in one struct for easy passing.
type aiCommitFlags struct {
	force            bool
	dryRun           bool
	provider         string
	lang             string
	stagedOnly       bool
	includeUnstaged  bool
	allowSecretKinds []string
	abort            bool
	ci               bool
	yes              bool
	noWIPUnwrap      bool
}

func readAICommitFlags(cmd *cobra.Command) (aiCommitFlags, error) {
	var f aiCommitFlags
	f.force, _ = cmd.Flags().GetBool("force")
	f.dryRun, _ = cmd.Flags().GetBool("dry-run")
	f.provider, _ = cmd.Flags().GetString("provider")
	f.lang, _ = cmd.Flags().GetString("lang")
	f.stagedOnly, _ = cmd.Flags().GetBool("staged-only")
	f.includeUnstaged, _ = cmd.Flags().GetBool("include-unstaged")
	f.allowSecretKinds, _ = cmd.Flags().GetStringSlice("allow-secret-kind")
	f.abort, _ = cmd.Flags().GetBool("abort")
	f.ci, _ = cmd.Flags().GetBool("ci")
	f.yes, _ = cmd.Flags().GetBool("yes")
	f.noWIPUnwrap, _ = cmd.Flags().GetBool("no-wip-unwrap")
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

// collectGroupDiffs runs `git diff --cached` / `git diff` per group
// file set. Binary files fall back to `--stat`-only output so the
// provider sees a stub instead of junk bytes.
//
// Per-group diffs are capped to aicommit.DefaultComposeDiffByteCap
// before they leave this function — protects against a single huge
// diff (typical: a generated lockfile that escaped the heuristic
// bypass) blowing through the daily token budget.
func collectGroupDiffs(ctx context.Context, runner git.Runner, groups []provider.Group, wipCommit wipCommitForAICommit) (map[string]string, error) {
	diffs := map[string]string{}
	for _, g := range groups {
		var b strings.Builder
		if wipCommit.Present {
			depth := wipCommit.ChainLen
			if depth <= 0 {
				depth = 1
			}
			rangeRef := fmt.Sprintf("HEAD~%d..HEAD", depth)
			args := append([]string{"diff", rangeRef, "--"}, g.Files...)
			wipDiff, _, _ := runner.Run(ctx, args...)
			b.Write(wipDiff)
			if len(wipDiff) > 0 && !strings.HasSuffix(string(wipDiff), "\n") {
				b.WriteByte('\n')
			}
		}
		args := append([]string{"diff", "--cached", "--"}, g.Files...)
		cached, _, _ := runner.Run(ctx, args...)
		unstagedArgs := append([]string{"diff", "--"}, g.Files...)
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

// providerModel returns the model identifier for debug logging.
// For nvidia it reads the Model field; for others returns "n/a".
func providerModel(p provider.Provider) string {
	if nv, ok := p.(*provider.Nvidia); ok {
		if nv.Model != "" {
			return nv.Model
		}
		return "meta/llama-3.1-8b-instruct"
	}
	// FallbackChain: check the first provider.
	if fc, ok := p.(*provider.FallbackChain); ok && len(fc.Providers) > 0 {
		return providerModel(fc.Providers[0])
	}
	return "n/a"
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
	fmt.Fprintf(out, "\nhint: run `gk commit --abort` to restore HEAD to %s\n", backup)
}

// printApplySummary prints the final commit list (or dry-run plan).
func printApplySummary(out interface{ Write(p []byte) (int, error) }, kept []aicommit.Message, res aicommit.ApplyResult, dryRun bool) {
	if dryRun {
		fmt.Fprintf(out, "commit: dry-run — %d commit(s) would be made (backup ref: %s)\n", len(kept), res.BackupRef)
		return
	}
	fmt.Fprintf(out, "commit: created %d commit(s) (backup ref: %s)\n", len(kept), res.BackupRef)
	for i, m := range kept {
		sha := ""
		if i < len(res.CommitShas) {
			sha = res.CommitShas[i]
		}
		fmt.Fprintf(out, "  %s  %s: %s\n", sha, m.Group.Type, m.Subject)
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
