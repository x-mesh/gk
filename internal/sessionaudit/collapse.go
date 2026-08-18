package sessionaudit

import (
	"sort"
	"strings"
)

// collapseMaxGap is how many interleaved turns may sit between two same-group
// git turns and still count as one collapsible run. 1 tolerates the common
// "git status → Read a file → git diff" shape while keeping genuinely unrelated,
// far-apart probes as separate (non-collapsible) uses.
const collapseMaxGap = 1

// CollapseLookback is the default maximum turn distance between a prior
// same-group turn and the pending command within which the real-time nudge
// (CollapseNudgeFor) still fires — the same gap tolerance the batch detector
// applies between adjacent hits (splitRuns' gapOK).
const CollapseLookback = collapseMaxGap + 1

// collapseGroupForKind maps a covered finding kind to the single git-kit call
// that absorbs a run of those raw commands. gitSegmentFinding stays the one
// classifier; this is the thin projection from "what is it" to "what one gk
// call replaces a sequence of it". Kinds absent here (release tag/push) are not
// turn-collapsed yet — the occurrence path still reports them.
var collapseGroupForKind = map[string]string{
	"raw-context-probes":  "context",
	"raw-commit-sequence": "commit",
	"raw-integration":     "integration",
	"raw-branch-switch":   "switch",
	"raw-worktree":        "worktree",
	"raw-full-diff":       "diff",
	"raw-diff-check":      "diff",
	"raw-stash":           "stash",
	"raw-apply":           "apply",
	// The history hunt is a real collapse, not a 1:1 swap: the agent pays a turn
	// per GUESS (--grep, then the pickaxe, then a path scope), and gk find runs
	// all of them in one call. raw-branch-list, raw-range-compare, raw-log-query
	// and raw-uncommit are absent on purpose — each is covered by a 1:1 swap
	// (gk branch list, gk log A..B, gk log, gk undo --soft), and one raw command
	// replaced by one gk command saves no turn.
	"raw-history-search": "find",
}

// gkForGroup is the default git-kit call a run of the group collapses into.
// Every group but "integration" maps 1:1; see gkCommandForRun for why that one
// has to be read off the run instead.
var gkForGroup = map[string]string{
	"context":     "git-kit context",
	"commit":      "git-kit commit",
	"integration": "git-kit pull",
	"switch":      "git-kit switch",
	"worktree":    "git-kit worktree",
	"diff":        "git-kit diff",
	"stash":       "git-kit stash",
	"apply":       "git-kit apply",
	"find":        "git-kit find",
}

// gkCommandForRun names the git-kit call for THIS run, refining gkForGroup with
// what the run actually did.
//
// Only "integration" needs it, and it needs it badly: the group spans pull,
// fetch+merge and fetch+rebase, which are different operations on different
// refs. Answering "git-kit pull" for a `git merge origin/feature` run is worse
// than answering nothing — gk pull integrates the UPSTREAM, so an agent that
// follows the nudge merges a branch it never asked for. Naming the wrong verb
// is the same defect class as reporting a covered command as a gap: the hook
// speaks with gk's authority, so a confident wrong answer costs more than
// silence.
//
// The LAST explicit merge/rebase in the run wins: a run reads fetch-then-merge,
// so the trailing verb is the integration that actually happened while the
// fetch only fed it. With no such verb the run really is a pull, and the
// default stands.
//
// `git rebase <upstream>` maps to gk sync, NOT gk rebase: gk rebase is the
// history-rewrite planner (the `rebase -i` replacement) and takes no positional
// ref at all, so naming it would hand the agent a command that cannot parse.
// gk sync rebases HEAD onto the BASE branch and likewise takes no ref, so the
// ref is dropped there — it is only the right answer when the rebase target is
// the base, and inventing `gk sync <ref>` would be a syntax that does not exist.
func gkCommandForRun(group string, cmds []string) string {
	base := gkForGroup[group]
	if group != "integration" {
		return base
	}
	out := base
	for _, cmd := range cmds {
		for _, seg := range classifyCommand(cmd).Segments {
			if seg.Tool != "git" {
				continue
			}
			sc, args, ok := gitSubcommand(seg.Text)
			if !ok {
				continue
			}
			switch sc {
			case "merge":
				out = "git-kit merge"
				if ref := firstRefOperand(args); ref != "" {
					out += " " + ref
				}
			case "rebase":
				out = "git-kit sync"
			}
		}
	}
	return out
}

// firstRefOperand returns the leading non-flag operand of a git segment — the
// ref a merge/rebase names. Shell redirections (`2>&1`) and pipes survive
// segment splitting as operand-shaped tokens, so anything carrying shell
// metacharacters is skipped rather than reported back as a branch name.
func firstRefOperand(args []string) string {
	for _, a := range args {
		a = trimShellToken(a)
		if a == "" || a == "--" || strings.HasPrefix(a, "-") {
			continue
		}
		if strings.ContainsAny(a, "<>|&;()") {
			continue
		}
		return a
	}
	return ""
}

