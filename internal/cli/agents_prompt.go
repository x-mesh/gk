package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
)

// --- gitIntentGate: conservative natural-language git-intent detector -------
//
// This gates a UserPromptSubmit prefetch: if it fires, gk pays a handful of
// git subprocess calls before the agent even sees the prompt. A false
// positive just wastes that prefetch; a false negative means the agent's
// first tool call pays the orientation cost instead, which is the normal
// case today. That asymmetry is why every rule below is written to answer
// "am I SURE this is a git action request", not "could this plausibly be
// related to git" — unclear input returns false.

// gitIntentPromptSizeCap bounds gitIntentGate's input. A prompt hook fires on
// every submit, so a large paste/attachment must not turn into unbounded
// scanning; anything past this is assumed to be a paste, not a short git
// request, so it short-circuits to false rather than paying the scan.
const gitIntentPromptSizeCap = 64 * 1024 // bytes

// koreanImperativeEndings are the verb endings that turn a git-action stem
// into an imperative or hortative ("do this now / let's do this") request.
// Longer, more specific endings and the single-syllable "해" all live in one
// list because match order never affects the boolean result here (see the
// call sites) — only presence does.
var koreanImperativeEndings = []string{
	"해서", "해줄래", "해줘", "해봐", "해놔", "해두", "하고", "하자", "할래", "해",
}

// koreanGitStems are git actions spelled as nativized Korean loanwords
// (커밋 = commit, 머지 = merge, ...). A bare stem is just a noun ("the
// commit"), but stem+imperative-ending is unambiguous: nobody attaches "해줘"
// to a word without meaning "please do this."
var koreanGitStems = []string{"커밋", "푸시", "머지", "리베이스", "스태시", "체크아웃"}

// gitVerbActionStems are the Korean verbs that plausibly finish a "git
// <verb>[particle] ___" instrumental construction ("git commit으로
// 정리해" = "clean it up via git commit"). Deliberately narrow: discussion
// verbs like 설명(explain)/비교(compare)/알려주다(tell) are excluded so a
// prompt asking to explain or compare git commands doesn't trip this rule.
var gitVerbActionStems = []string{"정리", "처리", "마무리", "올려", "커밋"}

// branchCreationVerbs are the Korean verb forms that pair with 브랜치
// (branch) to mean "create one" — 파(다) is dev slang for "cut a branch."
// Bare "만들어" is deliberately excluded: it is a mid-sentence non-final form
// as often as an imperative (만들어야/만들어서/만들어볼까 all contain it as a
// substring), so only the forms that are already unambiguous requests
// (만들어줘/만들어줄래/만들자) are listed.
var branchCreationVerbs = []string{"파서", "파줘", "파줄래", "파봐", "파놔", "만들어줘", "만들어줄래", "만들자"}

// prCreationVerbs mirror branchCreationVerbs for "open/create a PR" phrasing.
var prCreationVerbs = []string{"만들어줘", "만들어줄래", "만들자", "열어줘", "올려줘", "생성해줘"}

// englishGitVerbPattern matches a bare English git subcommand at a word
// boundary, so "commit" inside "committed" or "commit" followed by "to"
// (the "committed to quality" / "commit to this plan" idioms) never matches
// this token — those need "t"/"o" glued on with no boundary, or a
// non-Korean-ending follower, either of which the callers already exclude.
var englishGitVerbPattern = regexp.MustCompile(`\b(commit|push|pull|merge|rebase|stash|checkout)\b`)

// prTokenPattern matches a standalone "pr" token (word-bounded so it never
// fires inside "process", "practice", "print", ...).
var prTokenPattern = regexp.MustCompile(`\bpr\b`)

// bareLeadingVerbs is the allow-list for gitIntentGate's "bare command"
// shape ("rebase develop"). Only verbs with no common non-git English
// meaning are included: "push"/"pull"/"checkout"/"merge" are excluded here
// because "push notifications", "pull request status", and "checkout this
// article" are all plausible bare sentence openers in a coding chat, and
// this rule has no other context to rule those out. Those verbs are still
// covered via englishVerbWithKoreanEnding and englishGitVerbInstrumentalAction
// when there's a stronger co-occurring signal.
var bareLeadingVerbs = map[string]bool{"rebase": true, "stash": true}

