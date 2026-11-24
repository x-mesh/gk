package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/secrets"
	"github.com/x-mesh/gk/internal/secrets/gitleaksrules"
	"github.com/x-mesh/gk/internal/ui"
)

// --- gk find: the evidence behind each match -----------------------------------
//
// A row tagged [content] says a commit changed the query, but not where or how,
// so the reader ran `git show` on it next. The evidence is that answer, fetched
// for the rows that are printed: one more git call per mode, after the ranking
// and --limit have picked them.

const (
	// findEvidencePerCommit caps the lines of one kind shown under a commit.
	// Agent output goes into a context window, so it gets one line less.
	findEvidencePerCommit      = 3
	findEvidencePerCommitAgent = 2
	// findEvidenceTextMax is the runes of a line that are shown.
	findEvidenceTextMax = 160
	// findEvidenceScanMax is the bytes of a line that the secret scan reads.
	// It is far wider than the text shown, so a secret cut by this window
	// lies outside the text shown.
	findEvidenceScanMax = 4096
	// findEvidenceLineMax is the bytes kept of one diff line. A longer line,
	// as in a minified file, shows no evidence when the query is past it.
	findEvidenceLineMax = 1 << 20
	findEvidenceLocMax  = 48
	// findEvidenceColsMin is the narrowest a line is clipped to, however
	// narrow the terminal is.
	findEvidenceColsMin = 40
)

// findTermWidth reports the terminal width, so a test can set one.
var findTermWidth = ui.TTYWidth

// findEvidence is one reason a commit matched: a changed line, a line of the
// message, or a path.
type findEvidence struct {
	Mode string `json:"mode"` // content | message | path
	// Op is "+" or "-" for a content line and the git status letter for a path.
	Op   string `json:"op,omitempty"`
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	Text string `json:"text,omitempty"`
	// Masked reports that a secret in Text is replaced by a mask. Revealed
	// reports that Text holds a secret which --reveal left as it is.
	Masked   bool `json:"masked,omitempty"`
	Revealed bool `json:"revealed,omitempty"`
}

type findEvidenceOpts struct {
	perCommit int
	reveal    bool
}

// commitEvidence is what one git call found for one commit, before the text
// is masked and clipped.
type commitEvidence struct {
	items []findEvidence
	more  int
}

func (c *commitEvidence) add(ev findEvidence, limit int) {
	if len(c.items) >= limit {
		c.more++
		return
	}
	c.items = append(c.items, ev)
}

type evidenceSet map[string]*commitEvidence

func (s evidenceSet) at(hash string) *commitEvidence {
	c := s[hash]
	if c == nil {
		c = &commitEvidence{}
		s[hash] = c
	}
	return c
}

// attachFindEvidence fills Evidence on the matches of res. A failed git call
// leaves the search result in place and is recorded in EvidenceError: the
// commits are still the right answer, and a missing detail must not read as a
// commit with nothing to show.
func attachFindEvidence(ctx context.Context, runner *git.ExecRunner, res *findResult, q findQuery, opts findEvidenceOpts) {
	hashes := map[string][]string{}
	for _, m := range res.Matches {
		for _, mode := range m.Matched {
			hashes[mode] = append(hashes[mode], m.Hash)
		}
	}
	// A path-only search names its path on the command line, so repeating it
	// under every row would add nothing.
	if q.query == "" {
		delete(hashes, findModePath)
	}

	modes := []string{findModeMessage, findModePath, findModeContent}
	sets := make([]evidenceSet, len(modes))
	errs := make([]error, len(modes))
	var wg sync.WaitGroup
	for i, mode := range modes {
		if len(hashes[mode]) == 0 {
			continue
		}
		wg.Add(1)
		go func(i int, mode string) {
			defer wg.Done()
			sets[i], errs[i] = findEvidenceFor(ctx, runner, q, mode, hashes[mode], opts.perCommit)
		}(i, mode)
	}
	wg.Wait()

	var failed []string
	for i, err := range errs {
		if err != nil {
			failed = append(failed, modes[i]+": "+err.Error())
		}
	}
	res.EvidenceError = strings.Join(failed, "; ")

	matchers := map[string]*regexp.Regexp{
		findModeMessage: findMessageMatcher(q.query),
		findModeContent: findContentMatcher(q.query),
	}
	for i := range res.Matches {
		m := &res.Matches[i]
		for j, set := range sets {
			c := set[m.Hash]
			if c == nil {
				continue
			}
			for _, ev := range c.items {
				if ev.Mode != findModePath {
					var secret bool
					ev.Text, secret = findEvidenceText(ev.File, ev.Text, matchers[modes[j]], opts.reveal)
					ev.Masked, ev.Revealed = secret && !opts.reveal, secret && opts.reveal
				}
				m.Evidence = append(m.Evidence, ev)
			}
			m.EvidenceMore += c.more
		}
	}
}

