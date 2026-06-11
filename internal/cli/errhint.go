package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/easy"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitstate"
	"github.com/x-mesh/gk/internal/ui"
)

// hintError wraps an error with a short "next step" hint rendered after the
// primary error line. The hint is advisory and does not affect errors.Is /
// errors.As chains — the wrapped error is always reachable via Unwrap.
type hintError struct {
	err      error
	hint     string
	remedies []errRemedy
}

// errRemedy is one machine-executable fix attached to an error — the agent
// envelope exposes these as `error.remedies` so tooling runs a command
// instead of interpreting hint prose. Safety marks whether running it can
// lose work ("safe" | "destructive").
type errRemedy struct {
	Command string `json:"command"`
	Safety  string `json:"safety"`
}

func (e *hintError) Error() string { return e.err.Error() }
func (e *hintError) Unwrap() error { return e.err }

// WithHint decorates err with a one-line remediation hint. Passing a nil err
// returns nil. An empty hint is ignored (err is returned unchanged).
func WithHint(err error, hint string) error {
	if err == nil {
		return nil
	}
	if hint = strings.TrimSpace(hint); hint == "" {
		return err
	}
	return &hintError{err: err, hint: hint}
}

// WithRemedy decorates err with a hint plus explicit machine-executable
// remedies. Call sites that know the exact fix command(s) use this instead
// of WithHint so agents get structure, not prose to parse.
func WithRemedy(err error, hint string, remedies ...errRemedy) error {
	if err == nil {
		return nil
	}
	return &hintError{err: err, hint: strings.TrimSpace(hint), remedies: remedies}
}

// RemediesFrom extracts machine-executable remedies from the error chain.
// Explicit WithRemedy entries win; otherwise a "try: <command>" hint (the
// dominant hintCommand convention, ~16 call sites) is promoted into a single
// safe remedy so existing hints feed the agent contract without rewriting
// their call sites.
func RemediesFrom(err error) []errRemedy {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if he, ok := e.(*hintError); ok && len(he.remedies) > 0 {
			return he.remedies
		}
	}
	if h := HintFrom(err); strings.HasPrefix(h, "try: ") {
		return []errRemedy{{Command: strings.TrimPrefix(h, "try: "), Safety: "safe"}}
	}
	return nil
}

// HintFrom walks the error chain and returns the first hint found, or "".
func HintFrom(err error) string {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if he, ok := e.(*hintError); ok && strings.TrimSpace(he.hint) != "" {
			return he.hint
		}
	}
	return ""
}

// FormatError returns the user-facing representation of an error raised by
// cli.Execute. Renders as:
//
//	gk: <error message>          (red)
//	  hint: <hint>                (magenta label, faint body)
//
// When Easy Mode is active and --json is not set, the error is formatted
// with emoji and beginner-friendly language via the EasyFormatter.
func FormatError(err error) string {
	if err == nil {
		return ""
	}

	// git 저장소 밖에서 실행된 명령은 git의 길고 불친절한 raw fatal
	// ("fatal: not a git repository ...")을 던진다. 명령마다 진입부 가드를
	// 다는 대신, 모든 에러가 지나는 이 단일 지점에서 표준 안내로 바꾼다.
	// 명령이 이미 자체 hint를 달아 의도적으로 처리했다면(status/diff 등)
	// 그 메시지를 존중해 건드리지 않는다.
	if HintFrom(err) == "" && isNotAGitRepoError(err) {
		err = WithHint(
			fmt.Errorf("git 저장소가 아닙니다"),
			"git init 으로 저장소를 초기화하거나, 올바른 디렉토리로 이동하세요",
		)
	}

	// Easy Mode branch: use EasyFormatter for friendlier output.
	// Skip when --json is active (Property 9: JSON Mode Bypass).
	if eng := EasyEngine(); eng != nil && eng.IsEnabled() && !JSONOut() {
		// Wire the engine's emoji mapper through so FormatError can
		// prefix ❌ / 💡. Previously this branch built the formatter
		// twice with a nil mapper, defeating the very emoji prefix
		// Easy Mode is supposed to add.
		fmtr := ui.NewEasyFormatter(eng.Emoji(), NoColorFlag())

		// Translate raw error text only. Hints come from the i18n
		// catalog already in user-language form — running them through
		// TranslateTerms a second time mangles the literal commands
		// they exist to suggest (e.g. "→ gk commit" becomes
		// "→ gk 변경사항 저장 (commit)" because \bcommit\b matches the
		// command token in the already-translated string).
		//
		// Error bodies also splice in raw child-process output (git
		// stderr/stdout, lint output). That output is verbatim text the
		// user must read literally — translating git terms inside it
		// corrupts source code and identifiers (the lint incident: a Go
		// struct tag's "branch" json key got replaced with the Korean
		// term). translateErrorBody masks those quoted spans before
		// translation and restores them after.
		translated := translateErrorBody(eng, err.Error())
		hint := selfRewrite(HintFrom(err))
		return fmtr.FormatError(fmt.Errorf("%s", translated), hint)
	}

	prefix := color.New(color.FgRed, color.Bold).Sprint("gk:")
	out := prefix + " " + err.Error()
	if h := selfRewrite(HintFrom(err)); h != "" {
		// Render the remediation as a branded HINT block (same chrome as
		// NOTE advisories) so gk's guidance is visually attributable to gk
		// even when the error line above quotes raw git output.
		out += "\n" + strings.TrimRight(renderAdvisory("hint", strings.Split(h, "\n")), "\n")
	}
	return out
}

