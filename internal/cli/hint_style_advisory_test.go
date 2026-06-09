package cli

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/easy"
)

// enableEasyKoForTest injects a Korean Easy-Mode engine into the package
// global so easyNoteLine resolves catalog entries, restoring the previous
// engine (and flags) on cleanup. The sync.Once is consumed up front so a
// later EasyEngine() call cannot rebuild from the developer's real config.
func enableEasyKoForTest(t *testing.T) {
	t.Helper()
	prevEng := easyEngine
	prevEasy, prevNoEasy := flagEasy, flagNoEasy
	flagEasy, flagNoEasy = true, false
	easyEngineOnce = sync.Once{}
	easyEngineOnce.Do(func() {})
	easyEngine = easy.NewEngine(config.OutputConfig{Easy: true, Lang: "ko", Emoji: true, Hints: "verbose"}, true, false)
	t.Cleanup(func() {
		easyEngine = prevEng
		flagEasy, flagNoEasy = prevEasy, prevNoEasy
		easyEngineOnce = sync.Once{}
	})
}

func TestRenderAdvisory_NoteBlock(t *testing.T) {
	withNoColor(t)
	got := renderAdvisory("note", []string{
		"'main' has no upstream configured — using origin/main",
		"set tracking with: git branch --set-upstream-to=origin/main main",
	})
	want := "█  NOTE\n" +
		"   'main' has no upstream configured — using origin/main\n" +
		"   set tracking with: git branch --set-upstream-to=origin/main main\n"
	if got != want {
		t.Errorf("renderAdvisory note block:\ngot:\n%q\nwant:\n%q", got, want)
	}
}

func TestRenderAdvisory_HintBlock(t *testing.T) {
	withNoColor(t)
	got := renderAdvisory("hint", []string{"try: gk pull --autostash"})
	want := "█  HINT\n   try: gk pull --autostash\n"
	if got != want {
		t.Errorf("renderAdvisory hint block:\ngot:\n%q\nwant:\n%q", got, want)
	}
}

func TestEasyNoteLine_OffByDefault(t *testing.T) {
	disableEasyForTest(t)
	if got := easyNoteLine("pull.note.no_upstream_same_name", []any{"main", "origin/main"}); got != "" {
		t.Errorf("easy off → want empty, got %q", got)
	}
}

func TestEasyNoteLine_CatalogMissSuppressed(t *testing.T) {
	enableEasyKoForTest(t)
	if got := easyNoteLine("no.such.catalog.key", []any{"x"}); got != "" {
		t.Errorf("catalog miss must not leak the raw key, got %q", got)
	}
}

func TestPrintNote_EasyElaborationKo(t *testing.T) {
	withNoColor(t)
	enableEasyKoForTest(t)

	buf := &bytes.Buffer{}
	printNote(buf, appendEasyLine([]string{
		"'main' has no upstream configured — using origin/main",
		"set tracking with: git branch --set-upstream-to=origin/main main",
	}, "pull.note.no_upstream_same_name", "main", "origin/main")...)

	out := buf.String()
	if !strings.Contains(out, "█  NOTE") {
		t.Errorf("missing NOTE header:\n%s", out)
	}
	if !strings.Contains(out, "'main' 브랜치가 아직 서버 브랜치와 연결(추적)되어 있지 않아요") {
		t.Errorf("missing Korean easy elaboration:\n%s", out)
	}
	if !strings.Contains(out, "origin/main를 대신 사용했어요") {
		t.Errorf("easy elaboration should embed the upstream name:\n%s", out)
	}
}
