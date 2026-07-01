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

const agentsContractVersion = 22

var (
	agentsBeginMarker = fmt.Sprintf("<!-- gk:agents:begin v%d — managed by `gk agents install`; edit outside this block -->", agentsContractVersion)
	agentsEndMarker   = "<!-- gk:agents:end -->"
	agentsBlockRE     = regexp.MustCompile(`(?s)<!-- gk:agents:begin[^>]*-->.*?<!-- gk:agents:end -->`)
	agentsVersionRE   = regexp.MustCompile(`<!-- gk:agents:begin v(\d+)`)
)

const agentsCompactContractBody = `## Git workflow (git-kit)

Use git-kit for git workflows whenever it has a path. In agent tool calls, run the full binary name with agent mode every time:
` + "`GK_AGENT=1 git-kit ...`" + `
` + "`gk`" + ` can be shell-aliased, and environment variables do not persist across calls.

Minimum rules:
- Orient with ` + "`git-kit context`" + ` before git work; add ` + "`--include=diff,log,precheck,remotes,release`" + ` instead of separate status/log/diff probes.
- Prefer git-kit verbs over raw git: ` + "`commit`" + `, ` + "`land`" + `, ` + "`pull --with-base`" + `, ` + "`sync`" + `, ` + "`merge`" + `, ` + "`rebase --plan`" + `, ` + "`diff --digest`" + `, ` + "`diff --raw-patch --json`" + `, ` + "`worktree ...`" + `, ` + "`ship`" + `, ` + "`batch --plan -`" + `.
- Keep read-only plumbing raw when needed: ` + "`git rev-parse`" + `, ` + "`git config --get`" + `, ` + "`git cat-file`" + `, ` + "`git ls-files`" + `.
- For commit + pull + push, use ` + "`git-kit land`" + `; for local-only integration use ` + "`git-kit promote`" + `; for releases inspect ` + "`git-kit ship --dry-run --json`" + ` before ` + "`git-kit ship -y`" + `.
- Agent-mode output is ` + "`{state, ok, result, error}`" + `. Branch on ` + "`state`" + `, not prose: ` + "`ok`" + `, ` + "`paused`" + `, ` + "`blocked`" + `, ` + "`error`" + `; ` + "`ok`" + ` is only ` + "`state==\"ok\"`" + `.
- A paused merge/rebase/conflict is not done. Use the resume/abort command in the result. When explicitly asked to resolve conflicts, use ` + "`git-kit resolve`" + `; otherwise report the paused state and await direction.
- On failure, run the first ` + "`error.remedies[]`" + ` command after checking its ` + "`safety`" + `; avoid retrying raw git variations.`

