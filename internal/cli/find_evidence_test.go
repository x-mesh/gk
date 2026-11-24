package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

// evidenceKey is a 32-byte key in hex, the form of an Ethereum private key.
// It is assembled at runtime so this file does not trip secret scanners.
var evidenceKey = "0x" + "9f2c71d3a8e4b65017c2d9e3f4a1b6c8" + "5d0e7f3a2b9c4d1e6f8a0b3c5d7e9f12"

// evidenceRepo has one commit per kind of evidence: a key that is added, the
// same key removed, a term only in a message body, and a term only in a path.
func evidenceRepo(t *testing.T) *testutil.Repo {
	t.Helper()
	repo := testutil.NewRepo(t)

	repo.WriteFile(".env", "PORT=8080\nDEPLOYER_KEY="+evidenceKey+"\n")
	repo.WriteFile("deploy.ts", "const a = 1\nconst key = process.env.DEPLOYER_KEY\n")
	repo.Commit("chore: add the deploy script")

	repo.WriteFile(".env", "PORT=8080\n")
	repo.Commit("chore: take the key out of the tree")

	repo.WriteFile("notes.md", "nothing")
	repo.Commit("docs: handover\n\nThe gizmoFlag setting moves to the vault after the handover.")

	repo.WriteFile("sprocket_table.go", "package main\n")
	repo.Commit("chore: add a table")
	return repo
}

func findWithEvidence(t *testing.T, repo *testutil.Repo, q findQuery, opts findEvidenceOpts) findResult {
	t.Helper()
	if q.limit == 0 {
		q.limit = 20
	}
	if opts.perCommit == 0 {
		opts.perCommit = findEvidencePerCommit
	}
	runner := &git.ExecRunner{Dir: repo.Dir}
	res := findCommits(context.Background(), runner, q)
	attachFindEvidence(context.Background(), runner, &res, q, opts)
	if res.EvidenceError != "" {
		t.Fatalf("the evidence lookup failed: %s", res.EvidenceError)
	}
	return res
}

func matchBySubject(t *testing.T, res findResult, part string) findMatch {
	t.Helper()
	for _, m := range res.Matches {
		if strings.Contains(m.Subject, part) {
			return m
		}
	}
	t.Fatalf("no match with %q in its subject: %+v", part, res.Matches)
	return findMatch{}
}

// The reason the feature exists: a [content] row must say where and how the
// query changed, for the added side and for the removed side.
func TestFindEvidence_ContentShowsAddedAndRemovedLines(t *testing.T) {
	repo := evidenceRepo(t)
	res := findWithEvidence(t, repo, findQuery{query: "DEPLOYER_KEY", content: true}, findEvidenceOpts{})

	added := matchBySubject(t, res, "add the deploy script")
	var got []string
	for _, ev := range added.Evidence {
		got = append(got, ev.Op+" "+ev.File+":"+strconv.Itoa(ev.Line))
	}
	want := []string{"+ .env:2", "+ deploy.ts:2"}
	if !slices.Equal(got, want) {
		t.Fatalf("added evidence = %v, want %v", got, want)
	}

	removed := matchBySubject(t, res, "take the key out")
	if len(removed.Evidence) != 1 {
		t.Fatalf("removed evidence = %+v, want one line", removed.Evidence)
	}
	if ev := removed.Evidence[0]; ev.Op != "-" || ev.File != ".env" || ev.Line != 2 {
		t.Errorf("removed evidence = %+v, want - .env:2", ev)
	}
}