// identifierLikeToken matches a bare-word/ref-shaped token (branch name,
// path, flag) — used only to keep bareLeadingGitVerb's second-word check
// simple; the real false-positive guard is the token-count cap next to it.
var identifierLikeToken = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*$`)

// fencedCodeBlock strips ```...``` spans before scanning: a prompt that only
// mentions git inside a pasted code block (e.g. quoting a command for
// explanation) is not itself a request to run anything.
var fencedCodeBlock = regexp.MustCompile(`(?s)` + "```.*?```")

// gitIntentGate reports whether prompt reads as a natural-language request
// to perform a git action (commit/push/merge/rebase/resolve conflicts/create
// a branch or PR/stash). It is a pure function so it can be unit tested
// without any git process at all.
func gitIntentGate(prompt string) bool {
	if len(prompt) == 0 || len(prompt) > gitIntentPromptSizeCap {
		return false
	}
	body := strings.ToLower(strings.TrimSpace(fencedCodeBlock.ReplaceAllString(prompt, "")))
	if body == "" {
		return false
	}
	return koreanImperativeGitVerb(body) ||
		conflictResolutionPhrase(body) ||
		branchCreationPhrase(body) ||
		prCreationPhrase(body) ||
		englishVerbWithKoreanEnding(body) ||
		englishGitVerbInstrumentalAction(body) ||
		bareLeadingGitVerb(body)
}

// koreanBoundaryOK reports whether the position `end` in s is a safe place
// for a matched Korean phrase to stop: end of string, or a non-Hangul rune.
// Korean conjugation appends more Hangul syllables onto a stem+ending
// (해줘 -> 해줬어, 만들어 -> 만들어야), so without this check a short phrase
// like "해" or "파서" could silently match as a prefix of a longer,
// differently-meaning conjugation it was never meant to cover.
func koreanBoundaryOK(s string, end int) bool {
	if end >= len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[end:])
	return !unicode.Is(unicode.Hangul, r)
}

// hasKoreanPhrase reports whether phrase occurs in body at a position that
// passes koreanBoundaryOK. It scans every occurrence (not just the first)
// because an early hit might fail the boundary check while a later one
// passes.
func hasKoreanPhrase(body, phrase string) bool {
	start := 0
	for {
		idx := strings.Index(body[start:], phrase)
		if idx == -1 {
			return false
		}
		abs := start + idx
		if koreanBoundaryOK(body, abs+len(phrase)) {
			return true
		}
		start = abs + 1
		if start >= len(body) {
			return false
		}
	}
}

// koreanImperativeGitVerb matches a Korean git-verb stem directly (or
// single-space) followed by an imperative ending — see koreanGitStems and
// koreanImperativeEndings for the rationale. Covers "커밋해줘",
// "커밋하고 푸시해", "머지해줘".
func koreanImperativeGitVerb(body string) bool {
	for _, stem := range koreanGitStems {
		for _, ending := range koreanImperativeEndings {
			if hasKoreanPhrase(body, stem+ending) || hasKoreanPhrase(body, stem+" "+ending) {
				return true
			}
		}
	}
	return false
}

// conflictResolutionPhrase matches "충돌 해결" (resolve conflict) plus an
// imperative ending. "충돌" (merge conflict) is specific enough — unlike the
// generic "갈등" (interpersonal conflict) — that pairing it with 해결+ending
// is a safe, git-specific signal on its own. Covers "충돌 해결해줘".
func conflictResolutionPhrase(body string) bool {
	for _, sep := range []string{"", " "} {
		for _, ending := range koreanImperativeEndings {
			if hasKoreanPhrase(body, "충돌"+sep+"해결"+ending) {
				return true
			}
		}
	}
	return false
}

// branchCreationPhrase requires both "브랜치" (branch) and a creation verb
// form anywhere in the prompt. "브랜치" alone is just the noun (e.g. "이
// 브랜치 전략 설명해줘" — an explanation request), so the verb form is what
// carries the action. Covers "브랜치 새로 파서 작업해".
func branchCreationPhrase(body string) bool {
	if !strings.Contains(body, "브랜치") {
		return false
	}
	for _, v := range branchCreationVerbs {
		if hasKoreanPhrase(body, v) {
			return true
		}
	}
	return false
}

// prCreationPhrase requires a standalone "pr" token plus a creation verb
// form. Covers "PR 만들어줘".
func prCreationPhrase(body string) bool {
	if !prTokenPattern.MatchString(body) {
		return false
	}
	for _, v := range prCreationVerbs {
		if hasKoreanPhrase(body, v) {
			return true
		}
	}
	return false
}

// englishVerbWithKoreanEnding matches an English git verb with a Korean
// imperative ending glued (or single-space attached) directly onto it —
// the mixed-language pattern Korean developers use for loanword verbs
// ("push해", "stash 해놔"). Requiring the ending immediately after the verb
// (rather than anywhere in the prompt) is what keeps "commit to this plan"
// and "we are committed to quality" from matching: "to"/"ted" never satisfy
// koreanImperativeEndings.
func englishVerbWithKoreanEnding(body string) bool {
	for _, loc := range englishGitVerbPattern.FindAllStringIndex(body, -1) {
		rest := strings.TrimPrefix(body[loc[1]:], " ")
		for _, ending := range koreanImperativeEndings {
			if strings.HasPrefix(rest, ending) && koreanBoundaryOK(rest, len(ending)) {
				return true
			}
		}
	}
	return false
}

// skipKoreanParticle advances past any leading run of Hangul runes — used to
// jump over a Korean particle glued directly onto an English word ("commit
// 으로") with no separating space.
func skipKoreanParticle(s string) string {
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !unicode.Is(unicode.Hangul, r) {
			break
		}
		i += size
	}
	return s[i:]
}

// englishGitVerbInstrumentalAction matches the literal phrase "git <verb>"
// followed (optionally through a glued Korean particle like 으로/로) by an
// action stem from gitVerbActionStems. Requiring the literal "git" prefix
// (not just the bare verb) is what keeps this rule from firing on
// discussion prompts like "explain git merge vs git rebase" — those never
// pair "git <verb>" with one of the narrow action stems this rule looks for.
// Covers "git commit으로 정리해".
func englishGitVerbInstrumentalAction(body string) bool {
	start := 0
	for {
		idx := strings.Index(body[start:], "git")
		if idx == -1 {
			return false
		}
		abs := start + idx
		rest := strings.TrimPrefix(body[abs+len("git"):], " ")
		for _, verb := range []string{"commit", "push", "pull", "merge", "rebase", "stash", "checkout"} {
			if !strings.HasPrefix(rest, verb) {
				continue
			}
			after := strings.TrimPrefix(skipKoreanParticle(rest[len(verb):]), " ")
			for _, stem := range gitVerbActionStems {
				if strings.HasPrefix(after, stem) {
					return true
				}
			}
			break
		}
		start = abs + 1
		if start >= len(body) {
			return false
		}
	}
}

// bareLeadingGitVerb matches a short, command-shaped prompt: the first word
// is a git verb with no ambiguous non-git meaning (see bareLeadingVerbs —
// this deliberately excludes push/pull/merge/checkout, which are common
// English words in non-git contexts like "push notifications" or "pull
// request status"), the prompt is at most 3 tokens (so a full sentence like
// "merge conflicts are annoying" or "push notifications broken" never
// qualifies), and any second token looks like a bare identifier/ref rather
// than a stray sentence fragment. Covers "rebase develop".
func bareLeadingGitVerb(body string) bool {
	fields := strings.Fields(body)
	if len(fields) == 0 || len(fields) > 3 {
		return false
	}
	verb := strings.Trim(fields[0], ".,!?:;\"'()")
	if !bareLeadingVerbs[verb] {
		return false
	}
	if len(fields) == 1 {
		return true
	}
	next := strings.Trim(fields[1], ".,!?:;\"'()")
	return identifierLikeToken.MatchString(next)
}

// --- collectPromptPayload: lightweight orientation for the prefetch hook ---
//
// This is deliberately NOT `gk context`: that collector runs a much larger
// set of git calls sized for an interactive/agent-driven query, not for
// something that adds latency to every matching prompt submit. Every probe
// here reuses an existing low-level helper instead of shelling out fresh, and
// degrades independently — a failed probe just drops its field rather than
// failing the whole payload (see the context.go Notes convention this
// mirrors).

// promptPayloadProbeTimeout bounds each individual probe. Probes run
// concurrently (the same fan-out shape as detectPromptInfo), so wall time
// stays close to one probe's latency, not the sum — this timeout is a safety
// net against a huge or lock-contended tree, not the expected case.
const promptPayloadProbeTimeout = 300 * time.Millisecond

// promptPayloadCharCap is collectPromptPayload's output size ceiling (runes).
// The hook injects this text ahead of the agent's first turn, so it must
// stay orientation-sized, never grow into a status dump.
const promptPayloadCharCap = 800

// collectPromptPayload returns a single human-readable orientation line for
// the repo at dir: current branch, upstream + ahead/behind, dirty counts,
// and any paused operation with its resume hint. ok is false only when dir
// isn't a usable git repository at all — every other probe degrades by
// omission, not by failing the call.
func collectPromptPayload(ctx context.Context, dir string) (string, bool) {
	runner := &git.ExecRunner{Dir: dir}

	rootCtx, cancel := context.WithTimeout(ctx, promptPayloadProbeTimeout)
	out, _, err := runner.Run(rootCtx, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	cancel()
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) < 2 {
		return "", false
	}
	commonDir := strings.TrimSpace(lines[1])
	if commonDir == "" {
		return "", false
	}

	var (
		wg                sync.WaitGroup
		branch, upstream  string
		ahead, behind     int
		haveUpstream      bool
		dirty             contextDirtyJSON
		haveDirty         bool
		opLabel, opResume string
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		c, cancel := context.WithTimeout(ctx, promptPayloadProbeTimeout)
		defer cancel()
		out, _, err := runner.Run(c, "branch", "--show-current")
		if err == nil {
			branch = strings.TrimSpace(string(out))
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		c, cancel := context.WithTimeout(ctx, promptPayloadProbeTimeout)
		defer cancel()
		out, _, err := runner.Run(c, "--no-optional-locks", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
		if err != nil {
			return
		}
		upstream = strings.TrimSpace(string(out))
		haveUpstream = true
		ac, acancel := context.WithTimeout(ctx, promptPayloadProbeTimeout)
		defer acancel()
		ahead, behind = detectPromptAheadBehind(ac, runner)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		c, cancel := context.WithTimeout(ctx, promptPayloadProbeTimeout)
		defer cancel()
		scan := scanWorktreeChanges(c, runner, "", false)
		dirty = scan.dirty
		haveDirty = true
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		st, derr := gitstate.DetectFromGitDir(commonDir)
		if derr != nil || st == nil || st.Kind == gitstate.StateNone {
			return
		}
		if label := inProgressOp(st); label != "" {
			opLabel = label
			opResume = selfCmd("continue")
		}
	}()

	wg.Wait()

	repo := repoNameFromCommonDir(commonDir)
	var parts []string
	switch {
	case repo != "" && branch != "":
		parts = append(parts, repo+"/"+branch)
	case branch != "":
		parts = append(parts, branch)
	case repo != "":
		parts = append(parts, repo)
	}
	if haveUpstream && upstream != "" {
		seg := "⇄ " + upstream
		if ahead != 0 || behind != 0 {
			seg += fmt.Sprintf(" ↑%d ↓%d", ahead, behind)
		}
		parts = append(parts, seg)
	}
	if haveDirty && (dirty.Staged+dirty.Unstaged+dirty.Untracked+dirty.Conflicts) > 0 {
		parts = append(parts, fmt.Sprintf("dirty(staged=%d unstaged=%d untracked=%d conflicts=%d)",
			dirty.Staged, dirty.Unstaged, dirty.Untracked, dirty.Conflicts))
	}
	if opLabel != "" {
		parts = append(parts, fmt.Sprintf("%s in progress — resume with `%s`", opLabel, opResume))
	}

	return clip(strings.Join(parts, " · "), promptPayloadCharCap), true
}

// --- runAgentsHookPrompt: the UserPromptSubmit hook handler ---
//
// This is the prefetch companion to runAgentsHookRun (agents_hook.go): where
// that hook steers a raw git call at the moment it runs, this one fires
// earlier — before the agent's first turn — and, for a prompt that reads as a
// git-action request, injects the same orientation an agent would otherwise
// spend a tool call re-deriving. Fail-open throughout, mirroring
// runAgentsHookRun's contract: any problem (unreadable stdin, a blank or
// slash-command prompt, no git intent, an unresolvable cwd, a repo that
// doesn't check out, or the payload already sitting in the transcript) emits
// nothing and returns nil, so it never blocks prompt submission.

// gkPromptHookMarker identifies gk's UserPromptSubmit prefetch injection
// inside the live transcript tail — runAgentsHookPrompt's own dedupe probe,
// the same role hookHintMarker plays for the PreToolUse advisory. It is a
// distinct constant from both gkHookMarker (the settings.json command-line
// marker) and hookHintMarker (the per-kind advisory sentence): this one only
// ever marks "a prefetch payload was already injected this session," and it
// is written verbatim into additionalContext so a plain substring probe over
// the tail is enough to detect it.
const gkPromptHookMarker = "[gk prefetch]"

// runAgentsHookPrompt is the `gk agents hook run --prompt` handler. See the
// section comment above for the fail-open contract.
func runAgentsHookPrompt(cmd *cobra.Command, _ []string) error {
	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return nil
	}
	prompt := gjson.GetBytes(data, "prompt").String()
	if prompt == "" || strings.HasPrefix(strings.TrimSpace(prompt), "/") {
		return nil // blank prompt, or a slash command — never a git-action request
	}
	if !gitIntentGate(prompt) {
		return nil
	}

	// The client field name for the working directory has been observed to
	// vary across Claude Code versions, so try each candidate in turn rather
	// than trusting one name.
	cwd := firstNonEmpty(
		strings.TrimSpace(gjson.GetBytes(data, "cwd").String()),
		strings.TrimSpace(gjson.GetBytes(data, "workspace.current_dir").String()),
		strings.TrimSpace(gjson.GetBytes(data, "workspace_roots.0").String()),
	)
	if cwd == "" {
		return nil
	}

	// Dedupe against a prior fire this session. A missing/unreadable
	// transcript only forfeits the dedupe, never the prefetch itself — one
	// extra injection costs a few tokens, a wrongly suppressed one costs the
	// whole point of this hook.
	if tp := strings.TrimSpace(gjson.GetBytes(data, "transcript_path").String()); tp != "" {
		if tail, terr := tailFile(tp, hookTranscriptTailBytes); terr == nil && bytes.Contains(tail, []byte(gkPromptHookMarker)) {
			return nil
		}
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	payload, ok := collectPromptPayload(ctx, cwd)
	if !ok || payload == "" {
		return nil
	}
	return emitPromptContext(cmd.OutOrStdout(), payload)
}

// emitPromptContext writes the Claude Code UserPromptSubmit payload to
// stdout. Unlike PreToolUse, this event has no permissionDecision field —
// there is nothing to allow or deny, only context to add — so this is a
// separate emit function rather than a variant of emitHookDecision.
func emitPromptContext(w io.Writer, payload string) error {
	type spec struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	}
	out := struct {
		HookSpecificOutput spec `json:"hookSpecificOutput"`
	}{spec{
		HookEventName: "UserPromptSubmit",
		AdditionalContext: fmt.Sprintf(
			"%s %s — this orientation was pre-fetched by gk; use it instead of re-probing.",
			gkPromptHookMarker, payload,
		),
	}}
	return json.NewEncoder(w).Encode(out)
}
