package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

// snapshotRefPrefix mirrors internal/cli/snapshot.go's constant of the same
// name. It is duplicated rather than imported: internal/cli sits ABOVE
// internal/chat/tools in the dependency graph (cli wires up the chat
// registry), so importing internal/cli here would create a cycle. Any
// change to gk's snapshot ref convention must be mirrored in both places.
const snapshotRefPrefix = "refs/wip/"

// Bounds on list-shaped results — same rationale as gitLogMaxCommits:
// keep a single tool call's output well under the registry's byte cap
// even against a long-lived branch with hundreds of snapshots or a
// working tree with hundreds of changed files.
const (
	gitStatusMaxChanged   = 200
	gitSnapshotMaxEntries = 200
)

// ── git_status ────────────────────────────────────────────────────────

// gitStatusEntry is one changed path from `git status --porcelain`.
type gitStatusEntry struct {
	Code string `json:"code"` // porcelain XY, e.g. "M ", " M", "??", "UU"
	Path string `json:"path"`
	// origin is a rename/copy's SOURCE path (unexported: never emitted to
	// the model, only used for deny filtering). A rename FROM a denied path
	// to a non-denied one must still be withheld — the R code plus a staged
	// flag on the visible destination would otherwise leak that a deny-listed
	// file took part in a rename, an oracle countChatDirty already suppresses.
	origin string
}

// gitStashEntry is one `git stash list` record.
type gitStashEntry struct {
	Index   int    `json:"index"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

// gitInProgressState reports an interrupted rebase/merge/cherry-pick/
// revert/bisect — the same operations `gk continue` / `gk abort` handle.
type gitInProgressState struct {
	Kind  string `json:"kind"`
	Onto  string `json:"onto,omitempty"`
	Step  int    `json:"step,omitempty"`
	Total int    `json:"total_steps,omitempty"`
}

type gitStatusOutput struct {
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	// Clean is true only when there is NOTHING to report: no staged,
	// modified, or conflicted paths AND no untracked files. This is
	// intentionally broader than git.DirtyFlags.Clean() — gk's internal
	// "dirty" notion by design does not count untracked files as dirty
	// (see internal/git/dirty.go), which is the right call for gk's other
	// callers but would make this field self-contradict UntrackedCount
	// (`clean:true` next to `untracked_count:3` reads as "nothing here" to
	// a model when 3 files sit unaddressed). Staged/Modified/Conflict below
	// still expose the narrower tracked-file-only signal for callers that
	// specifically want it.
	Clean            bool                `json:"clean"`
	Staged           bool                `json:"staged"`
	Modified         bool                `json:"modified"`
	Conflict         bool                `json:"conflict"`
	UntrackedCount   int                 `json:"untracked_count"`
	Changed          []gitStatusEntry    `json:"changed,omitempty"`
	ChangedTruncated bool                `json:"changed_truncated,omitempty"`
	Stash            []gitStashEntry     `json:"stash,omitempty"`
	InProgress       *gitInProgressState `json:"in_progress,omitempty"`
}

type gitStatusInput struct {
	Limit int `json:"limit,omitempty"`
}

func (g *GitTools) gitStatus(ctx context.Context, raw json.RawMessage) (string, error) {
	var in gitStatusInput
	if err := strictUnmarshal(raw, &in); err != nil {
		return "", err
	}
	limit := in.Limit
	if limit <= 0 || limit > gitStatusMaxChanged {
		limit = gitStatusMaxChanged
	}

	branch, detached, err := g.currentBranch(ctx)
	if err != nil {
		return "", err
	}

	// -z (NUL-terminated) rather than the default line format: git C-quotes
	// and wraps in "…" any path with spaces or special/non-ASCII bytes in
	// the default output, which would (a) leak a quoted denied filename into
	// changed[] and (b) make MatchDeny test the quoted string and miss, so a
	// specially named denied file would slip the filter. NUL output is raw.
	porcelain, err := g.run(ctx, "status", "--porcelain=v1", "-z")
	if err != nil {
		return "", err
	}

	entries, untrackedPaths := parseStatusEntries(porcelain)
	entries = g.filterDeniedEntries(entries)
	// Untracked paths are deny-filtered too: an untracked deny_paths file
	// (e.g. an un-gitignored .env) must not raise untracked_count or flip
	// clean, or those become an existence oracle for a path the deny list
	// hides — the same bar changed[] and the flags already meet.
	untracked := 0
	for _, p := range untrackedPaths {
		if len(g.DenyGlobs) == 0 || aicommit.MatchDeny(p, g.DenyGlobs) == "" {
			untracked++
		}
	}
	// Flags come from the DENY-FILTERED entries, not the raw porcelain.
	// Deriving them from the unfiltered output would leave `clean:false`
	// as a working oracle for "some denied path changed" even though
	// changed[] withholds the name — the same leak filterDeniedEntries
	// exists to close, one field over.
	flags := flagsFromEntries(entries)
	truncated := false
	if len(entries) > limit {
		entries = entries[:limit]
		truncated = true
	}

	stashOut, err := g.run(ctx, "stash", "list", "--format=%gd%x09%cI%x09%s")
	if err != nil {
		return "", err
	}
	stash := parseStashEntries(stashOut)

	// gitstate.Detect runs its own (hardcoded, argument-free) `git
	// rev-parse` to locate the git dir and then only stats fixed marker
	// filenames — no model-supplied input reaches it, so routing this one
	// check outside g.Runner doesn't reopen the injection surface the
	// rest of this package guards against. It's also the same helper
	// every other in-progress check in this codebase already uses.
	state, sErr := gitstate.Detect(ctx, g.Sandbox.Root)
	if sErr != nil {
		return "", fmt.Errorf("git_status: detect in-progress state: %w", sErr)
	}

	out := gitStatusOutput{
		Branch:   branch,
		Detached: detached,
		// See the Clean field's doc comment: untracked files count against
		// "clean" here even though git.DirtyFlags.Clean() doesn't.
		Clean:            flags.Clean() && untracked == 0,
		Staged:           flags.Staged,
		Modified:         flags.Modified,
		Conflict:         flags.Conflict,
		UntrackedCount:   untracked,
		Changed:          entries,
		ChangedTruncated: truncated,
		Stash:            stash,
		InProgress:       inProgressFromState(state),
	}
	b, mErr := json.MarshalIndent(out, "", "  ")
	if mErr != nil {
		return "", fmt.Errorf("git_status: encode: %w", mErr)
	}
	return string(b), nil
}

