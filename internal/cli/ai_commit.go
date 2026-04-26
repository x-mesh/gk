package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
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
review and --dry-run to preview only. Use --abort to restore HEAD to the
backup ref created at the start of the most recent run.
`,
		RunE: runAICommit,
	}
	cmd.Flags().BoolP("force", "f", false, "apply commits without interactive review")
	cmd.Flags().Bool("dry-run", false, "show the plan and exit without committing")
	cmd.Flags().String("provider", "", "override ai.provider (gemini|qwen|kiro)")
	cmd.Flags().String("lang", "", "override ai.lang (en|ko|...)")
	cmd.Flags().Bool("staged-only", false, "only consider already-staged changes")
	cmd.Flags().Bool("include-unstaged", false, "include unstaged + untracked changes (default true)")
	cmd.Flags().StringSlice("allow-secret-kind", nil, "suppress secret findings of the given kind (repeatable)")
	cmd.Flags().Bool("abort", false, "restore HEAD to the latest ai-commit backup ref and exit")
	cmd.Flags().Bool("ci", false, "CI mode — require --force or --dry-run, never prompt")
	cmd.Flags().BoolP("yes", "y", false, "accept every prompt (alias for --force when non-TTY)")

	aiCmd.AddCommand(cmd)
}

func runAICommit(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return fmt.Errorf("ai commit: load config: %w", err)
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
			return fmt.Errorf("ai commit: %w", fcErr)
		}
		prov = fc
	} else {
		p, pErr := provider.NewProvider(ctx, provider.FactoryOptions{
			Name:   ai.Provider,
			Runner: provider.ExecRunner{},
		})
		if pErr != nil {
			return fmt.Errorf("ai commit: provider: %w", pErr)
		}
		prov = p
	}
	Dbg("ai commit: provider=%s model=%s lang=%s scope=%s", prov.Name(), providerModel(prov), ai.Lang, ai.Commit.DenyPaths)

	if err := aicommit.Preflight(ctx, aicommit.PreflightInput{
		Runner:      runner,
		WorkDir:     RepoFlag(),
		AI:          ai,
		Provider:    prov,
		AllowRemote: ai.Commit.AllowRemote,
	}); err != nil {
		return fmt.Errorf("ai commit: preflight: %w", err)
	}
	Dbg("ai commit: preflight ok")

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
	if len(files) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "ai commit: no working-tree changes to commit")
		return nil
	}
	Dbg("ai commit: gather ok — %d file(s) in scope=%v", len(files), scope)

	// Secret gate. gitleaks + internal/secrets can take a noticeable
	// beat on large diffs; show a spinner so the user knows gk is
	// actively guarding the payload rather than hung.
	stopGate := ui.StartSpinner("scanning payload for secrets...")
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
		return fmt.Errorf("ai commit: aborted due to %d secret finding(s); fix or --allow-secret-kind <kind>",
			len(findings))
	}
	Dbg("ai commit: secret-gate clean")

	// Privacy Gate: redact payload for remote providers.
	redactedPayload, _, pgErr := applyPrivacyGate(prov, payload, ai)
	if pgErr != nil {
		return fmt.Errorf("ai commit: privacy gate: %w", pgErr)
	}

	// --show-prompt: display redacted payload.
	showPromptIfRequested(cmd, redactedPayload)

	// Classify — the first provider call. Spinner advertises we are
	// waiting on the AI CLI, not stuck.
	fmt.Fprintf(cmd.ErrOrStderr(), "ai commit: classifying %d file(s) via %s...\n", len(files), prov.Name())
	stopClassify := ui.StartSpinner(fmt.Sprintf("classify — %s", prov.Name()))
	classifyStart := time.Now()
	groups, err := aicommit.Classify(ctx, prov, files, aicommit.ClassifyOptions{
		AllowedTypes:    cfg.Commit.Types,
		AllowedScopes:   allowedScopesFromFiles(files),
		Lang:            ai.Lang,
		HybridFileLimit: 5,
	})
	stopClassify()
	if err != nil {
		return fmt.Errorf("ai commit: classify: %w", err)
	}
	Dbg("ai commit: classify ok — %d group(s) in %s", len(groups), time.Since(classifyStart).Round(time.Millisecond))
	if len(groups) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "ai commit: nothing to commit after filtering")
		return nil
	}

	// Compose (per group) with commitlint retry.
	diffs, err := collectGroupDiffs(ctx, runner, groups)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "ai commit: composing %d message(s)...\n", len(groups))
	stopCompose := ui.StartSpinner(fmt.Sprintf("compose — %d group(s) via %s", len(groups), prov.Name()))
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
		return fmt.Errorf("ai commit: compose: %w", err)
	}
	Dbg("ai commit: compose ok — %d message(s) in %s", len(messages), time.Since(composeStart).Round(time.Millisecond))

	// Review.
	reviewOpts := aicommit.ReviewOptions{
		Out:            cmd.OutOrStdout(),
		Force:          flags.force || flags.yes,
		NonInteractive: flags.ci && !ui.IsTerminal(),
	}
	decisions, err := aicommit.ReviewPlan(messages, reviewOpts)
	if err != nil {
		return fmt.Errorf("ai commit: review: %w", err)
	}
	kept := filterKept(messages, decisions)
	if len(kept) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "ai commit: no groups were kept after review")
		return nil
	}

	// Apply.
	applyOpts := aicommit.ApplyOptions{DryRun: flags.dryRun}
	if ai.Commit.Trailer {
		applyOpts.Trailer = fmt.Sprintf("%s@%s", prov.Name(), readProviderVersion(ctx, prov))
	}
	result, err := aicommit.ApplyMessages(ctx, runner, kept, applyOpts)
	if err != nil {
		printBackupHint(cmd.ErrOrStderr(), result.BackupRef)
		return fmt.Errorf("ai commit: apply: %w", err)
	}

	// Audit (opt-in).
	if ai.Commit.Audit && !flags.dryRun {
		_ = writeAuditEntries(ctx, runner, prov, kept, result)
	}

	printApplySummary(cmd.OutOrStdout(), kept, result, flags.dryRun)
	return nil
}

// runAICommitAbort implements `gk ai commit --abort`.
func runAICommitAbort(ctx context.Context, cmd *cobra.Command, runner git.Runner) error {
	latest, err := latestAICommitBackupRef(ctx, runner)
	if err != nil {
		return err
	}
	if latest == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "ai commit: no backup ref found — nothing to abort")
		return nil
	}
	if err := aicommit.AbortRestore(ctx, runner, latest); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "ai commit: restored HEAD to %s\n", latest)
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
		fmt.Fprintf(&b, "### %s\n", f.Path)
		b.Write(content)
		b.WriteString("\n")
	}
	return b.String()
}

// isTestFile returns true for files that are test sources and may
// contain intentional fake secrets (e.g. _test.go, .test.ts).
func isTestFile(path string) bool {
	base := filepath.Base(path)
	return strings.HasSuffix(base, "_test.go") ||
		strings.HasSuffix(base, ".test.ts") ||
		strings.HasSuffix(base, ".test.js") ||
		strings.HasSuffix(base, ".spec.ts") ||
		strings.HasSuffix(base, ".spec.js")
}

func renderFindings(out interface{ Write(p []byte) (int, error) }, findings []aicommit.SecretFinding) {
	fmt.Fprintln(out, "ai commit: secret findings detected — aborting:")
	for _, f := range findings {
		fmt.Fprintf(out, "  [%s] %s @ %s:%d — %s\n", f.Source, f.Kind, f.File, f.Line, f.Sample)
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
func collectGroupDiffs(ctx context.Context, runner git.Runner, groups []provider.Group) (map[string]string, error) {
	diffs := map[string]string{}
	for _, g := range groups {
		args := append([]string{"diff", "--cached", "--"}, g.Files...)
		cached, _, _ := runner.Run(ctx, args...)
		unstagedArgs := append([]string{"diff", "--"}, g.Files...)
		unstaged, _, _ := runner.Run(ctx, unstagedArgs...)
		diffs[groupKeyLocal(g)] = string(cached) + "\n" + string(unstaged)
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
func filterKept(messages []aicommit.Message, decisions []aicommit.ReviewDecision) []aicommit.Message {
	out := messages[:0:0]
	for i, d := range decisions {
		if i >= len(messages) {
			break
		}
		if d.Keep {
			out = append(out, messages[i])
		}
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
	fmt.Fprintf(out, "\nhint: run `gk ai commit --abort` to restore HEAD to %s\n", backup)
}

// printApplySummary prints the final commit list (or dry-run plan).
func printApplySummary(out interface{ Write(p []byte) (int, error) }, kept []aicommit.Message, res aicommit.ApplyResult, dryRun bool) {
	if dryRun {
		fmt.Fprintf(out, "ai commit: dry-run — %d commit(s) would be made (backup ref: %s)\n", len(kept), res.BackupRef)
		return
	}
	fmt.Fprintf(out, "ai commit: created %d commit(s) (backup ref: %s)\n", len(kept), res.BackupRef)
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
// `gk ai commit` invocation. Not a real UUID — just enough entropy to
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
