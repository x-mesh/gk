package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/policy"
	"github.com/x-mesh/gk/internal/policy/rules"
	"github.com/x-mesh/gk/internal/secrets"
	"github.com/x-mesh/gk/internal/secrets/gitleaksrules"
	"github.com/x-mesh/gk/internal/secrets/verify"
)

func init() {
	guardCmd := &cobra.Command{
		Use:   "guard",
		Short: "Declarative repo policies (secret scan, signing, commit rules)",
		Long: `gk guard evaluates every registered policy rule against the repo and
surfaces violations. Rules live in .gk.yaml under the policies: block; the
v0.9 MVP ships with secret_patterns. By default it scans the current content
of the tracked files; --staged scans the staged changes, and --history scans
every commit reachable from any ref. It also scans the files inside the .jar,
.war, .ear, and .zip archives in that scope and reports them as archive!file.
It uses the gitleaks ruleset embedded in
gk (a pinned release), which follows the gitleaks detection steps and runs
faster than the gitleaks binary. gk runs the gitleaks binary instead when the
repo has .gitleaks.toml or .gitleaksignore (or GITLEAKS_CONFIG or
GITLEAKS_CONFIG_TOML is set), because the embedded ruleset does not read
those files.
`,
	}

	checkCmd := &cobra.Command{
		Use:   "check",
		Short: "Run all policy rules and report violations",
		Long: `Evaluates every registered Rule in parallel and prints violations sorted
by severity (error → warn → info). Exit codes:

  0 — no violations, or info-only
  1 — one or more warnings
  2 — one or more errors

--verify checks each AWS access key, Slack token, GCP API key, and OCI API
signing key that the embedded ruleset finds, with a read-only identity call
to its issuer (one call for a Slack token or a GCP API key, up to three for
an AWS key or an OCI key):

  AWS    STS GetCallerIdentity. gk pairs the key ID with a secret access key
         on the same or nearby lines and tries up to three candidates. The
         secret signs the request and is not sent. The call can appear in
         the key owner's CloudTrail.
  Slack  auth.test on slack.com. The token is sent in the Authorization
         header. Webhook URLs and refresh tokens are not checked, because
         the checks for them post a message or rotate the token.
  GCP    the Discovery API on googleapis.com. The key is sent in the
         X-Goog-Api-Key header.
  OCI    Identity ListApiKeys for the key's own user. gk checks a private
         key when an OCI profile in the scanned content (a fingerprint with
         a user, a tenancy, and a region) has the fingerprint of that key.
         The profile and the key can be in different files or commits. The
         key signs the request and is not sent. Only the commercial realm
         (oc1) is checked.

Each credential gets a status: valid (the issuer accepted it; for a GCP key,
also a key whose restrictions blocked the call), rejected (the issuer said
it is invalid, revoked, or expired), unknown (the check failed or did not
decide), or unverifiable (no AWS secret or session token was found, an OCI
profile has no region, or the key type is not supported). For OCI, a 401
is rejected when exactly one profile names the key, and unknown when several
do or when the response shows that the local clock is off. OCI gives the
same 401 for a region the tenancy is not subscribed to, so rejected does not
prove that the key was deleted. The status annotates the finding; it never
removes it or changes its severity.
`,
		RunE: runGuardCheck,
	}
	checkCmd.Flags().Bool("json", false, "emit NDJSON violations (one per line) for scripting")
	checkCmd.Flags().Bool("staged", false, "scan only the staged changes (what the next commit adds); the pre-commit hook uses this")
	checkCmd.Flags().Bool("history", false, "scan every commit reachable from any ref instead of the current files")
	checkCmd.Flags().Bool("fetch", false, "fetch every remote first and scan the history, including branches that exist only on a remote (implies --history; network)")
	checkCmd.Flags().Bool("verify", false, "check each AWS key, Slack token, GCP API key, and OCI API key found with its issuer (network; sends Slack and GCP values; never with --staged)")
	checkCmd.Flags().Int("archive-readers", rules.DefaultArchiveReaders, "git processes that read the committed .jar/.war/.ear/.zip archives at once; each holds the archive it rebuilds in memory, so more is faster and uses more memory")
	checkCmd.MarkFlagsMutuallyExclusive("staged", "history")
	checkCmd.MarkFlagsMutuallyExclusive("staged", "verify")
	checkCmd.MarkFlagsMutuallyExclusive("staged", "fetch")

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Scaffold .gk.yaml with a commented policies block",
		Long: `Creates .gk.yaml in the repository root (or --repo path) with a
fully-commented policies: block so you can opt in rules one by one.

Exits non-zero when .gk.yaml already exists; pass --force to overwrite.
`,
		RunE: runGuardInit,
	}
	initCmd.Flags().Bool("force", false, "overwrite existing .gk.yaml")
	initCmd.Flags().String("out", "", "write to this path instead of <repo>/.gk.yaml")

	guardCmd.AddCommand(checkCmd)
	guardCmd.AddCommand(initCmd)
	rootCmd.AddCommand(guardCmd)
}

