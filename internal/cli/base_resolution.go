package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// BaseSource identifies the layer that supplied the resolved base branch.
// It is rendered verbatim as a faint suffix on the "from <base>" line and
// in the --explain-base block, so the constants double as user-facing
// labels.
type BaseSource string

const (
	BaseSourceUnresolved BaseSource = ""
	BaseSourceConfig     BaseSource = "config"
	BaseSourceConfigEnv  BaseSource = "GK_BASE_BRANCH"
	BaseSourceConfigGit  BaseSource = "git config"
	BaseSourceOriginHEAD BaseSource = "origin/HEAD"
	BaseSourceFallback   BaseSource = "fallback"
)

// DisplayLabel returns the human-friendly category shown on the
// "from <base> <label>" line. Internal source constants stay verbose
// for diagnostics, but the headline status output collapses them into
// three buckets: default / configured / guessed.
func (s BaseSource) DisplayLabel() string {
	switch s {
	case BaseSourceOriginHEAD:
		return "default"
	case BaseSourceConfig, BaseSourceConfigEnv, BaseSourceConfigGit:
		return "configured"
	case BaseSourceFallback:
		return "guessed"
	default:
		return ""
	}
}

// DetailLabel adds the technical layer name to the human-friendly
// label, e.g. "configured (git config)". Used by --explain-base and -v
// where the underlying source matters for debugging.
func (s BaseSource) DetailLabel() string {
	switch s {
	case BaseSourceOriginHEAD:
		return "default (origin/HEAD)"
	case BaseSourceConfig:
		return "configured (.gk.yaml)"
	case BaseSourceConfigEnv:
		return "configured (GK_BASE_BRANCH)"
	case BaseSourceConfigGit:
		return "configured (git config)"
	case BaseSourceFallback:
		return "guessed (no remote)"
	default:
		return ""
	}
}

// BaseResolution captures the full picture of how the base branch was
// chosen. Renderers consume the fields they need; Mismatch() and
// OriginHEADUnset() are the canonical signals that the user's config
// disagrees with the remote's view of "default branch".
type BaseResolution struct {
	Resolved string
	Source   BaseSource

	ConfigEnv    string
	ConfigGit    string
	ConfigMerged string
	OriginHEAD   string
	OriginLive   string
	FallbackUsed string
	Remote       string
}

// Mismatch reports whether an explicitly configured base disagrees with
// the cached origin/HEAD — the canonical "did the remote default change
// out from under me?" signal.
func (r BaseResolution) Mismatch() bool {
	cfg := strings.TrimSpace(r.ConfigMerged)
	if cfg == "" || r.OriginHEAD == "" {
		return false
	}
	return cfg != r.OriginHEAD
}

// OriginHEADUnset reports whether the remote has no cached HEAD
// symbolic-ref. The remediation is the same `git remote set-head -a` as
// for Mismatch, but the rendered hint differs.
func (r BaseResolution) OriginHEADUnset() bool {
	return r.OriginHEAD == ""
}

// resolveBaseForStatus picks the branch to compare the current branch
// against, exposing every input layer so renderers can show provenance
// (Phase 1 label) and surface mismatches (Phase 2 footer + explain).
//
// Resolution order matches the legacy resolver: explicit config wins,
// then the cached origin/HEAD symbolic ref, then a local-branch fallback
// (main → master → develop).
func resolveBaseForStatus(
	ctx context.Context,
	runner *git.ExecRunner,
	client *git.Client,
	cfg *config.Config,
) BaseResolution {
	res := BaseResolution{Remote: "origin"}
	if cfg != nil && cfg.Remote != "" {
		res.Remote = cfg.Remote
	}

	res.ConfigEnv = strings.TrimSpace(os.Getenv("GK_BASE_BRANCH"))
	if cfg != nil {
		res.ConfigMerged = strings.TrimSpace(cfg.BaseBranch)
	}
	if out, _, err := runner.Run(ctx, "config", "--get", "gk.base-branch"); err == nil {
		res.ConfigGit = strings.TrimSpace(string(out))
	}
	if name, err := client.DefaultBranch(ctx, res.Remote); err == nil {
		res.OriginHEAD = strings.TrimSpace(name)
	}

	if res.ConfigMerged != "" {
		res.Resolved = res.ConfigMerged
		res.Source = pickConfigSource(res)
		return res
	}
	if res.OriginHEAD != "" {
		res.Resolved = res.OriginHEAD
		res.Source = BaseSourceOriginHEAD
		return res
	}
	for _, cand := range []string{"main", "master", "develop"} {
		if localBranchExists(ctx, runner, cand) {
			res.Resolved = cand
			res.FallbackUsed = cand
			res.Source = BaseSourceFallback
			return res
		}
	}
	return res
}