// The name stays and the value changes, so the query is in the file once
// before the commit and once after it. A search that counts occurrences
// cannot see that commit, and it is the commit that puts a secret into
// history or takes it out.
func TestFindCommits_ValueOnlyChangeIsFound(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile(".env.local", "PORT=1\n# DEMO_KEY=<placeholder>\n")
	repo.Commit("chore: add a comment")
	repo.WriteFile(".env.local", "PORT=1\nDEMO_KEY=plain-value\n")
	repo.Commit("chore: put the value in")
	repo.WriteFile(".env.local", "PORT=1\nDEMO_KEY=\n")
	repo.Commit("chore: empty the value")
	repo.WriteFile(".env.local", "PORT=2\nDEMO_KEY=\n")
	repo.Commit("chore: change another line")

	res := findWithEvidence(t, repo, findQuery{query: "DEMO_KEY", content: true}, findEvidenceOpts{})
	var subjects []string
	for _, m := range res.Matches {
		subjects = append(subjects, m.Subject)
	}
	slices.Sort(subjects)
	want := []string{"chore: add a comment", "chore: empty the value", "chore: put the value in"}
	if !slices.Equal(subjects, want) {
		t.Fatalf("matches = %q, want %q", subjects, want)
	}

	var got []string
	for _, ev := range matchBySubject(t, res, "put the value in").Evidence {
		got = append(got, ev.Op+" "+ev.Text)
	}
	if want := []string{"- # DEMO_KEY=<placeholder>", "+ DEMO_KEY=plain-value"}; !slices.Equal(got, want) {
		t.Errorf("evidence = %q, want %q", got, want)
	}
}

// The query is a literal string, whatever characters it holds.
func TestFindCommits_ContentQueryIsALiteral(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "call(x+y) $a|b [c] {d} \\e ^f\n")
	repo.Commit("chore: symbols")
	repo.WriteFile("b.txt", "axb\n")
	repo.Commit("chore: letters")

	for _, query := range []string{"call(x+y)", "$a|b", "[c] {d}", `\e ^f`, "x+y"} {
		res := findWithEvidence(t, repo, findQuery{query: query, content: true}, findEvidenceOpts{})
		if len(res.Failed) != 0 || len(res.Matches) != 1 || res.Matches[0].Subject != "chore: symbols" {
			t.Errorf("query %q: matches = %+v, failed = %v; want only the symbols commit", query, res.Matches, res.Failed)
			continue
		}
		if len(res.Matches[0].Evidence) != 1 {
			t.Errorf("query %q: evidence = %+v, want the line", query, res.Matches[0].Evidence)
		}
	}
	// A dot is a dot: "a.b" is not in "axb".
	if res := findWithEvidence(t, repo, findQuery{query: "a.b", content: true}, findEvidenceOpts{}); len(res.Matches) != 0 {
		t.Errorf(`query "a.b" matched %+v, want nothing`, res.Matches)
	}
}

// A removed line is the case that leaks: the key is gone from the tree and
// still in history. It must be masked, and --reveal must show it.
func TestFindEvidence_SecretIsMaskedUnlessRevealed(t *testing.T) {
	repo := evidenceRepo(t)
	q := findQuery{query: "DEPLOYER_KEY", content: true}

	res := findWithEvidence(t, repo, q, findEvidenceOpts{})
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), evidenceKey[6:]) {
		t.Fatalf("the key is in the JSON output: %s", raw)
	}
	var buf bytes.Buffer
	renderFind(&buf, res)
	if strings.Contains(buf.String(), evidenceKey[6:]) {
		t.Fatalf("the key is in the text output:\n%s", buf.String())
	}
	ev := matchBySubject(t, res, "take the key out").Evidence[0]
	if !ev.Masked || !strings.HasPrefix(ev.Text, "DEPLOYER_KEY=") {
		t.Errorf("masked evidence = %+v, want the name kept and Masked set", ev)
	}
	if code := matchBySubject(t, res, "add the deploy script").Evidence[1]; code.Masked {
		t.Errorf("a line with no secret must not be marked masked: %+v", code)
	}

	res = findWithEvidence(t, repo, q, findEvidenceOpts{reveal: true})
	ev = matchBySubject(t, res, "take the key out").Evidence[0]
	if ev.Masked || !ev.Revealed || ev.Text != "DEPLOYER_KEY="+evidenceKey {
		t.Errorf("revealed evidence = %+v, want the line as it is and Revealed set", ev)
	}
	if code := matchBySubject(t, res, "add the deploy script").Evidence[1]; code.Revealed {
		t.Errorf("a line with no secret must not be marked revealed: %+v", code)
	}
}

