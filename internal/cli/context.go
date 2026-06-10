package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

// gk context is the one-call orientation an agent (or human) runs before any
// git work: everything that otherwise takes a status/branch/log/worktree
// probe sequence, in one stable JSON document. The schema is a public
// contract for agent tooling — fields are append-only; breaking changes bump
// `schema`.

type contextDirtyJSON struct {
	Staged    int `json:"staged"`
	Unstaged  int `json:"unstaged"`
	Untracked int `json:"untracked"`
	Conflicts int `json:"conflicts"`
}

type contextOpJSON struct {
	Kind   string `json:"kind"`
	Resume string `json:"resume"`
	Abort  string `json:"abort"`
}

type contextBaseJSON struct {
	Name string `json:"name"`
	// BehindRemote counts commits origin/<base> has that the local base
	// lacks — the "morning sync" signal `gk pull --with-base` clears.
	BehindRemote int    `json:"behind_remote"`
	CheckedOutIn string `json:"checked_out_in,omitempty"`
}

type contextWorktreeJSON struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
}

type contextJSON struct {
	Schema      int                   `json:"schema"`
	Branch      string                `json:"branch"`
	Detached    bool                  `json:"detached,omitempty"`
	Upstream    string                `json:"upstream,omitempty"`
	Ahead       int                   `json:"ahead"`
	Behind      int                   `json:"behind"`
	Dirty       contextDirtyJSON      `json:"dirty"`
	InProgress  *contextOpJSON        `json:"in_progress,omitempty"`
	Base        *contextBaseJSON      `json:"base,omitempty"`
	LatestTag   string                `json:"latest_tag,omitempty"`
	Worktrees   []contextWorktreeJSON `json:"worktrees,omitempty"`
	NextActions []string              `json:"next_actions"`
}

func init() {
	cmd := &cobra.Command{
		Use:     "context",
		Aliases: []string{"ctx"},
		Short:   "One-call repo orientation (agent-friendly with --json)",
		Long: `Collects everything needed to orient in this repository — current branch,
upstream and ahead/behind, dirty counts, any in-progress rebase/merge with its
resume/abort commands, base-branch drift, linked worktrees, and suggested next
actions — in a single call.

With the global --json flag the result is a stable machine-readable document
(schema-versioned, append-only fields) intended for AI agents: one call
replaces the usual status/branch/log/worktree probe sequence.`,
		RunE: runContext,
	}
	rootCmd.AddCommand(cmd)
}

func runContext(cmd *cobra.Command, args []string) error {
	cfg, _ := config.Load(cmd.Flags())
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	if err := ensureGitRepo(ctx, runner); err != nil {
		return err
	}

	out, err := collectContext(ctx, runner, cfg)
	if err != nil {
		return err
	}

	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), out)
	}
	renderContextText(cmd, out)
	return nil
}

func collectContext(ctx context.Context, runner *git.ExecRunner, cfg *config.Config) (contextJSON, error) {
	client := git.NewClient(runner)
	out := contextJSON{Schema: 1, NextActions: []string{}}

	branch, err := client.CurrentBranch(ctx)
	if err != nil || branch == "" || branch == "HEAD" {
		out.Detached = true
		if sha, _, serr := runner.Run(ctx, "rev-parse", "--short", "HEAD"); serr == nil {
			out.Branch = strings.TrimSpace(string(sha))
		}
	} else {
		out.Branch = branch
	}

	if upstream, _, _, ok := tryTrackingUpstream(ctx, runner); ok {
		out.Upstream = upstream
		if a, b, aerr := computeAheadBehind(ctx, runner, upstream); aerr == nil {
			out.Ahead, out.Behind = a, b
		}
	}

	out.Dirty = countContextDirty(ctx, runner)

	if st, derr := gitstate.Detect(ctx, RepoFlag()); derr == nil && st.Kind != gitstate.StateNone {
		if op := inProgressOp(st); op != "" {
			out.InProgress = &contextOpJSON{Kind: op, Resume: selfCmd("continue"), Abort: selfCmd("abort")}
		}
	}

	out.Base = collectContextBase(ctx, runner, client, cfg, out.Branch)

	if tagOut, _, terr := runner.Run(ctx, "describe", "--tags", "--abbrev=0"); terr == nil {
		out.LatestTag = strings.TrimSpace(string(tagOut))
	}

	if wtOut, _, werr := runner.Run(ctx, "worktree", "list", "--porcelain"); werr == nil {
		for _, e := range parseWorktreePorcelain(string(wtOut)) {
			if e.Bare {
				continue
			}
			out.Worktrees = append(out.Worktrees, contextWorktreeJSON{Path: e.Path, Branch: e.Branch})
		}
	}

	out.NextActions = contextNextActions(out)
	return out, nil
}

