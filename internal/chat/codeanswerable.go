package chat

import (
	"strings"
	"unicode"
)

// groundingRepromptInstruction is the single nudge the engine injects when the model
// answered a code-answerable question without touching a tool. It names the
// tools explicitly and forbids answering from prior knowledge, so the next
// round actually inspects the repository.
const groundingRepromptInstruction = "You answered from general knowledge without inspecting this repository, but this question is answerable from the code here. Before answering, use your tools (git_grep, file_list, file_read, git_log) to find the actual command / function / file, then answer citing the specific files and lines you saw. Do not answer from prior knowledge."

func groundingReprompt(question string) string {
	return groundingRepromptInstruction + "\n\nOriginal question:\n" + question
}

// Explicit repository references are sufficient by themselves. Generic code
// nouns must be paired with a lookup/deictic signal below, so "여기서 가까운
// 맛집" and "Python에서 파일 열기" stay repo-independent.
var koRepoSignals = []string{
	"이 저장소", "이 레포", "이 코드", "이 프로젝트", "이 파일", "이 함수",
	"저장소", "레포지토리",
}

var koCodeSignals = []string{
	"커맨드", "명령어", "명령", "서브커맨드", "함수", "메서드", "메소드",
	"클래스", "구조체", "인터페이스", "패키지", "모듈", "파일", "디렉토리",
	"구현", "동작", "옵션", "플래그", "설정값", "코드", "로직", "테스트",
	"버그", "예외처리", "리팩터", "리팩토링",
}

var koCommandSignals = []string{"커맨드", "명령어", "명령", "서브커맨드"}

var koLookupSignals = []string{
	"여기서", "여기에", "뭐", "어디", "어느", "언제", "왜", "찾아", "보여", "설명",
}

// English signals are whole words, including common plurals. A code noun must
// be paired with a lookup/deictic word unless the question names the repo
// explicitly. This keeps "which commands are available?" grounded while
// leaving "how do I open a file in Python?" alone.
var enCodeSignals = map[string]bool{
	"command": true, "commands": true, "subcommand": true, "subcommands": true,
	"function": true, "functions": true, "func": true, "method": true, "methods": true,
	"class": true, "classes": true, "struct": true, "structs": true,
	"interface": true, "interfaces": true, "package": true, "packages": true,
	"module": true, "modules": true, "file": true, "files": true,
	"directory": true, "directories": true,
	"implement": true, "implementation": true, "implementations": true,
	"option": true, "options": true, "flag": true, "flags": true,
	"config": true, "configs": true, "logic": true,
	"test": true, "tests": true, "bug": true, "bugs": true,
	"error": true, "errors": true, "refactor": true, "refactors": true,
}

var enRepoSignals = map[string]bool{
	"repo": true, "repos": true, "repository": true, "repositories": true, "codebase": true,
}

var enLookupSignals = map[string]bool{
	"which": true, "where": true, "when": true, "why": true,
	"this": true, "that": true, "these": true, "those": true, "here": true,
}

// IsCodeAnswerable reports whether a chat question should be grounded in the
// current repository's code before it is answered — i.e. whether the code
// here can plausibly answer it. It is a pure, deterministic, allocation-
// light heuristic over the question text alone (no model call): true when
// the question carries a code/repo signal, false otherwise.
//
// Ambiguity fails open toward false (do NOT force investigation), so a
// repo-independent question is never gated. The engine uses this only to
// decide whether to spend a single "use your tools first" reprompt — a nudge,
// not a wall — so a false negative merely leaves today's behavior unchanged,
// while a false positive costs at most one extra, harmless investigation
// round (which the engine caps).
func IsCodeAnswerable(question string) bool {
	q := strings.ToLower(strings.TrimSpace(question))
	if q == "" {
		return false
	}
	if containsAny(q, koRepoSignals) {
		return true
	}
	if containsAny(q, koCodeSignals) && containsAny(q, koLookupSignals) {
		return true
	}
	if strings.Contains(q, "확인") && containsAny(q, koCommandSignals) {
		return true
	}

	words := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	hasCode, hasLookup := false, false
	for _, w := range words {
		if enRepoSignals[w] {
			return true
		}
		hasCode = hasCode || enCodeSignals[w]
		hasLookup = hasLookup || enLookupSignals[w]
	}
	return hasCode && hasLookup
}

func containsAny(s string, signals []string) bool {
	for _, signal := range signals {
		if strings.Contains(s, signal) {
			return true
		}
	}
	return false
}
