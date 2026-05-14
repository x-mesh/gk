package cli

import (
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/diff"
)

// renderFileDiff(conflictsOnly=true)는 conflict marker가 포함된 hunk만
// 남기고 노란 배너를 본문 상단에 출력해야 한다.
func TestRenderFileDiff_ConflictsOnly(t *testing.T) {
	f := &diff.DiffFile{
		Hunks: []diff.Hunk{
			{
				Header: "@@ -1,2 +1,2 @@",
				Lines: []diff.DiffLine{
					{Kind: diff.LineContext, Content: "clean line", OldNum: 1, NewNum: 1},
				},
			},
			{
				Header: "@@ -10,5 +10,7 @@",
				Lines: []diff.DiffLine{
					{Kind: diff.LineContext, Content: "<<<<<<< HEAD", OldNum: 10, NewNum: 10},
					{Kind: diff.LineContext, Content: "ours", OldNum: 11, NewNum: 11},
					{Kind: diff.LineContext, Content: "=======", OldNum: 12, NewNum: 12},
					{Kind: diff.LineContext, Content: "theirs", OldNum: 13, NewNum: 13},
					{Kind: diff.LineContext, Content: ">>>>>>> feat", OldNum: 14, NewNum: 14},
				},
			},
		},
	}

	out := renderFileDiff(f, diff.RenderOptions{NoColor: true}, map[int]bool{}, true)

	if !strings.Contains(out, "conflicts only") {
		t.Errorf("배너가 출력에 없습니다:\n%s", out)
	}
	if strings.Contains(out, "clean line") {
		t.Errorf("conflict 없는 hunk가 필터링되지 않았습니다:\n%s", out)
	}
	if !strings.Contains(out, "<<<<<<< HEAD") {
		t.Errorf("conflict marker 라인이 출력에 없습니다:\n%s", out)
	}
}

// nextConflictIdx는 현재 인덱스보다 큰 첫 번째 충돌 인덱스로 점프하고,
// 끝에 도달하면 첫 번째로 wrap 한다.
func TestNextConflictIdx(t *testing.T) {
	cases := []struct {
		name      string
		conflicts []int
		current   int
		want      int
	}{
		{"empty", nil, 0, -1},
		{"jump forward", []int{1, 4, 7}, 0, 1},
		{"skip current", []int{1, 4, 7}, 1, 4},
		{"wrap from end", []int{1, 4, 7}, 7, 1},
		{"wrap past last", []int{1, 4, 7}, 9, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nextConflictIdx(tc.conflicts, tc.current); got != tc.want {
				t.Fatalf("nextConflictIdx(%v, %d) = %d; want %d",
					tc.conflicts, tc.current, got, tc.want)
			}
		})
	}
}