func TestFindEvidence_MessageBodyLine(t *testing.T) {
	repo := evidenceRepo(t)
	res := findWithEvidence(t, repo, findQuery{query: "gizmoflag", message: true}, findEvidenceOpts{})
	m := matchBySubject(t, res, "handover")
	if len(m.Evidence) != 1 || m.Evidence[0].Mode != findModeMessage {
		t.Fatalf("evidence = %+v, want the body line", m.Evidence)
	}
	if got := m.Evidence[0].Text; got != "The gizmoFlag setting moves to the vault after the handover." {
		t.Errorf("message evidence = %q", got)
	}
}

// A match in the subject is on the row already, so no line repeats it.
func TestFindEvidence_SubjectMatchAddsNoMessageLine(t *testing.T) {
	repo := evidenceRepo(t)
	res := findWithEvidence(t, repo, findQuery{query: "handover", message: true}, findEvidenceOpts{})
	if m := matchBySubject(t, res, "handover"); len(m.Evidence) != 0 {
		t.Errorf("evidence = %+v, want none", m.Evidence)
	}
}

func TestFindEvidence_PathNamesTheFile(t *testing.T) {
	repo := evidenceRepo(t)
	res := findWithEvidence(t, repo, findQuery{query: "sprocket", pathMode: true}, findEvidenceOpts{})
	m := matchBySubject(t, res, "add a table")
	if len(m.Evidence) != 1 {
		t.Fatalf("evidence = %+v, want the path", m.Evidence)
	}
	if ev := m.Evidence[0]; ev.Mode != findModePath || ev.Op != "A" || ev.File != "sprocket_table.go" {
		t.Errorf("path evidence = %+v", ev)
	}
}

// A path-only search names its path on the command line.
func TestFindEvidence_PathOnlySearchShowsNone(t *testing.T) {
	repo := evidenceRepo(t)
	res := findWithEvidence(t, repo, findQuery{path: ".env", pathMode: true}, findEvidenceOpts{})
	if len(res.Matches) == 0 {
		t.Fatal("the fixture must match")
	}
	for _, m := range res.Matches {
		if len(m.Evidence) != 0 {
			t.Errorf("evidence = %+v, want none", m.Evidence)
		}
	}
}

// The subject holds "ab" and the body holds the query "a+b". git reads the
// query as a basic regular expression, where + is literal, so the commit
// matches because of its body, and the body line is the evidence.
func TestFindEvidence_MessageBodyLineForAQueryWithAPlus(t *testing.T) {
	repo := evidenceRepo(t)
	repo.WriteFile("sum.txt", "1")
	repo.Commit("docs: ab\n\nThe sum is a+b.")
	res := findWithEvidence(t, repo, findQuery{query: "a+b", message: true}, findEvidenceOpts{})
	m := matchBySubject(t, res, "ab")
	if len(m.Evidence) != 1 || m.Evidence[0].Text != "The sum is a+b." {
		t.Errorf("evidence = %+v, want the body line", m.Evidence)
	}
}

// With --path, the path search lists the commits that touch that path, so
// the path evidence is the files under it, and not the files whose name holds
// the query.
func TestFindEvidence_PathScopeNamesTheFilesUnderThePath(t *testing.T) {
	repo := evidenceRepo(t)
	repo.WriteFile("internal/cli/widget.go", "package cli\n")
	repo.WriteFile("needle.txt", "out of the path\n")
	repo.Commit("feat: add the widget")
	res := findWithEvidence(t, repo, findQuery{query: "needle", pathMode: true, path: "internal/cli"}, findEvidenceOpts{})
	m := matchBySubject(t, res, "add the widget")
	if len(m.Evidence) != 1 {
		t.Fatalf("evidence = %+v, want internal/cli/widget.go", m.Evidence)
	}
	if ev := m.Evidence[0]; ev.Mode != findModePath || ev.File != "internal/cli/widget.go" {
		t.Errorf("path evidence = %+v, want internal/cli/widget.go", ev)
	}
}

