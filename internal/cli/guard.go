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
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
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
surfaces violations. It currently runs secret_patterns on every check; .gk.yaml
does not enable or configure rules yet. By default it scans the current content
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
		Short: "Scaffold .gk.yaml with gk guard notes",
		Long: `Creates .gk.yaml in the repository root (or --repo path) with notes
on gk guard and how to suppress an intentional secret finding (gitleaks:allow).

Exits non-zero when .gk.yaml already exists; pass --force to overwrite.
--force first copies the old file to .gk.yaml.bak.<unix time>.
`,
		RunE: runGuardInit,
	}
	initCmd.Flags().Bool("force", false, "overwrite existing .gk.yaml after a backup to .gk.yaml.bak.<unix time>")
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
	Commit   string `json:"commit,omitempty"`
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
			if err := emitAgentResult(w, guardResultJSON(violationsJSON(violations))); err != nil {
				return err
			}
		} else if err := printViolationsNDJSON(w, violations); err != nil {
			return err
		}
	} else {
		printViolationsHuman(w, violations)
	}

	// Exit code derives from the worst severity. In agent mode the result
	// envelope already reports the check as blocked, so no error envelope
	// follows it on stderr.
	printErr := !AgentOut()
	switch worstSeverity(violations) {
	case policy.SeverityError:
		return &ExitError{Code: 2, printErr: printErr, err: fmt.Errorf("%d error(s)", countSeverity(violations, policy.SeverityError))}
	case policy.SeverityWarn:
		return &ExitError{Code: 1, printErr: printErr, err: fmt.Errorf("%d warning(s)", countSeverity(violations, policy.SeverityWarn))}
	default:
		return nil
	}
}

// guardResultJSON is the agent-mode result. A warning or an error makes the
// check exit non-zero, and the envelope state is blocked to match.
type guardResultJSON []violationJSON

func (r guardResultJSON) agentState() string {
	for _, v := range r {
		if v.Severity == policy.SeverityError.String() || v.Severity == policy.SeverityWarn.String() {
			return envStateBlocked
		}
	}
	return ""
}

func violationsJSON(vs []policy.Violation) []violationJSON {
	out := make([]violationJSON, 0, len(vs))
	for _, v := range vs {
		j := violationJSON{
			RuleID:   v.RuleID,
			Severity: v.Severity.String(),
			File:     v.File,
			Line:     v.Line,
			Commit:   v.Commit,
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

func guardCommitShort(sha string) string {
	if sha == "" {
		return ""
	}
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
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
		if short := guardCommitShort(v.Commit); short != "" {
			loc += " @" + short
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

// gkYAMLTemplate is the scaffold written by `gk guard init`.
const gkYAMLTemplate = `# .gk.yaml — gk project configuration (shared with the team)
# Run: gk guard check
# Docs: https://github.com/x-mesh/gk/blob/main/docs/commands.md#gk-guard
#
# gk guard always runs the built-in secret_patterns rule. Keys under policies:
# in this file are not read yet; use scope flags on gk guard check instead
# (--staged, --history).
#
# To suppress one intentional finding, add a gitleaks:allow marker on the line:
#   api_key = "example"  # gitleaks:allow
`

func runGuardInit(cmd *cobra.Command, _ []string) error {
	outPath, _ := cmd.Flags().GetString("out")
	force, _ := cmd.Flags().GetBool("force")

	if outPath == "" {
		repo, err := guardInitRepoRoot(RepoFlag())
		if err != nil {
			return err
		}
		outPath = filepath.Join(repo, ".gk.yaml")
	}

	existing, statErr := os.Stat(outPath)
	if statErr == nil && !force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", outPath)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("guard init: stat %s: %w", outPath, statErr)
	}

	// .gk.yaml is the shared project config, so --force keeps the old file:
	// the scaffold has no keys to merge it into.
	if statErr == nil {
		prev, err := os.ReadFile(outPath)
		if err != nil {
			return fmt.Errorf("guard init: read %s: %w", outPath, err)
		}
		bak := fmt.Sprintf("%s.bak.%d", outPath, time.Now().Unix())
		if err := writeNewFile(bak, prev, existing.Mode()); err != nil {
			return fmt.Errorf("guard init: backup %s: %w", bak, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "backed up existing config to %s\n", bak)
	}

	perm := os.FileMode(0o644)
	if statErr == nil {
		perm = existing.Mode().Perm()
	}
	if err := replaceFile(outPath, []byte(gkYAMLTemplate), perm); err != nil {
		return fmt.Errorf("guard init: write %s: %w", outPath, err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "created %s\n", outPath)
	fmt.Fprintln(cmd.OutOrStdout(), stylizeHintLine("next: run `gk guard check`"))
	return nil
}

func guardInitRepoRoot(repoFlag string) (string, error) {
	search := repoFlag
	if root, _ := config.GitRepoRootIn(search); root != "" {
		return root, nil
	}
	if repoFlag != "" {
		return repoFlag, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("guard init: cannot determine working directory: %w", err)
	}
	return wd, nil
}

// maxSymlinkHops matches the Linux limit before open fails with ELOOP.
const maxSymlinkHops = 40

// replaceFile writes data to a temp file next to path and renames it over
// path, so a failed write leaves the old file whole. A symlink at path is
// followed, also when its target does not exist yet, as os.WriteFile follows
// it.
func replaceFile(path string, data []byte, perm os.FileMode) error {
	path, err := symlinkTarget(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// symlinkTarget follows the symlinks at path one link at a time, so a link
// to a file that does not exist yet resolves to that file.
func symlinkTarget(path string) (string, error) {
	for range maxSymlinkHops {
		link, err := os.Readlink(path)
		if err != nil {
			return path, nil
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(path), link)
		}
		path = link
	}
	return "", fmt.Errorf("%s: too many levels of symbolic links", path)
}

// writeNewFile fails instead of replacing a file that exists, so a second
// --force in the same second cannot overwrite the first backup.
func writeNewFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
