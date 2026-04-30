package git

import (
	"reflect"
	"testing"
)

func TestParseConflictLines_SingleRegion(t *testing.T) {
	lines := []string{
		"package x",       // 1
		"",                // 2
		"func main() {",   // 3
		"<<<<<<< HEAD",    // 4
		"    x := 1",      // 5
		"    y := x + 1",  // 6
		"=======",         // 7
		"    x := 2",      // 8
		">>>>>>> feature", // 9
		"    println(x)",  // 10
		"}",               // 11
	}
	regions := parseConflictLines(lines)
	if len(regions) != 1 {
		t.Fatalf("len(regions) = %d, want 1", len(regions))
	}
	r := regions[0]

	if r.StartMarkerLine != 4 || r.MidMarkerLine != 7 || r.EndMarkerLine != 9 {
		t.Errorf("marker lines = %d/%d/%d, want 4/7/9",
			r.StartMarkerLine, r.MidMarkerLine, r.EndMarkerLine)
	}
	if r.OursLabel != "HEAD" {
		t.Errorf("OursLabel = %q, want HEAD", r.OursLabel)
	}
	if r.TheirsLabel != "feature" {
		t.Errorf("TheirsLabel = %q, want feature", r.TheirsLabel)
	}
	wantOurs := []ConflictLine{
		{LineNum: 5, Text: "    x := 1"},
		{LineNum: 6, Text: "    y := x + 1"},
	}
	if !reflect.DeepEqual(r.Ours, wantOurs) {
		t.Errorf("Ours = %+v, want %+v", r.Ours, wantOurs)
	}
	wantTheirs := []ConflictLine{
		{LineNum: 8, Text: "    x := 2"},
	}
	if !reflect.DeepEqual(r.Theirs, wantTheirs) {
		t.Errorf("Theirs = %+v, want %+v", r.Theirs, wantTheirs)
	}
	if r.ContextBefore == nil || r.ContextBefore.LineNum != 3 || r.ContextBefore.Text != "func main() {" {
		t.Errorf("ContextBefore = %+v", r.ContextBefore)
	}
	if r.ContextAfter == nil || r.ContextAfter.LineNum != 10 || r.ContextAfter.Text != "    println(x)" {
		t.Errorf("ContextAfter = %+v", r.ContextAfter)
	}
}

func TestParseConflictLines_MultipleRegions(t *testing.T) {
	lines := []string{
		"a", "b",
		"<<<<<<< HEAD", "ours-1", "=======", "theirs-1", ">>>>>>> br1",
		"middle",
		"<<<<<<< HEAD", "ours-2", "=======", "theirs-2a", "theirs-2b", ">>>>>>> br1",
		"end",
	}
	regions := parseConflictLines(lines)
	if len(regions) != 2 {
		t.Fatalf("len(regions) = %d, want 2", len(regions))
	}
	if regions[0].OursLabel != "HEAD" || regions[1].OursLabel != "HEAD" {
		t.Errorf("OursLabels = %q,%q", regions[0].OursLabel, regions[1].OursLabel)
	}
	if len(regions[1].Theirs) != 2 {
		t.Errorf("regions[1].Theirs len = %d, want 2", len(regions[1].Theirs))
	}
}

func TestParseConflictLines_BareMarkerNoLabel(t *testing.T) {
	// Some tools (or hand-edited files) emit bare markers with no label.
	lines := []string{
		"<<<<<<<",
		"ours",
		"=======",
		"theirs",
		">>>>>>>",
	}
	regions := parseConflictLines(lines)
	if len(regions) != 1 {
		t.Fatalf("len(regions) = %d, want 1", len(regions))
	}
	if regions[0].OursLabel != "" || regions[0].TheirsLabel != "" {
		t.Errorf("expected empty labels, got %q / %q",
			regions[0].OursLabel, regions[0].TheirsLabel)
	}
}

func TestParseConflictLines_MissingMidMarkerSkipped(t *testing.T) {
	lines := []string{
		"<<<<<<< HEAD",
		"ours",
		">>>>>>> feature", // no ======= → not a valid region
		"after",
	}
	regions := parseConflictLines(lines)
	if len(regions) != 0 {
		t.Errorf("len(regions) = %d, want 0 (no ======= marker)", len(regions))
	}
}

func TestParseConflictLines_MissingEndMarkerSkipped(t *testing.T) {
	lines := []string{
		"<<<<<<< HEAD",
		"ours",
		"=======",
		"theirs",
		// no >>>>>>> end marker
	}
	regions := parseConflictLines(lines)
	if len(regions) != 0 {
		t.Errorf("len(regions) = %d, want 0", len(regions))
	}
}

func TestParseConflictLines_EmptySidesAllowed(t *testing.T) {
	// Genuine cases: deletion vs add. One side is empty.
	lines := []string{
		"<<<<<<< HEAD",
		"=======",
		"new",
		">>>>>>> feat",
	}
	regions := parseConflictLines(lines)
	if len(regions) != 1 {
		t.Fatalf("len(regions) = %d, want 1", len(regions))
	}
	if len(regions[0].Ours) != 0 {
		t.Errorf("Ours len = %d, want 0", len(regions[0].Ours))
	}
	if len(regions[0].Theirs) != 1 || regions[0].Theirs[0].Text != "new" {
		t.Errorf("Theirs = %+v", regions[0].Theirs)
	}
}

func TestParseConflictLines_NoContextAtFileEdges(t *testing.T) {
	lines := []string{
		"<<<<<<< HEAD",
		"o",
		"=======",
		"t",
		">>>>>>> b",
	}
	regions := parseConflictLines(lines)
	if len(regions) != 1 {
		t.Fatalf("got %d regions", len(regions))
	}
	if regions[0].ContextBefore != nil {
		t.Errorf("ContextBefore should be nil at file start, got %+v", regions[0].ContextBefore)
	}
	if regions[0].ContextAfter != nil {
		t.Errorf("ContextAfter should be nil at file end, got %+v", regions[0].ContextAfter)
	}
}

func TestTotalConflictLines(t *testing.T) {
	regions := []ConflictRegion{
		{Ours: make([]ConflictLine, 3), Theirs: make([]ConflictLine, 2)},
		{Ours: make([]ConflictLine, 1), Theirs: make([]ConflictLine, 4)},
	}
	if got := TotalConflictLines(regions); got != 10 {
		t.Errorf("TotalConflictLines = %d, want 10", got)
	}
}