const agentsFullContractBody = `## Git workflow (git-kit)

### Reach for git-kit first — raw git that has a git-kit path

| Don't (raw git) | Do (git-kit) |
| --- | --- |
| git status / log / diff --stat / branch (orienting) | git-kit context — one call; add --include=diff,log,precheck,remotes for more |
| git add + git commit | git-kit commit (AI groups) — or git-kit commit --plan - to group it yourself |
| git checkout / git switch (to a branch) | git-kit switch |
| git worktree … | git-kit worktree … |
| git pull / fetch / merge / rebase | git-kit pull / sync / merge / rebase (paused states stay in the envelope) |
| git tag + git push (cutting a release) | git-kit ship -y |
| git diff (the full patch) | git-kit diff --raw-patch --json — or --digest for a summary |
| git … && git … && git … (multi-step chains) | git-kit batch --plan - (one transaction) |
| the short gk (shadowed by shell aliases) | git-kit (always the full name) |

Read-only plumbing stays raw — git-kit does not wrap git rev-parse, git config --get, git cat-file, git ls-files, and the like.

### Detail

This repository is driven with git-kit, an agent-native git CLI. Always invoke it as ` + "`git-kit`" + ` — the short name ` + "`gk`" + ` is the same binary but is commonly shadowed by shell aliases (oh-my-zsh maps ` + "`gk`" + ` to gitk), so it is not reliable from an agent shell. Prefix every agent tool call with ` + "`GK_AGENT=1 git-kit …`" + ` — an agent shell does not persist environment between tool calls, so setting it just once would silently lapse to human-readable prose on the next call (a human at an interactive shell can ` + "`export GK_AGENT=1`" + ` once instead). With it set, every command emits a uniform envelope — ` + "`{state, ok, result}`" + ` on success, ` + "`{state:\"error\", ok:false, error:{code, message, remedies:[{command,safety}]}}`" + ` on failure — so you branch on fields, never parse prose. ` + "`state`" + ` is the dispatch key: ` + "`ok`" + ` (done) · ` + "`paused`" + ` (a conflict/operation is mid-flight — resume or abort it) · ` + "`blocked`" + ` (a precondition like a diverged base failed — run the remedy) · ` + "`error`" + ` (the command failed); ` + "`ok`" + ` is kept as a derived alias (` + "`ok == state==\"ok\"`" + `). **Quick start — most agent sessions are three turns:** ` + "`git-kit context`" + ` (orient) → make your edits → ` + "`git-kit land`" + ` (commit + pull + push in one transaction); add ` + "`git-kit ship -y`" + ` to cut a release. Prefer git-kit over raw git — each verb below collapses several git calls into one:

- **Orient first**: ` + "`git-kit context`" + ` — one call returns branch, upstream, ahead/behind, dirty counts, any in-progress rebase/merge (with resume/abort commands), base-branch drift, worktrees, and ` + "`next_actions`" + `. Add ` + "`--include=diff,log,precheck,remotes,release`" + ` (or ` + "`--include=all`" + `) to fuse the uncommitted-change digest (untracked included), the last 5 commits, the next-pull conflict forecast, per-remote drift, and the commits since the latest tag (what is still unreleased) into the same document — one call instead of six; a section that cannot be collected degrades to a ` + "`notes`" + ` entry, never an error. Never split orientation across separate tool calls (raw git status, then log, then diff): probes spread across turns are the single biggest source of avoidable turns — ` + "`git-kit context`" + ` collapses them into one, so make it the first action of a session, not a sequence of probes.
- **Wrap up**: ` + "`git-kit land`" + ` — commit (AI-grouped), pull --with-base, push as one transaction with per-step results; on failure the result names ` + "`failed_step`" + ` and the resume command. Add ` + "`--to parent|base|<branch>`" + ` to also forward-merge the current branch: ` + "`parent`" + ` = one hop to the gk-parent (base fallback), ` + "`base`" + ` = straight into the base, ` + "`<branch>`" + ` = chain-walk the parent links hop by hop up to that branch. Make it the default via ` + "`land.promote`" + ` config or ` + "`GK_LAND_PROMOTE`" + ` env (value ` + "`parent`" + ` or a branch name — for the base use its real name, not the word ` + "`base`" + `); ` + "`--no-push`" + ` makes the run local (commit + pull + local merge, no push). ` + "`--cleanup`" + ` also reclaims fully-merged branches and their worktrees. (` + "`--promote`" + ` is the deprecated alias for ` + "`--to`" + `; use ` + "`git-kit promote <branch>`" + ` for the multi-hop parent-chain walk.)
- **Local wrap-up (no network)**: ` + "`git-kit promote`" + ` — commit, then forward-merge the current branch into its parent/base (gk-parent metadata, trunk fallback); ` + "`git-kit promote <branch>`" + ` walks the parent chain hop by hop. Nothing is pushed without ` + "`--push`" + ` — use it when integration is local and land would push too early. Same per-step result contract as land.
- **Batch any sequence**: ` + "`git-kit batch --plan -`" + ` — run several git-kit commands as one transaction from a JSON plan on stdin: ` + "`{\"steps\":[{\"args\":[\"pull\",\"--with-base\"]},{\"args\":[\"push\"]}]}`" + `, optional per-step ` + "`on_failure: \"abort\"|\"continue\"`" + `. The result reports per-step outcomes plus ` + "`failed_step`" + `/` + "`resume`" + `; a gating failure skip-marks the remaining steps. Draft a plan with ` + "`--plan-template`" + `, preview with ` + "`--dry-run`" + `. N calls → 1.
- **Sync**: ` + "`git-kit pull`" + ` (add ` + "`--with-base`" + ` to also fast-forward the local base branch, FF-only). On conflict the result lists the files plus the exact resume/abort commands. ` + "`--from <remote>[/<branch>]`" + ` integrates from a secondary remote (mirror, org fork) that the upstream chain never fetches — tracking config stays untouched.
- **Forecast before integrating**: ` + "`git-kit precheck [target]`" + ` — read-only merge-tree simulation (no target = the next pull). Clean → integrate; conflicts listed → pick a strategy first instead of try→abort.
- **Inspect changes**: ` + "`git-kit diff --digest`" + ` — per-file change kind, ±lines, hunk count, and the changed symbols, without the patch body. Same ref/path arguments as plain diff (` + "`--staged`" + `, ` + "`HEAD~3`" + `, ` + "`main..feature`" + `). Read the full patch only for the files the digest makes interesting.
- **Agent worktree lifecycle**: for multi-turn isolated work, acquire a ready worktree first with ` + "`git-kit worktree acquire <branch> --json`" + `, then use ` + "`result.path`" + ` as the cwd for later tool calls; ` + "`worktree.init`" + ` runs by default, and ` + "`--no-init`" + ` skips it. Finish from inside that worktree with ` + "`git-kit worktree finish --to parent --cleanup`" + ` (local promote + remove the linked worktree); add ` + "`--push`" + ` to use ` + "`land --to`" + `, and ` + "`--delete-branch`" + ` when the finished branch should also be removed. Reclaim old finished worktrees with ` + "`git-kit worktree cleanup --merged --stale 7d --json`" + `, then rerun with ` + "`-y`" + ` after reviewing candidates.
- **Isolated one-shot worktree task**: ` + "`git-kit worktree run <branch> --init -- <command>`" + ` — create (or reuse) a worktree for ` + "`<branch>`" + `, bootstrap it (including reused worktrees), run ` + "`<command>`" + ` as the cwd, and exit with the command's own exit code. ` + "`--cleanup`" + ` reclaims success (and deletes the branch if this call created it); failing commands leave the worktree for inspection. ` + "`--from <ref>`" + ` bases a new branch elsewhere, and ` + "`--no-init`" + ` skips bootstrap. To find which worktree holds unfinished work without a per-path probe, ` + "`git-kit worktree list --json`" + ` reports each worktree's branch, ahead/behind, parent, lock state, and dirty counts in one call.
- **Commit / push**: ` + "`git-kit commit -f`" + ` groups changes into conventional commits; ` + "`git-kit push`" + ` scans for secrets before pushing.
- **Curated multi-commit**: when YOU decide the grouping instead of the AI, ` + "`git-kit commit --plan-template`" + ` emits the dirty files as a JSON draft; split it into ` + "`{\"commits\":[{\"message\":\"feat(x): ...\",\"files\":[...]}]}`" + ` and run ` + "`git-kit commit --plan -`" + ` — N curated commits in one deterministic call (no AI, secret scan included, backup ref behind ` + "`gk commit --abort`" + `). Duplicate/unknown files and malformed messages are rejected up front; files the plan does not cover stay dirty. Use this instead of chaining raw ` + "`git add`" + ` + ` + "`git commit`" + ` pairs.
- **History editing**: never open ` + "`git rebase -i`" + ` (the editor session is unusable for you). Instead: ` + "`git-kit rebase --plan-template`" + ` emits the commit range as JSON (action/commit/subject/pushed), you decide each commit's fate (pick/squash/fixup/reword/drop), then ` + "`git-kit rebase --plan -`" + ` validates it (every commit addressed, pushed commits guarded) and drives git's own rebase with a backup ref.
- **Conflicts**: ` + "`git-kit resolve`" + ` is the conflict-resolution surface; use it only when the user explicitly asks you to resolve conflicts. Mechanical strategies (` + "`--strategy ours|theirs`" + `) resolve and continue the operation, re-resolve later picks with the same strategy, auto-skip emptied picks, and handle delete/modify plus markerless conflicts from index stages. ` + "`--no-continue`" + ` stops after resolving; ` + "`git-kit continue`" + ` remains for manually edited resolutions. A paused state is a result — ` + "`state:\"paused\"`" + `, ` + "`ok:false`" + `, exit 3 — not an error; resume or abort it rather than running an error remedy.
- **Release**: read the plan first — ` + "`git-kit ship --dry-run --json`" + ` emits the full release plan (inferred version, CHANGELOG draft, the preflight/watch/verify step lists, and ` + "`merge_to_base`" + `). When it looks right, ` + "`git-kit ship -y`" + ` runs the whole pipeline — preflight (lint/test) → version/CHANGELOG → tag → push → CI watch → artifact verify — and works under GK_AGENT: human progress streams to stderr while stdout stays a clean result envelope ` + "`{tag, branch, base, merged_to_base, pushed, shipped_on}`" + ` (no ` + "`env -u GK_AGENT`" + ` dance needed). Preflight (lint/test) gates the release, so validate up front with ` + "`git-kit ship --preflight`" + ` (runs the configured checks on the working tree — dirty is fine — and never tags or pushes; ` + "`{result, steps, failed_step}`" + ` under GK_AGENT) and get them green before ` + "`-y`" + `; ` + "`git-kit commit`" + ` also warns on gofmt before it reaches preflight. From a non-base branch (e.g. develop) ship fast-forwards the base (main) and tags there; if history diverged it stops with ` + "`state:\"blocked\"`" + ` and the remedy ` + "`git-kit sync`" + ` (rebase the branch onto its base so base can fast-forward), then ship again. ` + "`--wait=false`" + ` (or ` + "`ship.wait`" + `) skips the CI watch; ` + "`ship.auto_confirm`" + ` makes ` + "`-y`" + ` the default. What's still unreleased: ` + "`git-kit context --include=release`" + `.
- **Stuck repo** (stale index.lock, orphan merge, prunable worktrees, asymmetric push-only remotes whose merged work never comes down): ` + "`git-kit doctor --fix`" + `.
- On any failure run the first entry of ` + "`error.remedies`" + ` (check ` + "`safety`" + ` first) instead of retrying variations.`

