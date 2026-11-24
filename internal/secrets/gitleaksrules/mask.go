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
// replaced. So is a line that is one opaque run of base64, such as a line of
// a PEM block: apart from the block around it, it matches no rule.
func MaskLines(path string, lines []string) []string { return get().maskLines(path, lines) }

func (e *engine) maskLines(path string, lines []string) []string {
	out := append([]string(nil), lines...)
	if len(lines) == 0 {
		return out
	}
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

	for _, hit := range hits {
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
		if !found && hit.FileLine >= 1 && hit.FileLine <= len(out) {
			out[hit.FileLine-1] = lineMask
		}
	}
	for i, l := range lines {
		if opaqueBlob(l) {
			out[i] = lineMask
		}
	}
	return out
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
