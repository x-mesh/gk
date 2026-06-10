package forget

import (
	"strings"
	"testing"
)

// collectHandler buffers parsed blocks for assertions.
type collectHandler struct {
	commits []*feCommit
	tags    []*feTag
	resets  []*feReset
	done    bool
}

func (c *collectHandler) OnCommit(x *feCommit) error { c.commits = append(c.commits, x); return nil }
func (c *collectHandler) OnTag(x *feTag) error       { c.tags = append(c.tags, x); return nil }
func (c *collectHandler) OnReset(x *feReset) error   { c.resets = append(c.resets, x); return nil }
func (c *collectHandler) OnDone() error              { c.done = true; return nil }

const sampleStream = `feature done
reset refs/heads/side
commit refs/heads/side
mark :1
original-oid eaa2403cc2613a792a20351de8cb71fd9047ab40
author t <t@t.io> 1781070450 +0900
committer t <t@t.io> 1781070450 +0900
data 10
root: both
M 100644 587be6b4c3f93f93c489c0111bba5596147a26cb ".xm/op/\355\225\234\352\270\200 \355\214\214\354\235\274.json"
M 100644 78981922613b2afb6025042ff6bd878ac1994e85 a.txt

commit refs/heads/main
mark :2
original-oid da7274d27aa6c7aac79c2f78fd5a320dc5020d3b
author t <t@t.io> 1781070450 +0900
committer t <t@t.io> 1781070450 +0900
data 10
main: code
from :1
M 100644 9ad2ebbaff6f3397bb65002dcf4294d8d6243982 a.txt
D old.txt

commit refs/heads/main
mark :4
original-oid efd10525becc88921c25e9f359b7156d676ca2d2
author t <t@t.io> 1781070450 +0900
committer t <t@t.io> 1781070450 +0900
data 10
merge side
from :2
merge :1

reset refs/tags/light
from :4

tag v1
from :4
original-oid 7e689a1b238312ba0162ea4bcba7995729ea7f05
tagger t <t@t.io> 1781070450 +0900
data 10
release v1
done
`

func TestParseFastExportSample(t *testing.T) {
	h := &collectHandler{}
	if err := parseFastExport(strings.NewReader(sampleStream), h); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !h.done {
		t.Fatal("done not seen")
	}
	if len(h.commits) != 3 || len(h.tags) != 1 || len(h.resets) != 2 {
		t.Fatalf("counts: commits=%d tags=%d resets=%d", len(h.commits), len(h.tags), len(h.resets))
	}

	root := h.commits[0]
	if root.Mark != 1 || root.HasFrom || len(root.Changes) != 2 {
		t.Errorf("root parsed wrong: %+v", root)
	}
	if got := root.Changes[0].Path; got != ".xm/op/한글 파일.json" {
		t.Errorf("quoted path decoded to %q", got)
	}
	if !strings.HasPrefix(root.Changes[0].Raw, `M 100644 587be6b4`) {
		t.Errorf("raw line not preserved: %q", root.Changes[0].Raw)
	}
	if string(root.Message) != "root: both" {
		t.Errorf("message = %q", root.Message)
	}

	c2 := h.commits[1]
	if !c2.HasFrom || c2.FromMark != 1 {
		t.Errorf("c2 from = %+v", c2)
	}
	if c2.Changes[1].Kind != 'D' || c2.Changes[1].Path != "old.txt" {
		t.Errorf("D line parsed wrong: %+v", c2.Changes[1])
	}

	merge := h.commits[2]
	if merge.FromMark != 2 || len(merge.MergeMarks) != 1 || merge.MergeMarks[0] != 1 {
		t.Errorf("merge parents wrong: %+v", merge)
	}

	tag := h.tags[0]
	if tag.Name != "v1" || tag.FromMark != 4 || string(tag.Message) != "release v1" {
		t.Errorf("tag parsed wrong: %+v", tag)
	}
	// reset without from (branch-name clear) and lightweight tag reset
	if h.resets[0].HasFrom {
		t.Errorf("bare reset should have no from: %+v", h.resets[0])
	}
	if !h.resets[1].HasFrom || h.resets[1].FromMark != 4 {
		t.Errorf("tag reset wrong: %+v", h.resets[1])
	}
}

func TestParseFastExportFailsClosed(t *testing.T) {
	cases := map[string]string{
		"rename":          "feature done\ncommit refs/heads/m\nmark :1\ncommitter t <t@t.io> 1 +0000\ndata 1\nx\nR a b\ndone\n",
		"unknown command": "feature done\nblob\nmark :1\ndata 1\nx\ndone\n",
		"no done":         "feature done\n",
		"sha parent":      "feature done\ncommit refs/heads/m\nmark :2\ncommitter t <t@t.io> 1 +0000\ndata 1\nx\nfrom deadbeefdeadbeefdeadbeefdeadbeefdeadbeef\ndone\n",
	}
	for name, stream := range cases {
		if err := parseFastExport(strings.NewReader(stream), &collectHandler{}); err == nil {
			t.Errorf("%s: want error, got nil", name)
		}
	}
}

func TestUnquotePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{`plain/path.txt`, "plain/path.txt"},
		{`"\355\225\234"`, "한"},
		{`"a\tb"`, "a\tb"},
		{`"q\"uote\\back"`, `q"uote\back`},
	}
	for _, c := range cases {
		got, err := unquotePath(c.in)
		if err != nil || got != c.want {
			t.Errorf("unquotePath(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{`"unterminated`, `"\q"`, `"\35"`} {
		if _, err := unquotePath(bad); err == nil {
			t.Errorf("unquotePath(%q): want error", bad)
		}
	}
}
