package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/diff"
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
	// Sections below are present only when requested via --include. They
	// fuse the follow-up probes an agent would otherwise issue as separate
	// calls (diff --digest, log, precheck, remote drift) into this same
	// document.
	Diff     *diffDigestJSON     `json:"diff,omitempty"`
	Log      []contextLogJSON    `json:"log,omitempty"`
	Precheck *precheckResult     `json:"precheck,omitempty"`
	Remotes  []contextRemoteJSON `json:"remotes,omitempty"`
	// Notes records sections that were requested but degraded (e.g.
	// precheck with no upstream) — absence of a section plus its note is
	// the contract for "asked, not available".
	Notes []string `json:"notes,omitempty"`
}

type contextLogJSON struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

// contextRemoteJSON describes one registered remote and the current
// branch's drift against it. Ahead/Behind reflect the last fetch — context
// is read-only and never touches the network; Fetched=false means the
// remote has no local tracking ref for this branch, so drift is unknown
// (run `git fetch <name>` or `gk pull --from <name>` to learn it).
type contextRemoteJSON struct {
	Name     string   `json:"name"`
	FetchURL string   `json:"fetch_url,omitempty"`
	// PushURLs lists push destinations that differ from the fetch URL —
	// non-empty means asymmetric config: work merged there never comes
	// down on fetch (see `gk doctor`).
	PushURLs []string `json:"push_urls,omitempty"`
	Branch   string   `json:"branch,omitempty"`
	Ahead    int      `json:"ahead"`
	Behind   int      `json:"behind"`
	Fetched  bool     `json:"fetched"`
}

// contextIncludeValues are the sections --include accepts; "all" expands to
// every entry.
var contextIncludeValues = []string{"diff", "log", "precheck", "remotes"}

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
replaces the usual status/branch/log/worktree probe sequence.

--include fuses the usual follow-up probes into the same document:

  gk context --include=diff,log,precheck,remotes   (or --include=all)

  diff      uncommitted changes as a digest (per-file ±lines, symbols),
            untracked files included
  log       the last 5 commits (sha, subject, author, date)
  precheck  merge-tree forecast for the next pull
  remotes   every registered remote with the current branch's drift as of
            the last fetch, plus asymmetric push URLs (see gk doctor)

