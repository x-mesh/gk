package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
)

// doctorStatus is the three-way status of a single check.
type doctorStatus string

const (
	statusPass doctorStatus = "PASS"
	statusWarn doctorStatus = "WARN"
	statusFail doctorStatus = "FAIL"
)

// doctorCheck is a single row in the report.
type doctorCheck struct {
	Name   string       `json:"name"`
	Status doctorStatus `json:"status"`
	Detail string       `json:"detail"`
	Fix    string       `json:"fix,omitempty"`
}

// minGitMajor/minGitMinor is the lowest git version that supports the features
// gk relies on (`merge-tree --write-tree`, landed in 2.38). The `--name-only`
// fast path used by `gk precheck` lands in 2.40 but is not strictly required.
const minGitMajor = 2
const minGitMinor = 38
const preferredGitMinor = 40

func init() {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Report the health of the gk environment (git, pager, editor, config, hooks)",
		Long: `Runs a set of non-invasive environment checks and prints a PASS/WARN/FAIL
report with copy-paste remediation hints. Intended to be the first command a
new user runs after installing gk.

Exit codes:
  0  no FAIL rows
  1  one or more FAIL rows`,
		RunE: runDoctor,
	}
	cmd.Flags().Bool("json", false, "emit machine-readable JSON")
	rootCmd.AddCommand(cmd)
}

func runDoctor(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")

	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()

	checks := []doctorCheck{
		checkGitVersion(ctx, runner),
		checkPager(),
		checkFzf(),
		checkEditor(),
		checkConfig(cmd),
		checkHook(ctx, runner, "commit-msg", "gk lint-commit"),
		checkHook(ctx, runner, "pre-push", "gk push"),
		checkBackupRefs(ctx, runner),
		checkGitleaks(),
	}

	w := cmd.OutOrStdout()
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(checks); err != nil {
			return err
		}
	} else {
		writeDoctorTable(w, checks)
	}

	for _, c := range checks {
		if c.Status == statusFail {
			return fmt.Errorf("doctor: %d FAIL check(s)", countStatus(checks, statusFail))
		}
	}
	return nil
}

// ---------- individual checks ----------

func checkGitVersion(ctx context.Context, r git.Runner) doctorCheck {
	out, _, err := r.Run(ctx, "--version")
	if err != nil {
		return doctorCheck{
			Name: "git version", Status: statusFail,
			Detail: "git not found on PATH",
			Fix:    "install git >= 2.38 (https://git-scm.com)",
		}
	}
	major, minor := parseGitVersion(string(out))
	detail := fmt.Sprintf("%d.%d", major, minor)

	if major < minGitMajor || (major == minGitMajor && minor < minGitMinor) {
		return doctorCheck{
			Name: "git version", Status: statusFail,
			Detail: detail + " (require >= 2.38)",
			Fix:    "upgrade git — gk precheck/preflight need `git merge-tree --write-tree`",
		}
	}
	if major == minGitMajor && minor < preferredGitMinor {
		return doctorCheck{
			Name: "git version", Status: statusWarn,
			Detail: detail + " (>= 2.40 preferred)",
			Fix:    "gk precheck will fall back to marker parsing on this version",
		}
	}
	return doctorCheck{Name: "git version", Status: statusPass, Detail: detail}
}

func checkPager() doctorCheck {
	for _, name := range []string{"delta", "bat", "less"} {
		if path, err := exec.LookPath(name); err == nil {
			return doctorCheck{
				Name: "pager", Status: statusPass,
				Detail: fmt.Sprintf("%s (%s)", name, path),
			}
		}
	}
	return doctorCheck{
		Name: "pager", Status: statusWarn,
		Detail: "none found",
		Fix:    "brew install git-delta  # or: brew install bat",
	}
}

func checkFzf() doctorCheck {
	if path, err := exec.LookPath("fzf"); err == nil {
		return doctorCheck{Name: "fzf", Status: statusPass, Detail: path}
	}
	return doctorCheck{
		Name: "fzf", Status: statusWarn,
		Detail: "not installed — gk undo/restore will use a numeric picker",
		Fix:    "brew install fzf",
	}
}

