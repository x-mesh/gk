package aicommit

import (
	"fmt"
	"strings"
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

// TruncateDiff trims a unified diff so its length stays under capBytes
// while preserving file boundaries. Per-file output is one of:
//
//   - the full file diff if it fits the remaining budget
//   - the file's "diff --git" header + a "[truncated: N bytes]" stub
//     when the file alone exceeds the per-file budget
//
// The function never splits inside a hunk — partial hunks confuse the
// LLM and produce nonsense subjects. capBytes <= 0 disables the cap
// and returns the input unchanged.
//
// The returned string ends with a "[gk: truncated to fit budget]"
// marker if any trimming occurred. The provider system prompt treats
// the diff fence as untrusted data, so this marker is informational
// for human auditors only.
func TruncateDiff(diff string, capBytes int) string {
	if capBytes <= 0 || len(diff) <= capBytes {
		return diff
	}

	files := splitDiffByFile(diff)
	if len(files) == 0 {
		// Not a recognisable git diff — fall back to byte trim with
		// a trailing marker so the caller knows truncation happened.
		return diff[:capBytes] + "\n[gk: truncated to fit budget]\n"
	}

	var b strings.Builder
	remaining := capBytes
	dropped := 0

	for _, f := range files {
		if len(f.body) <= remaining {
			b.WriteString(f.body)
			remaining -= len(f.body)
			continue
		}
		// File too big for the remaining budget. Emit just the header
		// (so the model still sees which file was touched) and a
		// length stub. If even the header doesn't fit, we drop the
		// rest entirely — better than a corrupt diff.
		stub := f.header + fmt.Sprintf("[gk: %d byte(s) of diff truncated]\n", len(f.body)-len(f.header))
		if len(stub) <= remaining {
			b.WriteString(stub)
			remaining -= len(stub)
		} else {
			dropped++
		}
	}

	if dropped > 0 {
		b.WriteString(fmt.Sprintf("\n[gk: %d additional file(s) omitted to fit budget]\n", dropped))
	}
	return b.String()
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
