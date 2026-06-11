package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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

const agentsContractVersion = 9

var (
	agentsBeginMarker = fmt.Sprintf("<!-- gk:agents:begin v%d — managed by `gk agents install`; edit outside this block -->", agentsContractVersion)
	agentsEndMarker   = "<!-- gk:agents:end -->"
	agentsBlockRE     = regexp.MustCompile(`(?s)<!-- gk:agents:begin[^>]*-->.*?<!-- gk:agents:end -->`)
	agentsVersionRE   = regexp.MustCompile(`<!-- gk:agents:begin v(\d+)`)
)

const agentsContractBody = `## Git workflow (git-kit)

This repository is driven with git-kit, an agent-native git CLI. Always invoke it as ` + "`git-kit`" + ` — the short name ` + "`gk`" + ` is the same binary but is commonly shadowed by shell aliases (oh-my-zsh maps ` + "`gk`" + ` to gitk), so it is not reliable from an agent shell. Set ` + "`export GK_AGENT=1`" + ` once: every command then emits a uniform envelope — ` + "`{ok, result}`" + ` on success, ` + "`{ok:false, error:{code, message, remedies:[{command,safety}]}}`" + ` on failure — so you branch on fields, never parse prose. Prefer git-kit over raw git:

- **Orient first**: ` + "`git-kit context`" + ` — one call returns branch, upstream, ahead/behind, dirty counts, any in-progress rebase/merge (with resume/abort commands), base-branch drift, worktrees, and ` + "`next_actions`" + `. Use it instead of probing with git status/branch/log. Add ` + "`--include=diff,log,precheck,remotes`" + ` (or ` + "`--include=all`" + `) to fuse the uncommitted-change digest (untracked included), the last 5 commits, the next-pull conflict forecast, and per-remote drift into the same document — one call instead of five; a section that cannot be collected degrades to a ` + "`notes`" + ` entry, never an error.
- **Wrap up**: ` + "`git-kit land`" + ` — commit (AI-grouped), pull --with-base, push as one transaction with per-step results; on failure the result names ` + "`failed_step`" + ` and the resume command. ` + "`--cleanup`" + ` also reclaims fully-merged branches and their worktrees.
- **Local wrap-up (no network)**: ` + "`git-kit promote`" + ` — commit, then forward-merge the current branch into its parent/base (gk-parent metadata, trunk fallback); ` + "`git-kit promote <branch>`" + ` walks the parent chain hop by hop. Nothing is pushed without ` + "`--push`" + ` — use it when integration is local and land would push too early. Same per-step result contract as land.
- **Batch any sequence**: ` + "`git-kit batch --plan -`" + ` — run several git-kit commands as one transaction from a JSON plan on stdin: ` + "`{\"steps\":[{\"args\":[\"pull\",\"--with-base\"]},{\"args\":[\"push\"]}]}`" + `, optional per-step ` + "`on_failure: \"abort\"|\"continue\"`" + `. The result reports per-step outcomes plus ` + "`failed_step`" + `/` + "`resume`" + `; a gating failure skip-marks the remaining steps. Draft a plan with ` + "`--plan-template`" + `, preview with ` + "`--dry-run`" + `. N calls → 1.
- **Sync**: ` + "`git-kit pull`" + ` (add ` + "`--with-base`" + ` to also fast-forward the local base branch, FF-only). On conflict the result lists the files plus the exact resume/abort commands. ` + "`--from <remote>[/<branch>]`" + ` integrates from a secondary remote (mirror, org fork) that the upstream chain never fetches — tracking config stays untouched.
- **Forecast before integrating**: ` + "`git-kit precheck [target]`" + ` — read-only merge-tree simulation (no target = the next pull). Clean → integrate; conflicts listed → pick a strategy first instead of try→abort.
- **Inspect changes**: ` + "`git-kit diff --digest`" + ` — per-file change kind, ±lines, hunk count, and the changed symbols, without the patch body. Same ref/path arguments as plain diff (` + "`--staged`" + `, ` + "`HEAD~3`" + `, ` + "`main..feature`" + `). Read the full patch only for the files the digest makes interesting.
- **Commit / push**: ` + "`git-kit commit -f`" + ` groups changes into conventional commits; ` + "`git-kit push`" + ` scans for secrets before pushing.
- **History editing**: never open ` + "`git rebase -i`" + ` (the editor session is unusable for you). Instead: ` + "`git-kit rebase --plan-template`" + ` emits the commit range as JSON (action/commit/subject/pushed), you decide each commit's fate (pick/squash/fixup/reword/drop), then ` + "`git-kit rebase --plan -`" + ` validates it (every commit addressed, pushed commits guarded) and drives git's own rebase with a backup ref.
- **Conflicts**: ` + "`git-kit resolve --ai`" + `, then ` + "`git-kit continue`" + ` (abort with ` + "`git-kit abort`" + `). A paused state is a result (exit 3), not an error.
- **Release**: ` + "`git-kit ship --dry-run`" + ` to read the full plan (version, changelog draft, pipeline steps); ` + "`git-kit ship -y`" + ` executes everything — preflight, version/CHANGELOG, tag, push, CI watch, artifact verify.
- **Stuck repo** (stale index.lock, orphan merge, prunable worktrees, asymmetric push-only remotes whose merged work never comes down): ` + "`git-kit doctor --fix`" + `.
- On any failure run the first entry of ` + "`error.remedies`" + ` (check ` + "`safety`" + ` first) instead of retrying variations.`

