package gitleaksrules

import (
	"regexp"
	"strings"
)

// lineMask replaces a line whose secret cannot be located in its text.
const lineMask = "********"

// MaskLines returns a copy of lines with every secret the ruleset finds
// masked in place, for callers that print source lines outside a scan report,
// such as `gk find`. path is the file the lines came from, or "" when they
// came from no file. The lines are scanned as one block, so a match that
// spans lines is found.
//
// It is stricter than a scan in three ways. The rules run with no path, so
// the path allowlists, which a scan uses to skip test fixtures and lock
// files, do not apply: a line that is printed must not depend on where it
// came from. Only the rules that are limited to a path, such as the Terraform
// password rule, run with path. A secret found only in decoded text, such as
// a base64 value, cannot be located in the line, so the whole line is
// replaced. When the encoded value is folded over the lines below, as in a
// YAML block, every line of it is replaced: the pieces would join back into
// the secret. The pieces of a folded value are also joined and scanned, since
// the decoder reads one line at a time and finds no secret in a short piece.
// Lines of text around the value that look like pieces, such as a word or the
// end of a heredoc, are left out by scanning each span of the lines. In a run
// longer than 48 lines, a value is found when it starts in its first 8 lines
// or ends in its last 8, and is at most 1 KiB long.
// So is a line that is one opaque run of base64, such as a line of a PEM
// block: apart from the block around it, it matches no rule.
func MaskLines(path string, lines []string) []string { return get().maskLines(path, lines) }

func (e *engine) maskLines(path string, lines []string) []string {
	out := append([]string(nil), lines...)
	if len(lines) == 0 {
		return out
	}
	runs := encodedRuns(lines)
	runOf := make([]int, len(lines))
	for i := range runOf {
		runOf[i] = -1
	}
	for r, run := range runs {
		for i := run.first; i <= run.last; i++ {
			runOf[i] = r
		}
	}
	masked := make([]bool, len(runs))
	// maskRun masks a run from its line from on; the lines above that are
	// text that only looked like a piece of the value.
	maskRun := func(r, from int) {
		if masked[r] {
			return
		}
		masked[r] = true
		run := runs[r]
		for i := run.first + from; i <= run.last; i++ {
			// Keep the text before the value on the first line, such as
			// "data: ", so the line still says what it holds.
			out[i] = lines[i][:run.starts[i-run.first]] + lineMask
		}
	}

	for _, hit := range e.scanLines(path, lines) {
		if hit.secret == "" {
			continue
		}
		found := false
		for _, part := range strings.Split(hit.secret, "\n") {
			if part == "" {
				continue
			}
			for i := range out {
				// A part too short to identify is masked only on the line
				// the match starts at; anywhere else it is ordinary text.
				if len(part) < 4 && i != hit.FileLine-1 {
					continue
				}
				// The search runs on the original line, because an earlier
				// hit may already hold a mask in out[i].
				if strings.Contains(lines[i], part) {
					found = true
					out[i] = strings.ReplaceAll(out[i], part, maskSecret(part))
				}
			}
		}
		if found || hit.FileLine < 1 || hit.FileLine > len(out) {
			continue
		}
		// The secret is in decoded text. Its encoded form can go on over the
		// lines below, and the pieces left there would join back into it.
		if r := runOf[hit.FileLine-1]; r >= 0 {
			maskRun(r, 0)
		} else {
			out[hit.FileLine-1] = lineMask
		}
	}
	// The decoder reads one line at a time, so an encoded secret folded over
	// short lines is found only with its pieces joined.
	for r, run := range runs {
		if masked[r] {
			continue
		}
		if from, ok := e.foldedSecret(path, run.pieces); ok {
			maskRun(r, from)
		}
	}
	for i, l := range lines {
		if opaqueBlob(l) {
			out[i] = lineMask
		}
	}
	return out
}

// scanLines scans lines as one block, the way MaskLines describes.
func (e *engine) scanLines(path string, lines []string) []hit {
	h := hunk{lines: lines, numbers: make([]int, len(lines))}
	for i := range h.numbers {
		h.numbers[i] = i + 1
	}
	h.joined = strings.Join(lines, "\n")
	h.lower = strings.ToLower(h.joined)
	f := fileDiff{hunks: []hunk{h}}
	raw, decoded := e.rawPasses(f), e.decodedPasses(f)
	hits := e.scanRaw(f, e.rules, raw)
	hits = append(hits, e.scanDecoded(f, e.rules, decoded)...)
	if path != "" {
		// With no path the pass above skipped these rules.
		var scoped []rule
		for _, r := range e.rules {
			if r.path != nil && r.regex != nil {
				scoped = append(scoped, r)
			}
		}
		f.path = path
		hits = append(hits, e.scanRaw(f, scoped, raw)...)
		hits = append(hits, e.scanDecoded(f, scoped, decoded)...)
	}
	return hits
}