// CollapseGroups returns the collapse group keys (gkForGroup's domain), sorted.
// It exists so cli-side mirrors of the group set (the tuned contract's leak
// phrases) can be wiring-tested against the real groups instead of silently
// drifting when one is added here.
func CollapseGroups() []string {
	groups := make([]string, 0, len(gkForGroup))
	for g := range gkForGroup {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	return groups
}

// groupPrecedence resolves per-turn PRIMARY attribution: a turn belongs to
// exactly ONE collapse group, write groups before read groups, so a compound
// like `git commit … && git log --oneline -1` is a commit turn — the trailing
// verification probe never extends a context run and by_group sums never
// double-count a turn.
var groupPrecedence = []string{
	"commit", "integration", "apply", "stash", "switch", "worktree", // write
	"context", "diff", "find", // read
}

// readOnlyCollapseGroups are the probe groups whose runs a mutating turn
// severs: probes before and after a state change observe different repos, so
// one gk call can never replace both sides.
var readOnlyCollapseGroups = map[string]bool{"context": true, "diff": true, "find": true}

// mutatingGitVerbs are git subcommands that change repo state (index,
// worktree, refs, or remotes) in every invocation form. Turns containing one
// terminate read-only probe runs — including mutations the finding
// classifiers do not map (`git reset --soft HEAD~1`, `git am`), which were
// previously invisible to the run splitter.
var mutatingGitVerbs = map[string]bool{
	"add": true, "am": true, "apply": true, "checkout": true, "cherry-pick": true,
	"clean": true, "commit": true, "fetch": true, "filter-repo": true,
	"merge": true, "mv": true, "pull": true, "push": true, "rebase": true,
	"reset": true, "restore": true, "revert": true, "rm": true, "switch": true,
}

// gitSegmentMutates reports whether one git segment changes repo state.
// stash list/show only read; tag/branch mutate only in their delete/move/
// create-with-flags forms — the bare forms are probes.
func gitSegmentMutates(subcmd string, args []string) bool {
	if mutatingGitVerbs[subcmd] {
		return true
	}
	switch subcmd {
	case "stash":
		if len(args) == 0 {
			return true
		}
		switch trimShellToken(args[0]) {
		case "list", "show":
			return false
		}
		return true
	case "tag":
		return hasAnyArg(args, "-d", "--delete", "-a", "--annotate", "-s", "--sign", "-f", "--force", "-m")
	case "branch":
		return hasAnyArg(args, "-d", "-D", "--delete", "-m", "-M", "--move", "-c", "-C", "--copy", "-f", "--force", "-u", "--set-upstream-to", "--unset-upstream")
	default:
		return false
	}
}

// commandMutates reports whether any git segment of cmd mutates repo state.
func commandMutates(cmd string) bool {
	for _, seg := range classifyCommand(cmd).Segments {
		if seg.Tool != "git" {
			continue
		}
		if subcmd, args, ok := gitSubcommand(seg.Text); ok && gitSegmentMutates(subcmd, args) {
			return true
		}
	}
	return false
}

// trivialPayloadTools are non-git tools that never do independent work. The
// stream filters below need argument-aware handling: `git log | head -3` only
// formats git output, while `head -3 notes.txt` reads data the gk call cannot
// replace. `ls` is deliberately absent because it is always a separate query.
var trivialPayloadTools = map[string]bool{
	"echo": true, "printf": true, "cd": true, "true": true,
}

var streamFilterTools = map[string]bool{
	"grep": true, "sed": true, "head": true, "tail": true,
	"wc": true, "sort": true, "cut": true, "tr": true,
}

// commandPayloadTrivial reports whether every non-git segment of cmd is
// trivial formatting. git-kit/gk segments count as payload: a turn that
// already runs git-kit is not a turn gk would remove.
func commandPayloadTrivial(cmd string) bool {
	groups := map[string]bool{}
	for _, seg := range classifyCommand(cmd).Segments {
		if seg.Tool == "git" {
			subcmd, args, ok := gitSubcommand(seg.Text)
			kind := gitSegmentFinding(subcmd, args)
			group := collapseGroupForKind[kind]
			if !ok || group == "" {
				return false
			}
			groups[group] = true
			continue
		}
		if !nonGitSegmentTrivial(seg) {
			return false
		}
	}
	if len(groups) > 1 {
		primary := primaryGroupOf(groups, commandMutates(cmd))
		if readOnlyCollapseGroups[primary] {
			return false
		}
		for group := range groups {
			// A write plus a small orientation check is one workflow (`commit`
			// followed by `log -1`). Exact diff/search work still survives.
			if group != primary && group != "context" {
				return false
			}
		}
	}
	return true
}

// nonGitSegmentTrivial distinguishes a pipe formatter from an independent
// file query. A formatter with an explicit input file or input redirection is
// payload: removing the neighboring git probes would not remove that turn.
func nonGitSegmentTrivial(seg shellSegment) bool {
	if trivialPayloadTools[seg.Tool] {
		return true
	}
	if !streamFilterTools[seg.Tool] {
		return false
	}
	fields := shellFields(seg.Text)
	for _, field := range fields[1:] {
		tok := trimShellToken(field)
		if tok == "<" || (strings.HasPrefix(tok, "<") && !strings.HasPrefix(tok, "<<")) {
			return false
		}
	}
	return !streamFilterReadsFile(seg.Tool, fields[1:])
}

func streamFilterReadsFile(tool string, rawArgs []string) bool {
	args := make([]string, 0, len(rawArgs))
	for _, raw := range rawArgs {
		a := trimShellToken(raw)
		if a == "" || strings.ContainsAny(a, "|;&>") {
			continue
		}
		args = append(args, a)
	}
	switch tool {
	case "grep":
		positional := 0
		skipNext := false
		for _, a := range args {
			if skipNext {
				skipNext = false
				continue
			}
			if a == "-r" || a == "-R" || strings.Contains(a, "r") && strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
				return true
			}
			if a == "-f" || a == "--file" || strings.HasPrefix(a, "--file=") {
				return true
			}
			if a == "-e" || a == "--regexp" {
				skipNext = true
				continue
			}
			if !strings.HasPrefix(a, "-") {
				positional++
			}
		}
		return positional > 1 // pattern plus at least one input path
	case "sed":
		positional := 0
		skipNext := false
		for _, a := range args {
			if skipNext {
				skipNext = false
				continue
			}
			if a == "-f" || strings.HasPrefix(a, "-f") && len(a) > 2 {
				return true
			}
			if a == "-e" {
				skipNext = true
				continue
			}
			if !strings.HasPrefix(a, "-") {
				positional++
			}
		}
		return positional > 1 // script plus at least one input path
	case "head", "tail":
		skipNext := false
		for _, a := range args {
			if skipNext {
				skipNext = false
				continue
			}
			if a == "-n" || a == "-c" {
				skipNext = true
				continue
			}
			if !strings.HasPrefix(a, "-") {
				return true
			}
		}
		return false
	case "wc":
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				return true
			}
		}
		return false
	case "sort", "cut":
		// These are normally pipe filters. Conservatively treat any bare operand
		// as a path except the value consumed by their common value flags.
		skipNext := false
		for _, a := range args {
			if skipNext {
				skipNext = false
				continue
			}
			if a == "-k" || a == "--key" || a == "-t" || a == "--field-separator" || a == "-d" || a == "--delimiter" || a == "-f" || a == "--fields" || a == "-c" || a == "--characters" || a == "-b" || a == "--bytes" {
				skipNext = true
				continue
			}
			if !strings.HasPrefix(a, "-") {
				return true
			}
		}
		return false
	case "tr":
		return false // operands are character sets; redirection was checked above
	default:
		return true
	}
}