// --path with a query scopes the search, so it scopes the evidence too.
func TestFindEvidence_PathScopeLimitsContentLines(t *testing.T) {
	repo := evidenceRepo(t)
	res := findWithEvidence(t, repo, findQuery{query: "DEPLOYER_KEY", content: true, path: "deploy.ts"}, findEvidenceOpts{})
	m := matchBySubject(t, res, "add the deploy script")
	if len(m.Evidence) != 1 || m.Evidence[0].File != "deploy.ts" {
		t.Errorf("evidence = %+v, want only deploy.ts", m.Evidence)
	}
}

// The output is parsed, so a user's diff and log settings must not change it.
// With diff.noprefix the path "b/term.txt" has no prefix to strip, and the
// parser would take its directory for one.
func TestFindEvidence_UserConfigDoesNotChangeThePaths(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.RunGit("config", "diff.noprefix", "true")
	repo.RunGit("config", "diff.mnemonicPrefix", "true")
	repo.RunGit("config", "color.ui", "always")
	repo.WriteFile("b/term.txt", "needleTerm here\n")
	repo.Commit("chore: one line")
	res := findWithEvidence(t, repo, findQuery{query: "needleTerm", content: true}, findEvidenceOpts{})
	ev := res.Matches[0].Evidence
	if len(ev) != 1 || ev[0].File != "b/term.txt" || ev[0].Line != 1 || ev[0].Text != "needleTerm here" {
		t.Errorf("evidence = %+v, want + b/term.txt:1", ev)
	}
}

// A rule that is limited to a path needs the path of the evidence line.
func TestFindEvidence_PathScopedRuleMasksByFile(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("main.tf", "administrator_login_password = \""+"hunter2pass"+"\"\n")
	repo.Commit("chore: add the database")
	res := findWithEvidence(t, repo, findQuery{query: "administrator_login_password", content: true}, findEvidenceOpts{})
	ev := res.Matches[0].Evidence
	if len(ev) != 1 || !ev[0].Masked || strings.Contains(ev[0].Text, "hunter2pass") {
		t.Errorf("evidence = %+v, want the password masked", ev)
	}
}

func TestFindEvidence_CapCountsTheRest(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("many.txt", strings.Repeat("needleTerm here\n", 5))
	repo.Commit("chore: many lines")
	res := findWithEvidence(t, repo, findQuery{query: "needleTerm", content: true}, findEvidenceOpts{perCommit: 2})
	m := res.Matches[0]
	if len(m.Evidence) != 2 || m.EvidenceMore != 3 {
		t.Errorf("evidence = %d lines and %d more, want 2 and 3", len(m.Evidence), m.EvidenceMore)
	}
}

// A failed lookup leaves the commits in place and says that the detail is
// missing; it does not read as commits with nothing to show.
func TestFindEvidence_FailedLookupIsReported(t *testing.T) {
	repo := evidenceRepo(t)
	runner := &git.ExecRunner{Dir: repo.Dir}
	q := findQuery{query: "DEPLOYER_KEY", content: true, limit: 20}
	res := findCommits(context.Background(), runner, q)
	count := res.Count
	res.Matches[0].Hash = "0000000000000000000000000000000000000000"
	attachFindEvidence(context.Background(), runner, &res, q, findEvidenceOpts{perCommit: 3})
	if res.EvidenceError == "" || res.Count != count {
		t.Fatalf("EvidenceError = %q, Count = %d; want an error and %d commits", res.EvidenceError, res.Count, count)
	}
	var buf bytes.Buffer
	renderFind(&buf, res)
	if !strings.Contains(buf.String(), "근거 줄 조회 실패") {
		t.Errorf("the failure is not in the output:\n%s", buf.String())
	}
}

func parsePatch(patch, query string, perCommit int) evidenceSet {
	return parseFindPatch(strings.NewReader(patch), query, perCommit)
}