// pickConfigSource attributes the merged config value to the most
// specific layer we can detect (env > git config > generic). YAML and
// flag layers are not subdivided here — they fold into "config".
func pickConfigSource(r BaseResolution) BaseSource {
	val := r.ConfigMerged
	if val == "" {
		return BaseSourceUnresolved
	}
	if r.ConfigEnv != "" && r.ConfigEnv == val {
		return BaseSourceConfigEnv
	}
	if r.ConfigGit != "" && r.ConfigGit == val {
		return BaseSourceConfigGit
	}
	return BaseSourceConfig
}

// renderBaseMismatchFooter returns a one-line warning when the configured
// base disagrees with the cached origin/HEAD. Returns "" when there is
// nothing actionable to surface (no config, no remote HEAD diff, etc).
//
// The hint deliberately points at `git remote set-head -a` rather than
// `git config gk.base-branch` because the most common cause is the
// remote's default branch having moved while the local cache is stale.
func renderBaseMismatchFooter(res BaseResolution) string {
	if !res.Mismatch() {
		return ""
	}
	yellow := color.New(color.FgYellow).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()
	label := res.Source.DisplayLabel()
	if label == "" {
		label = "configured"
	}
	header := fmt.Sprintf("%s base %s (%s) %s %s default %s",
		yellow("⚠"),
		color.CyanString("'"+res.ConfigMerged+"'"),
		label,
		yellow("≠"),
		res.Remote,
		color.CyanString("'"+res.OriginHEAD+"'"),
	)
	hint := faint(fmt.Sprintf("  run `git remote set-head %s -a` — origin default may have changed", res.Remote))
	return header + "\n" + hint
}

// renderBaseVerboseLine is the compact one-line `[base] ...` debug
// output emitted by `gk status -v`. Format follows the existing verbose
// summary's key=value style; rendered faint so it doesn't compete with
// the headline status block.
func renderBaseVerboseLine(res BaseResolution) string {
	faint := color.New(color.Faint).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
	parts := []string{
		"resolved=" + nonEmpty(res.Resolved),
		"source=" + nonEmpty(string(res.Source)),
		"origin/HEAD=" + nonEmpty(res.OriginHEAD),
		"cfg=" + nonEmpty(res.ConfigMerged),
	}
	if res.FallbackUsed != "" {
		parts = append(parts, "fallback="+res.FallbackUsed)
	}
	tail := ""
	switch {
	case res.Mismatch():
		tail = "  " + yellow("⚠ mismatch")
	case res.OriginHEADUnset():
		tail = "  " + yellow("⚠ origin/HEAD unset")
	}
	return faint("[base] "+strings.Join(parts, "  ")) + tail
}

