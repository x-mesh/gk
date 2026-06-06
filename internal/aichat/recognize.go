package aichat

import (
	"context"
	"regexp"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// Deterministic recognizers map a small set of unambiguous, high-frequency
// intents to a canonical ExecutionPlan WITHOUT an LLM round-trip. They make
// `gk do` faster and reproducible for the cases they cover, and — crucially —
// always correct (no hallucinated steps). Anything they don't clearly match
// falls through to the AI path. Because every plan is still previewed and
// confirmed before execution, a mistaken match is caught by the user.

// recognizer is one deterministic intent matcher.
type recognizer struct {
	name  string
	match func(ctx context.Context, in normalizedInput, rc *RepoContext, runner git.Runner, lang string) *ExecutionPlan
}

// normalizedInput carries the raw input plus derived forms computed once and
// shared across recognizers. `compact` is the lower-cased input with spaces
// removed: Korean is written with variable spacing ("싶지 않아" vs "싶지않아"),
// so Korean keywords are stored space-free and matched against compact.
type normalizedInput struct {
	raw     string
	lower   string
	compact string
	paths   []string
}

// hasIntent matches English keywords against the lower input and space-free
// Korean keywords against the compact input.
func (in normalizedInput) hasIntent(en, koCompact []string) bool {
	return containsAny(in.lower, en) || containsAny(in.compact, koCompact)
}

// recognizers run in order; the first non-empty plan wins. forget is checked
// before ignore so "히스토리에서도 지워" routes to history erasure, not a
// plain .gitignore edit.
var recognizers = []recognizer{
	{name: "forget-history", match: recognizeForget},
	{name: "ignore-file", match: recognizeIgnore},
	{name: "undo-last-commit", match: recognizeUndoCommit},
}

// recognizeIntent tries each deterministic recognizer and returns the first
// match along with its name. Returns (nil, "") when none apply.
//
// The deterministic path is only for SINGLE-intent requests. If the input also
// asks for another step (push/pull/merge/…), it is a multi-step plan best left
// to the LLM, so the whole fast path bows out — better to defer than to emit a
// partial plan that silently drops a step.
func recognizeIntent(ctx context.Context, input string, rc *RepoContext, runner git.Runner, lang string) (*ExecutionPlan, string) {
	lower := strings.ToLower(input)
	in := normalizedInput{
		raw:     input,
		lower:   lower,
		compact: strings.ReplaceAll(lower, " ", ""),
		paths:   mentionedPaths(input),
	}
	if containsAny(in.lower, multiStepSignals) || containsAny(in.compact, multiStepSignals) {
		return nil, ""
	}
	for _, r := range recognizers {
		if plan := r.match(ctx, in, rc, runner, lang); plan != nil && len(plan.Commands) > 0 {
			return plan, r.name
		}
	}
	return nil, ""
}

// multiStepSignals are actions that, when present alongside a recognized
// intent, mean the request spans more than one step — defer to the LLM.
// Deliberately limited to high-signal verbs that never appear in plain
// ignore/forget/undo phrasing (note: "commit" is intentionally absent — the
// ignore recognizer handles "...and commit" itself via `gk ignore --commit`).
var multiStepSignals = []string{
	"push", "푸시", "푸쉬",
	"pull", "fetch", "페치",
	"merge", "머지", "병합",
	"rebase", "리베이스",
	"clone", "클론", "복제",
	"stash", "스태시", "스태쉬",
	"cherry", "체리",
}

// ── intent keyword sets ──────────────────────────────────────────────
// EN sets match the lower input; KO sets are space-free and match compact.

var ignoreEN = []string{
	"gitignore", ".gitignore", "ignore", "stop tracking", "untrack",
	"don't track", "do not track", "exclude from git", "not commit", "keep out of git",
}
var ignoreKO = []string{
	"추적", "무시", "포함하고싶지않", "포함하기싫", "올리고싶지않", "올리기싫",
	"커밋하고싶지않", "커밋하기싫", "넣고싶지않", "넣기싫", "git에서빼",
}

var historyEN = []string{
	"history", "from history", "all commits", "every commit", "purge", "scrub", "permanently",
}
var historyKO = []string{
	"히스토리", "이력", "기록에서", "과거커밋", "전부지", "완전히지", "흔적", "영구삭제", "영구히",
}

var removeEN = []string{"remove", "delete", "erase", "purge", "scrub"}
var removeKO = []string{"지워", "지운", "지울", "삭제", "없애", "없앤", "없앨"}

var undoEN = []string{"undo last commit", "undo the last commit", "undo commit", "uncommit"}
var undoKO = []string{"마지막커밋취소", "방금커밋취소", "직전커밋취소", "마지막커밋되돌", "커밋취소", "커밋되돌"}

// commitAffirm signals an explicit "...and commit it" follow-on. "커밋해" (with
// 해) is used rather than "커밋하" so the negated "커밋하고 싶지 않" never trips it.
var commitAffirmEN = []string{"and commit", "then commit", "commit it", "commit them", "commit this"}
var commitAffirmKO = []string{"커밋해", "커밋까지", "커밋도", "커밋하자", "커밋한다"}

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// ── recognizers ──────────────────────────────────────────────────────

// recognizeForget matches "erase <path> from history" → gk forget <path>.
func recognizeForget(_ context.Context, in normalizedInput, _ *RepoContext, _ git.Runner, lang string) *ExecutionPlan {
	if !in.hasIntent(historyEN, historyKO) {
		return nil
	}
	// Require a remove/ignore sense too, so a neutral mention of "history"
	// (e.g. "show me the history") never triggers a destructive rewrite.
	if !in.hasIntent(removeEN, removeKO) && !in.hasIntent(ignoreEN, ignoreKO) {
		return nil
	}
	arg := pathArgs(in.paths)
	if arg == "" {
		return nil
	}
	desc := "permanently erase from all git history (filter-repo, hard to undo)"
	if isKoLangAichat(lang) {
		desc = "git 히스토리 전체에서 영구 삭제 (filter-repo, 되돌리기 매우 어려움)"
	}
	return &ExecutionPlan{Commands: []PlannedCommand{{
		Command:     "gk forget " + arg,
		Description: desc,
		Dangerous:   true,
	}}}
}

// recognizeIgnore matches "I don't want this file in git" → gk ignore <path>.
func recognizeIgnore(_ context.Context, in normalizedInput, _ *RepoContext, _ git.Runner, lang string) *ExecutionPlan {
	if !in.hasIntent(ignoreEN, ignoreKO) {
		return nil
	}
	arg := pathArgs(in.paths)
	if arg == "" {
		return nil
	}
	cmd := "gk ignore " + arg
	desc := "add to .gitignore and untrack if tracked (keeps the working file)"
	if isKoLangAichat(lang) {
		desc = ".gitignore에 추가하고, 추적 중이면 추적 해제(작업 파일은 유지)"
	}
	// "...and commit it" is still a single intent — gk ignore can finalize it.
	if in.hasIntent(commitAffirmEN, commitAffirmKO) {
		cmd += " --commit"
		desc = "add to .gitignore, untrack, and commit (keeps the working file)"
		if isKoLangAichat(lang) {
			desc = ".gitignore에 추가하고 추적 해제 후 커밋(작업 파일은 유지)"
		}
	}
	return &ExecutionPlan{Commands: []PlannedCommand{{
		Command:     cmd,
		Description: desc,
		Dangerous:   false,
	}}}
}

// recognizeUndoCommit matches "undo the last commit" → git reset --soft HEAD~1
// (keeps the changes staged). Only fires when a parent commit exists.
func recognizeUndoCommit(ctx context.Context, in normalizedInput, rc *RepoContext, runner git.Runner, lang string) *ExecutionPlan {
	if !in.hasIntent(undoEN, undoKO) {
		return nil
	}
	if rc == nil || !rc.IsRepo {
		return nil
	}
	// Need a parent commit to reset onto.
	if runner != nil {
		if _, _, err := runner.Run(ctx, "rev-parse", "--verify", "HEAD~1"); err != nil {
			return nil
		}
	}
	desc := "undo the last commit, keep the changes staged"
	if isKoLangAichat(lang) {
		desc = "마지막 커밋만 취소하고 변경 내용은 스테이지에 유지"
	}
	return &ExecutionPlan{Commands: []PlannedCommand{{
		Command:     "git reset --soft HEAD~1",
		Description: desc,
		Dangerous:   false,
	}}}
}

// ── path extraction ──────────────────────────────────────────────────

// pathSafeRun matches the leading path-like run of a token. Korean particles
// glued to a filename ("config.json를") are non-matching, so FindString
// returns just the path portion.
var pathSafeRun = regexp.MustCompile(`[A-Za-z0-9._~/\-]+`)

// mentionedPaths extracts candidate file/dir paths from free-form input.
func mentionedPaths(input string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tok := range strings.Fields(input) {
		tok = strings.Trim(tok, "\"'`()[]<>,")
		m := pathSafeRun.FindString(tok)
		if m == "" || !looksLikePath(m) {
			continue
		}
		m = strings.TrimSuffix(m, ".") // trailing sentence period
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// looksLikePath reports whether s resembles a file or directory path: it
// contains a path separator, is a dotfile (.env, .gitignore), or has a
// name.ext shape with a letter-bearing 2-10 char extension.
func looksLikePath(s string) bool {
	if s == "" {
		return false
	}
	if strings.Contains(s, "/") {
		return true
	}
	if strings.HasPrefix(s, ".") && len(s) > 1 && !strings.Contains(s[1:], ".") {
		return true // .env, .gitignore, .DS_Store
	}
	if i := strings.LastIndex(s, "."); i > 0 && i < len(s)-1 {
		ext := s[i+1:]
		if len(ext) >= 2 && len(ext) <= 10 && isWordy(ext) && hasLetter(ext) {
			return true
		}
	}
	return false
}

func isWordy(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			// word character — ok
		default:
			return false
		}
	}
	return true
}

func hasLetter(s string) bool {
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

// pathArgs joins paths into a command-argument string, quoting any that
// contain spaces so the executor's shellSplit keeps them intact.
func pathArgs(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.ContainsAny(p, " \t") {
			quoted = append(quoted, "\""+p+"\"")
		} else {
			quoted = append(quoted, p)
		}
	}
	return strings.Join(quoted, " ")
}

// isKoLangAichat reports whether a BCP-47 short code denotes Korean. (Local to
// the aichat package; cli has its own copy.)
func isKoLangAichat(lang string) bool {
	return strings.HasPrefix(strings.ToLower(lang), "ko")
}
