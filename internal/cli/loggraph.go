package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/fatih/color"
)

// This file renders the commit graph from scratch — git's own `--graph` art
// compresses every fork/join into a single-cell diagonal (\ /), which can't be
// smoothed into connected lines after the fact. Instead we run our own lane
// assignment from the parent SHAs (commitRecord.parents) and draw the topology
// with box-drawing characters, picking each glyph from the four directions it
// connects (up/down/left/right). That makes corners (├ ╮ ╰ ╯) line up exactly.
//
// Layout mirrors git: one node row per commit, with a link row inserted only
// when the lane structure actually changes (a merge forks lanes, several
// children join into one commit, or a lane ends). Pure linear steps emit no
// link row, so a straight history stays compact.

// Box-drawing glyphs for the graph. Rounded corners (╭ ╮ ╰ ╯) read as smoother
// joins than the sharp ┌ ┐ └ ┘ variants.
const (
	gNode    = "●"
	gVert    = "│"
	gHoriz   = "─"
	gTeeR    = "├" // up + down + right
	gTeeL    = "┤" // up + down + left
	gTeeDown = "┬" // down + left + right
	gTeeUp   = "┴" // up + left + right
	gCornDL  = "╮" // down + left   (incoming from the left, turning down)
	gCornDR  = "╭" // down + right
	gCornUL  = "╯" // up + left
	gCornUR  = "╰" // up + right
	gCross   = "┼" // up + down + left + right
)

// dir bit flags for boxGlyph.
const (
	dUp = 1 << iota
	dDown
	dLeft
	dRight
)

// boxGlyph maps a set of connected directions to the box-drawing glyph that
// joins exactly those sides. Unhandled combinations fall back to a vertical
// bar, which is the safest "something passes through here" default.
func boxGlyph(mask int) string {
	switch mask {
	case 0:
		return " "
	case dUp, dDown, dUp | dDown:
		return gVert
	case dLeft, dRight, dLeft | dRight:
		return gHoriz
	case dUp | dDown | dRight:
		return gTeeR
	case dUp | dDown | dLeft:
		return gTeeL
	case dDown | dLeft | dRight:
		return gTeeDown
	case dUp | dLeft | dRight:
		return gTeeUp
	case dDown | dLeft:
		return gCornDL
	case dDown | dRight:
		return gCornDR
	case dUp | dLeft:
		return gCornUL
	case dUp | dRight:
		return gCornUR
	case dUp | dDown | dLeft | dRight:
		return gCross
	default:
		return gVert
	}
}

// graphLanePalette rotates per-column so adjacent lanes stay distinguishable,
// matching the spirit of git's --graph coloring.
var graphLanePalette = []*color.Color{
	color.New(color.FgRed),
	color.New(color.FgGreen),
	color.New(color.FgYellow),
	color.New(color.FgBlue),
	color.New(color.FgMagenta),
	color.New(color.FgCyan),
}

// renderSelfGraph draws records (HEAD-first) as a box-drawing commit graph,
// calling renderRow to produce the per-commit viz content placed to the right
// of each node row. trimWidth>0 truncates each composed line to that many
// visible cells (graph art included). useColor tints lanes by column.
func renderSelfGraph(w io.Writer, records []commitRecord, useColor bool, trimWidth int, renderRow func(commitRecord) string) {
	g := &graphState{useColor: useColor}
	for _, c := range records {
		nodeArt, linkArt := g.step(c)
		line := nodeArt + renderRow(c)
		if trimWidth > 0 {
			line = trimToVisible(line, trimWidth)
		}
		fmt.Fprintln(w, line)
		if linkArt != "" {
			if trimWidth > 0 {
				linkArt = trimToVisible(linkArt, trimWidth)
			}
			fmt.Fprintln(w, linkArt)
		}
	}
}

// graphState carries the active lanes across commits. lanes[i] is the SHA that
// column i is currently waiting to draw ("" = free column).
type graphState struct {
	lanes    []string
	useColor bool
}

func (g *graphState) tint(col int, s string) string {
	if !g.useColor {
		return s
	}
	return graphLanePalette[col%len(graphLanePalette)].Sprint(s)
}

