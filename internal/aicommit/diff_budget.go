package aicommit

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/x-mesh/gk/internal/diff"
)

// DefaultComposeDiffByteCap is the per-group diff size we hand to
// Provider.Compose. ~32KB ≈ 8K tokens, well under the per-call ceiling
// for every supported model (Groq llama-3.3-70b allows 8K input
// tokens per request on the free tier, where this limit was set).
//
// Why bytes, not tokens: cheap to compute and within an order of
// magnitude of the actual token count. We don't need an exact bound —
// we need to stop a single 200KB lockfile diff from eating the daily
// quota.
const DefaultComposeDiffByteCap = 32 * 1024

// DefaultComposePerFileDiffCap is the per-file diff size above which a
// single file's patch is replaced by a one-line semantic digest (changed
// symbols, ±lines, hunk count) instead of its raw hunks.
//
// A handful of large files — generated code, big mechanical refactors,
// vendored blobs — would otherwise dominate the compose payload while
// adding little the model needs to write a subject. The digest still
// answers "what changed, where" at a fraction of the tokens. ~12KB ≈ 3K
// tokens: comfortably above an ordinary hand-edit, below the noise floor
// where digesting would start dropping detail the composer can use.
const DefaultComposePerFileDiffCap = 12 * 1024

// TruncateDiff trims a unified diff so its length stays under capBytes
// while preserving file boundaries. Per-file output is one of:
//
//   - a one-line semantic digest (changed symbols, ±lines, hunk count)
//     when the file's own diff exceeds DefaultComposePerFileDiffCap, or
//     when its raw hunks won't fit the remaining group budget
//   - the full file diff otherwise
//
// The function never splits inside a hunk — partial hunks confuse the
// LLM and produce nonsense subjects. capBytes <= 0 disables the cap (and
// per-file digesting) and returns the input unchanged.
//
// Digesting an oversized file instead of byte-truncating it keeps the
// signal the composer actually uses — which functions changed — at a
// tiny, predictable token cost, where the old behaviour emitted an opaque
// "[N bytes truncated]" stub the model could not reason about.
func TruncateDiff(unifiedDiff string, capBytes int) string {
	return truncateDiff(unifiedDiff, capBytes, DefaultComposePerFileDiffCap)
}

// truncateDiff is the cap-parameterised core; TruncateDiff supplies the
// default per-file cap. Split out so tests can exercise the per-file
// digest threshold without standing up a 12KB fixture.
func truncateDiff(rawDiff string, capBytes, perFileCap int) string {
	if capBytes <= 0 {
		return rawDiff
	}

	files := splitDiffByFile(rawDiff)
	if len(files) == 0 {
		// Not a recognisable git diff — fall back to byte trim with a
		// trailing marker so the caller knows truncation happened.
		if len(rawDiff) <= capBytes {
			return rawDiff
		}
		return rawDiff[:capBytes] + "\n[gk: truncated to fit budget]\n"
	}

	var b strings.Builder
	remaining := capBytes
	dropped := 0

	for _, f := range files {
		rep := f.body
		// Digest when the file is individually oversized, or when its raw
		// hunks can't fit what's left of the group budget.
		if (perFileCap > 0 && len(rep) > perFileCap) || len(rep) > remaining {
			rep = digestFileBlock(f)
		}
		if len(rep) <= remaining {
			b.WriteString(rep)
			remaining -= len(rep)
			continue
		}
		// Even the digest doesn't fit the sliver left. Keep just the
		// header so the model still sees which file was touched; drop it
		// entirely if even that won't fit (better than a corrupt diff).
		if len(f.header) <= remaining {
			b.WriteString(f.header)
			remaining -= len(f.header)
		} else {
			dropped++
		}
	}

	if dropped > 0 {
		fmt.Fprintf(&b, "\n[gk: %d additional file(s) omitted to fit budget]\n", dropped)
	}
	return b.String()
}

// digestFileBlock renders one file's per-file diff block as a compact
// one-line digest — the changed symbols, line deltas, and hunk count from
// the same model behind `gk diff --digest` — instead of its raw hunks.
// The "diff --git ... +++" header is kept so the file path and change kind
// stay visible; only the hunk body collapses. Falls back to a byte stub
// when the block can't be parsed as a unified diff.
func digestFileBlock(f fileDiff) string {
	res, err := diff.ParseUnifiedDiff(strings.NewReader(f.body))
	if err != nil || res == nil || len(res.Files) == 0 {
		return f.header + fmt.Sprintf("[gk: %d byte(s) of diff digested out]\n", len(f.body)-len(f.header))
	}
	fd := diff.BuildDigest(res).Files[0]
	summary := fmt.Sprintf("+%d/-%d lines · %d hunk(s)", fd.Added, fd.Deleted, fd.Hunks)
	// docs/build "symbols" are body text the generic hunk heuristic
	// mistook for signatures — noise. test/source function names are real.
	if kind := FileKind(fd.Path); kind != "docs" && kind != "build" {
		syms := fd.Symbols
		if len(syms) == 0 {
			// A pure-add file's only hunk is "@@ -0,0 +1,N @@" with no
			// function context, so BuildDigest finds no symbols and the
			// digest would read just "+N lines". Recover signal from the
			// added top-level declarations. This costs a few tokens — it is
			// a quality floor for digested adds, not a saving.
			syms = addedDeclSymbols(f.body)
		}
		if s := joinBareSymbols(syms, 6); s != "" {
			summary += " · symbols: " + s
		}
	}
	return f.header + "[gk: large diff digested · " + summary + "]\n"
}

