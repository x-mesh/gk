package aicommit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/x-mesh/gk/internal/scan"
	"github.com/x-mesh/gk/internal/secrets"
	"github.com/x-mesh/gk/internal/secrets/gitleaksrules"
)

// SecretFinding is one hit from the secret gate. Source names which
// scanner produced it ("builtin" or "gitleaks") so callers can phrase
// advice ("install gitleaks for deeper coverage") accurately.
type SecretFinding struct {
	Source string
	Kind   string
	File   string
	Line   int
	Sample string // masked / truncated; never raw secret
}

// SecretGateOptions configures the gate.
//
// ExtraPatterns adds repo-configured regexes to the built-in scanner
// (matches internal/secrets.Scan's second arg). AllowKinds suppresses
// findings whose Kind matches — used by `--allow-secret-kind` on the
// CLI. RunGitleaks gates the second scan with the gitleaks ruleset;
// default true. RepoDir is the repo root, where a gitleaks config is looked
// up; payload paths are relative to it.
type SecretGateOptions struct {
	ExtraPatterns []*regexp.Regexp
	AllowKinds    []string
	RunGitleaks   bool
	RepoDir       string
}

// Gitleaks lets tests inject a fake runner. When nil the payload is scanned
// with the gitleaks ruleset embedded in gk, or with the gitleaks binary when
// the repo has a gitleaks config (see defaultGitleaks).
type Gitleaks interface {
	Run(ctx context.Context) ([]scan.GitleaksFinding, error)
}

// defaultGitleaks picks the second scanner for a payload. A repo with a
// gitleaks config (.gitleaks.toml, .gitleaksignore, GITLEAKS_CONFIG*) gets
// the gitleaks binary, so its custom rules and ignores apply; every other
// repo gets the embedded ruleset, which needs no binary.
func defaultGitleaks(payload, repoDir string) Gitleaks {
	if scan.HasGitleaksConfig(repoDir) {
		return configuredGitleaks{payload: payload, repoDir: repoDir}
	}
	return embeddedGitleaks{payload: payload}
}

// configuredGitleaks writes the payload files into a temp directory and runs
// `gitleaks dir` there, so gitleaks scans exactly what the commit adds, not
// the process working directory (gk commit does not chdir to the repo root).
// The repo's config and ignore file are passed explicitly; reported paths
// stay repo-relative, so .gitleaksignore fingerprints still match. Without
// the binary it falls back to the embedded ruleset.
type configuredGitleaks struct{ payload, repoDir string }

func (c configuredGitleaks) Run(ctx context.Context) ([]scan.GitleaksFinding, error) {
	if _, _, ok := scan.FindGitleaks(); !ok {
		return embeddedGitleaks{payload: c.payload}.Run(ctx)
	}
	tmp, err := os.MkdirTemp("", "gk-secret-gate-*")
	if err != nil {
		return nil, fmt.Errorf("payload dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	for _, f := range splitPayload(c.payload) {
		if !filepath.IsLocal(f.path) {
			return nil, fmt.Errorf("payload path %q is outside the repo", f.path)
		}
		dst := filepath.Join(tmp, f.path)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, []byte(f.content), 0o600); err != nil {
			return nil, err
		}
	}

	var args []string
	// gitleaks prefers GITLEAKS_CONFIG* over the repo file; --config would
	// override that, so it is passed only when the repo file is the source.
	envConfig := os.Getenv("GITLEAKS_CONFIG") != "" || os.Getenv("GITLEAKS_CONFIG_TOML") != ""
	if cfg := filepath.Join(c.repoDir, ".gitleaks.toml"); !envConfig && fileExists(cfg) {
		args = append(args, "--config", cfg)
	}
	if fileExists(filepath.Join(c.repoDir, ".gitleaksignore")) {
		args = append(args, "--gitleaks-ignore-path", c.repoDir)
	}
	return scan.RunGitleaks(ctx, scan.GitleaksOptions{WorkDir: tmp, Mode: "dir", Redact: true, ExtraArgs: args})
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// embeddedGitleaks scans each file of the payload with the embedded gitleaks
// ruleset. It replaced a `gitleaks dir` run over the working directory, which
// blocked commits on files outside the payload, including the test files the
// payload deliberately leaves out.
type embeddedGitleaks struct{ payload string }

func (e embeddedGitleaks) Run(ctx context.Context) ([]scan.GitleaksFinding, error) {
	var out []scan.GitleaksFinding
	for _, f := range splitPayload(e.payload) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, hit := range gitleaksrules.ScanFile(f.path, f.content) {
			out = append(out, scan.GitleaksFinding{
				RuleID:    hit.Kind,
				File:      hit.File,
				StartLine: hit.FileLine,
				Match:     hit.Sample,
			})
		}
	}
	return out, nil
}

type payloadFile struct{ path, content string }

