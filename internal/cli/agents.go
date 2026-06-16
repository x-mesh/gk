package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

// gk agents manages the gk usage contract inside agent instruction files
// (CLAUDE.md for Claude, AGENTS.md for Codex and friends). The contract text
// lives in this binary so it always matches the installed gk's actual
// surface: when a release changes a JSON schema or adds a command, the same
// release updates the paragraph, and `gk agents check` (or `gk agents
// install`) brings the files back in sync.
//
// The block is fenced with versioned markers and everything outside it is
// never touched — the file stays the user's.

const agentsContractVersion = 16

var (
	agentsBeginMarker = fmt.Sprintf("<!-- gk:agents:begin v%d — managed by `gk agents install`; edit outside this block -->", agentsContractVersion)
	agentsEndMarker   = "<!-- gk:agents:end -->"
	agentsBlockRE     = regexp.MustCompile(`(?s)<!-- gk:agents:begin[^>]*-->.*?<!-- gk:agents:end -->`)
	agentsVersionRE   = regexp.MustCompile(`<!-- gk:agents:begin v(\d+)`)
)

const agentsContractBody = `## Git workflow (git-kit)

This repository is driven with git-kit, an agent-native git CLI. Always invoke it as ` + "`git-kit`" + ` — the short name ` + "`gk`" + ` is the same binary but is commonly shadowed by shell aliases (oh-my-zsh maps ` + "`gk`" + ` to gitk), so it is not reliable from an agent shell. Set ` + "`export GK_AGENT=1`" + ` once: every command then emits a uniform envelope — ` + "`{state, ok, result}`" + ` on success, ` + "`{state:\"error\", ok:false, error:{code, message, remedies:[{command,safety}]}}`" + ` on failure — so you branch on fields, never parse prose. ` + "`state`" + ` is the dispatch key: ` + "`ok`" + ` (done) · ` + "`paused`" + ` (a conflict/operation is mid-flight — resume or abort it) · ` + "`blocked`" + ` (a precondition like a diverged base failed — run the remedy) · ` + "`error`" + ` (the command failed); ` + "`ok`" + ` is kept as a derived alias (` + "`ok == state==\"ok\"`" + `). Prefer git-kit over raw git:

- **Orient first**: ` + "`git-kit context`" + ` — one call returns branch, upstream, ahead/behind, dirty counts, any in-progress rebase/merge (with resume/abort commands), base-branch drift, worktrees, and ` + "`next_actions`" + `. Add ` + "`--include=diff,log,precheck,remotes,release`" + ` (or ` + "`--include=all`" + `) to fuse the uncommitted-change digest (untracked included), the last 5 commits, the next-pull conflict forecast, per-remote drift, and the commits since the latest tag (what is still unreleased) into the same document — one call instead of six; a section that cannot be collected degrades to a ` + "`notes`" + ` entry, never an error. Never chain raw git status/branch/log/diff probes across separate calls — one context call answers them all.
- **Wrap up**: ` + "`git-kit land`" + ` — commit (AI-grouped), pull --with-base, push as one transaction with per-step results; on failure the result names ` + "`failed_step`" + ` and the resume command. ` + "`--cleanup`" + ` also reclaims fully-merged branches and their worktrees.
- **Local wrap-up (no network)**: ` + "`git-kit promote`" + ` — commit, then forward-merge the current branch into its parent/base (gk-parent metadata, trunk fallback); ` + "`git-kit promote <branch>`" + ` walks the parent chain hop by hop. Nothing is pushed without ` + "`--push`" + ` — use it when integration is local and land would push too early. Same per-step result contract as land.
- **Batch any sequence**: ` + "`git-kit batch --plan -`" + ` — run several git-kit commands as one transaction from a JSON plan on stdin: ` + "`{\"steps\":[{\"args\":[\"pull\",\"--with-base\"]},{\"args\":[\"push\"]}]}`" + `, optional per-step ` + "`on_failure: \"abort\"|\"continue\"`" + `. The result reports per-step outcomes plus ` + "`failed_step`" + `/` + "`resume`" + `; a gating failure skip-marks the remaining steps. Draft a plan with ` + "`--plan-template`" + `, preview with ` + "`--dry-run`" + `. N calls → 1.
- **Sync**: ` + "`git-kit pull`" + ` (add ` + "`--with-base`" + ` to also fast-forward the local base branch, FF-only). On conflict the result lists the files plus the exact resume/abort commands. ` + "`--from <remote>[/<branch>]`" + ` integrates from a secondary remote (mirror, org fork) that the upstream chain never fetches — tracking config stays untouched.
- **Forecast before integrating**: ` + "`git-kit precheck [target]`" + ` — read-only merge-tree simulation (no target = the next pull). Clean → integrate; conflicts listed → pick a strategy first instead of try→abort.
- **Inspect changes**: ` + "`git-kit diff --digest`" + ` — per-file change kind, ±lines, hunk count, and the changed symbols, without the patch body. Same ref/path arguments as plain diff (` + "`--staged`" + `, ` + "`HEAD~3`" + `, ` + "`main..feature`" + `). Read the full patch only for the files the digest makes interesting.
- **Isolated worktree task**: ` + "`git-kit worktree run <branch> -- <command>`" + ` — create (or reuse) a worktree for ` + "`<branch>`" + `, run the command with the worktree as its cwd, and exit with the command's own exit code: the single-shot CLI form of a parallel, isolated task (a new branch is cut off HEAD, gk-parent recorded, ` + "`worktree.init`" + ` applied). ` + "`--cleanup`" + ` reclaims the worktree when the command succeeds (and deletes the branch if this call created it); a failing command is left in place for inspection. ` + "`--from <ref>`" + ` bases a new branch elsewhere, ` + "`--init`" + `/` + "`--no-init`" + ` force or skip the gitignored-state bootstrap. To find which worktree holds unfinished work without a per-path probe, ` + "`git-kit worktree list --json`" + ` reports each worktree's branch, ahead/behind, parent, lock state, and dirty counts in one call.
- **Commit / push**: ` + "`git-kit commit -f`" + ` groups changes into conventional commits; ` + "`git-kit push`" + ` scans for secrets before pushing.
- **Curated multi-commit**: when YOU decide the grouping instead of the AI, ` + "`git-kit commit --plan-template`" + ` emits the dirty files as a JSON draft; split it into ` + "`{\"commits\":[{\"message\":\"feat(x): ...\",\"files\":[...]}]}`" + ` and run ` + "`git-kit commit --plan -`" + ` — N curated commits in one deterministic call (no AI, secret scan included, backup ref behind ` + "`gk commit --abort`" + `). Duplicate/unknown files and malformed messages are rejected up front; files the plan does not cover stay dirty. Use this instead of chaining raw ` + "`git add`" + ` + ` + "`git commit`" + ` pairs.
- **History editing**: never open ` + "`git rebase -i`" + ` (the editor session is unusable for you). Instead: ` + "`git-kit rebase --plan-template`" + ` emits the commit range as JSON (action/commit/subject/pushed), you decide each commit's fate (pick/squash/fixup/reword/drop), then ` + "`git-kit rebase --plan -`" + ` validates it (every commit addressed, pushed commits guarded) and drives git's own rebase with a backup ref.
- **Conflicts**: ` + "`git-kit resolve --ai`" + ` (or ` + "`--strategy ours|theirs`" + `) resolves AND finishes the operation — it runs the continue step itself, re-resolves later picks that conflict with the same strategy, auto-skips picks the resolution emptied, and also handles delete/modify and markerless conflicts from the index stages (AI decides keep/delete/merge with a rationale); one call takes a paused rebase to done (` + "`--no-continue`" + ` to stop after resolving, ` + "`git-kit abort`" + ` to give up). ` + "`git-kit continue`" + ` remains for manually edited resolutions. A paused state is a result — ` + "`state:\"paused\"`" + `, ` + "`ok:false`" + `, exit 3 — not an error; resume or abort it rather than running an error remedy.
- **Release**: read the plan first — ` + "`git-kit ship --dry-run --json`" + ` emits the full release plan (inferred version, CHANGELOG draft, the preflight/watch/verify step lists, and ` + "`merge_to_base`" + `). When it looks right, ` + "`git-kit ship -y`" + ` runs the whole pipeline — preflight (lint/test) → version/CHANGELOG → tag → push → CI watch → artifact verify — and works under GK_AGENT: human progress streams to stderr while stdout stays a clean result envelope ` + "`{tag, branch, base, merged_to_base, pushed, shipped_on}`" + ` (no ` + "`env -u GK_AGENT`" + ` dance needed). Preflight (lint/test) gates the release, so validate up front with ` + "`git-kit ship --preflight`" + ` (runs the configured checks on the working tree — dirty is fine — and never tags or pushes; ` + "`{result, steps, failed_step}`" + ` under GK_AGENT) and get them green before ` + "`-y`" + `; ` + "`git-kit commit`" + ` also warns on gofmt before it reaches preflight. From a non-base branch (e.g. develop) ship fast-forwards the base (main) and tags there; if history diverged it stops with ` + "`state:\"blocked\"`" + ` and the remedy ` + "`git-kit sync`" + ` (rebase the branch onto its base so base can fast-forward), then ship again. ` + "`--wait=false`" + ` (or ` + "`ship.wait`" + `) skips the CI watch; ` + "`ship.auto_confirm`" + ` makes ` + "`-y`" + ` the default. What's still unreleased: ` + "`git-kit context --include=release`" + `.
- **Stuck repo** (stale index.lock, orphan merge, prunable worktrees, asymmetric push-only remotes whose merged work never comes down): ` + "`git-kit doctor --fix`" + `.
- On any failure run the first entry of ` + "`error.remedies`" + ` (check ` + "`safety`" + ` first) instead of retrying variations.`