// translateErrorBody runs eng.TranslateTerms over s but shields spans that
// quote raw child-process output (git stderr/stdout, lint output) from
// translation. Those spans are verbatim text — translating git terms inside
// them rewrites source code and identifiers the user must read literally (the
// lint incident: a Go struct tag  Branch string json:"branch"  rendered with
// every git term replaced:  작업 갈래 (Branch) ... json:"작업 갈래 (branch)" ).
// Surrounding prose ("not a commit", "failed to push") still translates.
//
// Strategy: find each protected span, replace it with a non-colliding sentinel
// (\x00<idx>\x00 — NUL never appears in error prose), translate the masked
// string, then splice the originals back. Sentinels survive translation
// because no term pattern matches NUL or digits-between-NULs.
func translateErrorBody(eng *easy.Engine, s string) string {
	spans := protectedSpans(s)
	if len(spans) == 0 {
		return eng.TranslateTerms(s)
	}

	var (
		b       strings.Builder
		restore []string
		last    int
	)
	for _, sp := range spans {
		b.WriteString(s[last:sp[0]])
		// \x00<n>\x00 — the index into restore, fenced by NUL on both
		// sides so a translation pass can't fuse it with adjacent text.
		b.WriteString("\x00")
		b.WriteString(strconv.Itoa(len(restore)))
		b.WriteString("\x00")
		restore = append(restore, s[sp[0]:sp[1]])
		last = sp[1]
	}
	b.WriteString(s[last:])

	translated := eng.TranslateTerms(b.String())

	// Restore each masked span. The sentinel text is fixed, so a literal
	// Replace is exact — translation cannot have altered NUL-fenced digits.
	for i, orig := range restore {
		translated = strings.Replace(translated, "\x00"+strconv.Itoa(i)+"\x00", orig, 1)
	}
	return translated
}

// protectedSpans returns the [start,end) byte ranges in s that quote raw
// child-process output and must not be translated. Ranges are returned in
// ascending, non-overlapping order. The recognised quote formats (all
// confirmed against the actual fmt.Errorf call sites):
//
//   - " (stderr=" and " (stdout=" — aicommit wraps git output as
//     "...%w (stderr=%s stdout=%s)" / "(stderr=%s)" (apply.go). The child
//     stderr can itself contain ')' (multi-line git diagnostics), so a
//     balanced-paren match is unsafe; once a "(stderr=" opens, everything to
//     the end of the string is child output (the wrappers append nothing after
//     it). Protect from the marker to EOS.
//   - "exit code N: " — git.ExitError.Error() is
//     "git <args>: exit code %d: <stderr>"; the stderr tail runs to the next
//     " (stderr=" marker (when ExitError is itself wrapped that way) or EOS.
//   - "exit status N: " — exec.ExitError.Error() is "exit status N"; runShellStep
//     appends ": <CombinedOutput>" via "%w: %s", later wrapped by preflight as
//     'preflight failed at step "name": %w'. Everything after that colon is the
//     command's combined output. Protect to EOS.
//
// When several markers appear, the earliest-opening "to EOS" span subsumes the
// rest, so the function returns at most one open-ended span plus any bounded
// "exit code" spans that precede it.
func protectedSpans(s string) [][2]int {
	// Earliest position where an open-ended (to-EOS) protected region starts.
	eos := -1
	if i := indexOfMarker(s, " (stderr="); i >= 0 {
		eos = i
	}
	if i := indexOfMarker(s, " (stdout="); i >= 0 && (eos < 0 || i < eos) {
		eos = i
	}
	if i := matchExitStatus(s); i >= 0 && (eos < 0 || i < eos) {
		eos = i
	}

	var spans [][2]int
	// "exit code N: <stderr>" — bounded by the open-ended span (if any) or EOS.
	if i := matchExitCode(s); i >= 0 {
		end := len(s)
		if eos >= 0 && eos > i {
			end = eos
		}
		// Only keep it if it isn't entirely swallowed by an earlier EOS span.
		if eos < 0 || i < eos {
			spans = append(spans, [2]int{i, end})
		}
	}
	if eos >= 0 {
		spans = append(spans, [2]int{eos, len(s)})
	}

	// Merge / drop overlaps so the spans are clean and ascending.
	return mergeSpans(spans)
}

// indexOfMarker returns the byte index of the first occurrence of marker in s,
// or -1. Used for the literal " (stderr=" / " (stdout=" sentinels.
func indexOfMarker(s, marker string) int { return strings.Index(s, marker) }

