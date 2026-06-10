package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

func init() {
	cmd := &cobra.Command{
		Use:   "prompt-info",
		Short: "Emit a compact worktree indicator for shell prompts",
		Long: `Emit a compact indicator describing the current worktree, intended
for shell prompt integration.

Formats:

  plain    (default) Space-separated tokens. Always begins with the
           linked-worktree marker (empty in primary). Additional tokens
           are produced for each --include flag entry:

             wt | wt:<name>   linked worktree marker
             wip              HEAD subject matches a WIP pattern
             ±N               N modified/untracked entries in the tree
             ↑N               N commits ahead of upstream
             ↓N               N commits behind upstream
             !<state>         rebase/merge/cherry-pick/revert/bisect in progress

  segment  "<repo>/<branch>" when inside any git repo, empty otherwise.
           Designed to replace starship's $directory + $git_branch with
           a single, deduplicated label that always tells you both the
           project and the branch.

  json     Structured payload (linked, repo, name, path, branch, plus
           any optional signals enabled via --include) for prompt
           frameworks that compose their own segments.

Includes (--include=<csv>) are opt-in because each one adds a git call
that runs on every prompt render. Pick what you need:

  wip      git log -1 --format=%s          ~3ms
  dirty    git status --porcelain          5-50ms depending on repo size
  ahead    git rev-list --count            ~5ms
  behind   (paired with ahead, same call)
  state    .git/ file stat                 negligible

Detection uses git rev-parse --git-dir vs --git-common-dir; a mismatch
means we're in a linked worktree. Without --include the command makes
only those two calls, safe to invoke from a prompt that re-renders on
every keystroke.

Examples:

  # zsh prompt — show worktree + WIP + dirty count
  function gk_info() {
      local info=$(gk prompt-info --include=wip,dirty,ahead 2>/dev/null)
      [[ -n "$info" ]] && print -n " %F{yellow}[$info]%f"
  }

  # starship: configure a custom command segment
  [custom.gk_info]
  command = "gk prompt-info --include=wip,dirty,ahead,behind,state"
  when = "gk prompt-info"`,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE:          runPromptInfo,
	}
	cmd.Flags().String("format", "plain", "output format: plain|segment|json")
	cmd.Flags().String("include", "", "comma-separated optional signals: wip,dirty,ahead,behind,state")
	rootCmd.AddCommand(cmd)
}

// promptInfo is the structured payload returned by `gk prompt-info --format=json`.
// Linked is the load-bearing bit; Name/Path/Branch are populated only when
// inside a linked worktree so callers can render richer segments without
// extra git calls. Repo is populated whenever we're inside any repo (primary
// or linked) so prompts can show a "<repo>/<branch>" segment without an
// extra `git rev-parse` round-trip. The WIP/Dirty/Ahead/Behind/State fields
// are only populated when the corresponding --include flag is set, so the
// JSON shape grows opt-in alongside the plain output.
type promptInfo struct {
	Linked bool   `json:"linked"`
	Repo   string `json:"repo,omitempty"`
	Name   string `json:"name,omitempty"`
	Path   string `json:"path,omitempty"`
	Branch string `json:"branch,omitempty"`

	WIP    bool   `json:"wip,omitempty"`
	Dirty  int    `json:"dirty,omitempty"`
	Ahead  int    `json:"ahead,omitempty"`
	Behind int    `json:"behind,omitempty"`
	State  string `json:"state,omitempty"`
}

// promptIncludes is the parsed --include selector. Each field gates one
// optional git call; defaults (all false) preserve the original two-call
// fast path.
type promptIncludes struct {
	wip    bool
	dirty  bool
	ahead  bool
	behind bool
	state  bool
}

func parsePromptIncludes(spec string) (promptIncludes, error) {
	var p promptIncludes
	if strings.TrimSpace(spec) == "" {
		return p, nil
	}
	for raw := range strings.SplitSeq(spec, ",") {
		tok := strings.TrimSpace(raw)
		switch tok {
		case "":
		case "wip":
			p.wip = true
		case "dirty":
			p.dirty = true
		case "ahead":
			p.ahead = true
		case "behind":
			p.behind = true
		case "state":
			p.state = true
		default:
			return promptIncludes{}, fmt.Errorf("unknown include token %q (want wip,dirty,ahead,behind,state)", tok)
		}
	}
	return p, nil
}

func runPromptInfo(cmd *cobra.Command, args []string) error {
	format, _ := cmd.Flags().GetString("format")
	includeSpec, _ := cmd.Flags().GetString("include")
	includes, err := parsePromptIncludes(includeSpec)
	if err != nil {
		return err
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}
	info := detectPromptInfo(cmd.Context(), runner, includes)
	return formatPromptInfo(cmd.OutOrStdout(), info, format)
}