// agentsContractBlock is the full fenced block as written to files.
func agentsContractBlock() string {
	return agentsBeginMarker + "\n" + agentsContractBody + "\n" + agentsEndMarker
}

var agentsTargetNames = []string{"CLAUDE.md", "AGENTS.md"}

// agentsFile is one instruction-file location plus the scope it belongs to,
// so install/check output can group and label by where the file lives.
type agentsFile struct {
	path  string
	scope string // "local" (repo root) · "global" (~/.claude, ~/.codex) · "custom" (--file)
}

func init() {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Manage the gk usage contract in agent instruction files (CLAUDE.md / AGENTS.md)",
		Long: `Keeps a versioned "how to use gk" paragraph in agent instruction files,
so AI agents (Claude, Codex, ...) route git work through gk's one-call
commands instead of probing with raw git.

The paragraph is embedded in the gk binary — it always describes the
installed gk's real surface — and is fenced with markers; nothing outside
the block is ever modified.

Two scopes: the repo root (CLAUDE.md / AGENTS.md, the default) and the
per-agent global files (~/.claude/CLAUDE.md and ~/.codex/AGENTS.md, via
--global) that every project inherits.

  gk agents print              print the contract block (paste it anywhere)
  gk agents install            insert/refresh the block at the repo root
  gk agents install --global   insert/refresh ~/.claude/CLAUDE.md + ~/.codex/AGENTS.md
  gk agents check              report block status + version for local AND global`,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "print",
		Short: "Print the contract block to stdout",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), agentsContractBlock())
			return nil
		},
	})
	install := &cobra.Command{
		Use:   "install",
		Short: "Insert or refresh the contract block in CLAUDE.md and AGENTS.md",
		RunE:  runAgentsInstall,
	}
	install.Flags().StringSlice("file", nil, "restrict to specific files (default: CLAUDE.md and AGENTS.md at the repo root)")
	install.Flags().Bool("global", false, "install into the per-agent global files (~/.claude/CLAUDE.md, ~/.codex/AGENTS.md) instead of the repo root")
	cmd.AddCommand(install)
	check := &cobra.Command{
		Use:   "check",
		Short: "Report contract-block status and version (local + global)",
		RunE:  runAgentsCheck,
	}
	check.Flags().StringSlice("file", nil, "restrict to specific files")
	check.Flags().Bool("global", false, "check only the global files (default reports local + global)")
	cmd.AddCommand(check)
	rootCmd.AddCommand(cmd)
}