// parseStatusEntries walks `git status --porcelain=v1 -z` output into
// changed entries plus the list of untracked/ignored paths (returned as
// paths, not just a count, so the caller can deny-filter them). Records
// are NUL-separated with no C-quoting; a rename/copy record ('R'/'C' in
// either column) is immediately followed by its ORIGIN path in the next
// NUL field, which is consumed here so it is never mistaken for its own
// record. The entry keeps the destination path (what `changed[]` reports),
// matching the default format's `orig -> dest` where dest is what matters.
func parseStatusEntries(porcelain string) (entries []gitStatusEntry, untracked []string) {
	recs := strings.Split(porcelain, "\x00")
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		if len(rec) < 4 {
			continue
		}
		x, y := rec[0], rec[1]
		path := rec[3:]
		// Rename/copy: the very next NUL field is the origin path. Capture it
		// (not just skip it) so filterDeniedEntries can test the SOURCE too.
		origin := ""
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			if i+1 < len(recs) {
				origin = recs[i+1]
			}
			i++
		}
		if x == '?' || x == '!' {
			untracked = append(untracked, path)
			continue
		}
		entries = append(entries, gitStatusEntry{Code: string([]byte{x, y}), Path: path, origin: origin})
	}
	return entries, untracked
}

// flagsFromEntries recomputes the dirty flags over an already-filtered
// entry list. It re-renders the entries as porcelain lines and delegates
// to git.ParsePorcelainV1 rather than re-deriving the XY semantics here:
// conflict codes (DD/AA/UU/…) and the staged/modified column rules live
// in exactly one place, and this path stays correct if they change.
// parseStatusEntries has already dropped untracked/ignored rows, which
// the parser would skip anyway.
func flagsFromEntries(entries []gitStatusEntry) git.DirtyFlags {
	if len(entries) == 0 {
		return git.DirtyFlags{}
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Code)
		b.WriteByte(' ')
		b.WriteString(e.Path)
		b.WriteByte('\n')
	}
	return git.ParsePorcelainV1([]byte(b.String()))
}

