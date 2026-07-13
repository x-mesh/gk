package sessionaudit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

const (
	defaultMaxFiles = 200
	maxEvidence     = 5
)

// Options controls how much local session history Audit reads.
type Options struct {
	Paths    []string
	Home     string
	MaxFiles int
	// Metric selects which view to compute: "occurrences" (default) keeps the
	// historical output; "turns" / "both" additionally compute the turn-reduction
	// metric over Claude sessions. Unknown/empty == "occurrences".
	Metric string
	// Since, when non-zero, keeps only session files modified at or after it.
	// The caller computes the cutoff (time.Now()-window at the CLI) so this
	// package stays deterministic. Without it the report aggregates the entire
	// session history, which dilutes "is adoption improving now" with sessions
	// that predate any guidance fix.
	Since time.Time
}

type Report struct {
	Schema int `json:"schema"`
	// Since echoes the Options.Since cutoff (RFC3339) when a time window was
	// applied, so consumers know the numbers describe a window, not all history.
	Since    string       `json:"since,omitempty"`
	Files    []FileReport `json:"files"`
	Totals   Totals       `json:"totals"`
	Adoption Adoption     `json:"adoption"`
	Findings []Finding    `json:"findings,omitempty"`
	// Projects rolls adoption up per project (most raw git first), so the
	// global rate stops hiding WHERE agents leak raw git — the target list
	// for contract/hook installs. Claude sessions carry their workspace in
	// the parent directory name; Codex sessions aggregate as one bucket.
	Projects []ProjectAdoption `json:"projects,omitempty"`
	// Turns is the turn-reduction metric, present only when Options.Metric
	// requested it. Additive: existing consumers reading the fields above are
	// unaffected.
	Turns *TurnMetrics `json:"turns,omitempty"`
	// Trend carries the recorded run history when the CLI was asked for it
	// (--trend). Populated by the CLI from the history log, not by Audit —
	// present here so the agent envelope includes it instead of silently
	// dropping the flag on the JSON path.
	Trend []HistoryEntry `json:"trend,omitempty"`
	Notes []string       `json:"notes,omitempty"`
}

// turnsRequested reports whether the turn-reduction metric should be computed.
func turnsRequested(metric string) bool {
	return metric == "turns" || metric == "both"
}

// Adoption tracks how often agents reach for git-kit versus raw git across the
// scanned sessions. It is the regression metric: rerun the audit over time and
// watch Rate climb and CoveredRawHits fall as guidance lands.
type Adoption struct {
	// GitInvocations is RawGit + GitKit + GKShort — every git-shaped call.
	GitInvocations int `json:"git_invocations"`
	GitKit         int `json:"git_kit"`
	// Rate is GitKit / GitInvocations in [0,1].
	Rate float64 `json:"rate"`
	// CoveredRawHits is the number of raw-git pattern hits that already have a
	// git-kit replacement (the sum of covered raw-* findings). These are pure
	// habit leaks: the capability exists, the agent skipped it.
	CoveredRawHits int `json:"covered_raw_hits"`
	// UncoveredRawHits is the number of raw-git hits with no recognized git-kit
	// mapping (the uncovered-raw-git gap finding). Separating these from the rate
	// keeps the adoption number honest: plumbing like `git rev-parse` should not
	// count as a missed git-kit call.
	UncoveredRawHits int `json:"uncovered_raw_hits"`
}

type FileReport struct {
	Path        string `json:"path"`
	Source      string `json:"source"`
	Commands    int    `json:"commands"`
	RawGit      int    `json:"raw_git"`
	GitKit      int    `json:"git_kit"`
	GKShort     int    `json:"gk_short"`
	ShellChains int    `json:"shell_chains"`
}

// ProjectAdoption is one project's share of the adoption picture. Rate uses
// the same definition as the global Adoption.Rate: GitKit over ALL git-shaped
// calls (raw + git-kit + short alias) — the discouraged short alias counts in
// the denominator but never as adoption.
type ProjectAdoption struct {
	Project string  `json:"project"`
	Files   int     `json:"files"`
	RawGit  int     `json:"raw_git"`
	GitKit  int     `json:"git_kit"`
	GKShort int     `json:"gk_short,omitempty"`
	Rate    float64 `json:"rate"`
}

type Totals struct {
	Files       int `json:"files"`
	Commands    int `json:"commands"`
	RawGit      int `json:"raw_git"`
	GitKit      int `json:"git_kit"`
	GKShort     int `json:"gk_short"`
	ShellChains int `json:"shell_chains"`
}

type Finding struct {
	Kind           string   `json:"kind"`
	Severity       string   `json:"severity"`
	Status         string   `json:"status"`
	Count          int      `json:"count"`
	Recommendation string   `json:"recommendation"`
	CoveredBy      []string `json:"covered_by,omitempty"`
	Gap            string   `json:"gap,omitempty"`
	// Subcommands breaks a gap finding down by raw-git subcommand
	// (e.g. {"stash":40,"reset":22}) — the roadmap signal for which verbs
	// git-kit has no answer for. Empty for covered findings.
	Subcommands map[string]int `json:"subcommands,omitempty"`
	// OneShot names the gap subcommands whose raw form is a single call, so a
	// gk verb would save ~0 turns. They are real coverage holes but low
	// leverage — without the label, count-ranking promotes them over
	// multi-turn workflow gaps (an apply/reset recovery arc).
	OneShot  []string   `json:"one_shot,omitempty"`
	Evidence []Evidence `json:"evidence,omitempty"`

	// gapEvidenceSeen tracks which subcommands already carry an evidence
	// sample (one each, so rare subcommands aren't starved by frequent ones).
	gapEvidenceSeen map[string]bool
}

type Evidence struct {
	File    string     `json:"file,omitempty"`
	Command string     `json:"command"`
	Plan    *BatchPlan `json:"plan,omitempty"`
}

// BatchPlan is a git-kit batch --plan payload synthesized from an observed
// shell chain: each mappable raw-git segment becomes a git-kit step, so the
// agent can replace the whole `git … && git … && git …` line with one
// `git-kit batch --plan -` call. Steps carries the executable plan; Omitted
// names the non-git-kit segments (echo, tail, …) that batch cannot run and
// that drop out of the replacement.
type BatchPlan struct {
	Steps   []BatchStep `json:"steps"`
	Omitted []string    `json:"omitted,omitempty"`
}

type BatchStep struct {
	// Args is the git-kit argv for the step, e.g. ["context","--include=diff,log"].
	Args []string `json:"args"`
	// From is the raw git segment this step replaces, for human review.
	From string `json:"from,omitempty"`
}

type fileCandidate struct {
	path string
	info os.FileInfo
}

type findingSpec struct {
	kind, severity, status, recommendation, gap string
	coveredBy                                   []string
}