A requested section that cannot be collected (e.g. precheck with no
upstream) degrades to a note instead of failing the whole call.`,
		RunE: runContext,
	}
	cmd.Flags().StringSlice("include", nil,
		"extra sections to fuse into the result: diff, log, precheck, remotes, or all")
	rootCmd.AddCommand(cmd)
}

func runContext(cmd *cobra.Command, args []string) error {
	cfg, _ := config.Load(cmd.Flags())
	runner := &git.ExecRunner{Dir: RepoFlag()}
	ctx := cmd.Context()
	if err := ensureGitRepo(ctx, runner); err != nil {
		return err
	}

	includes, err := parseContextIncludes(cmd)
	if err != nil {
		return err
	}

	out, err := collectContext(ctx, runner, cfg)
	if err != nil {
		return err
	}
	collectContextIncludes(ctx, runner, cfg, includes, &out)

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
	if c.Diff != nil {
		fmt.Fprintf(w, "diff: %d files · %d hunks · +%d −%d\n",
			c.Diff.Stat.Files, c.Diff.Stat.Hunks, c.Diff.Stat.Added, c.Diff.Stat.Deleted)
	}
	for _, l := range c.Log {
		fmt.Fprintf(w, "  %s  %s\n", l.SHA, l.Subject)
	}
	if c.Precheck != nil {
		if c.Precheck.Clean {
			fmt.Fprintf(w, "precheck: clean merge → %s\n", c.Precheck.Target)
		} else {
			fmt.Fprintf(w, "precheck: %d conflict(s) merging into %s: %s\n",
				len(c.Precheck.Conflicts), c.Precheck.Target, strings.Join(c.Precheck.Conflicts, ", "))
		}
	}
	for _, r := range c.Remotes {
		line := "remote " + r.Name
		switch {
		case r.Fetched:
			line += fmt.Sprintf(": ↑%d ↓%d vs %s (as of last fetch)", r.Ahead, r.Behind, r.Branch)
		case r.Branch == "" && !r.Fetched:
			line += ": branch not fetched — drift unknown"
		}
		if len(r.PushURLs) > 0 {
			line += "  ⚠ also pushes to " + strings.Join(r.PushURLs, ", ")
		}
		fmt.Fprintln(w, line)
	}
	for _, n := range c.Notes {
		fmt.Fprintln(w, cellFaint("note: "+n))
	}
	if len(c.NextActions) > 0 {
		fmt.Fprintln(w, stylizeHintLine("next: "+strings.Join(c.NextActions, " · ")))
	}
}

// parseContextIncludes validates --include and expands "all". The set
// semantics are strict: an unknown section is a usage error, not a note —
// silently ignoring a typo would read as "section empty".
func parseContextIncludes(cmd *cobra.Command) (map[string]bool, error) {
	raw, _ := cmd.Flags().GetStringSlice("include")
	includes := map[string]bool{}
	for _, v := range raw {
		v = strings.ToLower(strings.TrimSpace(v))
		switch {
		case v == "":
		case v == "all":
			for _, k := range contextIncludeValues {
				includes[k] = true
			}
		default:
			known := false
			for _, k := range contextIncludeValues {
				if v == k {
					known = true
					break
				}
			}
			if !known {
				return nil, fmt.Errorf("context: unknown --include section %q (valid: %s, all)",
					v, strings.Join(contextIncludeValues, ", "))
			}
			includes[v] = true
		}
	}
	return includes, nil
}

// collectContextIncludes fills the requested fused sections. Every section
// is best-effort: a section that cannot be collected becomes a note, never
// an error — the agent asked for orientation, not a transaction.
func collectContextIncludes(ctx context.Context, runner *git.ExecRunner, cfg *config.Config, includes map[string]bool, out *contextJSON) {
	if includes["diff"] {
		if dg, err := collectContextDiff(ctx, runner); err == nil {
			out.Diff = dg
		} else {
			out.Notes = append(out.Notes, "diff skipped: "+err.Error())
		}
	}
	if includes["log"] {
		if entries, err := collectContextLog(ctx, runner, 5); err == nil {
			out.Log = entries
		} else {
			out.Notes = append(out.Notes, "log skipped: "+err.Error())
		}
	}
	if includes["precheck"] {
		if target, terr := precheckDefaultTarget(ctx, runner, cfg); terr != nil {
			out.Notes = append(out.Notes, "precheck skipped: "+terr.Error())
		} else if res, perr := collectPrecheck(ctx, runner, target, ""); perr == nil {
			out.Precheck = &res
		} else {
			out.Notes = append(out.Notes, "precheck skipped: "+perr.Error())
		}
	}
	if includes["remotes"] {
		if remotes, rerr := collectContextRemotes(ctx, runner, out.Branch, out.Detached); rerr == nil {
			out.Remotes = remotes
		} else {
			out.Notes = append(out.Notes, "remotes skipped: "+rerr.Error())
		}
	}
}

// collectContextRemotes reports every registered remote with the current
// branch's drift against its last-fetched state, plus any asymmetric push
// URLs. Read-only: no network.
func collectContextRemotes(ctx context.Context, runner *git.ExecRunner, branch string, detached bool) ([]contextRemoteJSON, error) {
	names := listRemotes(ctx, runner)
	if len(names) == 0 {
		return nil, fmt.Errorf("no remotes configured")
	}
	if detached {
		branch = ""
	}
	remotes := make([]contextRemoteJSON, 0, len(names))
	for _, name := range names {
		entry := contextRemoteJSON{Name: name}
		if urlOut, _, err := runner.Run(ctx, "config", "--get", "remote."+name+".url"); err == nil {
			entry.FetchURL = strings.TrimSpace(string(urlOut))
		}
		if pushOut, _, err := runner.Run(ctx, "config", "--get-all", "remote."+name+".pushurl"); err == nil {
			for _, u := range strings.Split(strings.TrimSpace(string(pushOut)), "\n") {
				u = strings.TrimSpace(u)
				if u != "" && u != entry.FetchURL {
					entry.PushURLs = append(entry.PushURLs, u)
				}
			}
		}
		if branch != "" {
			ref := name + "/" + branch
			if git.RefExists(ctx, runner, ref) {
				entry.Fetched = true
				entry.Branch = ref
				if raw, _, err := runner.Run(ctx, "rev-list", "--left-right", "--count", "HEAD..."+ref); err == nil {
					fields := strings.Fields(strings.TrimSpace(string(raw)))
					if len(fields) == 2 {
						entry.Ahead, _ = parsePositiveInt(fields[0])
						entry.Behind, _ = parsePositiveInt(fields[1])
					}
				}
			}
		}
		remotes = append(remotes, entry)
	}
	return remotes, nil
}

// collectContextDiff digests all uncommitted changes the same way `gk diff
// --digest` does: staged + unstaged via `git diff HEAD` (-U0 so git's
// funcname heuristic resolves symbols precisely), plus untracked files —
// `git diff HEAD` cannot see those, and an orientation diff that reports "0
// files" over a tree full of new files would send the agent off course.
//
// Before the first commit HEAD does not resolve; the empty tree takes its
// place so the contract ("uncommitted changes, untracked included") holds
// in a freshly-initialized repo — the very state where orientation matters
// most.
func collectContextDiff(ctx context.Context, runner *git.ExecRunner) (*diffDigestJSON, error) {
	baseRef := "HEAD"
	if _, _, herr := runner.Run(ctx, "rev-parse", "--verify", "HEAD"); herr != nil {
		emptyTree, _, terr := runner.Run(ctx, "hash-object", "-t", "tree", "/dev/null")
		if terr != nil {
			return nil, fmt.Errorf("unborn HEAD and no empty-tree fallback: %v", terr)
		}
		baseRef = strings.TrimSpace(string(emptyTree))
	}
	stdout, stderr, err := runner.Run(ctx, "diff", "-U0", baseRef)
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	result := &diff.DiffResult{}
	if strings.TrimSpace(string(stdout)) != "" {
		parsed, perr := diff.ParseUnifiedDiff(bytes.NewReader(stdout))
		if perr != nil {
			return nil, fmt.Errorf("diff parse: %v", perr)
		}
		result = parsed
	}
	dg := digestToJSON(diff.BuildDigest(result))
	appendUntrackedToDigest(ctx, runner, &dg)
	return &dg, nil
}