func checkEditor() doctorCheck {
	for _, env := range []string{"GIT_EDITOR", "VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			bin := strings.Fields(v)[0]
			if path, err := exec.LookPath(bin); err == nil {
				return doctorCheck{
					Name: "editor", Status: statusPass,
					Detail: fmt.Sprintf("%s (%s) via $%s", v, path, env),
				}
			}
			return doctorCheck{
				Name: "editor", Status: statusWarn,
				Detail: fmt.Sprintf("$%s=%s but %q not on PATH", env, v, bin),
				Fix:    "set $EDITOR to an installed binary (e.g., `export EDITOR=nvim`)",
			}
		}
	}
	return doctorCheck{
		Name: "editor", Status: statusWarn,
		Detail: "no $EDITOR/$VISUAL/$GIT_EDITOR set — git will use vi",
		Fix:    "export EDITOR=nvim  # or your preferred editor",
	}
}

func checkConfig(cmd *cobra.Command) doctorCheck {
	// config.Load parses all layers (XDG, repo-local, git config, env, flags)
	// and returns a fully resolved *Config. A non-nil error means at least one
	// layer failed to parse.
	_, err := config.Load(cmd.Flags())
	if err != nil {
		return doctorCheck{
			Name: "config", Status: statusFail,
			Detail: err.Error(),
			Fix:    "inspect the failing file; run `gk config show` to see which layers loaded",
		}
	}
	// Report which repo-local .gk.yaml (if any) was picked up.
	repo := RepoFlag()
	if repo == "" {
		repo = "."
	}
	repoLocal := filepath.Join(repo, ".gk.yaml")
	if _, statErr := os.Stat(repoLocal); statErr == nil {
		return doctorCheck{Name: "config", Status: statusPass, Detail: repoLocal + " ok"}
	}
	return doctorCheck{Name: "config", Status: statusPass, Detail: "defaults only (no .gk.yaml)"}
}

// checkHook inspects .git/hooks/<name> to see whether it invokes gk.
func checkHook(ctx context.Context, r git.Runner, name, suggested string) doctorCheck {
	gitDirOut, _, err := r.Run(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return doctorCheck{
			Name: "hooks: " + name, Status: statusWarn,
			Detail: "not in a git repo",
		}
	}
	gitDir := strings.TrimSpace(string(gitDirOut))
	if !filepath.IsAbs(gitDir) {
		if repo := RepoFlag(); repo != "" {
			gitDir = filepath.Join(repo, gitDir)
		}
	}
	hookPath := filepath.Join(gitDir, "hooks", name)

	info, statErr := os.Stat(hookPath)
	if statErr != nil {
		return doctorCheck{
			Name: "hooks: " + name, Status: statusWarn,
			Detail: "not installed",
			Fix:    fmt.Sprintf("gk hooks install --%s   # shim runs `%s`", name, suggested),
		}
	}
	if info.Mode()&0o111 == 0 {
		return doctorCheck{
			Name: "hooks: " + name, Status: statusFail,
			Detail: hookPath + " exists but is not executable",
			Fix:    fmt.Sprintf("chmod +x %s", hookPath),
		}
	}
	body, readErr := os.ReadFile(hookPath)
	if readErr == nil && strings.Contains(string(body), "gk ") {
		return doctorCheck{
			Name: "hooks: " + name, Status: statusPass,
			Detail: "installed (invokes gk)",
		}
	}
	return doctorCheck{
		Name: "hooks: " + name, Status: statusPass,
		Detail: "installed (custom)",
	}
}

// ---------- parsing + rendering ----------

// parseGitVersion extracts MAJOR.MINOR from strings like "git version 2.54.0".
// Returns 0, 0 on parse failure.
var gitVerRE = regexp.MustCompile(`(?m)version\s+(\d+)\.(\d+)`)