// encodedRun is a run of lines that can hold an encoded value folded over
// them, first to last. starts holds, for each of its lines, where its piece
// begins, and pieces the pieces.
type encodedRun struct {
	first, last int
	starts      []int
	pieces      []string
}

// encodedPiece is a line of a folded value: indentation, then base64 or hex
// and nothing else. A piece can be one character and its padding, since a
// value can be folded at any width. The first line of a value can have a key
// before it.
var (
	encodedPiece     = regexp.MustCompile(`^\s*([A-Za-z0-9+/_-]+={0,2})\s*$`)
	encodedFirstLine = regexp.MustCompile(`([A-Za-z0-9+/_-]+={0,2})\s*$`)
)

const (
	// encodedRunMin is the length of the shortest value that is scanned.
	// The encoded form of the shortest secret a rule finds is longer.
	encodedRunMin = 16
	// encodedRunAll is the longest run whose every span of lines is
	// scanned. A scan of a value takes 20 to 200 µs, so a run of 48 lines,
	// 1128 spans, takes at most about 0.2 s. In a longer run, the spans
	// that start in its first encodedRunEdge lines or end in its last ones
	// are scanned, up to encodedValueMax bytes long.
	encodedRunAll   = 48
	encodedRunEdge  = 8
	encodedValueMax = 1 << 10
)

// foldedSecret looks for a secret in a run whose lines are pieces of an
// encoded value, and returns the piece the value starts at. A line of text
// above or below the value can look like a piece, such as "abc" or the "EOF"
// that ends a heredoc, and a value read from the wrong character, or with
// text after its end, decodes to nothing. So the spans of the run are
// scanned, the widest first.
func (e *engine) foldedSecret(path string, pieces []string) (from int, ok bool) {
	offsets := make([]int, len(pieces)+1)
	for i, p := range pieces {
		offsets[i+1] = offsets[i] + len(p)
	}
	all := strings.Join(pieces, "")
	scan := func(k, end int) bool {
		text := all[offsets[k]:offsets[end]]
		return len(text) >= encodedRunMin && len(e.scanLines(path, []string{text})) > 0
	}
	n := len(pieces)
	if n <= encodedRunAll {
		for size := n; size >= 2; size-- {
			for k := 0; k+size <= n; k++ {
				if scan(k, k+size) {
					return k, true
				}
			}
		}
		return 0, false
	}
	// The widest span up to encodedValueMax first, from each edge line.
	for k := 0; k < encodedRunEdge; k++ {
		end := k + 2
		for end < n && offsets[end+1]-offsets[k] <= encodedValueMax {
			end++
		}
		for ; end >= k+2; end-- {
			if scan(k, end) {
				return k, true
			}
		}
	}
	for end := n; end > n-encodedRunEdge; end-- {
		k := end - 2
		for k > 0 && offsets[end]-offsets[k-1] <= encodedValueMax {
			k--
		}
		for ; k <= end-2; k++ {
			if scan(k, end) {
				return k, true
			}
		}
	}
	return 0, false
}

// encodedRuns returns the folded values in lines: a line that ends in a piece
// of base64, and one or more lines below it that hold nothing else.
func encodedRuns(lines []string) []encodedRun {
	var runs []encodedRun
	for i := 0; i < len(lines); i++ {
		head := encodedFirstLine.FindStringSubmatchIndex(lines[i])
		if head == nil {
			continue
		}
		run := encodedRun{first: i, starts: []int{head[2]}}
		pieces := []string{lines[i][head[2]:head[3]]}
		j := i + 1
		for ; j < len(lines); j++ {
			m := encodedPiece.FindStringSubmatchIndex(lines[j])
			if m == nil {
				break
			}
			run.starts = append(run.starts, m[2])
			pieces = append(pieces, lines[j][m[2]:m[3]])
		}
		if j-i < 2 {
			continue
		}
		run.last = j - 1
		run.pieces = pieces
		if len(strings.Join(pieces, "")) >= encodedRunMin {
			runs = append(runs, run)
		}
		i = j - 1
	}
	return runs
}

var blobLine = regexp.MustCompile(`^[A-Za-z0-9+/_-]{40,}={0,2}$`)

// blobEntropy separates a base64 run from a long identifier. Measured on
// 64-character lines: a line of a PEM block is at 4.7 bits a character or
// more, and a name made of words, a path, or a constant is at 4.3 or less. A
// hex digest never passes 4, so it is left alone.
const blobEntropy = 4.5

// opaqueBlob reports whether the line is nothing but one long, random run of
// base64. A run with no digit is read as a name.
func opaqueBlob(line string) bool {
	t := strings.TrimSpace(line)
	return blobLine.MatchString(t) && strings.ContainsAny(t, "0123456789") && shannon(t) >= blobEntropy
}