func findEvidenceFor(ctx context.Context, runner *git.ExecRunner, q findQuery, mode string, hashes []string, perCommit int) (evidenceSet, error) {
	// --no-walk=unsorted with --stdin reads exactly these commits: no history
	// walk, and no argument list that grows with --limit. The output is
	// parsed, so the options that a user's config can change are pinned.
	args := []string{"--no-optional-locks", "log", "--no-walk=unsorted", "--stdin", "--no-color", "--no-show-signature"}
	stdin := strings.NewReader(strings.Join(hashes, "\n") + "\n")
	switch mode {
	case findModeMessage:
		var out bytes.Buffer
		if err := runner.Stream(ctx, stdin, &out, append(args, "--format=%x01%H%x00%B")...); err != nil {
			return nil, findGitErr(err)
		}
		return parseFindMessages(out.String(), findMessageMatcher(q.query)), nil
	case findModePath:
		// The pathspec of the path search: with --path, the commits are
		// there because they touch that path.
		pathspec := "*" + q.query + "*"
		if q.path != "" {
			pathspec = q.path
		}
		var out bytes.Buffer
		args = append(args, "--name-status", "--format=%x01%H", "--", pathspec)
		if err := runner.Stream(ctx, stdin, &out, args...); err != nil {
			return nil, findGitErr(err)
		}
		return parseFindPaths(out.String(), perCommit), nil
	}

	// The same pickaxe as the search, so the diff holds only the files with
	// a changed line that holds the query. -U0 drops the context lines; the
	// lines without the query are dropped while the output streams, because
	// one new file can be thousands of them.
	args = append(args, "-U0", "--no-ext-diff", "--src-prefix=a/", "--dst-prefix=b/", "--format=%x01%H", findPickaxeArg(q.query))
	if q.path != "" {
		args = append(args, "--", q.path)
	}
	pr, pw := io.Pipe()
	var set evidenceSet
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The parser reads to the end of the pipe, so git never blocks on it.
		set = parseFindPatch(pr, q.query, perCommit)
	}()
	err := runner.Stream(ctx, stdin, pw, args...)
	_ = pw.Close()
	<-done
	if err != nil {
		return nil, findGitErr(err)
	}
	return set, nil
}

func findGitErr(err error) error {
	var exit *git.ExitError
	if errors.As(err, &exit) && strings.TrimSpace(exit.Stderr) != "" {
		return fmt.Errorf("%s", strings.TrimSpace(exit.Stderr))
	}
	return err
}

var findHunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// parseFindPatch reads `git log -U0 --format=%x01%H` output and keeps the
// added and removed lines that contain query. The hunk header gives the count
// of lines in a hunk, so a changed line that itself starts with "--- " or
// "@@ " is read as a line and not as a header.
func parseFindPatch(r io.Reader, query string, perCommit int) evidenceSet {
	set := evidenceSet{}
	br := bufio.NewReaderSize(r, 64<<10)
	var commit, oldPath, newPath string
	var oldLeft, newLeft, oldLine, newLine int
	for {
		line, err := readCappedLine(br, findEvidenceLineMax)
		if err != nil {
			return set
		}
		if oldLeft > 0 || newLeft > 0 {
			switch {
			case strings.HasPrefix(line, "+") && newLeft > 0:
				if commit != "" && strings.Contains(line[1:], query) {
					set.at(commit).add(findEvidence{
						Mode: findModeContent, Op: "+", File: firstPath(newPath, oldPath), Line: newLine, Text: line[1:],
					}, perCommit)
				}
				newLeft--
				newLine++
				continue
			case strings.HasPrefix(line, "-") && oldLeft > 0:
				if commit != "" && strings.Contains(line[1:], query) {
					set.at(commit).add(findEvidence{
						Mode: findModeContent, Op: "-", File: firstPath(oldPath, newPath), Line: oldLine, Text: line[1:],
					}, perCommit)
				}
				oldLeft--
				oldLine++
				continue
			case strings.HasPrefix(line, " ") && oldLeft > 0 && newLeft > 0:
				oldLeft, newLeft, oldLine, newLine = oldLeft-1, newLeft-1, oldLine+1, newLine+1
				continue
			case strings.HasPrefix(line, `\`):
				continue
			}
			// Not a line of the hunk: the count was wrong, so read on as headers.
			oldLeft, newLeft = 0, 0
		}
		switch {
		case strings.HasPrefix(line, "\x01"):
			commit, oldPath, newPath = line[1:], "", ""
		case strings.HasPrefix(line, "diff --git "):
			oldPath, newPath = "", ""
		case strings.HasPrefix(line, "--- "):
			oldPath = patchPath(line[4:])
		case strings.HasPrefix(line, "+++ "):
			newPath = patchPath(line[4:])
		default:
			m := findHunkHeader.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			oldLine, oldLeft = atoiOr(m[1], 0), atoiOr(m[2], 1)
			newLine, newLeft = atoiOr(m[3], 0), atoiOr(m[4], 1)
		}
	}
}

// readCappedLine reads one line without its line break and keeps at most limit
// bytes of it; the rest of a longer line is read and dropped.
func readCappedLine(br *bufio.Reader, limit int) (string, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if room := limit - len(buf); room > 0 {
			buf = append(buf, chunk[:min(room, len(chunk))]...)
		}
		switch err {
		case nil:
			return strings.TrimRight(string(buf), "\r\n"), nil
		case bufio.ErrBufferFull:
			continue
		default:
			if len(buf) > 0 {
				return strings.TrimRight(string(buf), "\r\n"), nil
			}
			return "", err
		}
	}
}

// patchPath turns the path of a "--- " or "+++ " line into a repository
// path. /dev/null, the side a new or deleted file lacks, becomes "".
func patchPath(s string) string {
	// git ends a path that holds a space with a tab.
	s = unquoteGitPath(strings.TrimRight(s, "\t"))
	if s == "/dev/null" {
		return ""
	}
	if len(s) > 2 && s[1] == '/' {
		return s[2:]
	}
	return s
}

// unquoteGitPath decodes a path that git printed in C quotes, which it does
// for a path that holds a quote, a backslash, or a control character.
func unquoteGitPath(s string) string {
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

func firstPath(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func atoiOr(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

// parseFindMessages keeps, per commit, the first line of the message body
// that matches. A match in the subject is already on the row.
func parseFindMessages(out string, match *regexp.Regexp) evidenceSet {
	set := evidenceSet{}
	if match == nil {
		return set
	}
	for _, rec := range strings.Split(out, "\x01") {
		hash, body, ok := strings.Cut(rec, "\x00")
		if !ok {
			continue
		}
		lines := strings.Split(strings.TrimSpace(body), "\n")
		if match.MatchString(lines[0]) {
			continue
		}
		for _, l := range lines[1:] {
			if match.MatchString(l) {
				set.at(strings.TrimSpace(hash)).add(findEvidence{Mode: findModeMessage, Text: l}, 1)
				break
			}
		}
	}
	return set
}

// parseFindPaths reads `git log --name-status --format=%x01%H` output.
func parseFindPaths(out string, perCommit int) evidenceSet {
	set := evidenceSet{}
	commit := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "\x01") {
			commit = strings.TrimSpace(line[1:])
			continue
		}
		f := strings.Split(line, "\t")
		if commit == "" || len(f) < 2 || f[0] == "" {
			continue
		}
		// A rename or a copy lists the old path, then the new one.
		set.at(commit).add(findEvidence{Mode: findModePath, Op: f[0][:1], File: unquoteGitPath(f[len(f)-1])}, perCommit)
	}
	return set
}

// findPickaxeArg is the git option that the content search and its evidence
// share. -G selects a commit whose diff adds or removes a line that holds the
// query. -S, which counts the occurrences in a file, misses a commit that
// changes the rest of such a line: when a value changes and its name stays,
// the name is in the file once before and once after. -G reads its argument
// as an extended regular expression, so the query is escaped to a literal.
func findPickaxeArg(query string) string { return "-G" + regexp.QuoteMeta(query) }

// findContentMatcher matches what the pickaxe matches: the query as a
// literal, case-sensitive string.
func findContentMatcher(query string) *regexp.Regexp {
	if query == "" {
		return nil
	}
	return regexp.MustCompile(regexp.QuoteMeta(query))
}

// findMessageMatcher follows `git log -i --grep`, which reads the query as a
// basic regular expression (BRE). The query is translated to a Go expression
// with the same meaning, so a line matches here when it matches for git. It
// is nil for a query that does not translate, such as one with an unclosed
// bracket, which git rejects as well.
func findMessageMatcher(query string) *regexp.Regexp {
	if query == "" {
		return nil
	}
	expr, ok := breToGo(query)
	if !ok {
		return nil
	}
	re, err := regexp.Compile("(?i)" + expr)
	if err != nil {
		return nil
	}
	return re
}

// breToGo translates a POSIX basic regular expression, with the GNU
// extensions that git accepts, to a Go expression. In a BRE, + ? | ( ) { }
// are literal and the backslash forms are the operators; . [ ] * ^ $ are the
// same as in Go, except that a * at the start of the expression or of a group
// is literal.
func breToGo(q string) (string, bool) {
	var b strings.Builder
	atStart := true // where a * is literal
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch c {
		case '\\':
			if i+1 == len(q) {
				return "", false
			}
			i++
			n := q[i]
			switch n {
			case '+', '?', '|', '(', ')', '{', '}':
				b.WriteByte(n)
				atStart = n == '(' || n == '|'
				continue
			case 'w', 'W', 's', 'S', 'b', 'B':
				b.WriteByte('\\')
				b.WriteByte(n)
			case '<', '>':
				b.WriteString(`\b`)
			default:
				b.WriteString(regexp.QuoteMeta(string(n)))
			}
		case '+', '?', '|', '(', ')', '{', '}':
			b.WriteString(regexp.QuoteMeta(string(c)))
		case '*':
			if atStart {
				b.WriteString(`\*`)
			} else {
				b.WriteByte('*')
			}
		case '^':
			if atStart {
				b.WriteByte('^')
				continue // a * after a leading ^ is literal too
			}
			b.WriteString(`\^`)
		case '.':
			b.WriteByte('.')
		case '$':
			// An anchor only at the end; elsewhere a BRE reads it as a $.
			if i == len(q)-1 {
				b.WriteByte('$')
			} else {
				b.WriteString(`\$`)
			}
		case '[':
			end := bracketEnd(q, i)
			if end < 0 {
				return "", false
			}
			// Inside the brackets a backslash is literal in a BRE.
			b.WriteString(strings.ReplaceAll(q[i:end+1], `\`, `\\`))
			i = end
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
		atStart = false
	}
	return b.String(), true
}

// bracketEnd returns the index of the ] that closes the bracket expression
// that starts at q[start], or -1. A ] right after [ or [^ is a member, and a
// [: :] class holds its own ].
func bracketEnd(q string, start int) int {
	i := start + 1
	if i < len(q) && q[i] == '^' {
		i++
	}
	if i < len(q) && q[i] == ']' {
		i++
	}
	for ; i < len(q); i++ {
		if q[i] == '[' && i+1 < len(q) && q[i+1] == ':' {
			if end := strings.Index(q[i+2:], ":]"); end >= 0 {
				i += end + 3
				continue
			}
		}
		if q[i] == ']' {
			return i
		}
	}
	return -1
}

// findEvidenceText prepares a raw line of file for display: masks its
// secrets, makes its control characters harmless, and clips it around the
// match. The mask runs before the clip, so a secret cut in half by the clip
// is already masked.
//
// secret reports that the line holds a secret. The scan runs with reveal too,
// so the output can say whether --reveal showed anything.
func findEvidenceText(file, raw string, match *regexp.Regexp, reveal bool) (text string, secret bool) {
	s := clipBytesAround(raw, match, findEvidenceScanMax)
	m := secrets.MaskLine(gitleaksrules.MaskLines(file, []string{s})[0])
	secret = m != s
	if !reveal {
		s = m
	}
	s = strings.TrimSpace(sanitizeEvidence(s))
	return clipAround(s, match, findEvidenceTextMax, oneColumn), secret
}

// countFindEvidence counts the evidence lines of res that pick selects.
func countFindEvidence(res findResult, pick func(findEvidence) bool) int {
	n := 0
	for _, m := range res.Matches {
		for _, ev := range m.Evidence {
			if pick(ev) {
				n++
			}
		}
	}
	return n
}

func evidenceMasked(ev findEvidence) bool   { return ev.Masked }
func evidenceRevealed(ev findEvidence) bool { return ev.Revealed }

// findLines words a count of lines.
func findLines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return strconv.Itoa(n) + " lines"
}

func matchStart(s string, match *regexp.Regexp) int {
	if match == nil {
		return 0
	}
	if loc := match.FindStringIndex(s); loc != nil {
		return loc[0]
	}
	return 0
}

// clipBytesAround keeps at most limit bytes of s, around the first match.
func clipBytesAround(s string, match *regexp.Regexp, limit int) string {
	if len(s) <= limit {
		return s
	}
	start := matchStart(s, match) - limit/4
	start = min(max(start, 0), len(s)-limit)
	return strings.ToValidUTF8(s[start:start+limit], "")
}

// clipAround keeps at most limit columns of s, around the first match, and
// marks each cut end with an ellipsis. width gives the columns of a rune: one
// for a count of runes, runewidth.RuneWidth for a terminal.
func clipAround(s string, match *regexp.Regexp, limit int, width func(rune) int) string {
	runes := []rune(s)
	total := 0
	for _, r := range runes {
		total += width(r)
	}
	if total <= limit {
		return s
	}
	// Both ellipses are reserved, so a cut at either end still fits.
	room := limit - 2
	start := utf8.RuneCountInString(s[:matchStart(s, match)])
	for lead := room / 4; start > 0 && lead >= width(runes[start-1]); start-- {
		lead -= width(runes[start-1])
	}
	end, used := start, 0
	for end < len(runes) && used+width(runes[end]) <= room {
		used += width(runes[end])
		end++
	}
	// A match near the end leaves room: fill it from before the match.
	for end == len(runes) && start > 0 && used+width(runes[start-1]) <= room {
		start--
		used += width(runes[start])
	}
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

func oneColumn(rune) int { return 1 }

// sanitizeEvidence makes text from history safe to print: a tab becomes a
// space, and any other control character or a bidirectional override becomes
// U+FFFD, so a line in a commit cannot move the cursor, clear the screen, or
// reorder the text around it.
func sanitizeEvidence(s string) string {
	s = strings.ToValidUTF8(s, string(utf8.RuneError))
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return utf8.RuneError
		case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
			return utf8.RuneError
		}
		return r
	}, s)
}

// highlightFindMatch paints text with paint and makes each match bold. The
// cell helpers reset only the attribute they set, so the bold span keeps the
// color around it.
func highlightFindMatch(text string, match *regexp.Regexp, paint func(string) string) string {
	if match == nil {
		return paint(text)
	}
	var b strings.Builder
	last := 0
	for _, loc := range match.FindAllStringIndex(text, -1) {
		if loc[0] == loc[1] {
			continue
		}
		if loc[0] > last {
			b.WriteString(paint(text[last:loc[0]]))
		}
		b.WriteString(cellBold(paint(text[loc[0]:loc[1]])))
		last = loc[1]
	}
	if last < len(text) {
		b.WriteString(paint(text[last:]))
	}
	return b.String()
}

func plainText(s string) string { return s }

// renderFindEvidence prints the evidence of one match under its row. On a
// terminal each line is clipped to the width, because a wrapped line reads as
// two pieces of evidence.
func renderFindEvidence(w io.Writer, m findMatch, query string) {
	locs := make([]string, len(m.Evidence))
	locWidth := 0
	for i, ev := range m.Evidence {
		if ev.Mode != findModeContent {
			continue
		}
		locs[i] = sanitizeEvidence(ev.File) + ":" + strconv.Itoa(ev.Line)
		locWidth = max(locWidth, min(runewidth.StringWidth(locs[i]), findEvidenceLocMax))
	}
	fit := func(text string, match *regexp.Regexp, used int) string {
		cols, ok := findTermWidth()
		if !ok {
			return text
		}
		// One column stays free: a line that fills the last column wraps on
		// some terminals.
		return clipAround(text, match, max(cols-used-1, findEvidenceColsMin), runewidth.RuneWidth)
	}
	literal := findContentMatcher(query)
	message := findMessageMatcher(query)
	for i, ev := range m.Evidence {
		switch ev.Mode {
		case findModeMessage:
			text := fit(ev.Text, message, len("    message  "))
			fmt.Fprintf(w, "    %s  %s\n", cellFaint("message"), highlightFindMatch(text, message, plainText))
		case findModePath:
			fmt.Fprintf(w, "    %s     %s %s\n", cellFaint("path"), ev.Op, highlightFindMatch(sanitizeEvidence(ev.File), literal, cellCyan))
		case findModeContent:
			paint := cellGreen
			if ev.Op == "-" {
				paint = cellRed
			}
			// Pad before coloring: the width of a colored string counts its codes.
			pad := strings.Repeat(" ", max(locWidth-runewidth.StringWidth(locs[i]), 0))
			text := fit(ev.Text, literal, len("    + ")+runewidth.StringWidth(locs[i])+len(pad)+2)
			fmt.Fprintf(w, "    %s %s%s  %s\n", paint(ev.Op), cellCyan(locs[i]), pad, highlightFindMatch(text, literal, paint))
		}
	}
	if m.EvidenceMore > 0 {
		fmt.Fprintf(w, "    %s\n", cellFaint(fmt.Sprintf("(+%d more)", m.EvidenceMore)))
	}
}