// agentsGlobalFiles returns the per-agent global instruction files: Claude's
// CLAUDE.md under $CLAUDE_CONFIG_DIR (default ~/.claude) and Codex's AGENTS.md
// under $CODEX_HOME (default ~/.codex). Installing the contract here routes
// git work through gk in every project, not just the current repo.
func agentsGlobalFiles() ([]agentsFile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("gk agents: cannot resolve home directory: %w", err)
	}
	claudeDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeDir == "" {
		claudeDir = filepath.Join(home, ".claude")
	}
	codexDir := os.Getenv("CODEX_HOME")
	if codexDir == "" {
		codexDir = filepath.Join(home, ".codex")
	}
	return []agentsFile{
		{path: filepath.Join(claudeDir, "CLAUDE.md"), scope: "global"},
		{path: filepath.Join(codexDir, "AGENTS.md"), scope: "global"},
	}, nil
}

// agentsLocalFiles returns the repo-root instruction files. ok=false (not an
// error) when we're not inside a git repository, so check can fall back to
// global-only and install can print an actionable hint.
func agentsLocalFiles(cmd *cobra.Command) ([]agentsFile, bool) {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	out, _, err := runner.Run(cmd.Context(), "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, false
	}
	root := strings.TrimSpace(string(out))
	files := make([]agentsFile, 0, len(agentsTargetNames))
	for _, name := range agentsTargetNames {
		files = append(files, agentsFile{path: filepath.Join(root, name), scope: "local"})
	}
	return files, true
}