// filterDeniedEntries drops any changed-file entry touching a denied path.
// Status entries carry no content (just a path and a two-letter code), so
// this only withholds a filename — but that's the same bar file_list
// applies to denied entries, and status is the cheapest possible oracle
// for "does .env exist" if left unfiltered.
func (g *GitTools) filterDeniedEntries(entries []gitStatusEntry) []gitStatusEntry {
	if len(g.DenyGlobs) == 0 {
		return entries
	}
	out := make([]gitStatusEntry, 0, len(entries))
	for _, e := range entries {
		// Both endpoints of a rename/copy count: origin is the -z source
		// field parseStatusEntries captured (the old " -> " split never fires
		// under -z, where there is no such text — dropping it here).
		paths := []string{e.Path}
		if e.origin != "" {
			paths = append(paths, e.origin)
		}
		denied := false
		for _, p := range paths {
			if aicommit.MatchDeny(p, g.DenyGlobs) != "" {
				denied = true
				break
			}
		}
		if !denied {
			out = append(out, e)
		}
	}
	return out
}

// parseStashEntries parses `git stash list --format=%gd%x09%cI%x09%s`.
func parseStashEntries(out string) []gitStashEntry {
	var stash []gitStashEntry
	for _, ln := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if ln == "" {
			continue
		}
		parts := strings.SplitN(ln, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		stash = append(stash, gitStashEntry{
			Index:   parseStashIndex(parts[0]),
			Date:    parts[1],
			Subject: parts[2],
		})
	}
	return stash
}

// parseStashIndex extracts N from a `%gd` selector like "stash@{2}".
// Returns -1 when the selector doesn't match the expected shape.
func parseStashIndex(gd string) int {
	i := strings.Index(gd, "@{")
	if i < 0 || !strings.HasSuffix(gd, "}") {
		return -1
	}
	n, err := strconv.Atoi(gd[i+2 : len(gd)-1])
	if err != nil {
		return -1
	}
	return n
}

// inProgressFromState converts a gitstate.State into the tool's public
// shape, returning nil when nothing is in progress.
func inProgressFromState(state *gitstate.State) *gitInProgressState {
	if state == nil || state.Kind == gitstate.StateNone {
		return nil
	}
	ip := &gitInProgressState{Kind: state.Kind.String()}
	switch state.Kind {
	case gitstate.StateRebaseMerge, gitstate.StateRebaseApply:
		ip.Onto = state.Onto
		ip.Step = state.Current
		ip.Total = state.Total
	}
	return ip
}

// currentBranch returns HEAD's short branch name. In detached HEAD it
// reports the short commit SHA instead and sets detached=true, so the
// model still has an anchor rather than an empty string.
func (g *GitTools) currentBranch(ctx context.Context) (name string, detached bool, err error) {
	out, e := g.run(ctx, "symbolic-ref", "--short", "HEAD")
	if e == nil {
		return strings.TrimSpace(out), false, nil
	}
	sha, e2 := g.run(ctx, "rev-parse", "--short", "HEAD")
	if e2 != nil {
		return "", false, fmt.Errorf("git_status: resolve HEAD: %w", e2)
	}
	return strings.TrimSpace(sha), true, nil
}

// snapshotBranch is currentBranch restricted to the branch case: gk
// snapshots live under refs/wip/<branch>, which has no meaning in
// detached HEAD (mirrors internal/cli/snapshot.go's snapshotBranch).
func (g *GitTools) snapshotBranch(ctx context.Context) (string, error) {
	name, detached, err := g.currentBranch(ctx)
	if err != nil {
		return "", err
	}
	if detached || name == "" {
		return "", fmt.Errorf("cannot resolve a snapshot ref in detached HEAD state")
	}
	return name, nil
}

// ── git_snapshot_list ─────────────────────────────────────────────────

type gitSnapshotListInput struct {
	Limit int `json:"limit,omitempty"`
}