// renderExplainBase produces the multi-line `--explain-base` diagnostic
// block. When OriginLive is populated (live origin lookup performed via
// --fetch-default), it is shown as a separate row so users can see drift
// between the cached and live remote HEAD.
func renderExplainBase(res BaseResolution) string {
	bold := color.New(color.Bold).SprintFunc()
	faint := color.New(color.Faint).SprintFunc()
	yellow := color.New(color.FgYellow).SprintFunc()
	cyan := color.New(color.FgCyan).SprintFunc()

	var b strings.Builder
	b.WriteString(bold("base resolution:") + "\n")

	row := func(label, val, note string, picked bool) {
		marker := "  "
		if picked {
			marker = bold("✓ ")
		}
		valStr := faint("(unset)")
		if val != "" {
			valStr = cyan(val)
		}
		line := fmt.Sprintf("%s%-32s %s", marker, label+":", valStr)
		if note != "" {
			line += "  " + faint(note)
		}
		b.WriteString(line + "\n")
	}

	pickedSource := res.Source
	row("GK_BASE_BRANCH", res.ConfigEnv, "", pickedSource == BaseSourceConfigEnv)
	row("git config gk.base-branch", res.ConfigGit, "", pickedSource == BaseSourceConfigGit)
	row("config (merged)", res.ConfigMerged, "from .gk.yaml + env + git config + flags", pickedSource == BaseSourceConfig)

	originNote := ""
	if res.Mismatch() {
		originNote = yellow("differs from config")
	}
	row(res.Remote+"/HEAD (cached)", res.OriginHEAD, originNote, pickedSource == BaseSourceOriginHEAD)

	if res.OriginLive != "" {
		liveNote := ""
		if res.OriginLive != res.OriginHEAD {
			liveNote = yellow("differs from cached")
		}
		row(res.Remote+"/HEAD (live)", res.OriginLive, liveNote, false)
	} else {
		row(res.Remote+"/HEAD (live)", "", "skipped — pass --fetch-default", false)
	}

	if res.FallbackUsed != "" {
		row("local fallback", res.FallbackUsed, "main → master → develop", pickedSource == BaseSourceFallback)
	} else {
		row("local fallback", "", "not consulted", false)
	}

	b.WriteString("\n")
	if res.Resolved == "" {
		b.WriteString("  → " + yellow("unresolved") + "\n")
	} else {
		fmt.Fprintf(&b, "  → resolved: %s  %s\n",
			cyan(res.Resolved),
			faint("(source: "+string(res.Source)+")"))
	}

	if hint := explainBaseActionHint(res); hint != "" {
		b.WriteString("\n" + bold("action hint:") + "\n")
		b.WriteString(hint)
	}
	return strings.TrimRight(b.String(), "\n")
}

// explainBaseActionHint suggests a remedy based on the discrepancies
// the resolution captured. Returns "" when nothing is actionable.
func explainBaseActionHint(res BaseResolution) string {
	faint := color.New(color.Faint).SprintFunc()
	var lines []string
	if res.Mismatch() {
		lines = append(lines,
			fmt.Sprintf("  if origin default changed → %s",
				faint("git remote set-head "+res.Remote+" -a")))
		lines = append(lines,
			"  if config is intentional → leave as-is, this is just an FYI")
	}
	if res.OriginHEADUnset() {
		lines = append(lines,
			fmt.Sprintf("  origin/HEAD is unset → %s",
				faint("git remote set-head "+res.Remote+" -a")))
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// fetchOriginLiveDefault performs a one-shot `git ls-remote --symref`
// against the configured remote to discover the live default branch
// without modifying any local refs. Bounded by the caller's ctx.
//
// Returns "" when the remote has no symbolic HEAD (uncommon) or when
// the call fails (offline, auth, etc) — callers treat empty as "skip".
func fetchOriginLiveDefault(ctx context.Context, runner git.Runner, remote string) string {
	if remote == "" {
		remote = "origin"
	}
	out, _, err := runner.Run(ctx, "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ref:") {
			continue
		}
		// Format: "ref: refs/heads/<branch>\tHEAD"
		fields := strings.Fields(strings.TrimPrefix(line, "ref:"))
		if len(fields) == 0 {
			continue
		}
		ref := fields[0]
		return strings.TrimPrefix(ref, "refs/heads/")
	}
	return ""
}

func nonEmpty(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}