// step advances the graph by one commit and returns its node row art and the
// link row art (empty when no structural change needs drawing). The trailing
// gap after the last lane separates the art from the viz content.
func (g *graphState) step(c commitRecord) (nodeArt, linkArt string) {
	// Locate the column waiting for this commit; allocate one if it has no
	// child in view (HEAD or a branch tip).
	myLane := indexOf(g.lanes, c.sha)
	if myLane < 0 {
		myLane = g.alloc(c.sha)
	}
	// Other columns waiting on the same commit are extra children that join
	// into myLane on the link row below.
	var joins []int
	for i, s := range g.lanes {
		if i != myLane && s == c.sha {
			joins = append(joins, i)
		}
	}

	// Snapshot the "before" activity for the link row's up-direction test.
	before := append([]string(nil), g.lanes...)

	// --- node row ---
	var nb strings.Builder
	for i := range g.lanes {
		switch {
		case i == myLane:
			nb.WriteString(g.tint(i, gNode))
		case g.lanes[i] != "":
			nb.WriteString(g.tint(i, gVert))
		default:
			nb.WriteByte(' ')
		}
		nb.WriteByte(' ')
	}
	nodeArt = nb.String()

	// --- advance lanes to this commit's parents ---
	for _, j := range joins {
		g.lanes[j] = ""
	}
	var forks []int
	if len(c.parents) == 0 {
		g.lanes[myLane] = ""
	} else {
		g.lanes[myLane] = c.parents[0]
		for _, p := range c.parents[1:] {
			col := indexOf(g.lanes, p)
			if col < 0 {
				col = g.alloc(p)
			}
			forks = append(forks, col)
		}
	}

	// A link row is only needed when something structural happens: a fork
	// (merge), a join (multi-child), or this commit ending its lane.
	ended := len(c.parents) == 0
	if len(joins) == 0 && len(forks) == 0 && !ended {
		g.trimTrailing()
		return nodeArt, ""
	}

	linkArt = g.linkRow(before, myLane, joins, forks)
	g.trimTrailing()
	return nodeArt, linkArt
}

// linkRow draws the transition between the node row above (lane activity in
// `before`, plus the node at myLane) and the next node row (current g.lanes).
// joins are columns merging leftward into myLane; forks are columns branching
// out of myLane for extra parents. Horizontal runs connect myLane to each
// join/fork endpoint, and every column's glyph is derived from the four sides
// it connects.
func (g *graphState) linkRow(before []string, myLane int, joins, forks []int) string {
	width := len(before)
	if len(g.lanes) > width {
		width = len(g.lanes)
	}

	// Horizontal coverage: a column has a left/right stub if it sits inside a
	// run between myLane and a join/fork endpoint. hGap[i] marks the cell
	// between column i and i+1 as a horizontal line.
	hGap := make([]bool, width)
	mark := func(a, b int) {
		if a > b {
			a, b = b, a
		}
		for i := a; i < b; i++ {
			hGap[i] = true
		}
	}
	for _, j := range joins {
		mark(myLane, j)
	}
	for _, f := range forks {
		mark(myLane, f)
	}

	up := func(i int) bool {
		// Active above if the column held a commit there (node) or a lane.
		return i == myLane || (i < len(before) && before[i] != "")
	}
	down := func(i int) bool {
		return i < len(g.lanes) && g.lanes[i] != ""
	}
	left := func(i int) bool { return i > 0 && hGap[i-1] }
	right := func(i int) bool { return i < width && hGap[i] }

	var b strings.Builder
	for i := 0; i < width; i++ {
		mask := 0
		if up(i) {
			mask |= dUp
		}
		if down(i) {
			mask |= dDown
		}
		if left(i) {
			mask |= dLeft
		}
		if right(i) {
			mask |= dRight
		}
		b.WriteString(g.tint(i, boxGlyph(mask)))
		// The inter-lane gap is a horizontal line when the run spans it,
		// tinted with the destination column so forks carry the new lane color.
		if hGap[i] {
			b.WriteString(g.tint(i+1, gHoriz))
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// alloc returns a free column for sha, reusing the leftmost empty lane or
// appending a new one.
func (g *graphState) alloc(sha string) int {
	for i, s := range g.lanes {
		if s == "" {
			g.lanes[i] = sha
			return i
		}
	}
	g.lanes = append(g.lanes, sha)
	return len(g.lanes) - 1
}

// trimTrailing drops empty lanes off the right edge so the graph doesn't keep
// dead columns after branches merge back.
func (g *graphState) trimTrailing() {
	for len(g.lanes) > 0 && g.lanes[len(g.lanes)-1] == "" {
		g.lanes = g.lanes[:len(g.lanes)-1]
	}
}

// indexOf returns the first index of s in xs, or -1.
func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}
