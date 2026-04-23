package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fatih/color"
)

func TestDbg_DisabledIsNoOp(t *testing.T) {
	t.Cleanup(func() { flagDebug = false })

	buf := &bytes.Buffer{}
	prev := SetDebugWriter(buf)
	t.Cleanup(func() { SetDebugWriter(prev) })

	flagDebug = false
	Dbg("should not appear: %d", 42)
	if buf.Len() != 0 {
		t.Errorf("debug off should produce no output, got %q", buf.String())
	}
}

func TestDbg_EnabledWritesToWriter(t *testing.T) {
	color.NoColor = true
	t.Cleanup(func() {
		color.NoColor = false
		flagDebug = false
	})

	buf := &bytes.Buffer{}
	prev := SetDebugWriter(buf)
	t.Cleanup(func() { SetDebugWriter(prev) })

	flagDebug = true
	Dbg("hello %s", "world")

	got := buf.String()
	if !strings.Contains(got, "[debug") {
		t.Errorf("missing prefix, got %q", got)
	}
	if !strings.Contains(got, "hello world") {
		t.Errorf("missing formatted body, got %q", got)
	}
}
