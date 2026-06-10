package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/diff"
)

// gk diff --digest answers "what changed, where" without the patch body —
// the most frequent multi-turn agent pattern (status → diff --stat → per-file
// diff → Read) collapsed into one call. The JSON shape is a public agent
// contract: fields are append-only.

type diffDigestFileJSON struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	Status  string `json:"status"`
	Binary  bool   `json:"binary,omitempty"`
	Hunks   int    `json:"hunks"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	// Symbols are the function contexts the hunks touched (git's hunk-header
	// heuristic — no .gitattributes required), deduplicated in hunk order.
	Symbols []string `json:"symbols,omitempty"`
	// Kind tags non-source files (test / docs / ci / build) so an agent can
	// weigh "is this a behavior change or scaffolding" without reading paths.
	Kind string `json:"kind,omitempty"`
}

type diffDigestJSON struct {
	Schema int                  `json:"schema"`
	Files  []diffDigestFileJSON `json:"files"`
	Stat   diffDigestStatJSON   `json:"stat"`
}

type diffDigestStatJSON struct {
	Files   int `json:"files"`
	Hunks   int `json:"hunks"`
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
}

func digestToJSON(d diff.Digest) diffDigestJSON {
	out := diffDigestJSON{
		Schema: 1,
		Files:  make([]diffDigestFileJSON, 0, len(d.Files)),
		Stat: diffDigestStatJSON{
			Files: d.Stat.Files, Hunks: d.Stat.Hunks,
			Added: d.Stat.Added, Deleted: d.Stat.Deleted,
		},
	}
	for _, f := range d.Files {
		kind := aicommit.FileKind(f.Path)
		symbols := f.Symbols
		// docs/build 파일의 "심볼"은 git generic 휴리스틱이 잡은 본문
		// 텍스트 줄이라 노이즈다 — 계약에서 뺀다. test/ci는 함수명이
		// 실제 정보라 유지.
		if kind == "docs" || kind == "build" {
			symbols = nil
		}
		out.Files = append(out.Files, diffDigestFileJSON{
			Path:    f.Path,
			OldPath: f.OldPath,
			Status:  f.Status.String(),
			Binary:  f.Binary,
			Hunks:   f.Hunks,
			Added:   f.Added,
			Deleted: f.Deleted,
			Symbols: symbols,
			Kind:    kind,
		})
	}
	return out
}

// renderDiffDigest prints the one-line-per-file human view:
//
//	M  internal/cli/pull.go        +56 −12  ·3   runPullCore, emitPullJSON
//	A  internal/cli/land.go        +280 −0  ·1
//	   3 files · 7 hunks · +340 −12
func renderDiffDigest(w io.Writer, d diff.Digest, noColor bool) {
	statusGlyph := func(s diff.FileStatus) string {
		switch s {
		case diff.StatusAdded:
			return "A"
		case diff.StatusDeleted:
			return "D"
		case diff.StatusRenamed:
			return "R"
		case diff.StatusCopied:
			return "C"
		case diff.StatusModeChanged:
			return "T"
		default:
			return "M"
		}
	}
	pathWidth := 0
	for _, f := range d.Files {
		if n := len(displayDigestPath(f)); n > pathWidth {
			pathWidth = n
		}
	}
	for _, f := range d.Files {
		glyph := statusGlyph(f.Status)
		delta := fmt.Sprintf("+%d −%d", f.Added, f.Deleted)
		if f.Binary {
			delta = "binary"
		}
		kind := aicommit.FileKind(f.Path)
		names := f.Symbols
		if kind == "docs" || kind == "build" {
			names = nil
		}
		symbols := joinSymbolNames(names, 3)
		if kind != "" {
			if symbols == "" {
				symbols = "[" + kind + "]"
			} else {
				symbols = "[" + kind + "] " + symbols
			}
		}
		line := fmt.Sprintf("%s  %-*s  %12s  ·%-3d %s",
			glyph, pathWidth, displayDigestPath(f), delta, f.Hunks, symbols)
		if !noColor {
			switch f.Status {
			case diff.StatusAdded:
				line = cellGreen(glyph) + line[len(glyph):]
			case diff.StatusDeleted:
				line = cellRed(glyph) + line[len(glyph):]
			}
		}
		fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
	fmt.Fprintf(w, "   %d files · %d hunks · +%d −%d\n",
		d.Stat.Files, d.Stat.Hunks, d.Stat.Added, d.Stat.Deleted)
}

func displayDigestPath(f diff.FileDigest) string {
	if f.OldPath != "" && f.OldPath != f.Path {
		return f.OldPath + " → " + f.Path
	}
	return f.Path
}

// joinSymbolNames renders symbols for the one-line human view: signatures
// reduce to their bare name ("func runPullCore(cmd ...) error" →
// "runPullCore") and long lists truncate with a count — the JSON contract
// keeps the full signatures, this is display only.
func joinSymbolNames(symbols []string, max int) string {
	names := make([]string, 0, len(symbols))
	for _, s := range symbols {
		names = append(names, symbolBareName(s))
	}
	if len(names) > max {
		rest := len(names) - max
		names = append(names[:max], fmt.Sprintf("+%d more", rest))
	}
	return strings.Join(names, ", ")
}

// symbolBareName extracts the identifier from a function-context line: the
// last word before the first "(" — language-agnostic enough for the
// signatures git's heuristics produce; lines without parens pass through
// truncated.
func symbolBareName(sig string) string {
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
	// 메서드 리시버만 남은 경우("func (g *graphState)") 등 — 본문 라인
	// 폴백은 그대로 자른다.
	if len(name) > 40 {
		name = name[:37] + "..."
	}
	return name
}