var findingSpecs = map[string]findingSpec{
	"raw-context-probes": {
		kind:           "raw-context-probes",
		severity:       "medium",
		status:         "covered",
		recommendation: "Use git-kit context --include=diff,log,precheck,remotes instead of separate status/log/diff probes.",
		coveredBy:      []string{"git-kit context --include=diff,log,precheck,remotes", "git-kit context --include=all"},
	},
	"raw-conflict-probes": {
		kind:           "raw-conflict-probes",
		severity:       "high",
		status:         "covered",
		recommendation: "Use git-kit context --include=conflict or git-kit diff --conflicts --json instead of raw git diff --cc/ls-files probes.",
		coveredBy:      []string{"git-kit context --include=conflict", "git-kit diff --conflicts --json"},
	},
	"raw-release-sequence": {
		kind:           "raw-release-sequence",
		severity:       "high",
		status:         "covered",
		recommendation: "Use git-kit ship --dry-run --json, then git-kit ship -y for commit/tag/push/release flows.",
		coveredBy:      []string{"git-kit ship --dry-run --json", "git-kit ship -y"},
	},
	"raw-commit-sequence": {
		kind:           "raw-commit-sequence",
		severity:       "medium",
		status:         "covered",
		recommendation: "Use git-kit commit or git-kit commit --plan - instead of raw git add/git commit sequences.",
		coveredBy:      []string{"git-kit commit", "git-kit commit --plan -"},
	},
	"raw-integration": {
		kind:           "raw-integration",
		severity:       "medium",
		status:         "covered",
		recommendation: "Use git-kit pull/sync/merge/rebase so paused and blocked states stay in the agent envelope.",
		coveredBy:      []string{"git-kit pull", "git-kit sync", "git-kit merge", "git-kit rebase"},
	},
	"raw-branch-switch": {
		kind:           "raw-branch-switch",
		severity:       "medium",
		status:         "covered",
		recommendation: "Use git-kit switch to move between branches — it records the gk-parent metadata that raw checkout/switch does not.",
		coveredBy:      []string{"git-kit switch"},
	},
	"raw-worktree": {
		kind:           "raw-worktree",
		severity:       "medium",
		status:         "covered",
		recommendation: "Use git-kit worktree (add/list/run) so worktrees get gk-parent metadata and the gitignored-state bootstrap.",
		coveredBy:      []string{"git-kit worktree"},
	},
	"raw-unstage": {
		kind:           "raw-unstage",
		severity:       "low",
		status:         "covered",
		recommendation: "Use git-kit unstage [paths] — drops files from the staging area without touching their contents.",
		coveredBy:      []string{"git-kit unstage"},
	},
	"raw-full-diff": {
		kind:           "raw-full-diff",
		severity:       "low",
		status:         "covered",
		recommendation: "Use git-kit diff --raw-patch --json for exact unified patch text, or git-kit diff --json for parsed hunks.",
		coveredBy:      []string{"git-kit diff --raw-patch --json", "git-kit diff --json", "git-kit diff --digest"},
	},
	// `git reset --hard <ref>` and the fsck recovery hunt DO have gk verbs; the
	// rest of the reset/restore family still does not. Mapped by target, not by
	// verb name:
	//   - index-only spellings (`git reset [HEAD]`, `git restore --staged`) →
	//     raw-unstage, via isRawUnstage.
	//   - `git reset --hard <ref>` → raw-reset-hard (gk reset --to takes the same
	//     target, fetches it first, and gates on a confirm — it does NOT write a
	//     backup ref; the reflog is the only way back, via gk undo).
	//   - `git fsck --lost-found/--unreachable` → raw-lost-found (gk restore --lost).
	// Deliberately still UNMAPPED, because no gk verb has the same meaning:
	//   - `git reset --soft <ref>` — gk undo is an interactive reflog picker, not
	//     a scriptable "uncommit but keep the work".
	//   - `git checkout -- <paths>` / `git restore <paths>` — per-path discard.
	//     gk wipe is whole-tree (and cleans untracked), so it is NOT this.
	// stash maps only for the subcommands git-kit stash actually registers —
	// gitKitStashCovers gates show/clear/branch/etc. back to a gap.
	// The branch survey is covered — but by gk branch list, NOT by gk context.
	// A 1:1 swap, so it gets no collapse group: it saves correctness, not turns.
	"raw-branch-list": {
		kind:           "raw-branch-list",
		severity:       "low",
		status:         "covered",
		recommendation: "Use git-kit branch list (--merged/--unmerged/--gone/--stale, --json) to survey branches — gk context reports the CURRENT branch, not the set of them.",
		coveredBy:      []string{"git-kit branch list"},
	},
	// History SEARCH is what gk find answers, and it is a real turn collapse (see
	// collapseGroupForKind): the agent does not know WHICH query will hit, so it
	// tries --grep, then the pickaxe, then a path scope, one raw turn each. gk
	// find runs all three at once and reports which matched.
	"raw-history-search": {
		kind:           "raw-history-search",
		severity:       "medium",
		status:         "covered",
		recommendation: "Use git-kit find <query> — it searches commit messages, changed content (pickaxe) and paths in ONE call across every ref, and says which matched, instead of one raw git log per guess.",
		coveredBy:      []string{"git-kit find"},
	},
	// A range comparison is NOT a search — `git log A..B` asks "what is in B that
	// is not in A". gk log --ahead/--behind --base answers the upstream/base
	// spellings of that question, but an arbitrary two-ref range has no verb, so
	// this stays an honest gap rather than being folded into gk find (which
	// cannot answer it) or gk context (which never could).
	"raw-range-compare": {
		kind:           "raw-range-compare",
		severity:       "low",
		status:         "gap",
		recommendation: "Ref-range comparison (git log A..B). gk log --ahead/--behind --base covers the upstream/base cases; an arbitrary two-ref range has no git-kit verb yet.",
		gap:            "git-kit has no arbitrary ref-range comparison (git log A..B)",
	},
	"raw-reset-hard": {
		kind:           "raw-reset-hard",
		severity:       "medium",
		status:         "covered",
		recommendation: "Use git-kit reset --to <ref> — the same destructive reset, but it fetches the target first and gates on a confirm (-y to skip); the pre-reset HEAD stays in the reflog, so git-kit undo can walk it back.",
		coveredBy:      []string{"git-kit reset --to <ref>"},
	},
	"raw-lost-found": {
		kind:           "raw-lost-found",
		severity:       "low",
		status:         "covered",
		recommendation: "Use git-kit restore --lost — it runs the fsck hunt and renders the dangling commits as a restorable list.",
		coveredBy:      []string{"git-kit restore --lost"},
	},
	"raw-stash": {
		kind:           "raw-stash",
		severity:       "low",
		status:         "covered",
		recommendation: "Use git-kit stash (push/list/pop/apply/drop) instead of raw git stash.",
		coveredBy:      []string{"git-kit stash"},
	},
	"raw-diff-check": {
		kind:           "raw-diff-check",
		severity:       "low",
		status:         "covered",
		recommendation: "Use git-kit diff --check --json so whitespace/conflict-marker checks stay in the git-kit JSON contract.",
		coveredBy:      []string{"git-kit diff --check", "git-kit diff --check --json"},
	},
	"raw-clone": {
		kind:           "raw-clone",
		severity:       "low",
		status:         "covered",
		recommendation: "Use git-kit clone (short-form URL expansion) instead of raw git clone.",
		coveredBy:      []string{"git-kit clone"},
	},
	"raw-apply": {
		kind:           "raw-apply",
		severity:       "medium",
		status:         "covered",
		recommendation: "Use git-kit apply instead of raw git apply so patch application stays in the agent envelope.",
		coveredBy:      []string{"git-kit apply"},
	},
	"raw-forget": {
		kind:           "raw-forget",
		severity:       "low",
		status:         "covered",
		recommendation: "Use git-kit forget to remove paths from history instead of raw git filter-repo.",
		coveredBy:      []string{"git-kit forget"},
	},
	"gk-short-alias": {
		kind:           "gk-short-alias",
		severity:       "medium",
		status:         "covered",
		recommendation: "Use git-kit in agent shells; the short gk name is commonly shadowed by shell aliases.",
		coveredBy:      []string{"git-kit"},
	},
	"shell-chain": {
		kind:           "shell-chain",
		severity:       "low",
		status:         "covered",
		recommendation: "Use git-kit batch --plan - for multi-step git-kit workflows instead of shell chains; a synthesized plan is attached to each example.",
		coveredBy:      []string{"git-kit batch --plan -"},
	},
	// uncovered-raw-git is the inverse of the covered findings: raw git the
	// audit recognised no git-kit mapping for. It is the roadmap signal — read
	// the subcommand breakdown to tell a missing git-kit verb (build it) from a
	// coverage gap in this audit (classify it) — not a compliance nag, so it
	// carries no covered_by.
	"uncovered-raw-git": {
		kind:           "uncovered-raw-git",
		severity:       "info",
		status:         "gap",
		recommendation: "Raw git with no recognized git-kit mapping. Read the subcommand breakdown: a frequent verb is either a missing git-kit command or a coverage gap in this audit's classifiers.",
		gap:            "no git-kit verb recognized for these raw-git subcommands",
	},
}

