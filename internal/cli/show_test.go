package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestParseShowRecords(t *testing.T) {
	raw := "sha\x00short\x00Alice\x00alice@example.com\x002026-01-02T03:04:05+09:00\x00parent\x00subject\x00first line\n\nsecond line\x1e"
	commits := parseShowRecords([]byte(raw))
	if len(commits) != 1 {
		t.Fatalf("got %d commits, want 1", len(commits))
	}
	if commits[0].Subject != "subject" || commits[0].Body != "first line\n\nsecond line" {
		t.Fatalf("parsed commit = %+v", commits[0])
	}
}

func TestLoadShowCommitAndRender(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.WriteFile("show.txt", "hello\n")
	repo.RunGit("add", "show.txt")
	repo.RunGit("commit", "-m", "feat: show commit", "-m", "details")
	sha := repo.RunGit("rev-parse", "HEAD")

	commit, err := loadShowCommit(context.Background(), &git.ExecRunner{Dir: repo.Dir}, sha)
	if err != nil {
		t.Fatalf("loadShowCommit: %v", err)
	}
	if commit.Subject != "feat: show commit" || commit.Body != "details" {
		t.Fatalf("metadata = %+v", commit)
	}
	if len(commit.Diff.Files) != 1 || commit.Diff.Files[0].NewPath != "show.txt" {
		t.Fatalf("diff = %+v", commit.Diff)
	}

	var out bytes.Buffer
	if err := writeShowText(&out, commit, false, false, false, false); err != nil {
		t.Fatalf("writeShowText: %v", err)
	}
	for _, want := range []string{"commit " + sha, "feat: show commit", "show.txt", "hello"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestWriteShowJSON(t *testing.T) {
	commit := showCommit{SHA: "sha", ShortSHA: "short", Subject: "subject"}
	var out bytes.Buffer
	if err := writeShowJSON(&out, commit, false); err != nil {
		t.Fatalf("writeShowJSON: %v", err)
	}
	var got showJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got.Schema != 1 || got.Commit.Subject != "subject" || got.Patch != "" {
		t.Fatalf("JSON = %+v", got)
	}
}

func TestShowBrowserNavigation(t *testing.T) {
	model := showBrowserModel{commits: []showCommit{{ShortSHA: "one", Subject: "one"}, {ShortSHA: "two", Subject: "two"}}}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	model = updated.(showBrowserModel)
	if !model.ready || model.leftWidth != 40 {
		t.Fatalf("layout = ready:%v left:%d", model.ready, model.leftWidth)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(showBrowserModel)
	if model.selected != 1 {
		t.Fatalf("selected = %d, want 1", model.selected)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	model = updated.(showBrowserModel)
	if !model.focusDetail {
		t.Fatal("tab did not focus detail")
	}
}

func TestShowBrowserLazyLoad(t *testing.T) {
	page := func(sha, short, subject string) showCommit {
		return showCommit{SHA: sha, ShortSHA: short, Subject: subject}
	}
	model := showBrowserModel{
		commits:  []showCommit{page("a", "a", "first")},
		pageSize: 1,
		nextSkip: 1,
		hasMore:  true,
		details:  make(map[string]showCommit),
	}
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(showBrowserModel)
	if cmd == nil || !model.loadingMore {
		t.Fatalf("load more not started: cmd=%v loading=%v", cmd != nil, model.loadingMore)
	}
	updated, _ = model.Update(showMoreLoadedMsg{commits: []showCommit{page("b", "b", "second")}})
	model = updated.(showBrowserModel)
	if len(model.commits) != 2 || model.nextSkip != 2 || !model.hasMore {
		t.Fatalf("page state = len:%d skip:%d more:%v", len(model.commits), model.nextSkip, model.hasMore)
	}

	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(showBrowserModel)
	if cmd == nil || !model.loadingDetail {
		t.Fatalf("detail load not started: cmd=%v loading=%v", cmd != nil, model.loadingDetail)
	}
	updated, _ = model.Update(showDetailLoadedMsg{sha: "b", commit: showCommit{SHA: "b", ShortSHA: "b", Subject: "second", Patch: "patch"}})
	model = updated.(showBrowserModel)
	if _, ok := model.details["b"]; !ok || model.loadingDetail {
		t.Fatalf("detail cache = %+v loading=%v", model.details, model.loadingDetail)
	}
}

func TestShowBrowserQueuesLatestDetailAfterFastNavigation(t *testing.T) {
	model := showBrowserModel{
		commits: []showCommit{{SHA: "a", ShortSHA: "a"}, {SHA: "b", ShortSHA: "b"}},
		details: make(map[string]showCommit),
	}
	updated, firstCmd := model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	model = updated.(showBrowserModel)
	if firstCmd == nil || model.detailRequest != "a" {
		t.Fatalf("initial detail request = %v, cmd=%v", model.detailRequest, firstCmd != nil)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(showBrowserModel)
	if model.selected != 1 || model.detailRequest != "a" {
		t.Fatalf("after fast navigation = selected:%d request:%q", model.selected, model.detailRequest)
	}
	updated, nextCmd := model.Update(showDetailLoadedMsg{sha: "a", commit: showCommit{SHA: "a", Patch: "patch-a"}})
	model = updated.(showBrowserModel)
	if nextCmd == nil || !model.loadingDetail || model.detailRequest != "b" {
		t.Fatalf("queued detail request = cmd:%v loading:%v request:%q", nextCmd != nil, model.loadingDetail, model.detailRequest)
	}
}

func TestShowBrowserSmallLayout(t *testing.T) {
	model := showBrowserModel{commits: []showCommit{{ShortSHA: "a", Subject: "first"}}, details: make(map[string]showCommit)}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	model = updated.(showBrowserModel)
	if !model.ready {
		t.Fatal("small layout is not ready")
	}
	if model.list.Height < 4 || model.detail.Height < 4 {
		t.Fatalf("viewport heights = list:%d detail:%d", model.list.Height, model.detail.Height)
	}
	if model.detail.Width != 36 {
		t.Fatalf("detail width = %d, want 36", model.detail.Width)
	}
	if model.list.Height+model.detail.Height > 15 {
		t.Fatalf("panels exceed usable height: %d", model.list.Height+model.detail.Height)
	}
}

func TestShowBrowserViewFitsSmallTerminal(t *testing.T) {
	commit := showCommit{SHA: "a", ShortSHA: "a", Subject: "first", Patch: "patch"}
	for _, height := range []int{10, 20} {
		model := showBrowserModel{
			commits: []showCommit{commit},
			details: map[string]showCommit{"a": commit},
			noPatch: true,
		}
		updated, _ := model.Update(tea.WindowSizeMsg{Width: 40, Height: height})
		model = updated.(showBrowserModel)

		lines := strings.Count(model.View(), "\n") + 1
		if lines > height {
			t.Fatalf("height %d rendered %d lines", height, lines)
		}
	}
}
