package aichat

import (
	"strings"
	"testing"
)

// testRepoContext returns a typical RepoContext for testing.
func testRepoContext() *RepoContext {
	return &RepoContext{
		Branch:       "feat/auth",
		HeadSHA:      "abc1234",
		Upstream:     "origin/feat/auth",
		Status:       " M internal/auth.go\n?? new.txt",
		RecentReflog: []string{"abc1234 commit: add auth", "def5678 checkout: feat/auth"},
		IsRepo:       true,
	}
}

// --- Language code inclusion tests ---

func TestAllPromptBuildersIncludeLanguageCode(t *testing.T) {
	langs := []string{"ko", "en", "ja", "zh", "fr"}
	rc := testRepoContext()

	for _, lang := range langs {
		t.Run("lang="+lang, func(t *testing.T) {
			doPrompt := buildDoUserPrompt("push changes", rc, lang)
			if !strings.Contains(doPrompt, lang) {
				t.Errorf("buildDoUserPrompt missing lang %q", lang)
			}

			explainPrompt := buildExplainUserPrompt("merge conflict", rc, lang)
			if !strings.Contains(explainPrompt, lang) {
				t.Errorf("buildExplainUserPrompt missing lang %q", lang)
			}

			explainLastPrompt := buildExplainLastUserPrompt(rc, lang)
			if !strings.Contains(explainLastPrompt, lang) {
				t.Errorf("buildExplainLastUserPrompt missing lang %q", lang)
			}

			askPrompt := buildAskUserPrompt("what is rebase?", rc, lang)
			if !strings.Contains(askPrompt, lang) {
				t.Errorf("buildAskUserPrompt missing lang %q", lang)
			}
		})
	}
}

func TestPromptBuildersDefaultLanguage(t *testing.T) {
	rc := testRepoContext()

	// Empty lang should default to "en".
	doPrompt := buildDoUserPrompt("push", rc, "")
	if !strings.Contains(doPrompt, "Respond in language: en") {
		t.Error("buildDoUserPrompt should default to 'en' when lang is empty")
	}
}

// --- buildDoUserPrompt tests ---

func TestBuildDoUserPrompt_IncludesGkCommandReference(t *testing.T) {
	rc := testRepoContext()
	prompt := buildDoUserPrompt("push my changes", rc, "ko")

	if !strings.Contains(prompt, "Available gk commands:") {
		t.Error("buildDoUserPrompt missing gk command reference")
	}
	if !strings.Contains(prompt, "gk sync") {
		t.Error("buildDoUserPrompt missing gk sync in command reference")
	}
	if !strings.Contains(prompt, "gk push") {
		t.Error("buildDoUserPrompt missing gk push in command reference")
	}
}

func TestBuildDoUserPrompt_IncludesJSONSchema(t *testing.T) {
	rc := testRepoContext()
	prompt := buildDoUserPrompt("push my changes", rc, "en")

	if !strings.Contains(prompt, "JSON schema:") {
		t.Error("buildDoUserPrompt missing JSON schema instructions")
	}
	if !strings.Contains(prompt, `"commands"`) {
		t.Error("buildDoUserPrompt missing commands key in JSON schema")
	}
	if !strings.Contains(prompt, `"dangerous"`) {
		t.Error("buildDoUserPrompt missing dangerous field in JSON schema")
	}
}

func TestBuildDoUserPrompt_IncludesDangerousCommandInstructions(t *testing.T) {
	rc := testRepoContext()
	prompt := buildDoUserPrompt("force push", rc, "en")

	if !strings.Contains(prompt, "dangerous") {
		t.Error("buildDoUserPrompt missing dangerous command flag instructions")
	}
	if !strings.Contains(prompt, "force push") {
		t.Error("buildDoUserPrompt missing force push in dangerous patterns")
	}
}

func TestBuildDoUserPrompt_IncludesUserInput(t *testing.T) {
	rc := testRepoContext()
	prompt := buildDoUserPrompt("어제 커밋 취소", rc, "ko")

	if !strings.Contains(prompt, "어제 커밋 취소") {
		t.Error("buildDoUserPrompt missing user input")
	}
}