// splitPayload cuts a payload at its secrets.PayloadFileHeader lines. Text
// before the first header has no file and is left to the built-in scanner.
func splitPayload(payload string) []payloadFile {
	var files []payloadFile
	var body strings.Builder
	flush := func() {
		if len(files) > 0 {
			files[len(files)-1].content = body.String()
		}
		body.Reset()
	}
	for line := range strings.SplitSeq(payload, "\n") {
		if m := secrets.PayloadFileHeaderRE.FindStringSubmatch(line); m != nil {
			flush()
			files = append(files, payloadFile{path: m[1]})
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	flush()
	return files
}

// ScanPayload runs both scanners over the supplied text (usually the
// aggregated diff) and returns deduplicated SecretFindings. An injected
// Gitleaks reporting ErrGitleaksNotInstalled is skipped silently — the
// built-in scanner is always the baseline.
func ScanPayload(ctx context.Context, payload string, opts SecretGateOptions, gl Gitleaks) ([]SecretFinding, error) {
	allow := map[string]bool{}
	for _, k := range opts.AllowKinds {
		allow[k] = true
	}

	var out []SecretFinding

	builtin := secrets.Scan(payload, opts.ExtraPatterns)
	fileMap := buildLineToFileMap(payload)
	for _, f := range builtin {
		if allow[f.Kind] {
			continue
		}
		out = append(out, SecretFinding{
			Source: "builtin",
			Kind:   f.Kind,
			File:   fileMap.fileAt(f.Line),
			Line:   fileMap.relLine(f.Line),
			Sample: f.Sample,
		})
	}

	if opts.RunGitleaks {
		if gl == nil {
			gl = defaultGitleaks(payload, opts.RepoDir)
		}
		gf, err := gl.Run(ctx)
		switch {
		case err == nil:
			out = append(out, convertGitleaks(gf, allow)...)
		case errors.Is(err, scan.ErrGitleaksNotInstalled):
			// Optional scanner; silent skip.
		default:
			return nil, fmt.Errorf("aicommit: gitleaks: %w", err)
		}
	}

	return dedupeFindings(out), nil
}

func convertGitleaks(findings []scan.GitleaksFinding, allow map[string]bool) []SecretFinding {
	out := make([]SecretFinding, 0, len(findings))
	for _, f := range findings {
		kind := f.RuleID
		if kind == "" {
			kind = f.Description
		}
		if allow[kind] {
			continue
		}
		out = append(out, SecretFinding{
			Source: "gitleaks",
			Kind:   kind,
			File:   f.File,
			Line:   f.StartLine,
			Sample: f.Match,
		})
	}
	return out
}

// dedupeFindings drops exact duplicates (same source+kind+file+line).
// Different sources reporting the same secret are kept — users want
// to know both caught it.
func dedupeFindings(in []SecretFinding) []SecretFinding {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := make([]SecretFinding, 0, len(in))
	for _, f := range in {
		key := f.Source + "|" + f.Kind + "|" + f.File + "|" + strconv.Itoa(f.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}

// lineFileMap은 aggregated diff의 줄 번호를 파일 경로로 매핑한다.
// diff 헤더 ("diff --git a/X b/X" 또는 "--- a/X")를 파싱하여
// 각 줄이 어떤 파일에 속하는지 추적한다.
type lineFileMap struct {
	// entries는 (startLine, file) 쌍의 정렬된 목록이다.
	// startLine은 1-based.
	entries []lineFileEntry
}

type lineFileEntry struct {
	startLine int
	file      string
}

// diffHeaderRE matches "diff --git a/path b/path".
var diffHeaderRE = regexp.MustCompile(`^diff --git a/(.+?) b/`)

// concatFileHeaderRE matches "--- path (status)" emitted by concatFileDiffs.
// We do NOT match a bare "### path" here — that previously caused
// markdown H3 headers in real source/doc content to be misread as
// file boundaries (e.g. "### 첫 호출" surfacing as a phantom filename).
// summariseForSecretScan / scanDiffAdditions now emit secrets.PayloadFileHeader
// instead, parsed below.
var concatFileHeaderRE = regexp.MustCompile(`^--- (.+?)(?:\s*\(.*\))?$`)

// buildLineToFileMap은 payload를 파싱하여 lineFileMap을 생성한다.
// 인식하는 형식:
//   - secrets.PayloadFileHeader (">>> gk-file <path> <<<") — secret scan payload
//   - "diff --git a/X b/X" — unified diff
//   - "--- X (status)" — concatFileDiffs
func buildLineToFileMap(payload string) lineFileMap {
	var m lineFileMap
	for i, line := range strings.Split(payload, "\n") {
		lineNum := i + 1 // 1-based
		switch {
		case secrets.PayloadFileHeaderRE.MatchString(line):
			groups := secrets.PayloadFileHeaderRE.FindStringSubmatch(line)
			m.entries = append(m.entries, lineFileEntry{startLine: lineNum, file: groups[1]})
		case diffHeaderRE.MatchString(line):
			groups := diffHeaderRE.FindStringSubmatch(line)
			m.entries = append(m.entries, lineFileEntry{startLine: lineNum, file: groups[1]})
		case concatFileHeaderRE.MatchString(line):
			groups := concatFileHeaderRE.FindStringSubmatch(line)
			m.entries = append(m.entries, lineFileEntry{startLine: lineNum, file: groups[1]})
		}
	}
	return m
}

// fileAt는 주어진 줄 번호가 속하는 파일 경로를 반환한다.
func (m *lineFileMap) fileAt(line int) string {
	var best string
	for _, e := range m.entries {
		if e.startLine <= line {
			best = e.file
		} else {
			break
		}
	}
	return best
}

// relLine converts a 1-based blob line number into the equivalent
// 1-based line number within the file that owns it.
//
// summariseForSecretScan emits secrets.PayloadFileHeader as a header, then
// the file content starts on the *next* blob line. So if the header sits at
// blob line H, the file's line 1 lives at blob line H+1, line 2 at H+2, etc.
// The mapping is therefore `blob - H` — not `blob - H + 1`, which would
// off-by-one every reported finding (was reporting line 47 for what is
// actually line 46 in the source file).
func (m *lineFileMap) relLine(line int) int {
	var bestStart int
	for _, e := range m.entries {
		if e.startLine <= line {
			bestStart = e.startLine
		} else {
			break
		}
	}
	if bestStart == 0 {
		return line
	}
	return line - bestStart
}