// agentsContractBlock is the full fenced block as written to files.
func agentsContractBlock() string {
	return agentsBeginMarker + "\n" + agentsContractBody + "\n" + agentsEndMarker
}

var agentsTargetNames = []string{"CLAUDE.md", "AGENTS.md"}

func init() {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "Manage the gk usage contract in agent instruction files (CLAUDE.md / AGENTS.md)",
		Long: `Keeps a versioned "how to use gk" paragraph in the repository's agent
instruction files, so AI agents (Claude, Codex, ...) route git work through
gk's one-call commands instead of probing with raw git.

The paragraph is embedded in the gk binary — it always describes the
installed gk's real surface — and is fenced with markers; nothing outside
the block is ever modified.

  gk agents print     print the contract block (paste it anywhere)
  gk agents install   insert or refresh the block in CLAUDE.md + AGENTS.md
  gk agents check     verify installed blocks match this gk version`,
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
	cmd.AddCommand(install)
	cmd.AddCommand(&cobra.Command{
		Use:   "check",
		Short: "Verify installed contract blocks match this gk version",
		RunE:  runAgentsCheck,
	})
	rootCmd.AddCommand(cmd)
}

// agentsTargets resolves the instruction-file paths at the repo root.
func agentsTargets(cmd *cobra.Command) ([]string, error) {
	runner := &git.ExecRunner{Dir: RepoFlag()}
	out, _, err := runner.Run(cmd.Context(), "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("gk agents: not inside a git repository")
	}
	root := strings.TrimSpace(string(out))

	if files, _ := cmd.Flags().GetStringSlice("file"); len(files) > 0 {
		paths := make([]string, 0, len(files))
		for _, f := range files {
			if filepath.IsAbs(f) {
				paths = append(paths, f)
			} else {
				paths = append(paths, filepath.Join(root, f))
			}
		}
		return paths, nil
	}
	paths := make([]string, 0, len(agentsTargetNames))
	for _, name := range agentsTargetNames {
		paths = append(paths, filepath.Join(root, name))
	}
	return paths, nil
}

func runAgentsInstall(cmd *cobra.Command, args []string) error {
	targets, err := agentsTargets(cmd)
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	for _, path := range targets {
		state, werr := installAgentsBlock(path)
		if werr != nil {
			return werr
		}
		fmt.Fprintln(w, successLine(state, filepath.Base(path)))
	}
	return nil
}

// installAgentsBlock writes the current contract block into path, replacing
// an existing fenced block or appending one. Returns the verb describing
// what happened: created / updated / unchanged.
func installAgentsBlock(path string) (string, error) {
	block := agentsContractBlock()
	b, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
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

func runAgentsCheck(cmd *cobra.Command, args []string) error {
	targets, err := agentsTargets(cmd)
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	stale := 0
	for _, path := range targets {
		name := filepath.Base(path)
		b, rerr := os.ReadFile(path)
		if os.IsNotExist(rerr) {
			fmt.Fprintf(w, "  %s %s — file missing\n", cellYellow("·"), name)
			stale++
			continue
		}
		if rerr != nil {
			return fmt.Errorf("gk agents: read %s: %w", path, rerr)
		}
		content := string(b)
		m := agentsVersionRE.FindStringSubmatch(content)
		switch {
		case m == nil:
			fmt.Fprintf(w, "  %s %s — no gk agents block\n", cellYellow("·"), name)
			stale++
		case !strings.Contains(content, agentsContractBlock()):
			fmt.Fprintf(w, "  %s %s — block out of date (have v%s, want v%d)\n", cellYellow("·"), name, m[1], agentsContractVersion)
			stale++
		default:
			fmt.Fprintf(w, "  %s %s — up to date (v%d)\n", cellGreen("✓"), name, agentsContractVersion)
		}
	}
	if stale > 0 {
		return WithHint(
			fmt.Errorf("gk agents: %d file(s) missing or stale", stale),
			hintCommand("gk agents install"),
		)
	}
	return nil
}