// --- Context wrapping tests ---

func TestContextWrappedInTags(t *testing.T) {
	rc := testRepoContext()
	prompt := buildDoUserPrompt("push", rc, "en")

	if !strings.Contains(prompt, "<CONTEXT>") {
		t.Error("prompt missing <CONTEXT> opening tag")
	}
	if !strings.Contains(prompt, "</CONTEXT>") {
		t.Error("prompt missing </CONTEXT> closing tag")
	}
	// Branch info should be inside context tags.
	ctxStart := strings.Index(prompt, "<CONTEXT>")
	ctxEnd := strings.Index(prompt, "</CONTEXT>")
	if ctxStart >= ctxEnd {
		t.Error("context tags are malformed")
	}
	ctxContent := prompt[ctxStart:ctxEnd]
	if !strings.Contains(ctxContent, "Branch: feat/auth") {
		t.Error("branch info should be inside <CONTEXT> tags")
	}
}

func TestContextWrappedInTags_AllBuilders(t *testing.T) {
	rc := testRepoContext()

	builders := map[string]string{
		"do":           buildDoUserPrompt("push", rc, "en"),
		"explain":      buildExplainUserPrompt("error", rc, "en"),
		"explain-last": buildExplainLastUserPrompt(rc, "en"),
		"ask":          buildAskUserPrompt("question", rc, "en"),
	}

	for name, prompt := range builders {
		if !strings.Contains(prompt, "<CONTEXT>") || !strings.Contains(prompt, "</CONTEXT>") {
			t.Errorf("%s prompt missing <CONTEXT>...</CONTEXT> tags", name)
		}
	}
}

func TestContextWrapping_NilRepoContext(t *testing.T) {
	prompt := buildDoUserPrompt("push", nil, "en")

	if strings.Contains(prompt, "<CONTEXT>") {
		t.Error("nil RepoContext should not produce <CONTEXT> tags")
	}
}

func TestContextWrapping_NonGitRepo(t *testing.T) {
	rc := &RepoContext{IsRepo: false}
	prompt := buildDoUserPrompt("push", rc, "en")

	// Non-git repo produces "Not a git repository." without context tags.
	if strings.Contains(prompt, "<CONTEXT>") {
		t.Error("non-git repo should not produce <CONTEXT> tags")
	}
}

// --- buildExplainUserPrompt tests ---

func TestBuildExplainUserPrompt_IncludesThreeSectionStructure(t *testing.T) {
	rc := testRepoContext()
	prompt := buildExplainUserPrompt("fatal: not a git repository", rc, "ko")

	if !strings.Contains(prompt, "Cause") {
		t.Error("buildExplainUserPrompt missing Cause section instruction")
	}
	if !strings.Contains(prompt, "Solution") {
		t.Error("buildExplainUserPrompt missing Solution section instruction")
	}
	if !strings.Contains(prompt, "Prevention") {
		t.Error("buildExplainUserPrompt missing Prevention section instruction")
	}
}

func TestBuildExplainUserPrompt_IncludesErrorMessage(t *testing.T) {
	rc := testRepoContext()
	prompt := buildExplainUserPrompt("fatal: merge conflict in file.go", rc, "en")

	if !strings.Contains(prompt, "fatal: merge conflict in file.go") {
		t.Error("buildExplainUserPrompt missing error message")
	}
}

// --- buildExplainLastUserPrompt tests ---

func TestBuildExplainLastUserPrompt_IncludesReflogInstructions(t *testing.T) {
	rc := testRepoContext()
	prompt := buildExplainLastUserPrompt(rc, "en")

	if !strings.Contains(prompt, "reflog") {
		t.Error("buildExplainLastUserPrompt missing reflog reference")
	}
	if !strings.Contains(prompt, "step-by-step") {
		t.Error("buildExplainLastUserPrompt missing step-by-step instruction")
	}
}

