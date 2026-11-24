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
	maskRun := func(r int) {
		if masked[r] {
			return
		}
		masked[r] = true
		run := runs[r]
		for i := run.first; i <= run.last; i++ {
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
			maskRun(r)
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
		for _, value := range run.values {
			if len(e.scanLines(path, []string{value})) > 0 {
				maskRun(r)
				break
			}
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

// encodedRun is an encoded value folded over lines first to last. starts
// holds, for each of its lines, where the piece of the value begins. values
// are the pieces put back together: all of them, and, when the first line has
// text before its piece, also the pieces below it. The word that ends such a
// line can be text and not the start of the value, and a base64 value read
// from the wrong character decodes to nothing.
type encodedRun struct {
	first, last int
	starts      []int
	values      []string
}

// encodedPiece is a line of a folded value: indentation, then base64 or hex
// and nothing else. A piece can be one character and its padding, since a
// value can be folded at any width. The first line of a value can have a key
// before it.
var (
	encodedPiece     = regexp.MustCompile(`^\s*([A-Za-z0-9+/_-]+={0,2})\s*$`)
	encodedFirstLine = regexp.MustCompile(`([A-Za-z0-9+/_-]+={0,2})\s*$`)
)

// encodedRunMin is the length of the shortest value that is scanned. The
// encoded form of the shortest secret a rule finds is longer, and a shorter
// run, such as two lines of code that end in a word, is not worth a scan.
const encodedRunMin = 16

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
		if all := strings.Join(pieces, ""); len(all) >= encodedRunMin {
			run.values = append(run.values, all)
		}
		if strings.TrimSpace(lines[i][:head[2]]) != "" {
			if rest := strings.Join(pieces[1:], ""); len(rest) >= encodedRunMin {
				run.values = append(run.values, rest)
			}
		}
		if len(run.values) > 0 {
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