// The hunk header counts the lines, so a changed line that looks like a
// header is read as a line.
func TestParseFindPatch_ChangedLineThatLooksLikeAHeader(t *testing.T) {
	patch := strings.Join([]string{
		"\x01aaa",
		"",
		"diff --git a/q.sql b/q.sql",
		"index 1..2 100644",
		"--- a/q.sql",
		"+++ b/q.sql",
		"@@ -7,2 +7,2 @@ select",
		"--- TERM is a comment",
		"-@@ TERM @@",
		"+++ TERM added",
		"+plain",
		"@@ -20 +20 @@",
		"-old TERM",
		"+new",
		`\ No newline at end of file`,
	}, "\n")
	set := parsePatch(patch, "TERM", 10)
	var got []string
	for _, ev := range set["aaa"].items {
		got = append(got, ev.Op+strconv.Itoa(ev.Line)+" "+ev.Text)
	}
	want := []string{"-7 -- TERM is a comment", "-8 @@ TERM @@", "+7 ++ TERM added", "-20 old TERM"}
	if !slices.Equal(got, want) {
		t.Fatalf("evidence = %q, want %q", got, want)
	}
}

func TestParseFindPatch_NewAndDeletedFilesAndCommits(t *testing.T) {
	patch := strings.Join([]string{
		"\x01aaa",
		"diff --git a/new file.txt b/new file.txt",
		"new file mode 100644",
		"--- /dev/null",
		"+++ b/new file.txt\t",
		"@@ -0,0 +1,2 @@",
		"+TERM one",
		"+two",
		"\x01bbb",
		"diff --git a/gone.txt b/gone.txt",
		"deleted file mode 100644",
		"--- a/gone.txt",
		"+++ /dev/null",
		"@@ -1 +0,0 @@",
		"-TERM gone",
	}, "\n")
	set := parsePatch(patch, "TERM", 10)
	if ev := set["aaa"].items[0]; ev.File != "new file.txt" || ev.Line != 1 || ev.Op != "+" {
		t.Errorf("new file evidence = %+v", ev)
	}
	if ev := set["bbb"].items[0]; ev.File != "gone.txt" || ev.Line != 1 || ev.Op != "-" {
		t.Errorf("deleted file evidence = %+v", ev)
	}
}

func TestReadCappedLine_DropsTheRestOfALongLine(t *testing.T) {
	br := bufio.NewReaderSize(strings.NewReader(strings.Repeat("a", 5000)+"\nnext\n"), 16)
	first, err := readCappedLine(br, 100)
	if err != nil || len(first) != 100 {
		t.Fatalf("first = %d bytes, err %v; want 100 bytes", len(first), err)
	}
	if second, _ := readCappedLine(br, 100); second != "next" {
		t.Errorf("second = %q, want the next line", second)
	}
}

func TestParseFindPaths_RenameShowsTheNewPath(t *testing.T) {
	out := "\x01aaa\n\nR100\told_name.go\tnew_name.go\nM\tb.go\nA\tc.go\n"
	set := parseFindPaths(out, 2)
	c := set["aaa"]
	if len(c.items) != 2 || c.more != 1 {
		t.Fatalf("paths = %+v and %d more, want 2 and 1", c.items, c.more)
	}
	if ev := c.items[0]; ev.Op != "R" || ev.File != "new_name.go" {
		t.Errorf("rename evidence = %+v", ev)
	}
}

// Go has no back-reference, so a query with one gets no matcher: a row
// shows no message line rather than a line that git did not match.
func TestFindMessageMatcherBackReference(t *testing.T) {
	for _, q := range []string{`\(a\)\1`, `\(ab\)\(c\)\2`} {
		if re := findMessageMatcher(q); re != nil {
			t.Errorf("findMessageMatcher(%q) = %s, want none", q, re)
		}
	}
	if re := findMessageMatcher(`a\\1`); re == nil || !re.MatchString(`a\1`) {
		t.Errorf("an escaped backslash before 1 is not a back-reference")
	}
}