// agentsCustomFiles resolves explicit --file paths (absolute as-is, otherwise
// relative to the repo root). Shared by install and check.
func agentsCustomFiles(cmd *cobra.Command, files []string) ([]agentsFile, error) {
	var root string
	out := make([]agentsFile, 0, len(files))
	for _, f := range files {
		if filepath.IsAbs(f) {
			out = append(out, agentsFile{path: f, scope: "custom"})
			continue
		}
		if root == "" {
			runner := &git.ExecRunner{Dir: RepoFlag()}
			top, _, err := runner.Run(cmd.Context(), "rev-parse", "--show-toplevel")
			if err != nil {
				return nil, fmt.Errorf("gk agents: --file %q is relative but not inside a git repository (use an absolute path)", f)
			}
			root = strings.TrimSpace(string(top))
		}
		out = append(out, agentsFile{path: filepath.Join(root, f), scope: "custom"})
	}
	return out, nil
}

// agentsInstallTargets resolves where install writes: --file wins, then
// --global, else the repo root (an error with a --global hint when outside a
// repo).
func agentsInstallTargets(cmd *cobra.Command) ([]agentsFile, error) {
	if files, _ := cmd.Flags().GetStringSlice("file"); len(files) > 0 {
		return agentsCustomFiles(cmd, files)
	}
	if global, _ := cmd.Flags().GetBool("global"); global {
		return agentsGlobalFiles()
	}
	local, ok := agentsLocalFiles(cmd)
	if !ok {
		return nil, fmt.Errorf("gk agents: not inside a git repository — use --global to install into ~/.claude and ~/.codex, or --file <path>")
	}
	return local, nil
}

// agentsCheckTargets resolves what check reports on: --file wins, then
// --global (global only), else the full picture — local files (when inside a
// repo) plus the global files.
func agentsCheckTargets(cmd *cobra.Command) ([]agentsFile, error) {
	if files, _ := cmd.Flags().GetStringSlice("file"); len(files) > 0 {
		return agentsCustomFiles(cmd, files)
	}
	if global, _ := cmd.Flags().GetBool("global"); global {
		return agentsGlobalFiles()
	}
	var out []agentsFile
	if local, ok := agentsLocalFiles(cmd); ok {
		out = append(out, local...)
	}
	g, err := agentsGlobalFiles()
	if err != nil {
		return nil, err
	}
	return append(out, g...), nil
}

func runAgentsInstall(cmd *cobra.Command, args []string) error {
	targets, err := agentsInstallTargets(cmd)
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	for _, t := range targets {
		state, werr := installAgentsBlock(t.path)
		if werr != nil {
			return werr
		}
		fmt.Fprintln(w, successLine(state, tildePath(t.path)))
	}
	return nil
}