func (g *GitTools) gitSnapshotList(ctx context.Context, raw json.RawMessage) (string, error) {
	var in gitSnapshotListInput
	if err := strictUnmarshal(raw, &in); err != nil {
		return "", err
	}
	limit := in.Limit
	if limit <= 0 || limit > gitSnapshotMaxEntries {
		limit = gitSnapshotMaxEntries
	}

	branch, err := g.snapshotBranch(ctx)
	if err != nil {
		return "", err
	}
	ref := snapshotRefPrefix + branch
	if _, err := g.run(ctx, "rev-parse", "--verify", "--quiet", ref); err != nil {
		return fmt.Sprintf("no snapshots for branch %q", branch), nil
	}

	// %gd → selector (refs/wip/<branch>@{n}), trimmed to "@{n}" below.
	// %cI (not %cd/--date=) keeps the timestamp a reproducible strict-ISO
	// string regardless of a --date option — an explicit --date here
	// would ALSO reformat %gd itself (git ties the reflog selector's
	// rendering to the active --date mode), turning "@{0}" into a
	// timestamp-based selector that snapshot restore/diff can't index by.
	out, err := g.run(ctx, "log", "-g",
		"--format=%gd%x09%cI%x09%gs", "-n", strconv.Itoa(limit), ref)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for _, ln := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if ln == "" {
			continue
		}
		parts := strings.SplitN(ln, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		fmt.Fprintf(&sb, "%s  %s  %s\n", trimSnapshotSelector(parts[0]), parts[1], parts[2])
	}
	if sb.Len() == 0 {
		return fmt.Sprintf("no snapshots for branch %q", branch), nil
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// trimSnapshotSelector trims "refs/wip/<branch>" off a reflog selector,
// leaving the "@{n}" suffix used as the restore/diff index.
func trimSnapshotSelector(sel string) string {
	if i := strings.LastIndex(sel, "@{"); i >= 0 {
		return sel[i:]
	}
	return sel
}

// ── git_snapshot_diff ─────────────────────────────────────────────────

type gitSnapshotDiffInput struct {
	Index int  `json:"index,omitempty"`
	Raw   bool `json:"raw,omitempty"`
}

func (g *GitTools) gitSnapshotDiff(ctx context.Context, raw json.RawMessage) (string, error) {
	var in gitSnapshotDiffInput
	if err := strictUnmarshal(raw, &in); err != nil {
		return "", err
	}
	if in.Index < 0 {
		return "", fmt.Errorf("git_snapshot_diff: index must be >= 0")
	}

	branch, err := g.snapshotBranch(ctx)
	if err != nil {
		return "", err
	}
	ref := snapshotRefPrefix + branch
	selector := fmt.Sprintf("%s@{%d}", ref, in.Index)
	shaOut, vErr := g.run(ctx, "rev-parse", "--verify", "--quiet", selector+"^{commit}")
	if vErr != nil {
		return "", fmt.Errorf("git_snapshot_diff: no snapshot at @{%d} for branch %q", in.Index, branch)
	}
	sha := strings.TrimSpace(shaOut)

	// Capture "now" (tracked + untracked, respecting .gitignore) into a
	// real tree object and diff tree-to-tree — see snapshotNowTree for why
	// a plain `git diff <sha>` (single ref) is wrong here: it compares the
	// snapshot against the CURRENT INDEX, not the working directory, so any
	// path the snapshot captured while untracked (present in the snapshot's
	// tree, absent from the real index) renders as "deleted" even though it
	// still sits on disk unchanged. This mirrors gk snapshot diff's own
	// runSnapshotDiff, which builds the same throwaway tree for the same
	// reason — snapshot → working tree is the documented direction.
	nowTree, err := g.snapshotNowTree(ctx)
	if err != nil {
		return "", err
	}

	// Same -c overrides as git_diff/git_show: pin prefixes for the digest
	// parser and disable ext-diff/textconv so no repo config can execute
	// code or rewrite what the deny filter parses.
	args := []string{"-c", "diff.noprefix=false", "-c", "diff.mnemonicPrefix=false",
		"-c", "diff.srcPrefix=a/", "-c", "diff.dstPrefix=b/",
		"diff", "--no-color", "--no-ext-diff", "--no-textconv", sha, nowTree}
	out, err := g.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return g.filterAndDigest(out, in.Raw)
}

// snapshotNowTree captures the CURRENT working tree (tracked changes plus
// untracked files, respecting .gitignore) into a tree object using a
// throwaway index — the same trick internal/cli/snapshot.go's snapshotTree
// uses, duplicated here for the same import-cycle reason as
// snapshotRefPrefix above (internal/cli sits above this package). The real
// index and working tree are never touched: the throwaway index file is
// created, populated, and removed within this call.
// denyPathspecs turns the chat deny globs into a git pathspec list for
// `add`: an include of the whole repo (`:/`) followed by one exclude per
// glob. A glob with no slash (a bare name like ".env" or "*.pem") also
// gets a `**/`-prefixed form so it excludes at any depth, approximating
// MatchDeny's base-name match; a glob that already has a slash is passed
// through as a rooted `:(glob)` pattern. This can't perfectly mirror
// MatchDeny (git's pathspec magic and Go's filepath.Match differ on
// corner cases), so callers must keep the authoritative content filter
// downstream — this only reduces which denied blobs get written.
func denyPathspecs(globs []string) []string {
	ps := []string{":/"}
	for _, g := range globs {
		if g == "" {
			continue
		}
		ps = append(ps, ":(exclude,glob)"+g)
		if !strings.Contains(g, "/") {
			ps = append(ps, ":(exclude,glob)**/"+g)
		}
	}
	return ps
}

func (g *GitTools) snapshotNowTree(ctx context.Context) (string, error) {
	er, ok := g.Runner.(*git.ExecRunner)
	if !ok {
		return "", fmt.Errorf("git_snapshot_diff: runner does not support building a throwaway index")
	}

	gitDirOut, err := g.run(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	gitDir := strings.TrimSpace(gitDirOut)

	// Reserve a unique path inside the git dir, then remove it so git
	// creates a fresh empty index there. `add -A` against an empty index
	// records the entire working tree as it stands right now.
	tmp, err := os.CreateTemp(gitDir, "gk-chat-snapshot-index-")
	if err != nil {
		return "", fmt.Errorf("git_snapshot_diff: create temp index: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	if rmErr := os.Remove(tmpPath); rmErr != nil {
		return "", fmt.Errorf("git_snapshot_diff: reset temp index: %w", rmErr)
	}
	defer os.Remove(tmpPath)

	idx := &git.ExecRunner{Dir: er.Dir, ExtraEnv: append(append([]string{}, er.ExtraEnv...), "GIT_INDEX_FILE="+tmpPath)}
	// `add -A` stages the ENTIRE working tree, which writes a blob into the
	// object DB for every changed/untracked file — including deny_paths
	// files. A read-only tool must not persist a denied file's CONTENT to
	// the object store, so the deny list is pushed down into the add as
	// pathspec exclusions: a denied file then never becomes a blob at all.
	// This is best-effort (git's :(glob) magic can't perfectly reproduce
	// MatchDeny's base/any-component matching — see denyPathspecs), so it is
	// belt-and-suspenders with the authoritative filterAndDigest pass on the
	// diff OUTPUT, which is what actually guarantees the model never SEES a
	// denied path. The pathspec merely stops most denied blobs from being
	// written in the first place.
	addArgs := append([]string{"add", "-A", "--"}, denyPathspecs(g.DenyGlobs)...)
	if _, stderr, e := idx.Run(ctx, addArgs...); e != nil {
		return "", fmt.Errorf("git_snapshot_diff: stage working tree: %s: %w", strings.TrimSpace(string(stderr)), e)
	}
	out, stderr, e := idx.Run(ctx, "write-tree")
	if e != nil {
		return "", fmt.Errorf("git_snapshot_diff: write-tree: %s: %w", strings.TrimSpace(string(stderr)), e)
	}
	return strings.TrimSpace(string(out)), nil
}

// RegisterStatusTools adds git_status, git_snapshot_list, and
// git_snapshot_diff to the registry. It reuses the same *GitTools (Runner
// + Sandbox + DenyGlobs) RegisterGitTools binds — no additional wiring.
func RegisterStatusTools(r *Registry, g *GitTools) {
	r.Register(Tool{
		Name: "git_status",
		Description: "Structured working-tree status: dirty flags (staged/modified/conflict), " +
			"untracked file count, the list of changed paths (with porcelain code), stash entries, " +
			"and any in-progress operation (rebase/merge/cherry-pick/revert/bisect). `clean` is true " +
			"only when there are no staged/modified/conflicted paths AND no untracked files — an " +
			"untracked-only working tree is NOT reported clean.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"limit":{"type":"integer","description":"max changed-file entries to list, default/max 200"}
		},"additionalProperties":false}`),
		Handler: g.gitStatus,
	})
	r.Register(Tool{
		Name: "git_snapshot_list",
		Description: "List gk snapshot entries (refs/wip/<branch> reflog) for the current branch — " +
			"selector (@{n}), date, and note for each. Snapshots are gk's non-destructive working-tree " +
			"safety net (see 'gk snapshot'), distinct from git stash.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"limit":{"type":"integer","description":"max snapshot entries to list, default/max 200"}
		},"additionalProperties":false}`),
		Handler: g.gitSnapshotList,
	})
	r.Register(Tool{
		Name: "git_snapshot_diff",
		Description: "Diff one gk snapshot against the current working tree (snapshot → working tree; " +
			"index=0 is the latest). Default returns a structured digest; set raw=true for the full patch.",
		Schema: json.RawMessage(`{"type":"object","properties":{
			"index":{"type":"integer","description":"snapshot index, 0 = latest (default 0)"},
			"raw":{"type":"boolean","description":"full patch instead of digest"}
		},"additionalProperties":false}`),
		Handler: g.gitSnapshotDiff,
	})
}