// agentsContractBlock is the compact fenced block written by default.
func agentsContractBlock() string {
	return agentsContractBlockFor(false)
}

// agentsFullContractBlock is the detailed fenced block for callers that opt in.
func agentsFullContractBlock() string {
	return agentsContractBlockFor(true)
}

func agentsContractBlockFor(full bool) string {
	body := agentsCompactContractBody
	if full {
		body = agentsFullContractBody
	}
	return agentsBeginMarker + "\n" + body + "\n" + agentsEndMarker
}

func hasCurrentAgentsContractBlock(content string) bool {
	return strings.Contains(content, agentsContractBlock()) ||
		strings.Contains(content, agentsFullContractBlock())
}

var agentsTargetNames = []string{"CLAUDE.md", "AGENTS.md"}

// agentsFile is one instruction-file location plus the scope it belongs to,
// so install/check output can group and label by where the file lives.
type agentsFile struct {
	path  string
	scope string // "local" (repo root) · "global" (~/.claude, ~/.codex) · "custom" (--file)
}

type agentsFileStatusJSON struct {
	Path    string `json:"path"`
	Label   string `json:"label"`
	Scope   string `json:"scope"`
	State   string `json:"state"` // ok | stale | absent
	Reason  string `json:"reason,omitempty"`
	Version int    `json:"version,omitempty"`
	Want    int    `json:"want"`
}