func TestBuildExplainLastUserPrompt_IncludesHEADIndexWorkingTree(t *testing.T) {
	rc := testRepoContext()
	prompt := buildExplainLastUserPrompt(rc, "ko")

	if !strings.Contains(prompt, "HEAD") {
		t.Error("buildExplainLastUserPrompt missing HEAD reference")
	}
	if !strings.Contains(prompt, "index") {
		t.Error("buildExplainLastUserPrompt missing index reference")
	}
	if !strings.Contains(prompt, "working tree") {
		t.Error("buildExplainLastUserPrompt missing working tree reference")
	}
}

// --- buildAskUserPrompt tests ---

func TestBuildAskUserPrompt_IncludesGkCommandSuggestions(t *testing.T) {
	rc := testRepoContext()
	prompt := buildAskUserPrompt("rebase란 무엇인가요?", rc, "ko")

	if !strings.Contains(prompt, "1-3 related gk commands") {
		t.Error("buildAskUserPrompt missing gk command suggestion instructions")
	}
	if !strings.Contains(prompt, "Available gk commands:") {
		t.Error("buildAskUserPrompt missing gk command reference")
	}
}

func TestBuildAskUserPrompt_IncludesQuestion(t *testing.T) {
	rc := testRepoContext()
	prompt := buildAskUserPrompt("how do I undo a commit?", rc, "en")

	if !strings.Contains(prompt, "how do I undo a commit?") {
		t.Error("buildAskUserPrompt missing user question")
	}
}

func TestBuildAskUserPrompt_IncludesRedirectInstruction(t *testing.T) {
	rc := testRepoContext()
	prompt := buildAskUserPrompt("what is the weather?", rc, "en")

	if !strings.Contains(prompt, "politely redirect") {
		t.Error("buildAskUserPrompt missing redirect instruction for non-git questions")
	}
}

// --- System prompt tests ---

func TestChatSystemPrompt_ContainsInjectionPrevention(t *testing.T) {
	if !strings.Contains(chatSystemPrompt, "UNTRUSTED") {
		t.Error("chatSystemPrompt missing UNTRUSTED injection prevention")
	}
	if !strings.Contains(chatSystemPrompt, "<CONTEXT>") {
		t.Error("chatSystemPrompt missing <CONTEXT> reference")
	}
}

func TestChatSystemPrompt_PrefersGkCommands(t *testing.T) {
	if !strings.Contains(chatSystemPrompt, "prefer gk commands") {
		t.Error("chatSystemPrompt missing gk command preference")
	}
}

// --- wrapContext tests ---

func TestWrapContext_NilContext(t *testing.T) {
	result := wrapContext(nil)
	if result != "" {
		t.Errorf("wrapContext(nil) = %q, want empty", result)
	}
}

func TestWrapContext_NonGitRepo(t *testing.T) {
	rc := &RepoContext{IsRepo: false}
	result := wrapContext(rc)
	if result != "Not a git repository." {
		t.Errorf("wrapContext(non-git) = %q, want %q", result, "Not a git repository.")
	}
}

func TestWrapContext_ValidRepo(t *testing.T) {
	rc := testRepoContext()
	result := wrapContext(rc)

	if !strings.HasPrefix(result, "<CONTEXT>\n") {
		t.Error("wrapContext should start with <CONTEXT>\\n")
	}
	if !strings.HasSuffix(result, "</CONTEXT>") {
		t.Error("wrapContext should end with </CONTEXT>")
	}
}

// --- langInstruction tests ---

func TestLangInstruction_EmptyDefaultsToEn(t *testing.T) {
	result := langInstruction("")
	if result != "Respond in language: en" {
		t.Errorf("langInstruction('') = %q, want %q", result, "Respond in language: en")
	}
}

func TestLangInstruction_Korean(t *testing.T) {
	result := langInstruction("ko")
	if result != "Respond in language: ko" {
		t.Errorf("langInstruction('ko') = %q, want %q", result, "Respond in language: ko")
	}
}