// --- the git-kit lens -------------------------------------------------------
//
// Everything above answers ONE question: how many turns would reaching for gk
// remove? By construction it cannot see a second kind of waste — three `gk
// context` calls across three turns are two wasted round-trips, but
// commandPayloadTrivial discounts any turn that already ran git-kit, so the
// number is zero by design.
//
// That blind spot grows exactly as adoption rises, so the remaining waste
// migrates into the part the metric cannot see. The lens below measures it,
// and it is kept STRICTLY SEPARATE from the adoption number: merging them would
// silently re-baseline ~/.gk/audit-history.jsonl, and that file already carries
// two classifier discontinuities readers are warned not to compare across.

// isGkTool reports whether a shell segment's leading tool is git-kit. Both
// spellings count — `gk` is the short alias the contract discourages, but it
// runs the same binary.
func isGkTool(tool string) bool { return tool == "gk" || tool == "git-kit" }

// gkSubcommand extracts the git-kit verb and its arguments from one segment,
// mirroring gitSubcommand: skip the global flags that may precede the verb
// (--repo, --json, -d …) and require the verb to be shaped like a subcommand.
func gkSubcommand(segment string) (string, []string, bool) {
	fields := shellFields(segment)
	for i := 0; i < len(fields); i++ {
		if !isGkTool(trimShellToken(fields[i])) {
			continue
		}
		args := fields[i+1:]
		for len(args) > 0 {
			head := trimShellToken(args[0])
			if head == "" {
				args = args[1:]
				continue
			}
			if head == "--repo" || head == "--provider" || head == "--lang" {
				if len(args) >= 2 {
					args = args[2:]
					continue
				}
				return "", nil, false
			}
			if strings.HasPrefix(head, "-") {
				args = args[1:]
				continue
			}
			if !isGitSubcommandToken(head) {
				return "", nil, false
			}
			return head, args[1:], true
		}
		return "", nil, false
	}
	return "", nil, false
}