// rawGitNonGap lists raw-git subcommands that must NOT be reported as a coverage
// gap: read-only plumbing an agent legitimately reaches for, the diff/show
// family (a covered domain whose rarer flag forms — `--numstat`, `--raw`,
// `--exit-code` — otherwise slip past the diff classifiers), and low-level
// plumbing git-kit intentionally never wraps. Every other unmatched raw-git
// subcommand is a genuine signal that git-kit has no verb for it. Subcommands
// that mix read-only and mutating forms (`remote`, `submodule`) are not listed
// here — their read-only invocations are suppressed by isRawReadOnlyForm so the
// mutating forms still surface as signals.
var rawGitNonGap = map[string]bool{
	// diff/show domain — covered conceptually; guard against false gaps.
	"diff": true, "show": true,
	// read-only inspection / plumbing.
	"rev-parse": true, "config": true, "cat-file": true, "symbolic-ref": true,
	"for-each-ref": true, "ls-tree": true, "ls-files": true, "show-ref": true,
	"ls-remote": true, "hash-object": true, "name-rev": true, "update-ref": true,
	"var": true, "check-ignore": true, "check-attr": true, "describe": true,
	"reflog": true, "blame": true, "grep": true, "shortlog": true,
	"count-objects": true, "verify-commit": true, "verify-tag": true, "version": true,
	"cherry": true, "help": true, "archive": true,
	// context-verb family: the non-sha forms are covered context probes; the
	// sha-archaeology forms (excluded by hasHexCommitOperand) are still
	// read-only inspection, never a missing-verb signal.
	"log": true, "rev-list": true, "merge-base": true, "branch": true,
	// low-level plumbing git-kit never wraps (some mutates the index/object
	// store, but none is a roadmap signal). init is dominated by temp test
	// fixtures and gk init already runs git init when needed; gc/commit-tree
	// are maintenance/plumbing like their listed siblings.
	"merge-tree": true, "read-tree": true, "checkout-index": true,
	"diff-tree": true, "update-index": true, "commit-graph": true,
	"init": true, "gc": true, "commit-tree": true,
	// `git kit …` is not a git subcommand at all — dev sessions probing gk's
	// own help (`git kit --help`) leave it behind; pure noise, never a gap.
	"kit": true,
}

// Audit reads local Codex/Claude JSONL sessions and reports git usage patterns.
func Audit(opts Options) (Report, error) {
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = defaultMaxFiles
	}
	if opts.Home == "" {
		if home, err := os.UserHomeDir(); err == nil {
			opts.Home = home
		}
	}
	paths := opts.Paths
	if len(paths) == 0 {
		paths = DefaultPaths(opts.Home)
	}
	expanded := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		expanded = append(expanded, expandHome(p, opts.Home))
	}

	files, notes := collectFiles(expanded, opts.MaxFiles, opts.Since)
	report := Report{Schema: 1, Notes: notes}
	if !opts.Since.IsZero() {
		report.Since = opts.Since.UTC().Format(time.RFC3339)
	}
	aggregate := map[string]*Finding{}

	wantTurns := turnsRequested(opts.Metric)
	var turns *TurnMetrics
	skippedUnknown := 0
	if wantTurns {
		turns = &TurnMetrics{Source: "claude,codex", ByGroup: map[string]int{}}
	}

	// Parse phase — each file's read + JSON/turn extraction is independent, so
	// fan out across cores. The merge below stays sequential in collectFiles
	// order, keeping the report deterministic regardless of finish order.
	outcomes := make([]fileOutcome, len(files))
	var eg errgroup.Group
	eg.SetLimit(runtime.GOMAXPROCS(0))
	for i, fc := range files {
		eg.Go(func() error {
			outcomes[i] = processFile(fc.path, wantTurns)
			return nil
		})
	}
	_ = eg.Wait()

	projects := map[string]*ProjectAdoption{}
	for i := range outcomes {
		o := &outcomes[i]
		if o.err != nil {
			report.Notes = append(report.Notes, fmt.Sprintf("%s: %v", files[i].path, o.err))
			continue
		}
		report.Files = append(report.Files, o.fr)
		report.Totals.Files++
		report.Totals.Commands += o.fr.Commands
		report.Totals.RawGit += o.fr.RawGit
		report.Totals.GitKit += o.fr.GitKit
		report.Totals.GKShort += o.fr.GKShort
		report.Totals.ShellChains += o.fr.ShellChains
		key := projectKeyForPath(files[i].path, o.fr.Source)
		pa := projects[key]
		if pa == nil {
			pa = &ProjectAdoption{Project: key}
			projects[key] = pa
		}
		pa.Files++
		pa.RawGit += o.fr.RawGit
		pa.GitKit += o.fr.GitKit
		pa.GKShort += o.fr.GKShort
		addFindings(aggregate, files[i].path, o.commands)

		if wantTurns {
			if o.unknownTurnSource {
				skippedUnknown++
				continue
			}
			turns.GitTurns += o.gitTurns
			for _, r := range o.runs {
				turns.EstimatedTurnsSaved += r.TurnsSaved
				turns.ByGroup[r.Group] += r.TurnsSaved
				turns.Runs = append(turns.Runs, r)
			}
		}
	}
	report.Findings = sortedFindings(aggregate)
	report.Adoption = computeAdoption(report.Totals, report.Findings)
	report.Projects = sortedProjects(projects)

	if wantTurns {
		if turns.GitTurns > 0 {
			turns.Rate = float64(turns.EstimatedTurnsSaved) / float64(turns.GitTurns)
		}
		sortRunsBySaved(turns.Runs)
		if len(turns.Runs) > maxTurnRuns {
			turns.Runs = turns.Runs[:maxTurnRuns]
		}
		if skippedUnknown > 0 {
			report.Notes = append(report.Notes, fmt.Sprintf("turn metric: Claude + Codex sessions (%d session(s) of unknown shape excluded)", skippedUnknown))
		}
		report.Turns = turns
	}
	return report, nil
}

// computeAdoption derives the git-kit adoption rate and the count of raw-git
// hits that already have a git-kit replacement. Rerunning the audit and
// comparing these numbers is how guidance changes are tracked for regression.
func computeAdoption(t Totals, findings []Finding) Adoption {
	a := Adoption{
		GitInvocations: t.RawGit + t.GitKit + t.GKShort,
		GitKit:         t.GitKit,
	}
	if a.GitInvocations > 0 {
		a.Rate = float64(t.GitKit) / float64(a.GitInvocations)
	}
	for _, f := range findings {
		switch {
		case f.Status == "covered" && strings.HasPrefix(f.Kind, "raw-"):
			a.CoveredRawHits += f.Count
		case f.Status == "gap":
			a.UncoveredRawHits += f.Count
		}
	}
	return a
}

func DefaultPaths(home string) []string {
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".codex", "sessions"),
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".claude", "sessions"),
	}
}

func collectFiles(paths []string, maxFiles int, since time.Time) ([]fileCandidate, []string) {
	var out []fileCandidate
	var notes []string
	skippedOld := 0
	// Applies uniformly to walked directories AND explicitly passed files, so a
	// window means the same thing regardless of how the corpus was named.
	keep := func(info os.FileInfo) bool {
		if !since.IsZero() && info.ModTime().Before(since) {
			skippedOld++
			return false
		}
		return true
	}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				notes = append(notes, fmt.Sprintf("%s: %v", p, err))
			}
			continue
		}
		if !info.IsDir() {
			if keep(info) {
				out = append(out, fileCandidate{path: p, info: info})
			}
			continue
		}
		err = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				notes = append(notes, fmt.Sprintf("%s: %v", path, err))
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".jsonl" {
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil {
				notes = append(notes, fmt.Sprintf("%s: %v", path, ierr))
				return nil
			}
			if keep(info) {
				out = append(out, fileCandidate{path: path, info: info})
			}
			return nil
		})
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s: %v", p, err))
		}
	}
	if skippedOld > 0 {
		notes = append(notes, fmt.Sprintf("since filter: skipped %d session file(s) modified before %s",
			skippedOld, since.UTC().Format(time.RFC3339)))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].info.ModTime().After(out[j].info.ModTime())
	})
	if maxFiles > 0 && len(out) > maxFiles {
		notes = append(notes, fmt.Sprintf("scanned newest %d of %d session files", maxFiles, len(out)))
		out = out[:maxFiles]
	}
	return out, notes
}

// fileOutcome carries one session file's parsed artifacts out of the parallel
// parse phase; merging into the report happens sequentially afterwards.
type fileOutcome struct {
	fr                FileReport
	commands          []string
	gitTurns          int
	runs              []CollapsibleRun
	unknownTurnSource bool
	err               error
}

// processFile reads and classifies one session file. With the turn metric it
// reads the bytes once and feeds both the occurrence classifier and the turn
// extractor — the turn path used to re-read the same file from disk.
func processFile(path string, wantTurns bool) fileOutcome {
	if !wantTurns {
		fr, commands, err := auditFile(path)
		if err != nil {
			return fileOutcome{err: err}
		}
		fr.Source = sourceForPath(path)
		return fileOutcome{fr: fr, commands: commands}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileOutcome{err: err}
	}
	fr, commands := auditData(path, data)
	fr.Source = sourceForPath(path)
	o := fileOutcome{fr: fr, commands: commands}
	// The turn model needs a per-source shape: Claude's message-id batch or
	// Codex's function_call batch. Sources with neither are skipped.
	var events []TurnEvent
	switch fr.Source {
	case "claude":
		events = SessionTurns(data)
	case "codex":
		events = CodexSessionTurns(data)
	default:
		o.unknownTurnSource = true
		return o
	}
	o.gitTurns, o.runs = turnEventsContribution(events)
	return o
}

func auditFile(path string) (FileReport, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileReport{}, nil, err
	}
	defer f.Close()

	fr := FileReport{Path: path}
	var commands []string
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			commands = auditLine(&fr, commands, line)
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			break
		}
		return fr, commands, err
	}
	return fr, commands, nil
}

// auditData is auditFile over bytes already in memory, so the turn-metric path
// can share one read between the occurrence classifier and the turn extractor.
func auditData(path string, data []byte) (FileReport, []string) {
	fr := FileReport{Path: path}
	var commands []string
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		var line []byte
		if i < 0 {
			line, data = data, nil
		} else {
			line, data = data[:i+1], data[i+1:]
		}
		commands = auditLine(&fr, commands, line)
	}
	return fr, commands
}

// auditLine folds one JSONL record's commands into the running file report.
func auditLine(fr *FileReport, commands []string, line []byte) []string {
	for _, cmd := range ExtractCommands(line) {
		commands = append(commands, cmd)
		class := classifyCommand(cmd)
		fr.Commands++
		fr.RawGit += class.RawGit
		fr.GitKit += class.GitKit
		fr.GKShort += class.GKShort
		if class.ShellChain {
			fr.ShellChains++
		}
	}
	return commands
}

// ExtractCommands returns shell command strings from one JSONL record.
func ExtractCommands(line []byte) []string {
	var v any
	if err := json.Unmarshal(line, &v); err != nil {
		return nil
	}
	var out []string
	walkJSON(v, "", &out)
	return compactCommands(out)
}

func walkJSON(v any, key string, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			lower := strings.ToLower(k)
			if lower == "cmd" || lower == "command" {
				if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
					*out = append(*out, s)
					continue
				}
			}
			walkJSON(val, lower, out)
		}
	case []any:
		for _, elem := range x {
			walkJSON(elem, key, out)
		}
	case string:
		if key == "arguments" && looksJSON(x) {
			var inner any
			if err := json.Unmarshal([]byte(x), &inner); err == nil {
				walkJSON(inner, "", out)
			}
		}
	}
}

func compactCommands(commands []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(commands))
	for _, cmd := range commands {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" || seen[cmd] {
			continue
		}
		seen[cmd] = true
		out = append(out, cmd)
	}
	return out
}

func looksJSON(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[")
}

type commandClass struct {
	RawGit     int
	GitKit     int
	GKShort    int
	ShellChain bool
	Segments   []shellSegment
}

type shellSegment struct {
	Tool string
	Text string
}

func classifyCommand(cmd string) commandClass {
	parts, chained := splitShellSegments(cmd)
	out := commandClass{ShellChain: chained}
	for _, part := range parts {
		tool := leadingTool(part)
		if tool == "" {
			continue
		}
		seg := shellSegment{Tool: tool, Text: strings.TrimSpace(part)}
		out.Segments = append(out.Segments, seg)
		switch tool {
		case "git":
			out.RawGit++
		case "git-kit":
			out.GitKit++
		case "gk":
			out.GKShort++
		}
	}
	return out
}

func splitShellSegments(s string) ([]string, bool) {
	var parts []string
	var b strings.Builder
	var single, double, escaped bool
	chained := false
	// Heredoc terminators announced on the current command line, in order of
	// appearance. Body lines between the line break and the terminators are
	// data, not shell segments — without this, prose lines starting with "git"
	// inside a commit-message heredoc were classified as git commands.
	var heredocs []heredocDelim

	flush := func() {
		part := strings.TrimSpace(b.String())
		if part != "" {
			parts = append(parts, part)
		}
		b.Reset()
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' && !single {
			b.WriteByte(c)
			escaped = true
			continue
		}
		if c == '\'' && !double {
			single = !single
			b.WriteByte(c)
			continue
		}
		if c == '"' && !single {
			double = !double
			b.WriteByte(c)
			continue
		}
		if !single && !double {
			// << or <<- introduces a heredoc; <<< is a here-string (no body).
			if c == '<' && i+1 < len(s) && s[i+1] == '<' && (i+2 >= len(s) || s[i+2] != '<') {
				if delim, next := parseHeredocDelim(s, i+2); delim.word != "" {
					heredocs = append(heredocs, delim)
					b.WriteString(s[i:next])
					i = next - 1
					continue
				}
			}
			switch c {
			case '\n':
				if len(heredocs) > 0 {
					flush()
					i = skipHeredocBodies(s, i+1, heredocs) - 1
					heredocs = nil
					// Content after the terminator is a further command.
					if i+1 < len(s) && strings.TrimSpace(s[i+1:]) != "" {
						chained = true
					}
					continue
				}
				chained = true
				flush()
				continue
			case ';':
				chained = true
				flush()
				continue
			case '&':
				if i+1 < len(s) && s[i+1] == '&' {
					chained = true
					flush()
					i++
					continue
				}
			case '|':
				chained = true
				flush()
				if i+1 < len(s) && s[i+1] == '|' {
					i++
				}
				continue
			}
		}
		b.WriteByte(c)
	}
	flush()
	return parts, chained
}

// heredocDelim is one announced heredoc terminator: its word and whether the
// <<- form was used (only then may the terminator line be tab-indented).
type heredocDelim struct {
	word      string
	stripTabs bool
}

// parseHeredocDelim reads the heredoc terminator word starting at j (the index
// just past "<<"). It returns the unquoted terminator and the index of the
// first byte after the consumed operator text; an empty word means "not a
// heredoc" and the caller falls through to normal character handling.
func parseHeredocDelim(s string, j int) (heredocDelim, int) {
	var d heredocDelim
	if j < len(s) && s[j] == '-' {
		d.stripTabs = true
		j++
	}
	for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	if j >= len(s) {
		return heredocDelim{}, j
	}
	switch q := s[j]; q {
	case '\'', '"':
		end := strings.IndexByte(s[j+1:], q)
		if end < 0 {
			return heredocDelim{}, j
		}
		d.word = s[j+1 : j+1+end]
		return d, j + 1 + end + 1
	case '\\':
		j++
	}
	start := j
	for j < len(s) && !strings.ContainsRune(" \t\r\n;&|<>", rune(s[j])) {
		j++
	}
	word := s[start:j]
	// Shell arithmetic (`$((1<<20))`) also carries an unquoted "<<": its
	// would-be terminator keeps the closing parens (')' is not in the stop
	// set) or starts with a digit. No real heredoc terminator looks like
	// that, and treating it as one swallows every following command line.
	if strings.ContainsAny(word, "()") || (word != "" && word[0] >= '0' && word[0] <= '9') {
		return heredocDelim{}, j
	}
	d.word = word
	return d, j
}

// skipHeredocBodies consumes body lines from start until every queued
// terminator has been seen (in order), returning the index just past the last
// terminator line. A trailing \r is tolerated (CRLF input — the terminator
// word itself never carries one, its stop set includes \r); leading tabs are
// tolerated only for the <<- form, matching the shell.
func skipHeredocBodies(s string, start int, delims []heredocDelim) int {
	i := start
	for len(delims) > 0 && i < len(s) {
		end := strings.IndexByte(s[i:], '\n')
		var line string
		var next int
		if end < 0 {
			line, next = s[i:], len(s)
		} else {
			line, next = s[i:i+end], i+end+1
		}
		line = strings.TrimSuffix(line, "\r")
		if delims[0].stripTabs {
			line = strings.TrimLeft(line, "\t")
		}
		if line == delims[0].word {
			delims = delims[1:]
		}
		i = next
	}
	return i
}

func leadingTool(segment string) string {
	fields := shellFields(segment)
	for i := 0; i < len(fields); i++ {
		tok := trimShellToken(fields[i])
		if tok == "" || isEnvAssignment(tok) {
			continue
		}
		switch tok {
		case "env", "sudo", "command", "builtin", "time", "noglob", "if", "then", "do", "else":
			continue
		default:
			return tok
		}
	}
	return ""
}

func shellFields(s string) []string {
	var fields []string
	var b strings.Builder
	var single, double, escaped bool
	flush := func() {
		if b.Len() == 0 {
			return
		}
		fields = append(fields, b.String())
		b.Reset()
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' && !single {
			b.WriteByte(c)
			escaped = true
			continue
		}
		if c == '\'' && !double {
			single = !single
			b.WriteByte(c)
			continue
		}
		if c == '"' && !single {
			double = !double
			b.WriteByte(c)
			continue
		}
		if !single && !double && (c == ' ' || c == '\t' || c == '\r' || c == '\n') {
			flush()
			continue
		}
		b.WriteByte(c)
	}
	flush()
	return fields
}

func trimShellToken(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '\'' && s[len(s)-1] == '\'') || (s[0] == '"' && s[len(s)-1] == '"') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func isEnvAssignment(s string) bool {
	idx := strings.IndexByte(s, '=')
	if idx <= 0 {
		return false
	}
	for i := 0; i < idx; i++ {
		c := s[i]
		isAlpha := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
		isDigit := i > 0 && c >= '0' && c <= '9'
		if c != '_' && !isAlpha && !isDigit {
			return false
		}
	}
	return true
}

func addFindings(findings map[string]*Finding, file string, commands []string) {
	var gitTag, gitPush bool
	var releaseEvidence string
	for _, cmd := range commands {
		class := classifyCommand(cmd)
		rawInChain := false
		for _, seg := range class.Segments {
			switch seg.Tool {
			case "git":
				rawInChain = true
				subcmd, args, ok := gitSubcommand(seg.Text)
				if !ok {
					continue
				}
				matched := false
				// tag/push are release-flow signals aggregated across segments
				// (raw-release-sequence fires only when both appear), so they are
				// flagged here rather than via gitSegmentFinding.
				if subcmd == "tag" {
					gitTag = true
					if releaseEvidence == "" {
						releaseEvidence = seg.Text
					}
					matched = true
				}
				if subcmd == "push" {
					gitPush = true
					if releaseEvidence == "" {
						releaseEvidence = seg.Text
					}
					matched = true
				}
				if kind := gitSegmentFinding(subcmd, args); kind != "" {
					addFinding(findings, kind, file, seg.Text)
					matched = true
				}
				// Anything left over is raw git the audit has no git-kit mapping
				// for: a coverage gap or a missing verb. Plumbing/diff forms and
				// the read-only invocations of mutation-capable subcommands are
				// suppressed so the gap stays a real roadmap signal.
				if !matched && !rawGitNonGap[subcmd] && !isRawReadOnlyForm(subcmd, args) {
					addGapFinding(findings, file, subcmd, seg.Text)
				}
			case "gk":
				addFinding(findings, "gk-short-alias", file, seg.Text)
			}
		}
		if rawInChain && class.ShellChain {
			if ev := addFinding(findings, "shell-chain", file, cmd); ev != nil {
				ev.Plan = synthesizeBatchPlan(cmd)
			}
		}
	}
	if gitTag && gitPush {
		addFinding(findings, "raw-release-sequence", file, releaseEvidence)
	}
}

// addFinding records one hit and returns a pointer to the freshly appended
// Evidence (or nil if the evidence cap is already reached), so callers that
// need to enrich the evidence — e.g. attach a synthesized batch plan — can do
// so without re-scanning. The pointer is valid only until the next append.
// synthesizeBatchPlan turns one observed shell chain into a git-kit batch
// plan. Each git segment that maps to a git-kit verb becomes a step (collapsing
// consecutive duplicates — three context probes in a row are one context call);
// non-git-kit segments are recorded in Omitted so the caller can see what batch
// will not carry. Returns nil when nothing maps (a pure raw-git chain with no
// covered equivalent), leaving the recommendation as the only guidance.
func synthesizeBatchPlan(chain string) *BatchPlan {
	parts, _ := splitShellSegments(chain)
	plan := &BatchPlan{}
	seenStep := map[string]bool{}
	seenOmit := map[string]bool{}
	for _, part := range parts {
		tool := leadingTool(part)
		if tool == "" {
			continue
		}
		if tool == "git-kit" || tool == "gk" {
			// Already a git-kit call (or its short alias) — nothing to replace.
			continue
		}
		if tool != "git" {
			if !seenOmit[tool] {
				seenOmit[tool] = true
				plan.Omitted = append(plan.Omitted, tool)
			}
			continue
		}
		subcmd, gitArgs, ok := gitSubcommand(part)
		if !ok {
			continue
		}
		stepArgs, ok := batchStepForGit(subcmd, gitArgs)
		if !ok {
			label := "git " + subcmd
			if !seenOmit[label] {
				seenOmit[label] = true
				plan.Omitted = append(plan.Omitted, label)
			}
			continue
		}
		key := strings.Join(stepArgs, " ")
		if seenStep[key] {
			continue
		}
		seenStep[key] = true
		plan.Steps = append(plan.Steps, BatchStep{Args: stepArgs, From: "git " + subcmd})
	}
	if len(plan.Steps) == 0 {
		return nil
	}
	return plan
}

// batchStepForGit maps a raw git subcommand to the git-kit argv that replaces
// it, reusing the same classifiers the findings use so the plan and the
// recommendations never drift. Order matters: the narrower probe/check cases
// are tested before the catch-all full-diff.
func batchStepForGit(subcmd string, args []string) ([]string, bool) {
	switch {
	// Conflict before context, mirroring gitSegmentFinding: the unmerged-files
	// probe matches both shapes and must map to the conflict include.
	case isRawConflictProbe(subcmd, args):
		return []string{"context", "--include=conflict"}, true
	case isRawContextProbe(subcmd, args):
		return []string{"context", "--include=diff,log"}, true
	case subcmd == "add" || subcmd == "commit":
		return []string{"commit"}, true
	case subcmd == "pull" || subcmd == "fetch":
		return []string{"pull"}, true
	case subcmd == "merge":
		return []string{"merge"}, true
	case subcmd == "rebase":
		return []string{"rebase"}, true
	case subcmd == "push":
		return []string{"push"}, true
	case subcmd == "diff" && hasArg(args, "--check"):
		return []string{"diff", "--check"}, true
	case isRawFullDiff(subcmd, args):
		return []string{"diff", "--json"}, true
	}
	return nil, false
}

func addFinding(findings map[string]*Finding, kind, file, command string) *Evidence {
	spec, ok := findingSpecs[kind]
	if !ok {
		return nil
	}
	f := findings[kind]
	if f == nil {
		f = &Finding{
			Kind:           spec.kind,
			Severity:       spec.severity,
			Status:         spec.status,
			Recommendation: spec.recommendation,
			CoveredBy:      append([]string(nil), spec.coveredBy...),
			Gap:            spec.gap,
		}
		findings[kind] = f
	}
	f.Count++
	if len(f.Evidence) < maxEvidence {
		f.Evidence = append(f.Evidence, Evidence{File: file, Command: truncateOneLine(command, 220)})
		return &f.Evidence[len(f.Evidence)-1]
	}
	return nil
}

// addGapFinding records one uncovered raw-git hit, accumulating the per-subcommand
// breakdown that makes the gap finding a roadmap rather than a flat count.
func addGapFinding(findings map[string]*Finding, file, subcmd, command string) {
	const kind = "uncovered-raw-git"
	spec := findingSpecs[kind]
	f := findings[kind]
	if f == nil {
		f = &Finding{
			Kind:           spec.kind,
			Severity:       spec.severity,
			Status:         spec.status,
			Recommendation: spec.recommendation,
			Gap:            spec.gap,
			Subcommands:    map[string]int{},
		}
		findings[kind] = f
	}
	f.Count++
	f.Subcommands[subcmd]++
	// One sample per subcommand instead of a first-N-overall cap: under the cap
	// the frequent subcommands claimed every slot and the rare ones shipped with
	// no example at all, forcing a corpus re-grep to judge the gap.
	if f.gapEvidenceSeen == nil {
		f.gapEvidenceSeen = map[string]bool{}
	}
	if !f.gapEvidenceSeen[subcmd] {
		f.gapEvidenceSeen[subcmd] = true
		f.Evidence = append(f.Evidence, Evidence{File: file, Command: truncateOneLine(command, 220)})
	}
	if oneShotGapSubcommands[subcmd] && !slices.Contains(f.OneShot, subcmd) {
		f.OneShot = append(f.OneShot, subcmd)
		sort.Strings(f.OneShot)
	}
}

// oneShotGapSubcommands are gap subcommands whose raw invocation is one call —
// a gk replacement would be a 1:1 swap saving ~0 turns. Kept in the gap (the
// coverage hole is real) but labeled so the roadmap reads turn leverage, not
// raw counts. init/archive moved to rawGitNonGap (never a gap at all).
var oneShotGapSubcommands = map[string]bool{
	"clone": true, "mv": true, "rm": true, "clean": true,
}

func sortedFindings(findings map[string]*Finding) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		if severityRank(out[i].Severity) != severityRank(out[j].Severity) {
			return severityRank(out[i].Severity) > severityRank(out[j].Severity)
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func severityRank(s string) int {
	switch s {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func gitSubcommand(segment string) (string, []string, bool) {
	fields := shellFields(segment)
	for i := 0; i < len(fields); i++ {
		tok := trimShellToken(fields[i])
		if tok != "git" {
			continue
		}
		args := fields[i+1:]
		for len(args) > 0 {
			head := trimShellToken(args[0])
			if head == "" {
				args = args[1:]
				continue
			}
			if head == "-C" || head == "-c" || head == "--git-dir" || head == "--work-tree" {
				if len(args) >= 2 {
					args = args[2:]
					continue
				}
				return "", nil, false
			}
			if strings.HasPrefix(head, "-C") && len(head) > 2 {
				args = args[1:]
				continue
			}
			if strings.HasPrefix(head, "-c") && len(head) > 2 {
				args = args[1:]
				continue
			}
			if strings.HasPrefix(head, "--git-dir=") || strings.HasPrefix(head, "--work-tree=") {
				args = args[1:]
				continue
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

// isGitSubcommandToken reports whether tok is shaped like a git subcommand
// (^[a-z0-9][a-z0-9-]*$). Prose or expansion tokens after a literal "git" word
// (e.g. a Korean sentence starting with "git") must not classify as raw git.
func isGitSubcommandToken(tok string) bool {
	if tok == "" || tok[0] == '-' {
		return false
	}
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || (c == '-' && i > 0) {
			continue
		}
		return false
	}
	return true
}

// isRawContextProbe matches the raw probes `gk context` genuinely answers: WHERE
// AM I — the current branch, its upstream, ahead/behind, the dirty set, the
// recent commits on this branch.
//
// It deliberately does NOT match history SEARCH (`--grep`, `-S`, path-scoped
// log) or branch SURVEY (`git branch -a --merged`), even though both are
// read-only probes of the same verbs. gk context reports one repo's current
// state; it cannot answer "which commit mentions X" or "which branches are
// merged into main". Crediting those to gk context inflated the turn metric with
// savings gk cannot deliver AND made the inline hint push agents toward a
// command that does not answer their question — measured: roughly half of the
// context group's collapsible turns were probes of that kind. They route to
// isRawHistorySearch (a real gap) and isRawBranchList (gk branch list) instead.
//
// SHA archaeology (`git show <sha>`, `git merge-base <sha> <sha>`) is excluded
// for the same reason — HEAD/branch-name operands stay context probes.
func isRawContextProbe(subcmd string, args []string) bool {
	if isRawHistorySearch(subcmd, args) || isRawRangeCompare(subcmd, args) || isRawBranchList(subcmd, args) {
		return false
	}
	switch subcmd {
	case "status", "log", "rev-list", "merge-base":
		return !hasHexCommitOperand(args)
	case "diff", "show":
		statish := hasArg(args, "--stat") || hasArg(args, "--shortstat") || hasArg(args, "--name-only") || hasArg(args, "--name-status")
		return statish && !hasHexCommitOperand(args)
	default:
		return false
	}
}

// branchValueFlags are `git branch` filters that consume the NEXT token, so the
// operand they take is not a branch name to create — `--merged main` is a
// listing filter, not a rename target.
var branchValueFlags = map[string]bool{
	"--merged": true, "--no-merged": true, "--contains": true, "--no-contains": true,
	"--sort": true, "--format": true, "--points-at": true,
}

// branchMutationFlags turn `git branch` from a listing into a write. gk branch
// list does not cover these (delete/rename/copy/upstream edits), so they are not
// claimed as covered.
var branchMutationFlags = map[string]bool{
	"-d": true, "-D": true, "--delete": true,
	"-m": true, "-M": true, "--move": true,
	"-c": true, "-C": true, "--copy": true,
	"-u": true, "--set-upstream-to": true, "--unset-upstream": true,
	"--edit-description": true,
}

// isRawBranchList matches the `git branch` forms that SURVEY branches — the bare
// listing, `-v/-vv`, `-a/-r`, and the `--merged/--no-merged` filters. `gk branch
// list` is what covers these (it has --merged/--unmerged/--gone/--stale and
// --json); `gk context` never did — it reports the CURRENT branch, not the set
// of them, which is why these used to inflate the context collapse group.
//
// Excluded: mutations (delete/rename/upstream — see branchMutationFlags), a bare
// operand (that names a branch to create), and `--contains <ref>`, which is a
// history question gk branch cannot ask (it routes to isRawHistorySearch).
func isRawBranchList(subcmd string, args []string) bool {
	if subcmd != "branch" {
		return false
	}
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
		if !strings.HasPrefix(a, "-") {
			return false // a branch name operand — create/rename, not a listing
		}
		name, _, hasValue := strings.Cut(a, "=")
		if branchMutationFlags[name] {
			return false
		}
		if strings.HasPrefix(name, "--contains") || strings.HasPrefix(name, "--no-contains") {
			return false // a history question — isRawHistorySearch owns it
		}
		if branchValueFlags[name] && !hasValue {
			skipNext = true
		}
	}
	return true
}

// isRawHistorySearch matches the log/rev-list/branch forms that SEARCH history
// rather than report the current state — "which commit introduced X", "what
// changed in this file over time", "which branch contains this commit".
//
// This is what `gk find` collapses. The turn cost was never one query; it was
// that the agent cannot know which query will hit, so it pays a turn per guess
// (--grep, then the -S pickaxe, then a path scope). gk find runs them together.
//
// Note this is decided BEFORE isRawContextProbe: these are read-only probes of
// the very same verbs (log/branch), and crediting them to `gk context` — which
// reports the current repo's state and cannot answer any of them — is what
// inflated the context group's turn savings.
func isRawHistorySearch(subcmd string, args []string) bool {
	switch subcmd {
	case "log", "rev-list":
	case "branch":
		for _, raw := range args {
			a := trimShellToken(raw)
			if strings.HasPrefix(a, "--contains") || strings.HasPrefix(a, "--no-contains") {
				return true
			}
		}
		return false
	default:
		return false
	}
	for _, raw := range args {
		a := trimShellToken(raw)
		if a == "--" {
			return true // path-scoped history — gk find --path
		}
		switch a {
		case "--all", "-p", "--patch", "--follow", "--source", "--reverse":
			return true
		}
		for _, p := range []string{"--grep", "--author", "--committer", "-S", "-G"} {
			if strings.HasPrefix(a, p) {
				return true
			}
		}
	}
	return false
}

// isRawRangeCompare matches `git log A..B` — "what is in B that is not in A".
// It is NOT a search, and gk find cannot answer it, so it must not be folded in:
// over-claiming coverage is exactly what made the old context group's numbers
// fiction. gk log --ahead/--behind --base answers the upstream/base spellings;
// an arbitrary two-ref range still has no verb, and stays a gap.
func isRawRangeCompare(subcmd string, args []string) bool {
	switch subcmd {
	case "log", "rev-list":
	default:
		return false
	}
	for _, raw := range args {
		a := trimShellToken(raw)
		if a == "--" {
			break // pathspecs follow — a path is not a range
		}
		if !strings.HasPrefix(a, "-") && strings.Contains(a, "..") {
			return true
		}
	}
	return false
}

// minHexOperandLen is the shortest all-hex operand treated as an explicit
// commit sha — seven matches git's default abbreviation floor.
const minHexOperandLen = 7

// hasHexCommitOperand reports whether a non-flag operand names a commit by raw
// hex sha. Operands after `--` are pathspecs, a `<rev>:<path>` form only names
// a commit in its rev half, and `..`/`...` ranges are checked per side with
// `^`/`~` suffixes stripped.
func hasHexCommitOperand(args []string) bool {
	for _, a := range args {
		a = trimShellToken(a)
		if a == "--" {
			break
		}
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		if i := strings.IndexByte(a, ':'); i >= 0 {
			a = a[:i]
		}
		for part := range strings.SplitSeq(a, "..") {
			if i := strings.IndexAny(part, "^~"); i >= 0 {
				part = part[:i]
			}
			if isHexCommitToken(part) {
				return true
			}
		}
	}
	return false
}

// isHexCommitToken matches lowercase hex of sha length. At least one digit is
// required so a word that happens to spell in a-f (`deadbeef` as a branch
// name) is not mistaken for a sha; real 7+ char sha prefixes lack a digit
// only ~0.1% of the time.
func isHexCommitToken(tok string) bool {
	if len(tok) < minHexOperandLen {
		return false
	}
	digit := false
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		switch {
		case c >= '0' && c <= '9':
			digit = true
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return digit
}

func isRawConflictProbe(subcmd string, args []string) bool {
	switch subcmd {
	case "ls-files":
		return hasArg(args, "-u") || hasArg(args, "--unmerged")
	case "diff":
		return hasArg(args, "--cc") || (hasArg(args, "--name-only") && hasArgValue(args, "--diff-filter", "U"))
	default:
		return false
	}
}

func isRawIntegration(subcmd string) bool {
	switch subcmd {
	case "pull", "fetch", "merge", "rebase", "cherry-pick":
		return true
	default:
		return false
	}
}

// isRawReadOnlyForm matches the read-only invocations of subcommands that mix
// inspection with mutation (remote, submodule). An agent legitimately reaches
// for these to orient, so they are not a coverage gap; the mutating forms
// (`remote add`/`set-url`, `submodule update`/`add`) fall through and stay
// reportable as genuine missing-verb signals.
func isRawReadOnlyForm(subcmd string, args []string) bool {
	switch subcmd {
	case "remote":
		// bare `git remote`, `-v`, `show`, `get-url` list/inspect; the rest mutate.
		if len(args) == 0 {
			return true
		}
		switch args[0] {
		case "-v", "--verbose", "show", "get-url":
			return true
		}
		return false
	case "submodule":
		// bare `git submodule`, `status`, `summary` inspect; the rest mutate.
		if len(args) == 0 {
			return true
		}
		switch args[0] {
		case "status", "summary":
			return true
		}
		return false
	default:
		return false
	}
}

// isRawBranchSwitch matches raw branch movement that git-kit switch covers.
// `git switch` is always branch-oriented; for the overloaded `git checkout` the
// `--` (or bare-pathspec) restore form is excluded — that is file restoration,
// a different operation git-kit switch does not replace.
func isRawBranchSwitch(subcmd string, args []string) bool {
	switch subcmd {
	case "switch":
		return true
	case "checkout":
		return !hasArg(args, "--")
	default:
		return false
	}
}

func isRawWorktree(subcmd string) bool { return subcmd == "worktree" }

// gitKitStashCovers reports whether `git stash <args>` maps to a git-kit stash
// subcommand. git-kit stash registers push/list/pop/apply/drop (internal/cli/
// stash.go) plus the bare interactive picker; show/clear/branch/create/store
// have no git-kit verb and must stay a gap — mirroring the `checkout -- <file>`
// exclusion in isRawBranchSwitch.
func gitKitStashCovers(args []string) bool {
	if len(args) == 0 {
		return true // bare `git stash` → interactive picker / push
	}
	switch trimShellToken(args[0]) {
	case "push", "list", "pop", "apply", "drop":
		return true
	case "show", "clear", "branch", "create", "store", "save":
		return false // no git-kit stash verb — keep visible as a gap
	default:
		return true // a flag/message (-m, -p, -u): subcommand omitted == push
	}
}

// gitSegmentFinding maps one raw-git segment to the covered finding kind it
// triggers, or "" when the segment needs no git-kit nudge. It is the single
// source of truth shared by the audit aggregation (addFindings) and the
// single-command Hint, so adding a classifier improves both at once. tag/push
// are handled by the caller because raw-release-sequence aggregates across
// segments.
func gitSegmentFinding(subcmd string, args []string) string {
	switch {
	// Conflict probes first: `git diff --name-only --diff-filter=U` also
	// matches the context-probe shape, and the conflict finding must win.
	case isRawConflictProbe(subcmd, args):
		return "raw-conflict-probes"
	// History search and branch survey are read-only probes of the SAME verbs as
	// a context probe (log / branch), so they must be decided first — otherwise
	// `gk context` gets credited with turns it cannot save.
	case isRawHistorySearch(subcmd, args):
		return "raw-history-search"
	case isRawRangeCompare(subcmd, args):
		return "raw-range-compare"
	case isRawBranchList(subcmd, args):
		return "raw-branch-list"
	case isRawContextProbe(subcmd, args):
		return "raw-context-probes"
	case subcmd == "add" || subcmd == "commit":
		return "raw-commit-sequence"
	case subcmd == "apply":
		return "raw-apply"
	case isRawIntegration(subcmd):
		return "raw-integration"
	case isRawBranchSwitch(subcmd, args):
		return "raw-branch-switch"
	case isRawWorktree(subcmd):
		return "raw-worktree"
	case subcmd == "clone":
		return "raw-clone"
	case subcmd == "filter-repo":
		return "raw-forget"
	case subcmd == "stash" && gitKitStashCovers(args):
		return "raw-stash"
	case isRawUnstage(subcmd, args):
		return "raw-unstage"
	case isRawResetHard(subcmd, args):
		return "raw-reset-hard"
	case isRawLostFound(subcmd, args):
		return "raw-lost-found"
	case subcmd == "diff" && hasArg(args, "--check"):
		return "raw-diff-check"
	case isRawFullDiff(subcmd, args):
		return "raw-full-diff"
	default:
		return ""
	}
}

// isRawUnstage matches the index-only forms git-kit unstage covers:
// `git reset` with no branch-moving mode flag and no commit other than HEAD —
// `git reset`, `git reset [-q] [--mixed] HEAD [paths]`, `git reset -- paths` —
// and the restore spelling `git restore --staged <paths>` (adding --worktree
// touches file contents, which gk unstage never does, so that form stays
// uncovered). `--mixed` with HEAD is still index-only; with any other commit
// (`--soft/--hard/--mixed HEAD~1`, a sha) the branch moves and the form
// stays in the uncovered gap. Known limit: the pathspec-only form
// (`git reset a.go`) IS an unstage, but a bare token is indistinguishable
// from a commit-ish (`git reset origin/main`) without repo state, so it is
// deliberately left uncounted rather than misclassifying history resets.
func isRawUnstage(subcmd string, args []string) bool {
	switch subcmd {
	case "restore":
		return hasAnyArg(args, "--staged", "-S") && !hasAnyArg(args, "--worktree", "-W")
	case "reset":
	default:
		return false
	}
	sawTarget := false
	for _, a := range args {
		if a == "--" {
			break // everything after is pathspec
		}
		if strings.HasPrefix(a, "-") {
			switch a {
			case "-q", "--quiet", "--mixed":
				continue // --mixed is git's default: index-only when aimed at HEAD
			}
			return false // branch-moving mode flag or unknown switch
		}
		if !sawTarget {
			if a != "HEAD" {
				return false // a commit-ish target — history rewrite, not unstage
			}
			sawTarget = true
			continue
		}
		// tokens after HEAD are pathspecs — still an unstage.
	}
	return true
}

// isRawResetHard matches `git reset --hard <commit-ish>` — the destructive form
// `gk reset --to <ref>` covers: same target, but it writes a backup ref and gates
// on a confirm before throwing work away.
//
// A BARE `git reset --hard` is deliberately NOT matched. It discards the working
// tree at HEAD, whereas a bare `gk reset` resets to the UPSTREAM remote branch —
// same shape, different destination, and recommending it would move the branch
// the user never asked to move. `--soft` is likewise excluded: gk's only
// uncommit-but-keep-the-work path is the interactive `gk undo` picker, which an
// agent cannot drive.
func isRawResetHard(subcmd string, args []string) bool {
	if subcmd != "reset" || !hasArg(args, "--hard") {
		return false
	}
	for _, a := range args {
		if a == "--" {
			break // pathspecs follow; a hard reset takes no target after this
		}
		if !strings.HasPrefix(a, "-") {
			return true // an explicit commit-ish target — this is gk reset --to
		}
	}
	return false
}

// isRawLostFound matches the `git fsck` forms that hunt for dangling work, which
// is exactly what `gk restore --lost` wraps. A bare `git fsck` is an integrity
// check, not a recovery hunt, and stays a gap.
func isRawLostFound(subcmd string, args []string) bool {
	return subcmd == "fsck" && hasAnyArg(args, "--lost-found", "--unreachable", "--dangling")
}

func isRawFullDiff(subcmd string, args []string) bool {
	switch subcmd {
	case "diff":
		return !hasAnyArg(args,
			"--check",
			"--stat",
			"--shortstat",
			"--name-only",
			"--name-status",
			"--quiet",
			"--exit-code",
			"--raw",
			"--numstat",
			"--summary",
			"--cc",
		)
	case "show":
		return !hasAnyArg(args,
			"--stat",
			"--shortstat",
			"--name-only",
			"--name-status",
			"--quiet",
			"--raw",
			"--numstat",
			"--summary",
		)
	default:
		return false
	}
}

func hasAnyArg(args []string, wants ...string) bool {
	for _, want := range wants {
		if hasArg(args, want) {
			return true
		}
	}
	return false
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		arg = trimShellToken(arg)
		if arg == want || strings.HasPrefix(arg, want+"=") {
			return true
		}
	}
	return false
}

func hasArgValue(args []string, flag, value string) bool {
	for i, arg := range args {
		arg = trimShellToken(arg)
		if arg == flag {
			return i+1 < len(args) && trimShellToken(args[i+1]) == value
		}
		if strings.HasPrefix(arg, flag+"=") {
			return strings.TrimPrefix(arg, flag+"=") == value
		}
	}
	return false
}

func truncateOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func sourceForPath(path string) string {
	clean := filepath.ToSlash(path)
	switch {
	case strings.Contains(clean, "/.codex/sessions/"):
		return "codex"
	case strings.Contains(clean, "/.claude/"):
		return "claude"
	default:
		return "unknown"
	}
}

// sortedProjects finalizes the per-project rollup: sessions with no
// git-shaped calls at all drop out (chat-only sessions are not an adoption
// signal), rates are computed, and the order is most-raw-git-first — the
// contract/hook install priority list.
func sortedProjects(projects map[string]*ProjectAdoption) []ProjectAdoption {
	out := make([]ProjectAdoption, 0, len(projects))
	for _, pa := range projects {
		total := pa.RawGit + pa.GitKit + pa.GKShort
		if total == 0 {
			continue
		}
		pa.Rate = float64(pa.GitKit) / float64(total)
		out = append(out, *pa)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RawGit != out[j].RawGit {
			return out[i].RawGit > out[j].RawGit
		}
		return out[i].Project < out[j].Project
	})
	return out
}

// projectKeyForPath derives the per-project rollup key for one session file.
// Claude keeps one directory per workspace (the slash-encoded path, e.g.
// -Users-me-work-gk), so the parent directory name identifies the project.
// Codex buckets sessions by date with no project marker — those aggregate
// under one "codex-sessions" key rather than pretending to know.
func projectKeyForPath(path, source string) string {
	if source == "codex" {
		return "codex-sessions"
	}
	return filepath.Base(filepath.Dir(path))
}

func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") && home != "" {
		return filepath.Join(home, path[2:])
	}
	return path
}