// guardRegistry returns a freshly populated policy.Registry. Rules are
// constructed per-invocation so the binary stays safe for concurrent
// invocations and tests can swap rules via their own Registry when needed.
func guardRegistry() *policy.Registry {
	reg := policy.NewRegistry()
	secretRule := rules.NewSecretPatternsRule()
	secretRule.ScanDiff = gitleaksrules.ScanDiff
	secretRule.ScanFile = gitleaksrules.ScanFile
	secretRule.ScannerLabel = func() string {
		return fmt.Sprintf("the embedded gitleaks %s ruleset (%d rules) and %d gk rule(s)", gitleaksrules.Version(), gitleaksrules.RuleCount(), gitleaksrules.GKRuleCount())
	}
	secretRule.Verify = verifyCredentials
	_ = reg.Register(secretRule)
	return reg
}

func verifyCredentials(ctx context.Context, creds []*secrets.Credential) map[string]policy.Verification {
	provider := make(map[string]string, len(creds))
	for _, c := range creds {
		provider[c.ID()] = c.Provider()
	}
	results := (&verify.Verifier{}).Verify(ctx, creds)
	out := make(map[string]policy.Verification, len(results))
	for id, r := range results {
		out[id] = policy.Verification{
			Provider: provider[id], Status: string(r.Status),
			Account: r.Account, AccountName: r.AccountName, URL: r.URL,
			ARN: r.ARN, Identity: r.Identity, IdentityName: r.IdentityName, Code: r.Code,
		}
	}
	return out
}

// guardFetch fetches every remote so the history scan reaches branches that
// exist only on a remote. It does not prune: a remote-tracking ref whose
// branch was deleted upstream still names commits worth scanning. A failed
// fetch becomes a warning instead of an error, because the local refs are
// still worth scanning, but the result must not read as a full scan.
func guardFetch(ctx context.Context, runner *git.ExecRunner) []policy.Violation {
	out, stderr, err := runner.Run(ctx, "remote")
	if err != nil {
		return []policy.Violation{{
			RuleID:   "fetch",
			Severity: policy.SeverityWarn,
			Message:  fmt.Sprintf("could not list remotes; scanned local refs only: %s", strings.TrimSpace(string(stderr))),
		}}
	}
	remotes := strings.Fields(string(out))
	if len(remotes) == 0 {
		return []policy.Violation{{
			RuleID:   "fetch",
			Severity: policy.SeverityInfo,
			Message:  "no remote configured; scanned local refs only",
		}}
	}
	var vs []policy.Violation
	for _, res := range runContextFetch(ctx, runner, remotes) {
		if res.OK {
			continue
		}
		vs = append(vs, policy.Violation{
			RuleID:   "fetch",
			Severity: policy.SeverityWarn,
			Message:  fmt.Sprintf("fetch %s failed; branches only on %s were not scanned: %s", res.Remote, res.Remote, res.Error),
			Hint:     "check the remote URL and credentials, then rerun gk guard check --fetch",
		})
	}
	return vs
}

// ruleErrorID is the rule ID of the report entry for a rule that failed to
// run. No rule registers under it, so a consumer that counts findings by
// rule can tell a failed scan from a finding.
const ruleErrorID = "rule_error"

// ruleErrorViolations returns one error entry per rule failure. A rule that
// fails has checked nothing: without the entry, a scan that never ran (a
// directory that is not a repository, a bare repository without --history)
// printed "no violations" and exited 0.
func ruleErrorViolations(errs []error) []policy.Violation {
	out := make([]policy.Violation, 0, len(errs))
	for _, e := range errs {
		out = append(out, policy.Violation{
			RuleID:   ruleErrorID,
			Severity: policy.SeverityError,
			Message:  "rule did not run, so its checks were skipped: " + strings.TrimSpace(e.Error()),
			Hint:     "fix the cause and rerun; this result is incomplete",
		})
	}
	return out
}