// matchExitCode returns the byte index of the start of the stderr tail in a
// git.ExitError string ("...: exit code N: <stderr>") — i.e. the index just
// after "exit code N: " — or -1 if the pattern is absent. The index points at
// the first byte of the child stderr so the protected span excludes the
// "exit code N:" label (still prose-translatable, though it has no git terms).
func matchExitCode(s string) int { return afterColonDigits(s, "exit code ") }

// matchExitStatus is the exec.ExitError analogue ("exit status N: <output>").
func matchExitStatus(s string) int { return afterColonDigits(s, "exit status ") }

// afterColonDigits finds label immediately followed by one or more digits and a
// ": " separator, returning the index just past that separator (start of the
// child output), or -1. Example: afterColonDigits("x: exit code 12: boom",
// "exit code ") == index of "boom".
func afterColonDigits(s, label string) int {
	from := 0
	for {
		i := strings.Index(s[from:], label)
		if i < 0 {
			return -1
		}
		i += from
		j := i + len(label)
		k := j
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		if k > j && strings.HasPrefix(s[k:], ": ") {
			return k + len(": ")
		}
		from = i + len(label)
	}
}

// mergeSpans sorts the input ranges and merges any that overlap or abut,
// yielding a clean ascending, non-overlapping slice.
func mergeSpans(spans [][2]int) [][2]int {
	if len(spans) <= 1 {
		return spans
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	out := spans[:1]
	for _, sp := range spans[1:] {
		last := &out[len(out)-1]
		// Merge only on a strict overlap (start strictly inside the previous
		// span). Abutting spans (sp[0] == last[1]) are kept separate — they
		// are distinct quoted regions (e.g. an "exit code" tail immediately
		// followed by a "(stderr=…)" quote) and masking them independently is
		// correct; fusing them would just be cosmetic.
		if sp[0] < last[1] {
			if sp[1] > last[1] {
				last[1] = sp[1]
			}
			continue
		}
		out = append(out, sp)
	}
	return out
}

// hintCommand is a compact helper so call sites read like:
//
//	return WithHint(err, hintCommand("gk continue"))
func hintCommand(cmd string) string { return fmt.Sprintf("try: %s", cmd) }

// inProgressOp returns the user-facing name of an in-progress git operation
// that `gk continue` / `gk abort` can resolve (rebase / merge / cherry-pick /
// revert). It returns "" for a nil state, StateNone, or StateBisect — the
// operations those two commands do not handle.
func inProgressOp(state *gitstate.State) string {
	if state == nil {
		return ""
	}
	switch state.Kind {
	case gitstate.StateRebaseMerge, gitstate.StateRebaseApply:
		return "rebase"
	case gitstate.StateMerge:
		return "merge"
	case gitstate.StateCherryPick:
		return "cherry-pick"
	case gitstate.StateRevert:
		return "revert"
	default:
		return ""
	}
}

// inProgressHint returns a remediation hint when git is mid-operation
// (rebase / merge / cherry-pick / revert) and that operation is what blocks the
// command the user just ran. It names the operation and points at the two real
// ways out — `gk continue` (finish) or `gk abort` (cancel) — instead of
// `gk switch`, which git refuses while an operation is in progress.
//
// Returns "" when there is no resolvable in-progress operation (see
// inProgressOp): callers should fall back to their default hint.
func inProgressHint(state *gitstate.State) string {
	op := inProgressOp(state)
	if op == "" {
		return ""
	}
	return fmt.Sprintf(
		"%s in progress — finish it with 'gk continue' (after resolving with 'gk resolve') or cancel with 'gk abort'",
		op,
	)
}

// isNotAGitRepoError reports whether err originates from running git outside a
// repository. We check both the wrapped message and the ExitError's stderr so
// the detection survives any chain wrapping done above the runner layer. The
// match is case-insensitive: a hard `fatal: not a git repository` and the
// softer `warning: Not a git repository` that `git diff --no-index` emits are
// both the same "you're not in a repo" condition.
func isNotAGitRepoError(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(strings.ToLower(err.Error()), "not a git repository") {
		return true
	}
	var exitErr *git.ExitError
	if errors.As(err, &exitErr) && strings.Contains(strings.ToLower(exitErr.Stderr), "not a git repository") {
		return true
	}
	return false
}

// ensureGitRepo returns a not-a-git-repo error (which FormatError renders as the
// standard "git 저장소가 아닙니다" guidance) when the working directory is not
// inside a repository. Most commands need no such guard — git's own stderr
// flows up to FormatError untouched. This exists for the few whose first git
// call swallows that stderr behind a sentinel (e.g. DefaultBranch ->
// ErrNoDefaultBranch), which would otherwise surface a misleading "no upstream
// / no base branch" message outside a repo. Call it up front in those commands.
func ensureGitRepo(ctx context.Context, r git.Runner) error {
	_, _, err := r.Run(ctx, "rev-parse", "--git-dir")
	if isNotAGitRepoError(err) {
		return err
	}
	return nil
}
