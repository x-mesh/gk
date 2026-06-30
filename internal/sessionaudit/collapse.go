package sessionaudit

import (
	"sort"
	"strconv"
	"strings"
)

// collapseMaxGap is how many interleaved turns may sit between two same-group
// git turns and still count as one collapsible run. 1 tolerates the common
// "git status → Read a file → git diff" shape while keeping genuinely unrelated,
// far-apart probes as separate (non-collapsible) uses.
const collapseMaxGap = 1

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

// turnHit is one distinct turn that contributed a command of a given group.
// subcmd/target carry the group's git verb and its operand signature so the
// paging guard can tell repeated inspection of DIFFERENT objects
// (`git show A`, `git show B`) — which one gk call cannot replace — from a
// genuine probe sequence.
type turnHit struct {
	turn   int
	repo   string
	cmd    string
	subcmd string
	target string
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
// that group's git commands across distinct turns. Failed calls (IsError) are
// dropped first — a failed attempt is not a turn gk would have saved, and this
// also collapses failure→retry pairs to the single successful turn. Runs break
// across a repo/worktree boundary so commands from different working dirs are
// never merged. maxGap is the interleave tolerance (see collapseMaxGap).
func DetectCollapsibleRuns(events []TurnEvent, maxGap int) []CollapsibleRun {
	// group -> ordered distinct-turn hits (a turn appears once per group even if
	// it ran two same-group commands: same turn saves nothing among itself).
	byGroup := map[string][]turnHit{}
	seen := map[string]bool{} // group|turn already recorded
	for _, ev := range events {
		if ev.IsError {
			continue
		}
		for g := range commandGroups(ev.Cmd) {
			key := g + "|" + strconv.Itoa(ev.Turn)
			if seen[key] {
				continue
			}
			seen[key] = true
			sc, target := groupTarget(ev.Cmd, g)
			byGroup[g] = append(byGroup[g], turnHit{turn: ev.Turn, repo: ev.Repo, cmd: ev.Cmd, subcmd: sc, target: target})
		}
	}

	var runs []CollapsibleRun
	for _, group := range sortedKeys(byGroup) {
		hits := byGroup[group]
		sort.Slice(hits, func(i, j int) bool { return hits[i].turn < hits[j].turn })
		for _, run := range splitRuns(hits, maxGap) {
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

// turnContribution computes one Claude session's distinct git-turn count and its
// collapsible runs. Git turns are the denominator for the rate: a turn counts
// once no matter how many git commands it ran.
func turnContribution(data []byte) (gitTurns int, runs []CollapsibleRun) {
	events := SessionTurns(data)
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

// splitRuns breaks an ordered slice of distinct-turn hits into maximal runs:
// adjacent hits stay together while the turn gap is within maxGap and the repo
// scope is compatible.
func splitRuns(hits []turnHit, maxGap int) [][]turnHit {
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
		if gapOK && repoOK && !pagingSplit {
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