// gkReadOnlyVerbs are the git-kit verbs that only observe. Everything absent
// counts as mutating, which is the safe default here: a false "mutating" only
// severs a probe run and gives up a saving, while a false "read-only" would
// claim a collapse across a state change that never existed.
var gkReadOnlyVerbs = map[string]bool{
	"status": true, "st": true, "context": true, "log": true, "slog": true,
	"diff": true, "find": true, "local": true, "next": true, "explain": true,
	"ask": true, "hint": true, "precheck": true, "snapshots": true,
	"prompt-info": true, "doctor": true, "session": true, "issue": true,
	"inbox": true, "changelog": true, "lint-commit": true, "branch-check": true,
}

// gkCollapseGroup maps a git-kit read verb to the group whose run one single
// call absorbs — the same groups the raw-git lens uses, so both views agree on
// what "one gk call" means.
//
// Deliberately absent, for the same reasons their raw counterparts are:
//   - branch/find: `gk branch list` and `gk find` are already one call per
//     question, so repeats are DIFFERENT questions, not a re-probe.
//   - `gk log` carrying an operand: a revision range or a path scope is its own
//     question, which `gk context` never answers.
func gkCollapseGroup(verb string, args []string) string {
	// `gk diff --help` does not read the repo at all. Learning a verb's surface
	// and then querying with it are two different questions, and folding them
	// together would invent a saving that could not exist — the measured corpus
	// had exactly one candidate run and this was it.
	if hasArg(args, "--help") || hasArg(args, "-h") {
		return ""
	}
	switch verb {
	case "status", "st", "context":
		return "context"
	case "log", "slog":
		if gkLogHasOperand(args) {
			return "" // a range or path scope — not "where am I"
		}
		// KNOWN ASYMMETRY with the raw lens. On the raw side, `git log -20` and
		// `git log --since=1w` leave the context group (see logQueryBeyondContext
		// in audit.go): gk context cannot answer them, so crediting a collapse
		// would claim a saving that does not exist. Here the same shapes still
		// count as context, and deliberately so — this lens measures whether a
		// gk USER re-probed across turns, and `gk log -n 20` beside `gk status`
		// is that same orientation habit, whatever the count says. The two
		// lenses answer different questions, so they are not made to agree; what
		// would be wrong is letting the difference pass unremarked.
		return "context"
	case "diff":
		return "diff"
	default:
		return ""
	}
}

// gkLogValueFlags are `gk log` flags whose value is the NEXT token. Without
// them `gk log -n 5` reads 5 as a revision operand and the probe is discarded —
// the flag's argument is not the question being asked.
var gkLogValueFlags = map[string]bool{
	"-n": true, "--limit": true, "--since": true, "--format": true,
	"--lang": true, "--provider": true, "--vis": true,
	"-U": true, "--context": true,
}

// gkLogHasOperand reports whether `gk log` was given a revision or path — the
// forms that ask their own question rather than "where am I".
func gkLogHasOperand(args []string) bool {
	skipNext := false
	for _, raw := range args {
		a := trimShellToken(raw)
		if skipNext {
			skipNext = false
			continue
		}
		if a == "" {
			continue
		}
		if a == "--" {
			return true // pathspecs follow — a scoped history question
		}
		if strings.HasPrefix(a, "-") {
			name, _, hasValue := strings.Cut(a, "=")
			if gkLogValueFlags[name] && !hasValue {
				skipNext = true
			}
			continue
		}
		if strings.ContainsAny(a, "<>|&;()") {
			continue // shell noise that survived segment splitting
		}
		return true
	}
	return false
}

// gkSegmentMutates reports whether one git-kit segment changes repo state.
func gkSegmentMutates(verb string, args []string) bool {
	if !gkReadOnlyVerbs[verb] {
		return true
	}
	// The read-only verbs with a mutating subcommand form.
	if verb == "session" || verb == "doctor" {
		return hasArg(args, "--fix")
	}
	return false
}

// gkCommandGroups returns the collapse groups a command's git-kit segments
// belong to — the gk-lens twin of commandGroups.
func gkCommandGroups(cmd string) map[string]bool {
	groups := map[string]bool{}
	for _, seg := range classifyCommand(cmd).Segments {
		if !isGkTool(seg.Tool) {
			continue
		}
		verb, args, ok := gkSubcommand(seg.Text)
		if !ok {
			continue
		}
		if g := gkCollapseGroup(verb, args); g != "" {
			groups[g] = true
		}
	}
	return groups
}

