package aicommit

import (
	"fmt"
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
		if syms := joinBareSymbols(fd.Symbols, 6); syms != "" {
			summary += " · symbols: " + syms
		}
	}
	return f.header + "[gk: large diff digested · " + summary + "]\n"
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