// formatPromptInfo renders a detected promptInfo according to the named
// format. Split out from runPromptInfo so tests can drive the format
// logic with a fabricated promptInfo and skip the git plumbing.
func formatPromptInfo(w io.Writer, info promptInfo, format string) error {
	switch format {
	case "json":
		// Deliberately NOT routed through the agent envelope: prompt-info's
		// JSON consumer is a shell prompt (starship etc.) that runs with the
		// user's exported environment — wrapping under GK_AGENT=1 would break
		// every prompt render. Its --format flag is its own contract.
		return json.NewEncoder(w).Encode(info)
	case "plain", "":
		tokens := plainTokens(info)
		if len(tokens) == 0 {
			return nil
		}
		fmt.Fprintln(w, strings.Join(tokens, " "))
		return nil
	case "segment":
		if info.Repo == "" {
			return nil
		}
		if info.Branch != "" {
			fmt.Fprintln(w, info.Repo+"/"+info.Branch)
		} else {
			fmt.Fprintln(w, info.Repo)
		}
		return nil
	default:
		return fmt.Errorf("unknown format %q (want plain|segment|json)", format)
	}
}

// plainTokens assembles the space-separated token list for `--format=plain`.
// Order is fixed (worktree, wip, dirty, ahead, behind, state) so prompt
// configs can rely on positional parsing if they want, and so visual
// scanning of the prompt stays predictable.
func plainTokens(info promptInfo) []string {
	var tokens []string
	if info.Linked && info.Name != "" {
		// When the worktree dir matches the branch (gk's default
		// layout: ~/.gk/worktree/<repo>/<branch>) the branch name is
		// already in the shell prompt next door, so "wt:<name>" just
		// duplicates it. Collapse to "wt" — still unmissable as a
		// linked-worktree marker, without the redundant token.
		if info.Name == info.Branch {
			tokens = append(tokens, "wt")
		} else {
			tokens = append(tokens, "wt:"+info.Name)
		}
	}
	if info.WIP {
		tokens = append(tokens, "wip")
	}
	if info.Dirty > 0 {
		tokens = append(tokens, "±"+strconv.Itoa(info.Dirty))
	}
	if info.Ahead > 0 {
		tokens = append(tokens, "↑"+strconv.Itoa(info.Ahead))
	}
	if info.Behind > 0 {
		tokens = append(tokens, "↓"+strconv.Itoa(info.Behind))
	}
	if info.State != "" {
		tokens = append(tokens, "!"+info.State)
	}
	return tokens
}