// violationJSON is the NDJSON shape for --json output. Keep stable.
type violationJSON struct {
	RuleID   string `json:"rule_id"`
	Severity string `json:"severity"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
	// Verification is present only on findings that --verify checked.
	Verification *verificationJSON `json:"verification,omitempty"`
}

type verificationJSON struct {
	Provider string `json:"provider,omitempty"`
	Status   string `json:"status"`
	Account  string `json:"account,omitempty"`
	ARN      string `json:"arn,omitempty"`
	Identity string `json:"identity,omitempty"`
	Code     string `json:"code,omitempty"`
	// Slack only: the workspace name and URL, and the user name.
	AccountName  string `json:"account_name,omitempty"`
	URL          string `json:"url,omitempty"`
	IdentityName string `json:"identity_name,omitempty"`
}

func runGuardCheck(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	repo := RepoFlag()
	runner := &git.ExecRunner{Dir: repo}
	staged, _ := cmd.Flags().GetBool("staged")
	history, _ := cmd.Flags().GetBool("history")
	doFetch, _ := cmd.Flags().GetBool("fetch")
	readers, err := cmd.Flags().GetInt("archive-readers")
	if err == nil && readers < 1 {
		return fmt.Errorf("gk guard check: --archive-readers must be at least 1")
	}
	var fetchViolations []policy.Violation
	if doFetch {
		fetchViolations = guardFetch(ctx, runner)
	}

	reg := guardRegistry()
	in := policy.Input{
		Runner:  runner,
		WorkDir: repo,
		Staged:  staged,
		History: history || doFetch,
		// Zero when the flag is not defined: the rule's default.
		ArchiveReaders: readers,
	}
	in.Verify, _ = cmd.Flags().GetBool("verify")

	violations, errs := reg.Evaluate(ctx, in)
	violations = slices.Concat(ruleErrorViolations(errs), fetchViolations, violations)

	asJSON, _ := cmd.Flags().GetBool("json")
	asJSON = asJSON || JSONOut()
	w := cmd.OutOrStdout()
	if asJSON {
		// Agent mode wraps the violations as one enveloped array; the bare
		// --json NDJSON stream (one violation per line) stays for existing
		// line-oriented consumers.
		if AgentOut() {
			if err := emitAgentResult(w, violationsJSON(violations)); err != nil {
				return err
			}
		} else if err := printViolationsNDJSON(w, violations); err != nil {
			return err
		}
	} else {
		printViolationsHuman(w, violations)
	}

	// Surface rule infra errors after violations so the report is still useful.
	for _, e := range errs {
		fmt.Fprintf(cmd.ErrOrStderr(), "rule error: %v\n", e)
	}

	// Exit code derives from the worst severity.
	switch worstSeverity(violations) {
	case policy.SeverityError:
		return fmt.Errorf("%d error(s)", countSeverity(violations, policy.SeverityError))
	case policy.SeverityWarn:
		return fmt.Errorf("%d warning(s)", countSeverity(violations, policy.SeverityWarn))
	default:
		return nil
	}
}

func violationsJSON(vs []policy.Violation) []violationJSON {
	out := make([]violationJSON, 0, len(vs))
	for _, v := range vs {
		j := violationJSON{
			RuleID:   v.RuleID,
			Severity: v.Severity.String(),
			File:     v.File,
			Line:     v.Line,
			Message:  v.Message,
			Hint:     v.Hint,
		}
		if r := v.Verification; r != nil {
			j.Verification = &verificationJSON{
				Provider: r.Provider, Status: r.Status, Account: r.Account, ARN: r.ARN, Identity: r.Identity, Code: r.Code,
				AccountName: r.AccountName, URL: r.URL, IdentityName: r.IdentityName,
			}
		}
		out = append(out, j)
	}
	return out
}

func printViolationsNDJSON(w io.Writer, vs []policy.Violation) error {
	enc := json.NewEncoder(w)
	for _, v := range violationsJSON(vs) {
		if err := enc.Encode(v); err != nil {
			return err
		}
	}
	return nil
}

func printViolationsHuman(w io.Writer, vs []policy.Violation) {
	if len(vs) == 0 {
		fmt.Fprintln(w, "gk guard: no violations")
		return
	}
	for _, v := range vs {
		loc := v.RuleID
		if v.File != "" {
			if v.Line > 0 {
				loc = fmt.Sprintf("%s (%s:%d)", v.RuleID, v.File, v.Line)
			} else {
				loc = fmt.Sprintf("%s (%s)", v.RuleID, v.File)
			}
		}
		fmt.Fprintf(w, "%-5s  %s  %s\n", v.Severity, loc, v.Message)
		if v.Verification != nil {
			fmt.Fprintf(w, "       verify: %s\n", verificationLabel(*v.Verification))
		}
		if v.Hint != "" {
			fmt.Fprintf(w, "       hint: %s\n", v.Hint)
		}
	}
}

func verificationLabel(r policy.Verification) string {
	var parts []string
	if r.Account != "" {
		parts = append(parts, "account "+r.Account+namedSuffix(r.AccountName, r.URL))
	}
	if r.ARN != "" {
		parts = append(parts, r.ARN)
	}
	if r.Identity != "" {
		parts = append(parts, "user "+r.Identity+namedSuffix(r.IdentityName, ""))
	}
	if r.Code != "" {
		parts = append(parts, r.Code)
	}
	if len(parts) == 0 {
		return r.Status
	}
	return r.Status + " (" + strings.Join(parts, ", ") + ")"
}

// namedSuffix quotes a display name, which can contain spaces or commas, so
// it stays distinct from the ID before it and the parts after it.
func namedSuffix(name, url string) string {
	var s string
	if name != "" {
		s += " " + strconv.Quote(name)
	}
	if url != "" {
		s += " " + url
	}
	return s
}

// worstSeverity finds the lowest numeric Severity in vs (most severe).
// Returns SeverityInfo when vs is empty.
func worstSeverity(vs []policy.Violation) policy.Severity {
	if len(vs) == 0 {
		return policy.SeverityInfo
	}
	worst := policy.SeverityInfo
	for _, v := range vs {
		if v.Severity < worst {
			worst = v.Severity
		}
	}
	return worst
}

func countSeverity(vs []policy.Violation, s policy.Severity) int {
	n := 0
	for _, v := range vs {
		if v.Severity == s {
			n++
		}
	}
	return n
}

// gkYAMLTemplate is the scaffold written by `gk guard init`. Every rule block
// is commented out so the file is valid YAML and the user opts in explicitly.
const gkYAMLTemplate = `# .gk.yaml — gk repository policy configuration
# Run: gk guard check
# Docs: https://github.com/x-mesh/gk/blob/main/docs/commands.md#gk-guard
#
# Uncomment the rules you want to enforce. Each rule maps to a built-in
# gk guard policy; unknown keys are silently ignored so future rule names
# can be added here before upgrading gk.

policies:

  # ── secret_patterns ────────────────────────────────────────────────────────
  # Scans the tracked files (or, with --staged / --history, the staged
  # changes or the full history) for secrets with the gitleaks ruleset
  # embedded in gk. If .gitleaks.toml or .gitleaksignore
  # exists, gk runs gitleaks instead, because the embedded ruleset does not
  # read those files.
  # Optional: brew install gitleaks  (gk doctor will confirm)
  #
  # secret_patterns:
  #   enabled: true
  #   mode: files      # files (tracked files) | staged | history
  #   redact: true     # replace the matched value with REDACTED in output

  # ── max_commit_size ────────────────────────────────────────────────────────
  # Rejects commits whose diff exceeds a line or file count threshold.
  # Helps keep PRs reviewable and avoids accidental large-file check-ins.
  #
  # max_commit_size:
  #   enabled: true
  #   max_lines: 1000
  #   max_files: 50

  # ── required_trailers ──────────────────────────────────────────────────────
  # Enforces that every commit message contains specified git trailers
  # (e.g., Signed-off-by, Change-Id, Jira-Ticket).
  #
  # required_trailers:
  #   enabled: true
  #   trailers:
  #     - Signed-off-by
  #     # - Jira-Ticket

  # ── forbid_force_push_to ───────────────────────────────────────────────────
  # Blocks force-pushes to the listed branches when used as a pre-push hook.
  #
  # forbid_force_push_to:
  #   enabled: true
  #   branches:
  #     - main
  #     - master
  #     - develop

  # ── require_signed ─────────────────────────────────────────────────────────
  # Verifies that every commit in the push range carries a GPG/SSH signature.
  #
  # require_signed:
  #   enabled: false


# ── Allow-list ─────────────────────────────────────────────────────────────
# To suppress a specific finding, add an entry to .gk/allow.yaml instead of
# disabling the rule entirely. Each entry requires a justification and an
# optional expiry date so suppressions self-audit over time.
#
# Example .gk/allow.yaml:
#
#   - rule: secret_patterns
#     file: scripts/seed.sh
#     line: 42
#     reason: "Test credential — rotated monthly, never reaches prod"
#     expires: 2026-12-31
`

func runGuardInit(cmd *cobra.Command, _ []string) error {
	outPath, _ := cmd.Flags().GetString("out")
	force, _ := cmd.Flags().GetBool("force")

	if outPath == "" {
		repo := RepoFlag()
		if repo == "" {
			var err error
			repo, err = os.Getwd()
			if err != nil {
				return fmt.Errorf("guard init: cannot determine working directory: %w", err)
			}
		}
		outPath = filepath.Join(repo, ".gk.yaml")
	}

	if _, err := os.Stat(outPath); err == nil && !force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", outPath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("guard init: stat %s: %w", outPath, err)
	}

	if err := os.WriteFile(outPath, []byte(gkYAMLTemplate), 0o644); err != nil {
		return fmt.Errorf("guard init: write %s: %w", outPath, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "created %s\n", outPath)
	fmt.Fprintln(cmd.OutOrStdout(), stylizeHintLine("next: uncomment rules, then run `gk guard check`"))
	return nil
}
