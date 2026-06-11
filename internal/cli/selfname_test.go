package cli

import (
	"strings"
	"testing"
)

// setInvokedName overrides the binary name for a test and restores it.
func setInvokedName(t *testing.T, name string) {
	t.Helper()
	prev := invokedNameValue
	invokedNameValue = name
	t.Cleanup(func() { invokedNameValue = prev })
}

func TestComputeInvokedName(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"/usr/local/bin/gk"}, "gk"},
		{[]string{"/Users/x/.local/bin/git-kit"}, "git-kit"},
		{[]string{"gk-dev"}, "gk-dev"},
		{[]string{"gk.exe"}, "gk"},
		{[]string{"/tmp/cli.test"}, "gk"}, // test binaries fall back
		{nil, "gk"},
	}
	for _, c := range cases {
		if got := computeInvokedName(c.args); got != c.want {
			t.Errorf("computeInvokedName(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestSelfRewrite(t *testing.T) {
	setInvokedName(t, "git-kit")
	cases := []struct{ in, want string }{
		{"gk continue", "git-kit continue"},
		{"try: gk doctor --fix", "try: git-kit doctor --fix"},
		{"run `gk pull` first", "run `git-kit pull` first"},
		{"gk rebase --allow-pushed", "git-kit rebase --allow-pushed"},
		// ANSI-styled command (bold) — \b does not fire after the m
		{"\x1b[1mgk continue\x1b[0m", "\x1b[1mgit-kit continue\x1b[0m"},
		// not command references: uppercase, Korean particle, mid-word
		{"GK_AGENT=1", "GK_AGENT=1"},
		{"gk 명령", "gk 명령"},
		{"mgk continue", "mgk continue"},
		{"", ""},
		// paths whose basename is gk are NOT commands — a \b would fire
		// after "/" and rewrite the directory into a nonexistent one
		// (real case: merge --into's "receiver worktree: cd <path>" hint).
		{"receiver worktree: cd /work/agentic/gk or pass `--repo /work/agentic/gk`",
			"receiver worktree: cd /work/agentic/gk or pass `--repo /work/agentic/gk`"},
		{"cd ~/repos/gk first", "cd ~/repos/gk first"},
	}
	for _, c := range cases {
		if got := selfRewrite(c.in); got != c.want {
			t.Errorf("selfRewrite(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSelfRewriteNoopUnderCanonicalName(t *testing.T) {
	setInvokedName(t, "gk")
	in := "try: gk doctor --fix"
	if got := selfRewrite(in); got != in {
		t.Errorf("canonical name must be a no-op, got %q", got)
	}
}

func TestSelfCmd(t *testing.T) {
	setInvokedName(t, "git-kit")
	if got := selfCmd("continue"); got != "git-kit continue" {
		t.Errorf("selfCmd = %q", got)
	}
	if got := selfCmd(""); got != "git-kit" {
		t.Errorf("selfCmd empty = %q", got)
	}
}

// The envelope is where agents read remedies — they must be runnable
// verbatim under the name the agent invoked.
func TestFormatErrorJSONRewritesRemedies(t *testing.T) {
	setInvokedName(t, "git-kit")
	err := WithRemedy(
		WithHint(errTest("boom"), "try: gk continue"),
		"finish or abort it first",
		errRemedy{Command: "gk continue", Safety: "safe"},
		errRemedy{Command: "gk abort", Safety: "destructive"},
	)
	out := FormatErrorJSON(err)
	if strings.Contains(out, `"gk `) {
		t.Errorf("envelope still references gk:\n%s", out)
	}
	for _, want := range []string{`"git-kit continue"`, `"git-kit abort"`} {
		if !strings.Contains(out, want) {
			t.Errorf("envelope missing %s:\n%s", want, out)
		}
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }
