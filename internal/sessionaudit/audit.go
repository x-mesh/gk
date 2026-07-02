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
	"raw-full-diff": {
		kind:           "raw-full-diff",
		severity:       "low",
		status:         "covered",
		recommendation: "Use git-kit diff --raw-patch --json for exact unified patch text, or git-kit diff --json for parsed hunks.",
		coveredBy:      []string{"git-kit diff --raw-patch --json", "git-kit diff --json", "git-kit diff --digest"},
	},
	// git reset/restore are intentionally NOT mapped: gk reset means "reset to
	// remote" and gk restore recovers dangling work, so neither matches the raw
	// verbs' file/index semantics. stash maps only for the subcommands git-kit
	// stash actually registers — gitKitStashCovers gates show/clear/branch/etc.
	// back to a gap.
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
	"cherry": true,
	// low-level plumbing git-kit never wraps (some mutates the index/object
	// store, but none is a roadmap signal).
	"merge-tree": true, "read-tree": true, "checkout-index": true,
	"diff-tree": true, "update-index": true, "commit-graph": true,
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
			switch c {
			case '\n', ';':
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
	case isRawContextProbe(subcmd, args):
		return []string{"context", "--include=diff,log"}, true
	case isRawConflictProbe(subcmd, args):
		return []string{"context", "--include=conflict"}, true
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
// raw counts.
var oneShotGapSubcommands = map[string]bool{
	"init": true, "clone": true, "mv": true, "rm": true, "archive": true, "clean": true,
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
			return head, args[1:], true
		}
		return "", nil, false
	}
	return "", nil, false
}

func isRawContextProbe(subcmd string, args []string) bool {
	switch subcmd {
	case "status", "branch", "log", "rev-list", "merge-base":
		return true
	case "diff", "show":
		return hasArg(args, "--stat") || hasArg(args, "--shortstat") || hasArg(args, "--name-only") || hasArg(args, "--name-status")
	default:
		return false
	}
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
	case isRawContextProbe(subcmd, args):
		return "raw-context-probes"
	case isRawConflictProbe(subcmd, args):
		return "raw-conflict-probes"
	case subcmd == "add" || subcmd == "commit":
		return "raw-commit-sequence"
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
	case subcmd == "diff" && hasArg(args, "--check"):
		return "raw-diff-check"
	case isRawFullDiff(subcmd, args):
		return "raw-full-diff"
	default:
		return ""
	}
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

func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") && home != "" {
		return filepath.Join(home, path[2:])
	}
	return path
}
