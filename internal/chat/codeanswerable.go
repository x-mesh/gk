package chat

import (
	"strings"
	"unicode"
)

// groundingReprompt is the single nudge the engine injects when the model
// answered a code-answerable question without touching a tool. It names the
// tools explicitly and forbids answering from prior knowledge, so the next
// round actually inspects the repository.
const groundingReprompt = "You answered from general knowledge without inspecting this repository, but this question is answerable from the code here. Before answering, use your tools (git_grep, file_list, file_read, git_log) to find the actual command / function / file, then answer citing the specific files and lines you saw. Do not answer from prior knowledge."

// koCodeSignals are substrings that mark a question as answerable from the
// repository the user is standing in: code/repo nouns and the this-repo
// deixis that point an answer at actual files rather than at general
// knowledge. Korean has no word boundaries, so these match as substrings.
// The list is biased toward code/repo vocabulary, NOT question shape
// ("어떻게/뭐야") alone — a bare "오늘 어때" must never trip it.
var koCodeSignals = []string{
	// this-repo deixis
	"이 저장소", "이 레포", "이 코드", "이 프로젝트", "이 파일", "이 함수",
	"여기서", "여기에",
	// code / repo nouns
	"커맨드", "명령어", "명령", "서브커맨드", "함수", "메서드", "메소드",
	"클래스", "구조체", "인터페이스", "패키지", "모듈", "파일", "디렉토리",
	"구현", "동작", "옵션", "플래그", "설정값", "코드", "로직", "테스트",
	"버그", "예외처리", "리팩터", "리팩토링", "저장소", "레포지토리",
}

// enCodeSignals are English code/repo words matched whole-word (not
// substring) so "latest" never trips "test" and "terror" never trips
// "error". Deliberately excludes over-generic terms like "git"/"gk" that
// appear in nearly every question this tool sees.
var enCodeSignals = map[string]bool{
	"command": true, "subcommand": true, "function": true, "func": true,
	"method": true, "class": true, "struct": true, "interface": true,
	"package": true, "module": true, "file": true, "directory": true,
	"implement": true, "implementation": true, "option": true, "flag": true,
	"config": true, "logic": true, "test": true, "bug": true, "error": true,
	"refactor": true, "repo": true, "repository": true, "codebase": true,
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
	// Korean / substring signals first: the script has no word boundaries.
	for _, s := range koCodeSignals {
		if strings.Contains(q, s) {
			return true
		}
	}
	// English signals matched as whole words to avoid substring accidents.
	words := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, w := range words {
		if enCodeSignals[w] {
			return true
		}
	}
	return false
}
