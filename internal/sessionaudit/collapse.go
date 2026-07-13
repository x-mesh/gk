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
	// all of them in one call. raw-branch-list and raw-range-compare are absent
	// on purpose — the first IS a 1:1 swap (gk branch list) and the second has no
	// verb at all, so neither may claim turn savings.
	"raw-history-search": "find",
}

// gkForGroup is the single git-kit call a run of the group collapses into.
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

// trivialPayloadTools are non-git tools that only format or page output. A
// turn whose non-git segments all come from this set still exists for its git
// work; anything else (cargo, go, npm, …) means the turn would survive even
// with its git segments folded into a gk call — such turns are not saveable.
var trivialPayloadTools = map[string]bool{
	"echo": true, "printf": true, "grep": true, "sed": true, "head": true,
	"tail": true, "wc": true, "sort": true, "cut": true, "tr": true,
	"cd": true, "true": true, "ls": true,
}

// commandPayloadTrivial reports whether every non-git segment of cmd is
// trivial formatting. git-kit/gk segments count as payload: a turn that
// already runs git-kit is not a turn gk would remove.
func commandPayloadTrivial(cmd string) bool {
	for _, seg := range classifyCommand(cmd).Segments {
		if seg.Tool == "git" {
			continue
		}
		if !trivialPayloadTools[seg.Tool] {
			return false
		}
	}
	return true
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
func attributeTurns(events []TurnEvent) []turnAttr {
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
		a.mutating = a.mutating || commandMutates(ev.Cmd)
		a.discounted = a.discounted || !commandPayloadTrivial(ev.Cmd)
		for g := range commandGroups(ev.Cmd) {
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
				if commandGroups(c)[ta.group] {
					ta.cmd = c
					ta.subcmd, ta.target = groupTarget(c, ta.group)
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
	attrs := attributeTurns(events)
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
				GkCommand:  gkForGroup[group],
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
}

// turnEventsContribution computes one session's distinct git-turn count and its
// collapsible runs from already-parsed turn events. Git turns are the
// denominator for the rate: a turn counts once no matter how many git commands
// it ran.
func turnEventsContribution(events []TurnEvent) (gitTurns int, runs []CollapsibleRun) {
	seenTurn := map[int]bool{}
	for _, ev := range events {
		if seenTurn[ev.Turn] {
			continue
		}
		if classifyCommand(ev.Cmd).RawGit > 0 {
			seenTurn[ev.Turn] = true
			gitTurns++
		}
	}
	return gitTurns, DetectCollapsibleRuns(events, collapseMaxGap)
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

	attrs := attributeTurns(recent)
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
	return &CollapseNudge{Group: g, GkCommand: gkForGroup[g], PriorTurns: len(prior), Recent: prior}
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