func parseGitVersion(s string) (int, int) {
	m := gitVerRE.FindStringSubmatch(s)
	if len(m) != 3 {
		return 0, 0
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	return maj, min
}

func countStatus(checks []doctorCheck, s doctorStatus) int {
	n := 0
	for _, c := range checks {
		if c.Status == s {
			n++
		}
	}
	return n
}

// writeDoctorTable renders an aligned table to w.
func writeDoctorTable(w io.Writer, checks []doctorCheck) {
	const nameCol = 22
	for _, c := range checks {
		marker := statusMarker(c.Status)
		fmt.Fprintf(w, "%s  %-*s  %s\n", marker, nameCol, c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(w, "     %-*s  fix: %s\n", nameCol, "", c.Fix)
		}
	}
	pass := countStatus(checks, statusPass)
	warn := countStatus(checks, statusWarn)
	fail := countStatus(checks, statusFail)
	fmt.Fprintf(w, "\n%d PASS · %d WARN · %d FAIL\n", pass, warn, fail)
}

func statusMarker(s doctorStatus) string {
	switch s {
	case statusPass:
		return "✓ PASS"
	case statusWarn:
		return "⚠ WARN"
	case statusFail:
		return "✗ FAIL"
	}
	return "? ????"
}

// checkGitleaks detects the `gitleaks` binary and its version. gitleaks is
// the industry-standard secret scanner; when present, gk guard (v0.9+) uses
// it as the default `secret_patterns` rule evaluator. When missing, gk
// falls back to the built-in keyword scanner — still functional but less
// thorough. This is a WARN, not FAIL, so doctor does not block CI when
// gitleaks happens to be absent.
func checkGitleaks() doctorCheck {
	path, err := exec.LookPath("gitleaks")
	if err != nil {
		return doctorCheck{
			Name: "gitleaks", Status: statusWarn,
			Detail: "not installed — gk guard will fall back to the built-in keyword scanner",
			Fix:    "brew install gitleaks  # or: go install github.com/gitleaks/gitleaks/v8@latest",
		}
	}
	// Best-effort version probe. Absent version is non-fatal — the presence
	// of the binary is the main signal.
	cmd := exec.Command(path, "version")
	out, _ := cmd.Output()
	version := strings.TrimSpace(string(out))
	if version == "" {
		version = path
	} else {
		version = version + " (" + path + ")"
	}
	return doctorCheck{Name: "gitleaks", Status: statusPass, Detail: version}
}

// checkBackupRefs summarizes the gk-managed backup refs (refs/gk/*-backup/).
// Always PASS — it's diagnostic context, never a failure condition. Empty
// repo shows "0 refs"; populated repos show count + age of oldest/newest by
// kind so users can spot stale or orphaned backups at a glance.
func checkBackupRefs(ctx context.Context, r git.Runner) doctorCheck {
	refs, err := gitsafe.ListBackups(ctx, r)
	if err != nil {
		// Common outside-a-repo case surfaces the raw git message — trim to
		// a single line and hide the full argv so the doctor table stays
		// scannable.
		msg := err.Error()
		if strings.Contains(msg, "not a git repository") {
			return doctorCheck{
				Name: "gk backup refs", Status: statusWarn,
				Detail: "not in a git repo",
			}
		}
		// First line only, stripped.
		if idx := strings.IndexByte(msg, '\n'); idx >= 0 {
			msg = msg[:idx]
		}
		return doctorCheck{
			Name: "gk backup refs", Status: statusWarn,
			Detail: "could not enumerate: " + strings.TrimSpace(msg),
		}
	}
	if len(refs) == 0 {
		return doctorCheck{
			Name:   "gk backup refs",
			Status: statusPass,
			Detail: "0 refs (no gk undo/wipe/timemachine backups yet)",
		}
	}

	// Tally by kind + track oldest/newest.
	byKind := map[string]int{}
	var oldest, newest time.Time
	for _, b := range refs {
		byKind[b.Kind]++
		if b.When.IsZero() {
			continue
		}
		if oldest.IsZero() || b.When.Before(oldest) {
			oldest = b.When
		}
		if b.When.After(newest) {
			newest = b.When
		}
	}

	parts := make([]string, 0, len(byKind)+2)
	parts = append(parts, fmt.Sprintf("%d refs", len(refs)))
	for kind, n := range byKind {
		parts = append(parts, fmt.Sprintf("%s=%d", kind, n))
	}
	if !newest.IsZero() {
		parts = append(parts, "newest "+humanSince(time.Since(newest)))
	}
	if !oldest.IsZero() && !oldest.Equal(newest) {
		parts = append(parts, "oldest "+humanSince(time.Since(oldest)))
	}

	return doctorCheck{
		Name:   "gk backup refs",
		Status: statusPass,
		Detail: strings.Join(parts, " · "),
	}
}
