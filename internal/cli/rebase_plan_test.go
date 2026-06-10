package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func planRange(shas ...string) []rebaseRangeCommit {
	out := make([]rebaseRangeCommit, 0, len(shas))
	for i, s := range shas {
		out = append(out, rebaseRangeCommit{SHA: s, Subject: "c" + string(rune('0'+i)), Parents: 1})
	}
	return out
}

const (
	shaA = "aaaa111aaaa111aaaa111aaaa111aaaa111aaaa1"
	shaB = "bbbb222bbbb222bbbb222bbbb222bbbb222bbbb2"
	shaC = "cccc333cccc333cccc333cccc333cccc333cccc3"
)

func TestParseRebasePlan_Shapes(t *testing.T) {
	// bare array
	p, err := parseRebasePlan(strings.NewReader(`[{"action":"pick","commit":"aaaa111"}]`))
	if err != nil || len(p.Entries) != 1 {
		t.Fatalf("array shape: %v %+v", err, p)
	}
	// template object shape
	p, err = parseRebasePlan(strings.NewReader(`{"commits":[{"action":"drop","commit":"bbbb222"}]}`))
	if err != nil || p.Entries[0].Action != "drop" {
		t.Fatalf("object shape: %v %+v", err, p)
	}
	if _, err := parseRebasePlan(strings.NewReader("  ")); err == nil {
		t.Error("empty plan must fail")
	}
}

func TestValidateRebasePlan_Rules(t *testing.T) {
	rng := planRange(shaA, shaB, shaC)
	mk := func(entries ...rebasePlanEntry) rebasePlan { return rebasePlan{Entries: entries} }
	noPushed := map[string]bool{}

	cases := []struct {
		name    string
		plan    rebasePlan
		rng     []rebaseRangeCommit
		pushed  map[string]bool
		allow   bool
		wantErr string
	}{
		{"missing commit", mk(
			rebasePlanEntry{Action: "pick", Commit: "aaaa111"},
			rebasePlanEntry{Action: "pick", Commit: "bbbb222"},
		), rng, noPushed, false, "does not address"},
		{"unknown sha", mk(
			rebasePlanEntry{Action: "pick", Commit: "deadbeef"},
		), rng, noPushed, false, "not in the rebase range"},
		{"duplicate", mk(
			rebasePlanEntry{Action: "pick", Commit: "aaaa111"},
			rebasePlanEntry{Action: "drop", Commit: "aaaa111"},
			rebasePlanEntry{Action: "pick", Commit: "bbbb222"},
			rebasePlanEntry{Action: "pick", Commit: "cccc333"},
		), rng, noPushed, false, "more than once"},
		{"leading squash", mk(
			rebasePlanEntry{Action: "squash", Commit: "aaaa111"},
			rebasePlanEntry{Action: "pick", Commit: "bbbb222"},
			rebasePlanEntry{Action: "pick", Commit: "cccc333"},
		), rng, noPushed, false, "first entry cannot be squash"},
		{"reword without message", mk(
			rebasePlanEntry{Action: "reword", Commit: "aaaa111"},
			rebasePlanEntry{Action: "pick", Commit: "bbbb222"},
			rebasePlanEntry{Action: "pick", Commit: "cccc333"},
		), rng, noPushed, false, "needs a message"},
		{"message on pick", mk(
			rebasePlanEntry{Action: "pick", Commit: "aaaa111", Message: "nope"},
			rebasePlanEntry{Action: "pick", Commit: "bbbb222"},
			rebasePlanEntry{Action: "pick", Commit: "cccc333"},
		), rng, noPushed, false, "only valid with reword"},
		{"merge commit in range", mk(
			rebasePlanEntry{Action: "pick", Commit: "aaaa111"},
		), []rebaseRangeCommit{{SHA: shaA, Subject: "merge", Parents: 2}}, noPushed, false, "merge commit"},
		{"unknown action", mk(
			rebasePlanEntry{Action: "edit", Commit: "aaaa111"},
			rebasePlanEntry{Action: "pick", Commit: "bbbb222"},
			rebasePlanEntry{Action: "pick", Commit: "cccc333"},
		), rng, noPushed, false, "unknown action"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateRebasePlan(tc.plan, tc.rng, tc.pushed, true, tc.allow)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}

	// happy path: reorder + squash + reword + drop, full SHA resolution
	v, err := validateRebasePlan(mk(
		rebasePlanEntry{Action: "pick", Commit: "bbbb222"},
		rebasePlanEntry{Action: "squash", Commit: "aaaa111"},
		rebasePlanEntry{Action: "reword", Commit: "cccc333", Message: "feat: better subject"},
	), rng, noPushed, true, false)
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if v.Entries[0].Commit != shaB || v.Entries[1].Commit != shaA {
		t.Errorf("resolved SHAs: %+v", v.Entries)
	}
}

func TestValidateRebasePlan_PushedGuard(t *testing.T) {
	rng := planRange(shaA, shaB, shaC)
	pushed := map[string]bool{shaA: true, shaB: true}

	// shaB가 pushed인데 squash → 가드
	plan := rebasePlan{Entries: []rebasePlanEntry{
		{Action: "pick", Commit: "aaaa111"},
		{Action: "squash", Commit: "bbbb222"},
		{Action: "pick", Commit: "cccc333"},
	}}
	_, err := validateRebasePlan(plan, rng, pushed, true, false)
	if err == nil || !strings.Contains(err.Error(), "already on a remote") {
		t.Fatalf("want pushed guard, got %v", err)
	}
	if len(RemediesFrom(err)) == 0 {
		t.Error("pushed guard must carry a remedy")
	}

	// 변경이 pushed 영역 뒤(미push shaC만 reword)면 통과 — pick된 pushed 커밋은 그대로
	plan = rebasePlan{Entries: []rebasePlanEntry{
		{Action: "pick", Commit: "aaaa111"},
		{Action: "pick", Commit: "bbbb222"},
		{Action: "reword", Commit: "cccc333", Message: "feat: x"},
	}}
	if _, err := validateRebasePlan(plan, rng, pushed, true, false); err != nil {
		t.Errorf("change after pushed region must pass: %v", err)
	}

	// --allow-pushed면 통과
	plan = rebasePlan{Entries: []rebasePlanEntry{
		{Action: "drop", Commit: "aaaa111"},
		{Action: "pick", Commit: "bbbb222"},
		{Action: "pick", Commit: "cccc333"},
	}}
	if _, err := validateRebasePlan(plan, rng, pushed, true, true); err != nil {
		t.Errorf("allow-pushed must pass: %v", err)
	}
}

func TestBuildRebaseTodo(t *testing.T) {
	msgDir := t.TempDir()
	v := rebasePlanValidated{Entries: []rebasePlanEntry{
		{Action: "pick", Commit: shaB},
		{Action: "squash", Commit: shaA},
		{Action: "reword", Commit: shaC, Message: "feat: better\n\nbody with 'quotes'"},
	}}
	todo, files, err := buildRebaseTodo(v, msgDir)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(todo, "\n"), "\n")
	want := []string{
		"pick " + shaB,
		"squash " + shaA,
		"pick " + shaC,
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
	if !strings.HasPrefix(lines[3], "exec git commit --amend -F '") {
		t.Errorf("reword exec line: %q", lines[3])
	}
	if len(files) != 1 {
		t.Fatalf("message files: %v", files)
	}
	b, _ := os.ReadFile(files[0])
	if !strings.Contains(string(b), "body with 'quotes'") {
		t.Errorf("message content: %q", b)
	}
	if filepath.Dir(files[0]) != msgDir {
		t.Errorf("message file location: %s", files[0])
	}
}