// installAgentsBlock writes the current contract block into path, replacing
// an existing fenced block or appending one (creating the parent directory
// and file when absent). Returns the verb describing what happened:
// created / updated / unchanged.
func installAgentsBlock(path string) (string, error) {
	block := agentsContractBlock()
	b, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		if mkerr := os.MkdirAll(filepath.Dir(path), 0o755); mkerr != nil {
			return "", fmt.Errorf("gk agents: mkdir %s: %w", filepath.Dir(path), mkerr)
		}
		content := block + "\n"
		if werr := os.WriteFile(path, []byte(content), 0o644); werr != nil {
			return "", fmt.Errorf("gk agents: write %s: %w", path, werr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("gk agents: read %s: %w", path, err)
	}

	before := string(b)
	var after string
	if agentsBlockRE.MatchString(before) {
		after = agentsBlockRE.ReplaceAllString(before, block)
	} else {
		after = strings.TrimRight(before, "\n") + "\n\n" + block + "\n"
	}
	if after == before {
		return "unchanged", nil
	}
	if werr := os.WriteFile(path, []byte(after), 0o644); werr != nil {
		return "", fmt.Errorf("gk agents: write %s: %w", path, werr)
	}
	return "updated", nil
}

// agentsState is the outcome of checking one instruction file.
type agentsState int

const (
	agentsOK     agentsState = iota // block present and current
	agentsStale                     // block present but an older version (drift)
	agentsAbsent                    // file missing, or present without a gk block
)

// checkAgentsFile prints one status line and reports the file's state.
func checkAgentsFile(w io.Writer, path, label string) agentsState {
	b, rerr := os.ReadFile(path)
	switch {
	case os.IsNotExist(rerr):
		fmt.Fprintf(w, "  %s %s — not installed\n", cellYellow("·"), label)
		return agentsAbsent
	case rerr != nil:
		fmt.Fprintf(w, "  %s %s — unreadable: %v\n", cellYellow("·"), label, rerr)
		return agentsAbsent
	}
	content := string(b)
	m := agentsVersionRE.FindStringSubmatch(content)
	switch {
	case m == nil:
		fmt.Fprintf(w, "  %s %s — no gk agents block\n", cellYellow("·"), label)
		return agentsAbsent
	case !strings.Contains(content, agentsContractBlock()):
		fmt.Fprintf(w, "  %s %s — out of date (have v%s, want v%d)\n", cellYellow("·"), label, m[1], agentsContractVersion)
		return agentsStale
	default:
		fmt.Fprintf(w, "  %s %s — up to date (v%d)\n", cellGreen("✓"), label, agentsContractVersion)
		return agentsOK
	}
}

func runAgentsCheck(cmd *cobra.Command, args []string) error {
	targets, err := agentsCheckTargets(cmd)
	if err != nil {
		return err
	}
	// In an explicit target (--global or --file), "not installed" is a
	// failure the user asked us to flag. In the default combined view it's
	// just an absent scope (you may not want global installed), so only
	// version *drift* fails the command there.
	explicit := false
	if g, _ := cmd.Flags().GetBool("global"); g {
		explicit = true
	}
	if f, _ := cmd.Flags().GetStringSlice("file"); len(f) > 0 {
		explicit = true
	}

	w := cmd.OutOrStdout()
	drift, absent := 0, 0
	staleScopes := map[string]bool{}
	lastScope := ""
	for _, t := range targets {
		if t.scope != lastScope {
			if lastScope != "" {
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, color.New(color.Faint).Sprint(agentsScopeHeader(t.scope)))
			lastScope = t.scope
		}
		switch checkAgentsFile(w, t.path, agentsFileLabel(t)) {
		case agentsStale:
			drift++
			staleScopes[t.scope] = true
		case agentsAbsent:
			absent++
			staleScopes[t.scope] = true
		}
	}

	if drift > 0 || (explicit && absent > 0) {
		var cmds []string
		if staleScopes["local"] || staleScopes["custom"] {
			cmds = append(cmds, "gk agents install")
		}
		if staleScopes["global"] {
			cmds = append(cmds, "gk agents install --global")
		}
		n := drift
		if explicit {
			n += absent
		}
		return WithHint(
			fmt.Errorf("gk agents: %d file(s) out of date or missing", n),
			hintCommand(strings.Join(cmds, " && ")),
		)
	}
	return nil
}

// agentsScopeHeader is the faint group header printed above each scope's files.
func agentsScopeHeader(scope string) string {
	switch scope {
	case "local":
		return "local (repo)"
	case "global":
		return "global"
	default:
		return "files"
	}
}

// agentsFileLabel shows the bare filename for local/custom files (the scope
// header gives context) and the ~-relative path for global files (two
// different dirs, so the path disambiguates).
func agentsFileLabel(t agentsFile) string {
	if t.scope == "global" {
		return tildePath(t.path)
	}
	return filepath.Base(t.path)
}

// tildePath shortens an absolute path to ~-relative form for display,
// leaving paths outside the home directory untouched.
func tildePath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, rerr := filepath.Rel(home, path); rerr == nil &&
			rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.Join("~", rel)
		}
	}
	return path
}