func TestFindMessageMatcher(t *testing.T) {
	cases := []struct {
		query, text string
		want        bool
	}{
		{"gizmoflag", "The gizmoFlag setting", true}, // git --grep runs with -i
		{"a+b", "sum is a+b here", true},             // a literal to git
		{"fix.*bug", "fix the bug", true},            // an expression to both
		{"call(", "the call( site", true},            // not a Go expression
		{"gizmo", "nothing here", false},
		// git reads the query as a basic regular expression: + ? | ( ) { }
		// are literal there, and the backslash forms are the operators.
		{"a+b", "ab", false},
		{"a+b", "aab", false},
		{"a?b", "b", false},
		{"x|y", "y", false},
		{"x|y", "x|y", true},
		{`a\+b`, "aab", true},
		{`a\+b`, "a+b", false},
		{`x\|y`, "y", true},
		{`\(ab\)*c`, "ababc", true},
		{"a{2}", "a{2}", true},
		{"a{2}", "aa", false},
		{`a\{2\}`, "aa", true},
		{"*x", "*x", true},       // a leading * is literal
		{"^fix", "fix it", true}, // anchors are kept
		{"^fix", "a fix", false},
		{"[ab]x", "bx", true}, // a bracket expression is kept
		{"[]a]x", "]x", true}, // ] first in the brackets is literal
		{`a\.b`, "a.b", true}, // an escaped dot is a dot
		{`a\.b`, "axb", false},
		{"fix$", "a fix", true},
		{"a$b", "a$b", true},                  // $ is an anchor only at the end
		{"[unclosed", "the [unclosed", false}, // git rejects it too
		// A query is UTF-8 to git; it is read by rune, not by byte.
		{"café", "the café opens", true},
		{"카페", "카페 메뉴", true},
		{"caf.", "café", true}, // . is one rune
		{"[é]x", "éx", true},
	}
	for _, c := range cases {
		re := findMessageMatcher(c.query)
		if got := re != nil && re.MatchString(c.text); got != c.want {
			t.Errorf("findMessageMatcher(%q).MatchString(%q) = %v, want %v", c.query, c.text, got, c.want)
		}
	}
}

// Text from history reaches a terminal, so it must not carry an escape
// sequence or reorder the text around it.
func TestSanitizeEvidence(t *testing.T) {
	override := string(rune(0x202E)) // right-to-left override
	got := sanitizeEvidence("a\tb\x1b[2Jc" + override + "d\xff")
	if strings.ContainsAny(got, "\t\x1b"+override) || !utf8.ValidString(got) {
		t.Errorf("a control character survived: %q", got)
	}
	if !strings.HasPrefix(got, "a b") {
		t.Errorf("a tab must become a space: %q", got)
	}
}

func TestFindEvidenceText_ClipsAroundTheMatch(t *testing.T) {
	raw := strings.Repeat("x", 500) + "needleTerm" + strings.Repeat("y", 500)
	got, _ := findEvidenceText("a.txt", raw, findContentMatcher("needleTerm"), false)
	if n := len([]rune(got)); n > findEvidenceTextMax {
		t.Errorf("clipped to %d runes, want at most %d", n, findEvidenceTextMax)
	}
	if !strings.Contains(got, "needleTerm") || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Errorf("the clip must keep the match and mark both cuts: %q", got)
	}
}

// The mask runs on the whole line before the clip, so a secret that the clip
// cuts is already masked.
func TestFindEvidenceText_MasksBeforeTheClip(t *testing.T) {
	raw := "needleTerm " + strings.Repeat("pad ", 30) + "DEPLOYER_KEY=" + evidenceKey
	got, masked := findEvidenceText("a.txt", raw, findContentMatcher("needleTerm"), false)
	if !masked || strings.Contains(got, evidenceKey[2:12]) {
		t.Errorf("masked = %v, text = %q; a part of the key is shown", masked, got)
	}
}

func TestClipAround_CountsWideRunesByColumn(t *testing.T) {
	wide := func(r rune) int {
		if r > 0x1100 {
			return 2
		}
		return 1
	}
	got := clipAround(strings.Repeat("가", 40)+"needle", findContentMatcher("needle"), 20, wide)
	cols := 0
	for _, r := range got {
		cols += wide(r)
	}
	if cols > 20 || !strings.Contains(got, "needle") {
		t.Errorf("clip = %q (%d columns), want the match within 20 columns", got, cols)
	}
}