// gkCommandMutates reports whether any git-kit segment of cmd mutates state.
// A raw git mutation counts too: the barrier is "did the repo change between
// these probes", and it does not matter which binary changed it.
func gkCommandMutates(cmd string) bool {
	for _, seg := range classifyCommand(cmd).Segments {
		if isGkTool(seg.Tool) {
			if verb, args, ok := gkSubcommand(seg.Text); ok && gkSegmentMutates(verb, args) {
				return true
			}
			continue
		}
		if seg.Tool == "git" {
			if subcmd, args, ok := gitSubcommand(seg.Text); ok && gitSegmentMutates(subcmd, args) {
				return true
			}
		}
	}
	return false
}

// gkPayloadTrivial mirrors commandPayloadTrivial with the roles swapped: for
// this lens the git-kit segments are the work, so RAW GIT counts as payload —
// folding `gk context` away does not remove a turn that also ran `git show`.
func gkPayloadTrivial(cmd string) bool {
	for _, seg := range classifyCommand(cmd).Segments {
		if isGkTool(seg.Tool) {
			continue
		}
		if !nonGitSegmentTrivial(seg) {
			return false
		}
	}
	return true
}

// gkGroupTarget is groupTarget for git-kit segments, feeding the same paging
// guard: `gk diff a.go` then `gk diff b.go` inspects different objects and one
// call cannot replace both.
func gkGroupTarget(cmd, group string) (subcmd, target string) {
	for _, seg := range classifyCommand(cmd).Segments {
		if !isGkTool(seg.Tool) {
			continue
		}
		verb, args, ok := gkSubcommand(seg.Text)
		if !ok {
			continue
		}
		if gkCollapseGroup(verb, args) == group {
			return verb, operandSig(args)
		}
	}
	return "", ""
}

// collapseLens is the per-command judgement set the run detector needs. Both
// lenses share every rule that follows (gap tolerance, mutation barriers, the
// paging guard, primary-group attribution) and differ only in which binary's
// segments they read — so the two numbers stay comparable in meaning while
// never being summed.
type collapseLens struct {
	name    string
	groups  func(string) map[string]bool
	mutates func(string) bool
	trivial func(string) bool
	target  func(string, string) (string, string)
}

var gitLens = collapseLens{
	name:    "git",
	groups:  commandGroups,
	mutates: commandMutates,
	trivial: commandPayloadTrivial,
	target:  groupTarget,
}

var gkLens = collapseLens{
	name:    "git-kit",
	groups:  gkCommandGroups,
	mutates: gkCommandMutates,
	trivial: gkPayloadTrivial,
	target:  gkGroupTarget,
}

// CollapsibleRun is a maximal run of same-group git commands spread across
// distinct turns that one git-kit call would have replaced. TurnsSaved is the
// number of agent round-trips removed: a run touching N distinct turns folds to
// one call, saving N-1.
type CollapsibleRun struct {
	Group      string   `json:"group"`
	GkCommand  string   `json:"gk_command"`
	Repo       string   `json:"repo,omitempty"`
	Turns      []int    `json:"turns"`
	Commands   []string `json:"commands"`
	TurnsSaved int      `json:"turns_saved"`
}

// commandGroups returns the distinct collapse groups a command belongs to,
// reusing the audit's one classifier so a command and the turn engine agree on
// what each git segment is. A shell chain (`git status && git add`) can belong
// to more than one group; it still occupies a single turn.
func commandGroups(cmd string) map[string]bool {
	groups := map[string]bool{}
	for _, seg := range classifyCommand(cmd).Segments {
		if seg.Tool != "git" {
			continue
		}
		subcmd, args, ok := gitSubcommand(seg.Text)
		if !ok {
			continue
		}
		if kind := gitSegmentFinding(subcmd, args); kind != "" {
			if g := collapseGroupForKind[kind]; g != "" {
				groups[g] = true
			}
		}
	}
	return groups
}

// turnHit is one distinct turn attributed to a group. subcmd/target carry the
// group's git verb and its operand signature so the paging guard can tell
// repeated inspection of DIFFERENT objects (`git show A`, `git show B`) —
// which one gk call cannot replace — from a genuine probe sequence.
type turnHit struct {
	turn   int
	repo   string
	cmd    string
	subcmd string
	target string
}

// turnAttr is one distinct turn's collapse attribution: the single PRIMARY
// group the turn counts toward plus the flags the run splitter needs.
type turnAttr struct {
	turnHit
	group    string // "" when the turn is not collapsible
	mutating bool   // the turn ran a state-changing git segment
}

// attributeTurns folds ordered turn events into per-turn attributions, in
// ascending turn order. Each distinct turn gets at most ONE collapse group
// (primaryGroupOf), is flagged mutating when any of its commands mutates, and
// is discounted entirely when any command carries non-trivial non-git payload
// (`git log -1; cargo clippy` — the turn exists for cargo). Failed calls
// (IsError) are dropped: a failed attempt is not a turn gk would have saved.
func attributeTurns(events []TurnEvent, lens collapseLens) []turnAttr {
	type accum struct {
		groups     map[string]bool
		mutating   bool
		discounted bool
		repo       string
		cmds       []string
	}
	var order []int
	acc := map[int]*accum{}
	for _, ev := range events {
		if ev.IsError {
			continue
		}
		a := acc[ev.Turn]
		if a == nil {
			a = &accum{groups: map[string]bool{}}
			acc[ev.Turn] = a
			order = append(order, ev.Turn)
		}
		if a.repo == "" {
			a.repo = ev.Repo
		}
		a.cmds = append(a.cmds, ev.Cmd)
		a.mutating = a.mutating || lens.mutates(ev.Cmd)
		a.discounted = a.discounted || !lens.trivial(ev.Cmd)
		for g := range lens.groups(ev.Cmd) {
			a.groups[g] = true
		}
	}
	sort.Ints(order)

	out := make([]turnAttr, 0, len(order))
	for _, tn := range order {
		a := acc[tn]
		ta := turnAttr{turnHit: turnHit{turn: tn, repo: a.repo}, mutating: a.mutating}
		if !a.discounted {
			ta.group = primaryGroupOf(a.groups, a.mutating)
		}
		if ta.group != "" {
			for _, c := range a.cmds {
				if lens.groups(c)[ta.group] {
					ta.cmd = c
					ta.subcmd, ta.target = lens.target(c, ta.group)
					break
				}
			}
		}
		out = append(out, ta)
	}
	return out
}

// primaryGroupOf picks the single group a turn is attributed to: first match
// in groupPrecedence (write groups win over read groups). A mutating turn is
// never attributed to a read-only group — the turn exists to change state, so
// gk context/diff cannot replace it and it must never join a probe run.
func primaryGroupOf(groups map[string]bool, mutating bool) string {
	for _, g := range groupPrecedence {
		if !groups[g] {
			continue
		}
		if mutating && readOnlyCollapseGroups[g] {
			continue
		}
		return g
	}
	return ""
}

// groupTarget returns the git verb and operand signature of the segment that
// puts cmd in group, for the paging guard.
func groupTarget(cmd, group string) (subcmd, target string) {
	for _, seg := range classifyCommand(cmd).Segments {
		if seg.Tool != "git" {
			continue
		}
		sc, args, ok := gitSubcommand(seg.Text)
		if !ok {
			continue
		}
		if kind := gitSegmentFinding(sc, args); kind != "" && collapseGroupForKind[kind] == group {
			return sc, operandSig(args)
		}
	}
	return "", ""
}

// operandSig joins a git segment's non-flag operands (paths, refs, pathspecs),
// sorted, so two commands targeting the same objects compare equal.
func operandSig(args []string) string {
	var ops []string
	for _, a := range args {
		a = trimShellToken(a)
		if a == "" || a == "--" || strings.HasPrefix(a, "-") {
			continue
		}
		ops = append(ops, a)
	}
	sort.Strings(ops)
	return strings.Join(ops, " ")
}

// DetectCollapsibleRuns finds, per collapse group, the maximal local runs of
// that group's turns. Each distinct turn carries exactly one PRIMARY group
// (attributeTurns), so a compound write-plus-probe turn counts once and
// by_group sums never double-count. Runs break across a repo/worktree
// boundary, and read-only probe runs additionally break on any interleaved
// mutating turn. maxGap is the interleave tolerance (see collapseMaxGap).
func DetectCollapsibleRuns(events []TurnEvent, maxGap int) []CollapsibleRun {
	return detectRuns(events, maxGap, gitLens)
}

// DetectGkReprobeRuns is DetectCollapsibleRuns read through the git-kit lens:
// runs of gk's own read verbs that one gk call would have answered. It measures
// waste that survives adoption, so its result must never be added to
// DetectCollapsibleRuns' — the two answer different questions.
func DetectGkReprobeRuns(events []TurnEvent, maxGap int) []CollapsibleRun {
	return detectRuns(events, maxGap, gkLens)
}