type agentsInstallFileJSON struct {
	Path    string `json:"path"`
	Label   string `json:"label"`
	Scope   string `json:"scope"`
	Action  string `json:"action"` // created | updated | unchanged
	Version int    `json:"version"`
}

type agentsCheckJSON struct {
	Schema          int                    `json:"schema"`
	Files           []agentsFileStatusJSON `json:"files"`
	Drift           int                    `json:"drift"`
	Absent          int                    `json:"absent"`
	NeedsInstall    bool                   `json:"needs_install,omitempty"`
	InstallCommands []string               `json:"install_commands,omitempty"`
}

type agentsInstallJSON struct {
	Schema int                     `json:"schema"`
	Files  []agentsInstallFileJSON `json:"files"`
}

func (r agentsCheckJSON) agentState() string {
	if r.NeedsInstall {
		return envStateBlocked
	}
	return ""
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

  gk agents print              print the compact contract block (paste it anywhere)
  gk agents print --full       print the detailed contract block
  gk agents install            insert/refresh the compact block at the repo root
  gk agents install --global   insert/refresh ~/.claude/CLAUDE.md + ~/.codex/AGENTS.md
  gk agents check              report block status + version for local AND global
  gk agents hook install       register the Claude Code PreToolUse hook (enforcement)
  gk agents hook uninstall     remove that hook (revert)
  gk agents hook status        report hook install state`,
	}
	print := &cobra.Command{
		Use:   "print",
		Short: "Print the contract block to stdout",
		RunE: func(cmd *cobra.Command, args []string) error {
			full, _ := cmd.Flags().GetBool("full")
			fmt.Fprintln(cmd.OutOrStdout(), agentsContractBlockFor(full))
			return nil
		},
	}
	print.Flags().Bool("full", false, "print the detailed contract block instead of the compact default")
	cmd.AddCommand(print)
	install := &cobra.Command{
		Use:   "install",
		Short: "Insert or refresh the compact contract block in CLAUDE.md and AGENTS.md",
		RunE:  runAgentsInstall,
	}
	install.Flags().StringSlice("file", nil, "restrict to specific files (default: CLAUDE.md and AGENTS.md at the repo root)")
	install.Flags().Bool("global", false, "install into the per-agent global files (~/.claude/CLAUDE.md, ~/.codex/AGENTS.md) instead of the repo root")
	install.Flags().Bool("full", false, "install the detailed contract block instead of the compact default")
	cmd.AddCommand(install)
	check := &cobra.Command{
		Use:   "check",
		Short: "Report contract-block status and version (local + global)",
		RunE:  runAgentsCheck,
	}
	check.Flags().StringSlice("file", nil, "restrict to specific files")
	check.Flags().Bool("global", false, "check only the global files (default reports local + global)")
	cmd.AddCommand(check)
	cmd.AddCommand(newAgentsHookCmd())
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
	full, _ := cmd.Flags().GetBool("full")
	w := cmd.OutOrStdout()
	var res agentsInstallJSON
	if JSONOut() {
		res.Schema = 1
	}
	for _, t := range targets {
		state, werr := installAgentsBlockFor(t.path, full)
		if werr != nil {
			return werr
		}
		if JSONOut() {
			res.Files = append(res.Files, agentsInstallFileJSON{
				Path:    t.path,
				Label:   agentsFileLabel(t),
				Scope:   t.scope,
				Action:  state,
				Version: agentsContractVersion,
			})
			continue
		}
		fmt.Fprintln(w, successLine(state, tildePath(t.path)))
	}
	if JSONOut() {
		return emitAgentResult(w, res)
	}
	return nil
}

// installAgentsBlock writes the current contract block into path, replacing
// an existing fenced block or appending one (creating the parent directory
// and file when absent). Returns the verb describing what happened:
// created / updated / unchanged.
func installAgentsBlock(path string) (string, error) {
	return installAgentsBlockFor(path, false)
}

func installAgentsBlockFor(path string, full bool) (string, error) {
	block := agentsContractBlockFor(full)
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
	st := inspectAgentsFile(path, label)
	renderAgentsFileStatus(w, st)
	return agentsStateFromString(st.State)
}

func inspectAgentsFile(path, label string) agentsFileStatusJSON {
	st := agentsFileStatusJSON{
		Path:  path,
		Label: label,
		Want:  agentsContractVersion,
	}
	b, rerr := os.ReadFile(path)
	switch {
	case os.IsNotExist(rerr):
		st.State = "absent"
		st.Reason = "missing"
		return st
	case rerr != nil:
		st.State = "absent"
		st.Reason = "unreadable"
		return st
	}
	content := string(b)
	m := agentsVersionRE.FindStringSubmatch(content)
	switch {
	case m == nil:
		st.State = "absent"
		st.Reason = "no-block"
	case !hasCurrentAgentsContractBlock(content):
		st.State = "stale"
		st.Version = atoiDefault(m[1], 0)
	default:
		st.State = "ok"
		st.Version = agentsContractVersion
	}
	return st
}

func renderAgentsFileStatus(w io.Writer, st agentsFileStatusJSON) {
	switch st.State {
	case "ok":
		fmt.Fprintf(w, "  %s %s — up to date (v%d)\n", cellGreen("✓"), st.Label, agentsContractVersion)
	case "stale":
		fmt.Fprintf(w, "  %s %s — out of date (have v%d, want v%d)\n", cellYellow("·"), st.Label, st.Version, agentsContractVersion)
	default:
		switch st.Reason {
		case "unreadable":
			fmt.Fprintf(w, "  %s %s — unreadable\n", cellYellow("·"), st.Label)
		case "no-block":
			fmt.Fprintf(w, "  %s %s — no gk agents block\n", cellYellow("·"), st.Label)
		default:
			fmt.Fprintf(w, "  %s %s — not installed\n", cellYellow("·"), st.Label)
		}
	}
}

func agentsStateFromString(s string) agentsState {
	switch s {
	case "ok":
		return agentsOK
	case "stale":
		return agentsStale
	default:
		return agentsAbsent
	}
}

func atoiDefault(s string, def int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return def
	}
	return n
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
	res := agentsCheckJSON{Schema: 1}
	for _, t := range targets {
		if !JSONOut() && t.scope != lastScope {
			if lastScope != "" {
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, color.New(color.Faint).Sprint(agentsScopeHeader(t.scope)))
			lastScope = t.scope
		}
		st := inspectAgentsFile(t.path, agentsFileLabel(t))
		st.Scope = t.scope
		res.Files = append(res.Files, st)
		if !JSONOut() {
			renderAgentsFileStatus(w, st)
		}
		switch agentsStateFromString(st.State) {
		case agentsStale:
			drift++
			staleScopes[t.scope] = true
		case agentsAbsent:
			absent++
			staleScopes[t.scope] = true
		}
	}
	res.Drift = drift
	res.Absent = absent

	if JSONOut() {
		res.NeedsInstall = drift > 0 || (explicit && absent > 0)
		res.InstallCommands = agentsInstallCommands(staleScopes)
		return emitAgentResult(w, res)
	}

	if drift > 0 || (explicit && absent > 0) {
		cmds := agentsInstallCommands(staleScopes)
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

func agentsInstallCommands(scopes map[string]bool) []string {
	var cmds []string
	if scopes["local"] || scopes["custom"] {
		cmds = append(cmds, selfCmd("agents install"))
	}
	if scopes["global"] {
		cmds = append(cmds, selfCmd("agents install --global"))
	}
	return cmds
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
