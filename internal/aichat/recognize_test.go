package aichat

import (
	"context"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

func TestRecognizeIntent_Ignore(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantName string
		wantCmd  string
		wantDang bool
	}{
		{
			name:     "korean ignore with full path and glued particle",
			input:    "mesh-explorer-web/frontend/.omc/state/idle-notif-cooldown.json를 git에 포함하고 싶지않아",
			wantName: "ignore-file",
			wantCmd:  "gk ignore mesh-explorer-web/frontend/.omc/state/idle-notif-cooldown.json",
			wantDang: false,
		},
		{
			name:     "korean ignore + commit",
			input:    "config.json 무시하고 커밋해줘",
			wantName: "ignore-file",
			wantCmd:  "gk ignore config.json --commit",
			wantDang: false,
		},
		{
			name:     "english stop tracking",
			input:    "stop tracking .env",
			wantName: "ignore-file",
			wantCmd:  "gk ignore .env",
			wantDang: false,
		},
		{
			name:     "forget from history is dangerous",
			input:    "secret.txt를 히스토리에서 완전히 지워줘",
			wantName: "forget-history",
			wantCmd:  "gk forget secret.txt",
			wantDang: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, name := recognizeIntent(context.Background(), tc.input, nil, nil, "ko")
			if plan == nil {
				t.Fatalf("expected a deterministic match, got none")
			}
			if name != tc.wantName {
				t.Errorf("recognizer = %q, want %q", name, tc.wantName)
			}
			if len(plan.Commands) != 1 {
				t.Fatalf("expected 1 command, got %d", len(plan.Commands))
			}
			if plan.Commands[0].Command != tc.wantCmd {
				t.Errorf("command = %q, want %q", plan.Commands[0].Command, tc.wantCmd)
			}
			if plan.Commands[0].Dangerous != tc.wantDang {
				t.Errorf("dangerous = %v, want %v", plan.Commands[0].Dangerous, tc.wantDang)
			}
		})
	}
}

func TestRecognizeIntent_NoMatch(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"multi-step defers to LLM", "undo last commit and force push"},
		{"ignore plus push is multi-step", "ignore build.log and push"},
		{"neutral history mention", "show me the commit history"},
		{"no path for ignore", "이 파일 git에 포함하고 싶지 않아"},
		{"unrelated request", "현재 브랜치 상태 보여줘"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, name := recognizeIntent(context.Background(), tc.input, nil, nil, "ko")
			if plan != nil {
				t.Errorf("expected no match, got %q: %+v", name, plan.Commands)
			}
		})
	}
}

func TestRecognizeIntent_UndoLastCommit(t *testing.T) {
	runner := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"rev-parse --verify HEAD~1": {Stdout: "abc123\n"},
		},
	}
	rc := &RepoContext{IsRepo: true}

	plan, name := recognizeIntent(context.Background(), "마지막 커밋 취소해줘", rc, runner, "ko")
	if plan == nil {
		t.Fatal("expected undo recognizer to match")
	}
	if name != "undo-last-commit" {
		t.Fatalf("recognizer = %q, want undo-last-commit", name)
	}
	if got := plan.Commands[0].Command; got != "git reset --soft HEAD~1" {
		t.Errorf("command = %q, want git reset --soft HEAD~1", got)
	}
	if plan.Commands[0].Dangerous {
		t.Error("soft reset should not be flagged dangerous")
	}
}

func TestRecognizeUndoCommit_NoParentCommit(t *testing.T) {
	// Initial commit (no HEAD~1) → recognizer must bow out, not emit a
	// command that would fail at runtime.
	runner := &git.FakeRunner{
		DefaultResp: git.FakeResponse{Err: &git.ExitError{Code: 128}},
	}
	rc := &RepoContext{IsRepo: true}
	plan, _ := recognizeIntent(context.Background(), "마지막 커밋 취소", rc, runner, "ko")
	if plan != nil {
		t.Errorf("expected no match without a parent commit, got %+v", plan.Commands)
	}
}

func TestMentionedPaths(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"a/b/c.json를 무시", []string{"a/b/c.json"}},
		{"ignore .env and .gitignore", []string{".env", ".gitignore"}},
		{"please ignore config.json, thanks", []string{"config.json"}},
		{"버전 v0.64.0 관련", nil},      // version-like, not a path
		{"e.g. this and that", nil}, // 1-char ext rejected
		{"단순 텍스트만 있음", nil},         // no paths
	}
	for _, tc := range cases {
		got := mentionedPaths(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("mentionedPaths(%q) = %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("mentionedPaths(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}