func TestRenderFindEvidence_Layout(t *testing.T) {
	m := findMatch{
		Evidence: []findEvidence{
			{Mode: findModeMessage, Text: "the TERM in the body"},
			{Mode: findModePath, Op: "A", File: "dir/TERM.go"},
			{Mode: findModeContent, Op: "+", File: "a.go", Line: 3, Text: "x := TERM"},
			{Mode: findModeContent, Op: "-", File: "dir/long_name.go", Line: 120, Text: "TERM()"},
		},
		EvidenceMore: 4,
	}
	var buf bytes.Buffer
	setNoColor(t, true, func() { renderFindEvidence(&buf, m, "TERM") })
	want := strings.Join([]string{
		"    message  the TERM in the body",
		"    path     A dir/TERM.go",
		"    + a.go:3                x := TERM",
		"    - dir/long_name.go:120  TERM()",
		"    (+4 more)",
		"",
	}, "\n")
	if buf.String() != want {
		t.Errorf("layout:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestRenderFindEvidence_Colors(t *testing.T) {
	m := findMatch{Evidence: []findEvidence{
		{Mode: findModeContent, Op: "+", File: "a.go", Line: 3, Text: "x := TERM"},
		{Mode: findModeContent, Op: "-", File: "a.go", Line: 9, Text: "y := TERM"},
	}}
	var buf bytes.Buffer
	setNoColor(t, false, func() { renderFindEvidence(&buf, m, "TERM") })
	lines := strings.Split(buf.String(), "\n")
	if !strings.Contains(lines[0], ansiFgGreen+"x := ") || !strings.Contains(lines[1], ansiFgRed+"y := ") {
		t.Errorf("an added line must be green and a removed line red:\n%q", buf.String())
	}
	if !strings.Contains(lines[0], ansiBold+ansiFgGreen+"TERM") {
		t.Errorf("the match must be bold in the color of its line: %q", lines[0])
	}
	if !strings.Contains(lines[0], ansiFgCyan+"a.go:3") {
		t.Errorf("the location must be cyan: %q", lines[0])
	}
}

func TestRenderFindEvidence_ClipsToTheTerminal(t *testing.T) {
	prev := findTermWidth
	findTermWidth = func() (int, bool) { return 60, true }
	t.Cleanup(func() { findTermWidth = prev })
	m := findMatch{Evidence: []findEvidence{
		{Mode: findModeContent, Op: "+", File: "a.go", Line: 3, Text: strings.Repeat("word ", 30) + "TERM"},
	}}
	var buf bytes.Buffer
	setNoColor(t, true, func() { renderFindEvidence(&buf, m, "TERM") })
	line := strings.TrimSuffix(buf.String(), "\n")
	if n := len([]rune(line)); n > 60 || !strings.Contains(line, "TERM") {
		t.Errorf("line = %q (%d columns), want the match within 60", line, n)
	}
}

// runFindCmd runs `gk find` through the root command, so the flags are
// tested as a user sets them.
func runFindCmd(t *testing.T, repo *testutil.Repo, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	// The rerank is a network call that the user's own config can turn on.
	t.Setenv("GK_AI_JEV_FIND_RERANK", "false")
	reset := func() {
		flagRepo = ""
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		if c, _, findErr := rootCmd.Find([]string{"find"}); findErr == nil {
			_ = c.Flags().Set("oneline", "false")
			_ = c.Flags().Set("reveal", "false")
			_ = c.Flags().Set("json", "false")
		}
	}
	reset()
	t.Cleanup(reset)
	var out, errOut strings.Builder
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs(append([]string{"--repo", repo.Dir, "--no-color", "find"}, args...))
	err = rootCmd.Execute()
	return out.String(), errOut.String(), err
}

func TestFindCmd_EvidenceIsOnByDefaultAndMasked(t *testing.T) {
	repo := evidenceRepo(t)
	out, _, err := runFindCmd(t, repo, "DEPLOYER_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "    - .env:2") || !strings.Contains(out, "    + deploy.ts:2") {
		t.Errorf("the evidence lines are missing:\n%s", out)
	}
	if strings.Contains(out, evidenceKey[6:]) {
		t.Errorf("the key is printed:\n%s", out)
	}
	// The key is added in one commit and removed in the next.
	if !strings.Contains(out, "2 lines masked · --reveal prints the values") {
		t.Errorf("the output must say that lines are masked:\n%s", out)
	}
}

// A result with nothing to mask must not suggest that something is hidden.
func TestFindCmd_NoSecretShowsNoMaskLine(t *testing.T) {
	repo := evidenceRepo(t)
	out, _, err := runFindCmd(t, repo, "process.env")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "deploy.ts:2") || strings.Contains(out, "masked") {
		t.Errorf("want the evidence and no mask line:\n%s", out)
	}
}

// --reveal on a result with no secret changes nothing, and must say so
// without the warning that secrets are on the screen.
func TestFindCmd_RevealWithNoSecretSaysSo(t *testing.T) {
	repo := evidenceRepo(t)
	_, errOut, err := runFindCmd(t, repo, "process.env", "--reveal")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "가려진 값이 없어") || strings.Contains(errOut, "터미널이나 로그") {
		t.Errorf("stderr = %q, want the note that nothing was masked", errOut)
	}
}