// declLineRE matches a top-level declaration on an added line (after its
// leading '+'), capturing the declared name. It is deliberately
// conservative: a single decl keyword at column 0, optionally past a Go
// method receiver "(r *T)". That surfaces real API for pure-add files —
// where hunk headers give no function context — without dredging up
// locals, nested closures, or prose, across Go/Rust/Python/JS/TS/Java/C#.
//
// It favours zero false positives over completeness: it intentionally
// skips const/var *block* members (they are indented, indistinguishable
// from struct fields/map entries) and Java/C# return-typed methods (no
// decl keyword), trading a few missed names for never emitting garbage
// into a commit subject. Missed names only soften the digest; they never
// corrupt it.
var declLineRE = regexp.MustCompile(
	`^(?:export |default |pub |public |private |protected |static |async |final |abstract )*` +
		`(?:function|func|fn|def|class|type|struct|interface|enum|trait|impl|record|const|var|let)\b[ \t]+` +
		`(?:\([^)]*\)[ \t]*)?([A-Za-z_][A-Za-z0-9_]*)`)

// addedDeclSymbols extracts top-level declaration names from the added
// ('+') lines of a unified-diff file block, in first-seen order, deduped.
// Used as a digest fallback when the hunk headers carry no function
// context (the pure-add case). Only column-0 declarations count, so a
// generated file's nested fields/locals stay out of the digest.
func addedDeclSymbols(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		// Added content only; skip the "+++ b/path" file header.
		if len(line) < 2 || line[0] != '+' || strings.HasPrefix(line, "+++") {
			continue
		}
		content := line[1:]
		// Top-level only: no leading indentation after the '+'.
		if content[0] == ' ' || content[0] == '\t' {
			continue
		}
		m := declLineRE.FindStringSubmatch(content)
		if m == nil {
			continue
		}
		if name := m[1]; !seen[name] {
			seen[name] = true
			out = append(out, name)
			if len(out) >= 32 { // hard cap; joinBareSymbols trims to display count
				break
			}
		}
	}
	return out
}

// joinBareSymbols renders hunk-context signatures as bare identifiers for
// the digest line, capping the list with a "+N more" tail. Mirrors the
// cli package's joinSymbolNames/symbolBareName — kept separate to avoid an
// import edge for two tiny helpers, same rationale as groupKey/groupKeyLocal.
func joinBareSymbols(symbols []string, max int) string {
	if len(symbols) == 0 {
		return ""
	}
	names := make([]string, 0, len(symbols))
	for _, s := range symbols {
		names = append(names, bareSymbolName(s))
	}
	if len(names) > max {
		rest := len(names) - max
		names = append(names[:max], fmt.Sprintf("+%d more", rest))
	}
	return strings.Join(names, ", ")
}

// bareSymbolName extracts the identifier from a function-context line: the
// last word before the first "(" — language-agnostic enough for the
// signatures git's heuristics produce; lines without parens pass through
// truncated.
func bareSymbolName(sig string) string {
	s := sig
	if i := strings.IndexByte(s, '('); i >= 0 {
		s = s[:i]
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		if len(sig) > 40 {
			return sig[:37] + "..."
		}
		return sig
	}
	name := fields[len(fields)-1]
	if len(name) > 40 {
		name = name[:37] + "..."
	}
	return name
}

// FilterDiffByDeny removes per-file blocks whose path matches any deny
// glob from a unified git diff, returning the filtered diff and the
// dropped paths. Any prefix before the first "diff --git" marker (commit
// headers from `git show`/`git log -p`) is preserved. Input without git
// diff markers passes through unchanged — callers must not treat that as
// "filtered".
//
// This exists because deny_paths historically applied only to the
// working-tree file list: `git show <old-sha>` or `git log -p` would
// happily print a denied file's HISTORIC content, bypassing the gate
// entirely (gk chat cross-vendor research, Critical finding). Structural
// removal of whole file blocks is the defense; line-level secret regexes
// are the fallback, not the primary.
func FilterDiffByDeny(diff string, denyGlobs []string) (string, []string) {
	// Merge commits emit combined-diff blocks ("diff --cc <path>") that
	// splitDiffByFile's "diff --git" marker misses — normalize by
	// filtering those blocks first so `git show <merge>` cannot leak a
	// denied file through the combined view.
	diff, ccDropped := filterCombinedDiffByDeny(diff, denyGlobs)

	files := splitDiffByFile(diff)
	if files == nil {
		return diff, ccDropped
	}
	var b strings.Builder
	if i := strings.Index(diff, "diff --git "); i > 0 {
		b.WriteString(diff[:i])
	}
	dropped := ccDropped
	for _, f := range files {
		// A rename's PRE-image (a/) side counts too: "a/.env → b/kept.txt"
		// still describes the denied file's history.
		oldPath, newPath := diffBlockPaths(f.body)
		matched := ""
		if newPath != "" && matchDeny(newPath, denyGlobs) != "" {
			matched = newPath
		} else if oldPath != "" && oldPath != newPath && matchDeny(oldPath, denyGlobs) != "" {
			matched = oldPath // report the path that actually tripped the deny
		}
		if matched != "" {
			dropped = append(dropped, matched)
			continue
		}
		b.WriteString(f.body)
	}
	return b.String(), dropped
}