// countContextDirty tallies `git status --porcelain` XY codes. Conflict
// states (both-modified etc.) are counted separately because they change the
// suggested next action entirely.
func countContextDirty(ctx context.Context, runner git.Runner) contextDirtyJSON {
	var d contextDirtyJSON
	raw, _, err := runner.Run(ctx, "status", "--porcelain")
	if err != nil {
		return d
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if len(line) < 2 {
			continue
		}
		x, y := line[0], line[1]
		switch {
		case x == '?' && y == '?':
			d.Untracked++
		case x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D'):
			d.Conflicts++
		default:
			if x != ' ' {
				d.Staged++
			}
			if y != ' ' {
				d.Unstaged++
			}
		}
	}
	return d
}

func collectContextBase(ctx context.Context, runner git.Runner, client *git.Client, cfg *config.Config, current string) *contextBaseJSON {
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}
	name := cfg.BaseBranch
	if name == "" {
		if detected, err := client.DefaultBranch(ctx, remote); err == nil {
			name = detected
		}
	}
	if name == "" || name == current {
		return nil
	}
	b := &contextBaseJSON{Name: name}
	if !git.RefExists(ctx, runner, "refs/heads/"+name) || !git.RefExists(ctx, runner, remote+"/"+name) {
		return b
	}
	if out, _, err := runner.Run(ctx, "rev-list", "--count", name+".."+remote+"/"+name); err == nil {
		if n, perr := parsePositiveInt(strings.TrimSpace(string(out))); perr == nil {
			b.BehindRemote = n
		}
	}
	if entry, err := findWorktreeForBranch(ctx, runner, name); err == nil && entry != nil {
		b.CheckedOutIn = entry.Path
	}
	return b
}

func parsePositiveInt(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// contextNextActions derives the suggested command sequence the same way a
// git-fluent human would triage: paused operation first, then conflicts,
// then local changes, then sync direction, then base drift.
func contextNextActions(c contextJSON) []string {
	var actions []string
	switch {
	case c.InProgress != nil:
		if c.Dirty.Conflicts > 0 {
			actions = append(actions, selfCmd("resolve --ai"))
		}
		actions = append(actions, c.InProgress.Resume, c.InProgress.Abort)
		return actions
	case c.Dirty.Conflicts > 0:
		return append(actions, selfCmd("resolve --ai"), selfCmd("continue"))
	}
	if c.Dirty.Staged+c.Dirty.Unstaged+c.Dirty.Untracked > 0 {
		actions = append(actions, selfCmd("commit"))
	}
	if c.Behind > 0 {
		actions = append(actions, selfCmd("pull"))
	}
	if c.Ahead > 0 {
		actions = append(actions, selfCmd("push"))
	}
	if c.Base != nil && c.Base.BehindRemote > 0 {
		actions = append(actions, selfCmd("pull --with-base"))
	}
	return actions
}

func renderContextText(cmd *cobra.Command, c contextJSON) {
	w := cmd.OutOrStdout()
	branch := c.Branch
	if c.Detached {
		branch += " (detached)"
	}
	sync := ""
	if c.Upstream != "" {
		sync = fmt.Sprintf("  ⇄ %s  ↑%d ↓%d", c.Upstream, c.Ahead, c.Behind)
	}
	fmt.Fprintf(w, "%s%s\n", cellCyanBold(branch), sync)
	fmt.Fprintf(w, "dirty: %d staged · %d unstaged · %d untracked · %d conflicts\n",
		c.Dirty.Staged, c.Dirty.Unstaged, c.Dirty.Untracked, c.Dirty.Conflicts)
	if c.InProgress != nil {
		fmt.Fprintf(w, "in progress: %s  (%s | %s)\n", c.InProgress.Kind, c.InProgress.Resume, c.InProgress.Abort)
	}
	if c.Base != nil {
		line := fmt.Sprintf("base %s: ↓%d behind origin", c.Base.Name, c.Base.BehindRemote)
		if c.Base.CheckedOutIn != "" {
			line += "  (checked out in " + c.Base.CheckedOutIn + ")"
		}
		fmt.Fprintln(w, line)
	}
	if c.LatestTag != "" {
		fmt.Fprintf(w, "latest tag: %s\n", c.LatestTag)
	}
	if len(c.NextActions) > 0 {
		fmt.Fprintln(w, stylizeHintLine("next: "+strings.Join(c.NextActions, " · ")))
	}
}
