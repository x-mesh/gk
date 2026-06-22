package sessionaudit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
}

type Report struct {
	Schema   int          `json:"schema"`
	Files    []FileReport `json:"files"`
	Totals   Totals       `json:"totals"`
	Findings []Finding    `json:"findings,omitempty"`
	Notes    []string     `json:"notes,omitempty"`
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
	Kind           string     `json:"kind"`
	Severity       string     `json:"severity"`
	Status         string     `json:"status"`
	Count          int        `json:"count"`
	Recommendation string     `json:"recommendation"`
	CoveredBy      []string   `json:"covered_by,omitempty"`
	Gap            string     `json:"gap,omitempty"`
	Evidence       []Evidence `json:"evidence,omitempty"`
}

type Evidence struct {
	File    string `json:"file,omitempty"`
	Command string `json:"command"`
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
	"raw-full-diff": {
		kind:           "raw-full-diff",
		severity:       "low",
		status:         "covered",
		recommendation: "Use git-kit diff --raw-patch --json for exact unified patch text, or git-kit diff --json for parsed hunks.",
		coveredBy:      []string{"git-kit diff --raw-patch --json", "git-kit diff --json", "git-kit diff --digest"},
	},
	"raw-diff-check": {
		kind:           "raw-diff-check",
		severity:       "low",
		status:         "covered",
		recommendation: "Use git-kit diff --check --json so whitespace/conflict-marker checks stay in the git-kit JSON contract.",
		coveredBy:      []string{"git-kit diff --check", "git-kit diff --check --json"},
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
		status:         "partial",
		recommendation: "Use git-kit batch --plan - for multi-step git-kit workflows instead of shell chains.",
		coveredBy:      []string{"git-kit batch --plan -"},
		gap:            "session audit reports shell chains but does not yet synthesize a replacement batch plan from the observed commands.",
	},
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

	files, notes := collectFiles(expanded, opts.MaxFiles)
	report := Report{Schema: 1, Notes: notes}
	aggregate := map[string]*Finding{}
	for _, fc := range files {
		fr, commands, err := auditFile(fc.path)
		if err != nil {
			report.Notes = append(report.Notes, fmt.Sprintf("%s: %v", fc.path, err))
			continue
		}
		fr.Source = sourceForPath(fc.path)
		report.Files = append(report.Files, fr)
		report.Totals.Files++
		report.Totals.Commands += fr.Commands
		report.Totals.RawGit += fr.RawGit
		report.Totals.GitKit += fr.GitKit
		report.Totals.GKShort += fr.GKShort
		report.Totals.ShellChains += fr.ShellChains
		addFindings(aggregate, fc.path, commands)
	}
	report.Findings = sortedFindings(aggregate)
	return report, nil
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

func collectFiles(paths []string, maxFiles int) ([]fileCandidate, []string) {
	var out []fileCandidate
	var notes []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				notes = append(notes, fmt.Sprintf("%s: %v", p, err))
			}
			continue
		}
		if !info.IsDir() {
			out = append(out, fileCandidate{path: p, info: info})
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
			out = append(out, fileCandidate{path: path, info: info})
			return nil
		})
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s: %v", p, err))
		}
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
				if subcmd == "tag" {
					gitTag = true
					if releaseEvidence == "" {
						releaseEvidence = seg.Text
					}
				}
				if subcmd == "push" {
					gitPush = true
					if releaseEvidence == "" {
						releaseEvidence = seg.Text
					}
				}
				if isRawContextProbe(subcmd, args) {
					addFinding(findings, "raw-context-probes", file, seg.Text)
				}
				if isRawConflictProbe(subcmd, args) {
					addFinding(findings, "raw-conflict-probes", file, seg.Text)
				}
				if subcmd == "add" || subcmd == "commit" {
					addFinding(findings, "raw-commit-sequence", file, seg.Text)
				}
				if isRawIntegration(subcmd) {
					addFinding(findings, "raw-integration", file, seg.Text)
				}
				if subcmd == "diff" && hasArg(args, "--check") {
					addFinding(findings, "raw-diff-check", file, seg.Text)
				}
				if isRawFullDiff(subcmd, args) {
					addFinding(findings, "raw-full-diff", file, seg.Text)
				}
			case "gk":
				addFinding(findings, "gk-short-alias", file, seg.Text)
			}
		}
		if rawInChain && class.ShellChain {
			addFinding(findings, "shell-chain", file, cmd)
		}
	}
	if gitTag && gitPush {
		addFinding(findings, "raw-release-sequence", file, releaseEvidence)
	}
}

func addFinding(findings map[string]*Finding, kind, file, command string) {
	spec, ok := findingSpecs[kind]
	if !ok {
		return
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
	}
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