// appendUntrackedToDigest adds untracked files to the digest as
// status:"untracked" entries with their line counts, mirroring what `git
// diff --numstat` would report once the file is added. Best-effort: an
// unreadable file is skipped rather than failing orientation.
func appendUntrackedToDigest(ctx context.Context, runner *git.ExecRunner, dg *diffDigestJSON) {
	out, _, err := runner.Run(ctx, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return
	}
	dir := runner.Dir
	if dir == "" {
		dir = "."
	}
	for _, p := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if p == "" {
			continue
		}
		entry := diffDigestFileJSON{Path: p, Status: "untracked", Kind: aicommit.FileKind(p)}
		if data, rerr := os.ReadFile(filepath.Join(dir, p)); rerr == nil {
			if isBinaryContent(data) {
				entry.Binary = true
			} else if len(data) > 0 {
				entry.Hunks = 1
				entry.Added = countLines(data)
			}
		}
		dg.Files = append(dg.Files, entry)
		dg.Stat.Files++
		dg.Stat.Hunks += entry.Hunks
		dg.Stat.Added += entry.Added
	}
}

// isBinaryContent applies git's heuristic: a NUL byte in the first 8000
// bytes marks the content binary.
func isBinaryContent(data []byte) bool {
	probe := data
	if len(probe) > 8000 {
		probe = probe[:8000]
	}
	return bytes.IndexByte(probe, 0) >= 0
}

func countLines(data []byte) int {
	n := bytes.Count(data, []byte{'\n'})
	if len(data) > 0 && data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// collectContextLog returns the last n commits on HEAD. Unit-separator
// formatting (\x1f) keeps subjects containing tabs/pipes intact.
func collectContextLog(ctx context.Context, runner *git.ExecRunner, n int) ([]contextLogJSON, error) {
	stdout, stderr, err := runner.Run(ctx, "log",
		fmt.Sprintf("--max-count=%d", n), "--pretty=format:%h\x1f%s\x1f%an\x1f%cI")
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	var entries []contextLogJSON
	for _, line := range strings.Split(strings.TrimRight(string(stdout), "\n"), "\n") {
		parts := strings.SplitN(line, "\x1f", 4)
		if len(parts) != 4 {
			continue
		}
		entries = append(entries, contextLogJSON{
			SHA: parts[0], Subject: parts[1], Author: parts[2], Date: parts[3],
		})
	}
	return entries, nil
}