func detectRuns(events []TurnEvent, maxGap int, lens collapseLens) []CollapsibleRun {
	attrs := attributeTurns(events, lens)
	var mutatingTurns []int // ascending, for the read-only barrier check
	byGroup := map[string][]turnHit{}
	for _, ta := range attrs {
		if ta.mutating {
			mutatingTurns = append(mutatingTurns, ta.turn)
		}
		if ta.group == "" {
			continue
		}
		byGroup[ta.group] = append(byGroup[ta.group], ta.turnHit)
	}

	var runs []CollapsibleRun
	for _, group := range sortedKeys(byGroup) {
		hits := byGroup[group] // ascending by turn (attributeTurns order)
		var barriers []int
		if readOnlyCollapseGroups[group] {
			barriers = mutatingTurns
		}
		for _, run := range splitRuns(hits, maxGap, barriers) {
			if len(run) < 2 {
				continue // one turn = nothing to collapse
			}
			turns := make([]int, len(run))
			cmds := make([]string, len(run))
			for i, h := range run {
				turns[i] = h.turn
				cmds[i] = h.cmd
			}
			runs = append(runs, CollapsibleRun{
				Group:      group,
				GkCommand:  gkCommandForRun(group, cmds),
				Repo:       run[0].repo,
				Turns:      turns,
				Commands:   cmds,
				TurnsSaved: len(run) - 1,
			})
		}
	}
	return runs
}

// maxTurnRuns caps how many collapsible runs the report carries as evidence,
// keeping the envelope bounded on large session corpora. They are sorted by
// turns saved first, so the cap keeps the highest-leverage examples.
const maxTurnRuns = 20

// TurnMetrics is the turn-reduction view of the audit: how many agent
// round-trips the scanned sessions could have saved by reaching for git-kit.
// It is computed only for Claude sessions (the message-id turn boundary and the
// tool_use/tool_result join the model needs) and only when the turn metric is
// requested, so the occurrence output stays byte-identical by default.
type TurnMetrics struct {
	Source              string           `json:"source"`
	GitTurns            int              `json:"git_turns"`
	EstimatedTurnsSaved int              `json:"estimated_turns_saved"`
	Rate                float64          `json:"rate"`
	ByGroup             map[string]int   `json:"by_group,omitempty"`
	Runs                []CollapsibleRun `json:"runs,omitempty"`
	// GkReprobe is the SECOND, independent number: waste that survives adoption.
	// It is reported beside the fields above and never folded into them —
	// EstimatedTurnsSaved answers "what would adopting gk remove", this answers
	// "what would using gk better remove". Summing them would double-count
	// nothing and mean nothing, and it would re-baseline the recorded history.
	GkReprobe *GkReprobeMetrics `json:"gk_reprobe,omitempty"`
}

// GkReprobeMetrics counts turns an already-adopted session spent re-asking gk
// a question one gk call answers. GkTurns is its own denominator: turns that
// ran git-kit at all.
type GkReprobeMetrics struct {
	GkTurns    int              `json:"gk_turns"`
	TurnsSaved int              `json:"turns_saved"`
	Rate       float64          `json:"rate"`
	ByGroup    map[string]int   `json:"by_group,omitempty"`
	Runs       []CollapsibleRun `json:"runs,omitempty"`
}

// turnEventsContribution computes one session's distinct git-turn count and its
// collapsible runs from already-parsed turn events. Git turns are the
// denominator for the rate: a turn counts once no matter how many git commands
// it ran. The git-kit lens gets the same treatment in its own return values.
func turnEventsContribution(events []TurnEvent) (gitTurns int, runs []CollapsibleRun, gkTurns int, gkRuns []CollapsibleRun) {
	seenGit := map[int]bool{}
	seenGk := map[int]bool{}
	for _, ev := range events {
		c := classifyCommand(ev.Cmd)
		if c.RawGit > 0 && !seenGit[ev.Turn] {
			seenGit[ev.Turn] = true
			gitTurns++
		}
		if (c.GitKit > 0 || c.GKShort > 0) && !seenGk[ev.Turn] {
			seenGk[ev.Turn] = true
			gkTurns++
		}
	}
	return gitTurns, DetectCollapsibleRuns(events, collapseMaxGap),
		gkTurns, DetectGkReprobeRuns(events, collapseMaxGap)
}

// sortRunsBySaved orders runs by turns saved (desc), then group, then first
// turn — deterministic and highest-leverage first for the evidence cap.
func sortRunsBySaved(runs []CollapsibleRun) {
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].TurnsSaved != runs[j].TurnsSaved {
			return runs[i].TurnsSaved > runs[j].TurnsSaved
		}
		if runs[i].Group != runs[j].Group {
			return runs[i].Group < runs[j].Group
		}
		return len(runs[i].Turns) > 0 && len(runs[j].Turns) > 0 && runs[i].Turns[0] < runs[j].Turns[0]
	})
}

// CollapseNudge is a real-time opportunity: the pending command continues a
// raw-git run the agent already started in a recent turn, so one git-kit call
// would have covered both. It is what turns the audit's after-the-fact measure
// into prevention at the PreToolUse hook.
type CollapseNudge struct {
	Group      string   // the collapse group both share, e.g. "context"
	GkCommand  string   // the single git-kit call that covers the run
	PriorTurns int      // how many recent turns already ran this group
	Recent     []string // the recent commands, for the message
}