// detectPromptInfo identifies the current worktree's role using one
// combined `rev-parse --git-dir --git-common-dir` call (different paths
// → linked worktree; same → primary). All git errors collapse to "not
// in a repo" so prompts never see noise. The branch lookup and any
// optional signals (`includes`) fan out as goroutines so wall-clock
// stays at one fork after the gating call — prompt-info reruns on
// every shell keystroke, so each saved fork compounds.
func detectPromptInfo(ctx context.Context, r git.Runner, includes promptIncludes) promptInfo {
	// --path-format=absolute (git 2.31+) makes both outputs absolute
	// regardless of the runner's working directory, so the equality
	// check below is comparing apples to apples and Repo extraction
	// from common-dir doesn't depend on process cwd. Passing both
	// flags in one rev-parse call collapses what used to be two
	// sequential forks into one.
	out, _, err := r.Run(ctx, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return promptInfo{}
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 2 {
		return promptInfo{}
	}
	gd := resolveAbs(strings.TrimSpace(lines[0]))
	cd := resolveAbs(strings.TrimSpace(lines[1]))
	if gd == "" || cd == "" {
		return promptInfo{}
	}
	linked := gd != cd
	info := promptInfo{Linked: linked, Repo: repoNameFromCommonDir(cd)}

	// Fan out every independent git call so wall-time is one round
	// trip, not N. The runner builds a fresh exec.Cmd per call and
	// Dir/ExtraEnv are read-only, so concurrent goroutines are safe.
	var (
		wg               sync.WaitGroup
		branch, top      string
		wip              bool
		dirty            int
		aheadVal, behVal int
		state            string
	)

	wg.Go(func() {
		// Branch is needed for the "segment" format ("<repo>/<branch>")
		// even in the primary worktree, so it's always queried. Empty
		// for detached HEAD and brand-new repos with no commits.
		bout, _, _ := r.Run(ctx, "branch", "--show-current")
		branch = strings.TrimSpace(string(bout))
	})
	if linked {
		wg.Go(func() {
			tout, _, _ := r.Run(ctx, "rev-parse", "--show-toplevel")
			top = strings.TrimSpace(string(tout))
		})
	}
	if includes.wip {
		wg.Go(func() {
			wip = detectPromptWIP(ctx, r)
		})
	}
	if includes.dirty {
		wg.Go(func() {
			dirty = detectPromptDirty(ctx, r)
		})
	}
	if includes.ahead || includes.behind {
		wg.Go(func() {
			aheadVal, behVal = detectPromptAheadBehind(ctx, r)
		})
	}
	if includes.state {
		wg.Go(func() {
			if s, err := gitstate.DetectFromGitDir(cd); err == nil && s != nil && s.Kind != gitstate.StateNone {
				state = s.Kind.String()
			}
		})
	}
	wg.Wait()

	info.Branch = branch
	if linked {
		info.Name = filepath.Base(top)
		info.Path = top
	}
	info.WIP = wip
	info.Dirty = dirty
	if includes.ahead {
		info.Ahead = aheadVal
	}
	if includes.behind {
		info.Behind = behVal
	}
	info.State = state
	return info
}

// promptWIPMatcher is a process-lifetime cache of the compiled default
// WIP patterns wrapped in a match closure. Compiling the regexes on
// every prompt render would be wasted work — the defaults never change
// within a process. Returns a no-op matcher if compile somehow fails so
// callers can use it unconditionally. Wrapping in a closure also keeps
// the regexp package out of this file's imports.
var (
	promptWIPOnce  sync.Once
	promptWIPMatch func(string) bool
)

func promptWIPMatcher() func(string) bool {
	promptWIPOnce.Do(func() {
		patterns, err := aicommit.CompileWIPPatterns(nil)
		if err != nil {
			promptWIPMatch = func(string) bool { return false }
			return
		}
		promptWIPMatch = func(s string) bool { return aicommit.IsWIPSubject(s, patterns) }
	})
	return promptWIPMatch
}

// detectPromptWIP reports whether HEAD's subject matches a WIP pattern.
// Uses the baked-in defaults only — loading the layered config on every
// keystroke would defeat the point of prompt-info's fast path, and the
// defaults cover the conventions (wip/tmp/save/fixup!/squash!/--wip--)
// that prompt indication is worth flagging for.
func detectPromptWIP(ctx context.Context, r git.Runner) bool {
	out, _, err := r.Run(ctx, "log", "-1", "--format=%s")
	if err != nil {
		return false
	}
	subject := strings.TrimSpace(string(out))
	if subject == "" {
		return false
	}
	return promptWIPMatcher()(subject)
}

// detectPromptDirty counts entries in `git status --porcelain=v1`. Each
// porcelain line is one path (modified, untracked, staged, etc.) so a
// simple line count is the right unit for "how noisy is my tree". The
// -c core.optionalLocks=false avoids index.lock contention with a
// concurrent git operation triggered by another shell or editor.
func detectPromptDirty(ctx context.Context, r git.Runner) int {
	out, _, err := r.Run(ctx, "-c", "core.optionalLocks=false", "status", "--porcelain=v1")
	if err != nil {
		return 0
	}
	trimmed := strings.TrimRight(string(out), "\n")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}

// detectPromptAheadBehind returns (ahead, behind) vs HEAD's upstream.
// `--left-right --count` returns "<behind>\t<ahead>" because the LHS of
// `@{u}...HEAD` is upstream. Returns zeros silently when no upstream is
// configured (common for fresh branches) — that's not an error worth
// surfacing in a prompt.
func detectPromptAheadBehind(ctx context.Context, r git.Runner) (int, int) {
	out, _, err := r.Run(ctx, "rev-list", "--count", "--left-right", "@{u}...HEAD")
	if err != nil {
		return 0, 0
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 {
		return 0, 0
	}
	behind, _ := strconv.Atoi(fields[0])
	ahead, _ := strconv.Atoi(fields[1])
	return ahead, behind
}

// repoNameFromCommonDir mirrors the logic in detectRepoName (status_branch.go)
// but works on an already-resolved absolute path. Kept local to avoid an
// extra `git rev-parse --git-common-dir` round-trip — detectPromptInfo
// already has the common-dir output in hand.
func repoNameFromCommonDir(cd string) string {
	if cd == "" {
		return ""
	}
	base := filepath.Base(cd)
	if base == ".git" {
		return filepath.Base(filepath.Dir(cd))
	}
	return strings.TrimSuffix(base, ".git")
}

// resolveAbs normalizes git's mixed relative/absolute path output so
// the --git-dir vs --git-common-dir comparison isn't tripped up by
// "." or relative-from-cwd shenanigans.
func resolveAbs(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}