func TestFindCmd_OnelinePrintsOnlyTheRows(t *testing.T) {
	repo := evidenceRepo(t)
	out, _, err := runFindCmd(t, repo, "DEPLOYER_KEY", "--oneline")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, ".env:2") || strings.Contains(out, "deploy.ts") {
		t.Errorf("--oneline must print no evidence:\n%s", out)
	}
	if !strings.Contains(out, "take the key out of the tree") {
		t.Errorf("the commit rows are missing:\n%s", out)
	}
}

func TestFindCmd_RevealPrintsTheLineAndWarns(t *testing.T) {
	repo := evidenceRepo(t)
	out, errOut, err := runFindCmd(t, repo, "DEPLOYER_KEY", "--reveal")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "DEPLOYER_KEY="+evidenceKey) {
		t.Errorf("--reveal must print the line as it is:\n%s", out)
	}
	if !strings.Contains(errOut, "근거 줄 2개") || !strings.Contains(errOut, "터미널이나 로그") {
		t.Errorf("--reveal must warn on stderr with the count of lines, got %q", errOut)
	}
	if strings.Contains(out, "masked") {
		t.Errorf("nothing is masked with --reveal, so no mask line:\n%s", out)
	}
}

func setAgentMode(t *testing.T) {
	t.Helper()
	prevAgent, prevJSON := flagAgent, flagJSON
	flagAgent, flagJSON = true, true
	t.Cleanup(func() { flagAgent, flagJSON = prevAgent, prevJSON })
}

// The agent output goes to a model, so --reveal is refused there.
func TestFindCmd_AgentModeRefusesReveal(t *testing.T) {
	repo := evidenceRepo(t)
	setAgentMode(t)
	out, _, err := runFindCmd(t, repo, "DEPLOYER_KEY", "--reveal")
	if err == nil || !strings.Contains(err.Error(), "--reveal") {
		t.Fatalf("err = %v, want a refusal that names --reveal", err)
	}
	if strings.Contains(out, evidenceKey[6:]) {
		t.Errorf("the key reached the output:\n%s", out)
	}
}

func TestFindCmd_AgentModeShowsFewerLines(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("many.txt", strings.Repeat("needleTerm here\n", 5))
	repo.Commit("chore: many lines")
	setAgentMode(t)
	out, _, err := runFindCmd(t, repo, "needleTerm")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out, `"mode": "content"`); got != findEvidencePerCommitAgent {
		t.Errorf("agent output has %d content lines, want %d:\n%s", got, findEvidencePerCommitAgent, out)
	}
	if !strings.Contains(out, `"evidence_more": 3`) {
		t.Errorf("the lines left out must be counted:\n%s", out)
	}
}