// filterCombinedDiffByDeny removes "diff --cc <path>" blocks (merge
// combined diffs) whose path matches a deny glob. Blocks end at the next
// "diff --cc " or "diff --git " marker.
func filterCombinedDiffByDeny(diff string, denyGlobs []string) (string, []string) {
	const marker = "diff --cc "
	if !strings.Contains(diff, marker) {
		return diff, nil
	}
	var b strings.Builder
	var dropped []string
	rest := diff
	for {
		i := strings.Index(rest, marker)
		if i < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:i])
		block := rest[i:]
		end := len(block)
		for _, m := range []string{"\ndiff --cc ", "\ndiff --git "} {
			if j := strings.Index(block[len(marker):], m); j >= 0 && j+len(marker)+1 < end {
				end = j + len(marker) + 1
			}
		}
		header := block
		if nl := strings.IndexByte(block, '\n'); nl >= 0 {
			header = block[:nl]
		}
		p := unquoteDiffPath(strings.TrimPrefix(header, marker))
		if p != "" && matchDeny(p, denyGlobs) != "" {
			dropped = append(dropped, p)
		} else {
			b.WriteString(block[:end])
		}
		rest = block[end:]
	}
	return b.String(), dropped
}

// diffBlockPaths extracts the pre- and post-image paths from a
// "diff --git a/X b/Y" header line. Quoted paths (core.quotePath: spaces,
// non-ASCII) are C-unquoted so deny globs match the real spelling.
func diffBlockPaths(block string) (oldPath, newPath string) {
	line := block
	if i := strings.IndexByte(block, '\n'); i >= 0 {
		line = block[:i]
	}
	line = strings.TrimPrefix(line, "diff --git ")

	// Post-image (b/) side.
	if i := strings.LastIndex(line, ` "b/`); i >= 0 {
		newPath = unquoteDiffPath(`"` + line[i+4:])
		line = line[:i]
	} else if i := strings.LastIndex(line, " b/"); i >= 0 {
		newPath = line[i+3:]
		line = line[:i]
	}
	// Pre-image (a/) side — whatever remains.
	if strings.HasPrefix(line, `"a/`) {
		oldPath = unquoteDiffPath(`"` + strings.TrimPrefix(line, `"a/`))
	} else if strings.HasPrefix(line, "a/") {
		oldPath = strings.TrimPrefix(line, "a/")
	}
	return oldPath, newPath
}

// unquoteDiffPath undoes git's C-style path quoting when present. A path
// that fails to unquote keeps its raw form (redaction still applies).
func unquoteDiffPath(p string) string {
	p = strings.TrimSpace(p)
	if len(p) >= 2 && strings.HasPrefix(p, `"`) {
		if !strings.HasSuffix(p, `"`) {
			p += `"`
		}
		if u, err := strconv.Unquote(p); err == nil {
			// No prefix stripping here: every caller passes a path with
			// its a// b/ prefix already consumed, and a REAL directory
			// named "a" or "b" must keep its name or full-path deny globs
			// miss it.
			return u
		}
		return strings.Trim(p, `"`)
	}
	return p
}

// fileDiff is one "diff --git ..." block.
type fileDiff struct {
	header string // includes the "diff --git" line + index/mode/--- /+++ lines
	body   string // header + hunks, i.e. the full per-file block
}

// splitDiffByFile splits a unified diff into per-file blocks. Returns
// nil when the input isn't a git diff (no "diff --git " markers).
func splitDiffByFile(diff string) []fileDiff {
	const marker = "diff --git "
	if !strings.Contains(diff, marker) {
		return nil
	}
	var out []fileDiff
	idx := 0
	for {
		start := strings.Index(diff[idx:], marker)
		if start < 0 {
			break
		}
		start += idx
		// Find the next "diff --git " — that's where this file ends.
		next := strings.Index(diff[start+len(marker):], marker)
		var end int
		if next < 0 {
			end = len(diff)
		} else {
			end = start + len(marker) + next
		}
		body := diff[start:end]
		out = append(out, fileDiff{
			header: extractFileHeader(body),
			body:   body,
		})
		idx = end
	}
	return out
}

// extractFileHeader returns everything from "diff --git" up to (but not
// including) the first hunk marker ("@@ "). When no hunk is present,
// returns the whole body — that's the "binary files differ" or
// rename-without-content case.
func extractFileHeader(fileBlock string) string {
	i := strings.Index(fileBlock, "\n@@ ")
	if i < 0 {
		return fileBlock
	}
	return fileBlock[:i+1] // include the trailing newline before "@@"
}