// CollapseNudgeFor reports whether running `current` continues a same-group raw
// run from the recent turns — i.e. the agent could have folded them into one
// git-kit call. recent is oldest→newest; lastTurn is the session's last
// allocated turn index (SessionTurnsWithLast), which the pending command runs
// right after. A prior turn folds in only when its distance to that pending
// turn is within lookback — the batch detector's gap tolerance (splitRuns'
// gapOK). Distance is measured in turn indices, not inspected events: turns
// occupied by non-shell tools (Read/Edit) allocate indices without emitting
// events, and a probe separated from the pending one by several such turns is
// exactly what the turn metric refuses to count as collapsible. It applies
// the same per-turn primary attribution as the batch detector: `current` gets
// exactly one group (write groups win, non-trivial payload discounts), a
// mutating recent turn severs a read-only probe run — so a commit flow with
// trailing verification probes never nudges "use gk context" — and the repo
// and paging guards still apply.
func CollapseNudgeFor(current string, recent []TurnEvent, lastTurn, lookback int) *CollapseNudge {
	if lookback <= 0 || !commandPayloadTrivial(current) {
		return nil
	}
	g := primaryGroupOf(commandGroups(current), commandMutates(current))
	if g == "" {
		return nil
	}
	curRepo := repoScope(current)
	curSub, curTgt := groupTarget(current, g)

	// The nudge stays on the git lens: it fires on a PENDING raw git command,
	// telling the agent to reach for gk. Nagging someone who already ran `gk
	// context` twice is a different message and not this hook's job.
	attrs := attributeTurns(recent, gitLens)
	pending := lastTurn + 1 // the turn the pending command will occupy
	var prior []string
	for i := len(attrs) - 1; i >= 0; i-- {
		ta := attrs[i]
		if pending-ta.turn > lookback {
			break // outside the gap tolerance — older turns can't fold in
		}
		if readOnlyCollapseGroups[g] && ta.mutating {
			break // a state change severs the probe run — older turns can't fold in
		}
		if ta.group != g {
			continue
		}
		if curRepo != "" && ta.repo != "" && curRepo != ta.repo {
			continue // different repo — not collapsible
		}
		if ta.subcmd == curSub && curTgt != "" && ta.target != "" && curTgt != ta.target {
			continue // same verb, different target — paging, not a run
		}
		prior = append(prior, ta.cmd)
	}
	if len(prior) == 0 {
		return nil
	}
	// prior was collected newest-first; gkCommandForRun's "last verb wins" rule
	// needs the run in the order it happened, with the pending command last —
	// in a fetch-then-merge shape that pending command IS the merge, and it is
	// what decides which git-kit verb the nudge may name.
	run := make([]string, 0, len(prior)+1)
	for i := len(prior) - 1; i >= 0; i-- {
		run = append(run, prior[i])
	}
	run = append(run, current)
	return &CollapseNudge{Group: g, GkCommand: gkCommandForRun(g, run), PriorTurns: len(prior), Recent: prior}
}

// splitRuns breaks an ordered slice of distinct-turn hits into maximal runs:
// adjacent hits stay together while the turn gap is within maxGap, the repo
// scope is compatible, and no barrier turn (ascending; a mutating turn, for
// read-only groups) sits strictly between them.
func splitRuns(hits []turnHit, maxGap int, barriers []int) [][]turnHit {
	barrierBetween := func(a, b int) bool {
		i := sort.SearchInts(barriers, a+1)
		return i < len(barriers) && barriers[i] < b
	}
	var runs [][]turnHit
	var cur []turnHit
	for _, h := range hits {
		if len(cur) == 0 {
			cur = []turnHit{h}
			continue
		}
		prev := cur[len(cur)-1]
		gapOK := h.turn-prev.turn <= maxGap+1
		repoOK := prev.repo == "" || h.repo == "" || prev.repo == h.repo
		// Paging guard: the same verb aimed at different objects (git show A,
		// git show B) is separate inspection, not a collapsible sequence — one
		// gk call cannot stand in for distinct targets.
		pagingSplit := prev.subcmd != "" && prev.subcmd == h.subcmd &&
			prev.target != "" && h.target != "" && prev.target != h.target
		if gapOK && repoOK && !pagingSplit && !barrierBetween(prev.turn, h.turn) {
			cur = append(cur, h)
			continue
		}
		runs = append(runs, cur)
		cur = []turnHit{h}
	}
	if len(cur) > 0 {
		runs = append(runs, cur)
	}
	return runs
}

func sortedKeys(m map[string][]turnHit) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
