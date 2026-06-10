package forget

// fastexport.go — a streaming parser/serializer for the subset of the
// `git fast-export` grammar that gk's native forget engine produces and
// consumes. The engine always invokes fast-export with --no-data, so blob
// commands never appear; file modifications reference original blob OIDs
// that stay valid in-repo. The parser fails closed: any command or
// filechange form outside the expected subset aborts the rewrite before
// fast-import moves a single ref.

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// feChange is one filechange line of a commit. Raw preserves the exact
// original bytes (including path quoting) for byte-stable re-emission;
// Path is the decoded path used for target matching.
type feChange struct {
	Raw  string
	Kind byte // 'M' or 'D'
	Path string
}

// feCommit is a parsed commit block. Header lines are kept verbatim so
// untouched commits round-trip byte-identically (same SHAs after import).
type feCommit struct {
	Ref           string
	Mark          int
	OriginalOID   string
	AuthorLine    string // may be empty (root commits always have committer)
	CommitterLine string
	ExtraHeaders  []string // e.g. "encoding <enc>" — passed through verbatim
	Message       []byte
	HasFrom       bool
	FromMark      int
	MergeMarks    []int
	DeleteAll     bool
	Changes       []feChange
}

// feTag is a parsed annotated-tag block.
type feTag struct {
	Name        string
	Mark        int // 0 when git did not assign one
	FromMark    int
	OriginalOID string
	TaggerLine  string
	Message     []byte
}

// feReset is a parsed reset block. From is the raw mark (0 = absent).
type feReset struct {
	Ref      string
	FromMark int
	HasFrom  bool
}

// feHandler receives parsed blocks in stream order.
type feHandler interface {
	OnCommit(c *feCommit) error
	OnTag(t *feTag) error
	OnReset(r *feReset) error
	// OnDone fires when the stream's `done` command is reached.
	OnDone() error
}

// parseFastExport reads a fast-export stream produced with
// --no-data --show-original-ids --use-done-feature and dispatches blocks
// to h. Unknown commands are hard errors, not passthroughs.
func parseFastExport(r io.Reader, h feHandler) error {
	br := bufio.NewReaderSize(r, 1<<16)
	sawDone := false
	for {
		line, err := readLine(br)
		if err == io.EOF {
			if !sawDone {
				return fmt.Errorf("fast-export stream ended without done command")
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case line == "":
			continue
		case line == "done":
			sawDone = true
			if err := h.OnDone(); err != nil {
				return err
			}
		case strings.HasPrefix(line, "feature "):
			// `feature done` is the only feature our flag set produces.
			continue
		case strings.HasPrefix(line, "progress "):
			continue
		case strings.HasPrefix(line, "reset "):
			rs, err := parseReset(br, strings.TrimPrefix(line, "reset "))
			if err != nil {
				return err
			}
			if err := h.OnReset(rs); err != nil {
				return err
			}
		case strings.HasPrefix(line, "commit "):
			c, err := parseCommit(br, strings.TrimPrefix(line, "commit "))
			if err != nil {
				return err
			}
			if err := h.OnCommit(c); err != nil {
				return err
			}
		case strings.HasPrefix(line, "tag "):
			t, err := parseTag(br, strings.TrimPrefix(line, "tag "))
			if err != nil {
				return err
			}
			if err := h.OnTag(t); err != nil {
				return err
			}
		default:
			return fmt.Errorf("fast-export: unsupported command %q — falling back to filter-repo is required for this repository", firstToken(line))
		}
	}
}

// parseReset consumes the optional `from` line of a reset block.
func parseReset(br *bufio.Reader, ref string) (*feReset, error) {
	rs := &feReset{Ref: ref}
	peek, err := peekLine(br)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if strings.HasPrefix(peek, "from ") {
		if _, err := readLine(br); err != nil {
			return nil, err
		}
		m, err := parseMarkRef(strings.TrimPrefix(peek, "from "))
		if err != nil {
			return nil, fmt.Errorf("reset %s: %w", ref, err)
		}
		rs.FromMark, rs.HasFrom = m, true
	}
	return rs, nil
}

func parseCommit(br *bufio.Reader, ref string) (*feCommit, error) {
	c := &feCommit{Ref: ref}
	// Header section: mark / original-oid / author / committer / extras, then data.
	for {
		line, err := readLine(br)
		if err != nil {
			return nil, fmt.Errorf("commit %s: truncated header: %w", ref, err)
		}
		switch {
		case strings.HasPrefix(line, "mark :"):
			m, perr := strconv.Atoi(strings.TrimPrefix(line, "mark :"))
			if perr != nil {
				return nil, fmt.Errorf("commit %s: bad mark %q", ref, line)
			}
			c.Mark = m
		case strings.HasPrefix(line, "original-oid "):
			c.OriginalOID = strings.TrimPrefix(line, "original-oid ")
		case strings.HasPrefix(line, "author "):
			c.AuthorLine = line
		case strings.HasPrefix(line, "committer "):
			c.CommitterLine = line
		case strings.HasPrefix(line, "data "):
			msg, derr := readData(br, line)
			if derr != nil {
				return nil, fmt.Errorf("commit %s: %w", ref, derr)
			}
			c.Message = msg
			goto body
		case strings.HasPrefix(line, "gpgsig "):
			return nil, fmt.Errorf("commit %s: unexpected embedded signature (gpgsig) in export stream", ref)
		case strings.HasPrefix(line, "encoding "):
			c.ExtraHeaders = append(c.ExtraHeaders, line)
		default:
			return nil, fmt.Errorf("commit %s: unsupported header %q", ref, firstToken(line))
		}
	}
body:
	// Parent and filechange section, terminated by a blank line or the
	// next command (peeked, not consumed).
	for {
		peek, err := peekLine(br)
		if err == io.EOF {
			return c, nil
		}
		if err != nil {
			return nil, err
		}
		switch {
		case peek == "":
			_, _ = readLine(br)
			return c, nil
		case strings.HasPrefix(peek, "from "):
			_, _ = readLine(br)
			m, perr := parseMarkRef(strings.TrimPrefix(peek, "from "))
			if perr != nil {
				return nil, fmt.Errorf("commit %s: %w", ref, perr)
			}
			c.HasFrom, c.FromMark = true, m
		case strings.HasPrefix(peek, "merge "):
			_, _ = readLine(br)
			m, perr := parseMarkRef(strings.TrimPrefix(peek, "merge "))
			if perr != nil {
				return nil, fmt.Errorf("commit %s: %w", ref, perr)
			}
			c.MergeMarks = append(c.MergeMarks, m)
		case peek == "deleteall":
			_, _ = readLine(br)
			c.DeleteAll = true
		case strings.HasPrefix(peek, "M "), strings.HasPrefix(peek, "D "):
			_, _ = readLine(br)
			ch, perr := parseFileChange(peek)
			if perr != nil {
				return nil, fmt.Errorf("commit %s: %w", ref, perr)
			}
			c.Changes = append(c.Changes, ch)
		case strings.HasPrefix(peek, "R "), strings.HasPrefix(peek, "C "), strings.HasPrefix(peek, "N "):
			return nil, fmt.Errorf("commit %s: unsupported filechange %q (rename/copy/note)", ref, firstToken(peek))
		default:
			// Next command begins — commit block is complete.
			return c, nil
		}
	}
}

func parseTag(br *bufio.Reader, name string) (*feTag, error) {
	t := &feTag{Name: name}
	for {
		line, err := readLine(br)
		if err != nil {
			return nil, fmt.Errorf("tag %s: truncated: %w", name, err)
		}
		switch {
		case strings.HasPrefix(line, "mark :"):
			m, perr := strconv.Atoi(strings.TrimPrefix(line, "mark :"))
			if perr != nil {
				return nil, fmt.Errorf("tag %s: bad mark %q", name, line)
			}
			t.Mark = m
		case strings.HasPrefix(line, "from "):
			m, perr := parseMarkRef(strings.TrimPrefix(line, "from "))
			if perr != nil {
				return nil, fmt.Errorf("tag %s: %w", name, perr)
			}
			t.FromMark = m
		case strings.HasPrefix(line, "original-oid "):
			t.OriginalOID = strings.TrimPrefix(line, "original-oid ")
		case strings.HasPrefix(line, "tagger "):
			t.TaggerLine = line
		case strings.HasPrefix(line, "data "):
			msg, derr := readData(br, line)
			if derr != nil {
				return nil, fmt.Errorf("tag %s: %w", name, derr)
			}
			t.Message = msg
			return t, nil
		default:
			return nil, fmt.Errorf("tag %s: unsupported header %q", name, firstToken(line))
		}
	}
}

// parseFileChange splits an M/D line and decodes its (possibly quoted) path.
func parseFileChange(line string) (feChange, error) {
	ch := feChange{Raw: line, Kind: line[0]}
	var pathPart string
	switch ch.Kind {
	case 'M':
		// M <mode> <dataref> <path>
		rest := line[2:]
		i := strings.IndexByte(rest, ' ')
		if i < 0 {
			return ch, fmt.Errorf("malformed filechange %q", line)
		}
		rest = rest[i+1:]
		j := strings.IndexByte(rest, ' ')
		if j < 0 {
			return ch, fmt.Errorf("malformed filechange %q", line)
		}
		pathPart = rest[j+1:]
	case 'D':
		pathPart = line[2:]
	}
	p, err := unquotePath(pathPart)
	if err != nil {
		return ch, fmt.Errorf("filechange %q: %w", line, err)
	}
	ch.Path = p
	return ch, nil
}

// unquotePath decodes git's C-style path quoting ("..." with octal and
// control escapes). Unquoted paths pass through as-is.
func unquotePath(s string) (string, error) {
	if !strings.HasPrefix(s, `"`) {
		return s, nil
	}
	if len(s) < 2 || !strings.HasSuffix(s, `"`) {
		return "", fmt.Errorf("unterminated quoted path")
	}
	body := s[1 : len(s)-1]
	var b bytes.Buffer
	for i := 0; i < len(body); i++ {
		cur := body[i]
		if cur != '\\' {
			b.WriteByte(cur)
			continue
		}
		i++
		if i >= len(body) {
			return "", fmt.Errorf("dangling escape in quoted path")
		}
		switch e := body[i]; e {
		case '"', '\\':
			b.WriteByte(e)
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case '0', '1', '2', '3':
			if i+2 >= len(body) {
				return "", fmt.Errorf("truncated octal escape in quoted path")
			}
			n, err := strconv.ParseUint(body[i:i+3], 8, 8)
			if err != nil {
				return "", fmt.Errorf("bad octal escape in quoted path: %w", err)
			}
			b.WriteByte(byte(n))
			i += 2
		default:
			return "", fmt.Errorf("unknown escape \\%c in quoted path", e)
		}
	}
	return b.String(), nil
}

// parseMarkRef resolves a `from`/`merge` operand. The full-history export
// gk performs guarantees every referenced parent was emitted with a mark;
// a raw SHA here means an assumption broke, so it is an error.
func parseMarkRef(s string) (int, error) {
	if !strings.HasPrefix(s, ":") {
		return 0, fmt.Errorf("non-mark parent reference %q (partial export?)", s)
	}
	m, err := strconv.Atoi(s[1:])
	if err != nil {
		return 0, fmt.Errorf("bad mark reference %q", s)
	}
	return m, nil
}

// readData consumes a `data <n>` payload (the header line is already read
// and passed in). fast-export emits exact byte counts; the optional
// trailing LF after the payload is consumed when present.
func readData(br *bufio.Reader, header string) ([]byte, error) {
	nStr := strings.TrimPrefix(header, "data ")
	if strings.HasPrefix(nStr, "<<") {
		return nil, fmt.Errorf("delimited data blocks are not supported")
	}
	n, err := strconv.Atoi(nStr)
	if err != nil {
		return nil, fmt.Errorf("bad data header %q", header)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(br, buf); err != nil {
		return nil, fmt.Errorf("truncated data block: %w", err)
	}
	if next, err := br.Peek(1); err == nil && next[0] == '\n' {
		_, _ = br.Discard(1)
	}
	return buf, nil
}

// readLine returns the next line without its trailing LF.
func readLine(br *bufio.Reader) (string, error) {
	s, err := br.ReadString('\n')
	if err == io.EOF && s != "" {
		return s, nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(s, "\n"), nil
}

// peekLine returns the next line without consuming it.
func peekLine(br *bufio.Reader) (string, error) {
	for size := 1 << 10; ; size *= 2 {
		buf, err := br.Peek(size)
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			return string(buf[:i]), nil
		}
		if err != nil {
			if err == io.EOF {
				return string(buf), io.EOF
			}
			return "", err
		}
		if size > 1<<20 {
			return "", fmt.Errorf("fast-export line exceeds 1MB")
		}
	}
}

func firstToken(line string) string {
	if i := strings.IndexByte(line, ' '); i > 0 {
		return line[:i]
	}
	return line
}

// ─── serialization ───

// writeCommit re-emits a commit block. Header lines are verbatim copies,
// so a commit whose parents and changes were untouched round-trips
// byte-identically (and therefore keeps its SHA through fast-import).
// rootReset must be emitted by the caller when the commit lost all parents.
func writeCommit(w *bufio.Writer, c *feCommit, parents []int, changes []feChange) {
	fmt.Fprintf(w, "commit %s\n", c.Ref)
	fmt.Fprintf(w, "mark :%d\n", c.Mark)
	if c.OriginalOID != "" {
		fmt.Fprintf(w, "original-oid %s\n", c.OriginalOID)
	}
	if c.AuthorLine != "" {
		_, _ = w.WriteString(c.AuthorLine)
		_ = w.WriteByte('\n')
	}
	_, _ = w.WriteString(c.CommitterLine)
	_ = w.WriteByte('\n')
	for _, h := range c.ExtraHeaders {
		_, _ = w.WriteString(h)
		_ = w.WriteByte('\n')
	}
	fmt.Fprintf(w, "data %d\n", len(c.Message))
	_, _ = w.Write(c.Message)
	if len(parents) > 0 {
		fmt.Fprintf(w, "from :%d\n", parents[0])
		for _, m := range parents[1:] {
			fmt.Fprintf(w, "merge :%d\n", m)
		}
	}
	if c.DeleteAll {
		_, _ = w.WriteString("deleteall\n")
	}
	for _, ch := range changes {
		_, _ = w.WriteString(ch.Raw)
		_ = w.WriteByte('\n')
	}
	_ = w.WriteByte('\n')
}

func writeTag(w *bufio.Writer, t *feTag, fromMark int) {
	fmt.Fprintf(w, "tag %s\n", t.Name)
	if t.Mark != 0 {
		fmt.Fprintf(w, "mark :%d\n", t.Mark)
	}
	fmt.Fprintf(w, "from :%d\n", fromMark)
	if t.OriginalOID != "" {
		fmt.Fprintf(w, "original-oid %s\n", t.OriginalOID)
	}
	if t.TaggerLine != "" {
		_, _ = w.WriteString(t.TaggerLine)
		_ = w.WriteByte('\n')
	}
	fmt.Fprintf(w, "data %d\n", len(t.Message))
	_, _ = w.Write(t.Message)
	_ = w.WriteByte('\n')
}

func writeReset(w *bufio.Writer, ref string, fromMark int) {
	fmt.Fprintf(w, "reset %s\n", ref)
	if fromMark != 0 {
		fmt.Fprintf(w, "from :%d\n", fromMark)
	}
}
