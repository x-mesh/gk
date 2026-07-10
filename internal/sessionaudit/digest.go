package sessionaudit

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Digest caps and budgets. The digest is a handoff block a resumed agent reads
// instead of re-running the status/log/diff orientation probes, so every list
// is capped and every command line truncated — the whole payload stays in the
// low-KB range no matter how long the source session ran.
const (
	// digestSchema versions the digest wire shape.
	digestSchema = 1
	// digestMaxRepos caps the repos-touched list (most-active first).
	digestMaxRepos = 5
	// digestMaxBranches caps the branches created/switched-to list.
	digestMaxBranches = 8
	// digestMaxCommits caps the commit-subject list; over the cap the OLDEST
	// subjects drop, since a resumed agent cares about the most recent work.
	digestMaxCommits = 10
	// digestMaxReprobes caps the re-probed collapse-group list.
	digestMaxReprobes = 5
	// digestTruncateLen is the byte budget per command/subject line, mirroring
	// the lean audit output's truncation discipline.
	digestTruncateLen = 120
	// digestUnfinishedWindow is how many trailing distinct turns are inspected
	// for an errored mutating command — the "stopped mid-operation" signal.
	digestUnfinishedWindow = 3
)

// Digest is one agent session's git activity compressed into a resume/handoff
// block: what happened (repos, branches, commits, integration), what may be
// unfinished, and which probe groups the session kept re-running (plus the one
// gk call that collapses each).
type Digest struct {
	Schema int    `json:"schema"`
	File   string `json:"file"`
	Source string `json:"source"` // claude | codex | unknown
	// Turns/Commands say how much was digested: distinct shell turns and the
	// shell commands they ran.
	Turns    int            `json:"turns"`
	Commands int            `json:"commands"`
	Repos    []RepoActivity `json:"repos,omitempty"`
	Branches []string       `json:"branches,omitempty"`
	// Commits carries extracted subject lines in execution order (most recent
	// last); CommitCount also counts commits with no extractable subject
	// (--amend, gk commit's generated messages).
	Commits     []string           `json:"commits,omitempty"`
	CommitCount int                `json:"commit_count,omitempty"`
	Integration *IntegrationDigest `json:"integration,omitempty"`
	Unfinished  *UnfinishedSignal  `json:"unfinished,omitempty"`
	Reprobes    []ReprobeDigest    `json:"reprobes,omitempty"`
}

// RepoActivity is one repo/worktree the session touched, with how many shell
// commands carried that scope.
type RepoActivity struct {
	Path     string `json:"path"`
	Commands int    `json:"commands"`
}

// IntegrationDigest summarizes pull/fetch/merge/rebase/cherry-pick/push
// activity (raw git and the git-kit verbs) and whether any attempt errored —
// the "did this session integrate cleanly" signal a resumer needs first.
type IntegrationDigest struct {
	Attempts  int            `json:"attempts"`
	Errored   int            `json:"errored"`
	Verbs     map[string]int `json:"verbs,omitempty"`
	LastError string         `json:"last_error,omitempty"` // most recent errored attempt, truncated
}

// UnfinishedSignal flags a session that likely stopped mid-operation: an
// errored mutating command in the final turns (a failed push, a merge that
// conflicted) with nothing after it that resolved the state.
type UnfinishedSignal struct {
	Turn    int    `json:"turn"`
	Command string `json:"command"` // truncated
	Reason  string `json:"reason"`
}

// ReprobeDigest is one collapse group the session re-ran across turns, with
// the turns that one gk call would have saved and that call.
type ReprobeDigest struct {
	Group      string `json:"group"`
	TurnsSaved int    `json:"turns_saved"`
	GkCommand  string `json:"gk_command"`
}

// DigestFile reads one session JSONL and compresses its git activity into a
// Digest. The transcript shape is taken from the path when it sits under a
// known session root and sniffed from the content otherwise, so explicit
// paths (copies, fixtures) work anywhere on disk.
func DigestFile(path string) (Digest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Digest{}, err
	}
	source, events := sniffSessionEvents(path, data)
	return buildDigest(path, source, events), nil
}

// NewestSessionFile returns the most recently modified .jsonl session file
// under the default session roots for home — the file `session digest --last`
// picks by default. Note: called from inside a live agent session, that is
// almost always the caller's OWN transcript (it is appended every turn);
// NthNewestSessionFile(home, 2) is the previous session.
func NewestSessionFile(home string) (string, error) {
	return NthNewestSessionFile(home, 1)
}

// NthNewestSessionFile returns the n-th most recently modified .jsonl session
// file (1 = newest) under the default session roots for home. It reuses the
// audit's discovery (collectFiles sorts newest-first) so `session digest
// --last` and `session audit` always agree on the corpus.
func NthNewestSessionFile(home string, n int) (string, error) {
	if home == "" {
		return "", fmt.Errorf("cannot locate session roots: no home directory")
	}
	if n < 1 {
		n = 1
	}
	roots := DefaultPaths(home)
	files, _ := collectFiles(roots, n, time.Time{})
	if len(files) == 0 {
		return "", fmt.Errorf("no .jsonl session files under %s", strings.Join(roots, ", "))
	}
	if len(files) < n {
		return "", fmt.Errorf("only %d .jsonl session file(s) under %s — cannot pick the %d newest",
			len(files), strings.Join(roots, ", "), n)
	}
	return files[n-1].path, nil
}

// sniffSessionEvents resolves the transcript shape and parses its turn events.
// Paths under the known roots decide directly; anything else tries the Claude
// shape first, then Codex — whichever yields events wins.
func sniffSessionEvents(path string, data []byte) (string, []TurnEvent) {
	switch source := sourceForPath(path); source {
	case "claude":
		return source, SessionTurns(data)
	case "codex":
		return source, CodexSessionTurns(data)
	}
	if events := SessionTurns(data); len(events) > 0 {
		return "claude", events
	}
	if events := CodexSessionTurns(data); len(events) > 0 {
		return "codex", events
	}
	return "unknown", nil
}

// digestIntegrationGkVerbs are the git-kit verbs counted as integration
// attempts; land/promote/ship integrate (pull/merge/push) as part of their
// flow. The raw-git side reuses isRawIntegration plus push.
var digestIntegrationGkVerbs = map[string]bool{
	"pull": true, "sync": true, "merge": true, "rebase": true, "push": true,
	"land": true, "promote": true, "ship": true,
}

// digestCommitGkVerbs are the git-kit verbs that create a commit when they
// succeed (their subjects are generated, so they count without a subject).
var digestCommitGkVerbs = map[string]bool{"commit": true, "land": true}

// buildDigest folds parsed turn events into the digest. All extraction reuses
// the audit's segment classifiers (splitShellSegments, gitSubcommand), so
// heredoc bodies never reach the digest — the splitter skips them.
func buildDigest(path, source string, events []TurnEvent) Digest {
	d := Digest{Schema: digestSchema, File: path, Source: source}
	seenTurn := map[int]bool{}
	repoCount := map[string]int{}
	var repoOrder []string
	branchSeen := map[string]bool{}
	var branches, commits []string
	integ := IntegrationDigest{Verbs: map[string]int{}}

	for _, ev := range events {
		d.Commands++
		if !seenTurn[ev.Turn] {
			seenTurn[ev.Turn] = true
			d.Turns++
		}
		if ev.Repo != "" {
			if repoCount[ev.Repo] == 0 {
				repoOrder = append(repoOrder, ev.Repo)
			}
			repoCount[ev.Repo]++
		}
		for _, seg := range classifyCommand(ev.Cmd).Segments {
			var verb string
			var args []string
			var committed bool
			switch seg.Tool {
			case "git":
				subcmd, gitArgs, ok := gitSubcommand(seg.Text)
				if !ok {
					continue
				}
				args = gitArgs
				if b := branchFromGitSegment(subcmd, gitArgs); b != "" && !ev.IsError {
					if !branchSeen[b] {
						branchSeen[b] = true
						branches = append(branches, b)
					}
				}
				committed = subcmd == "commit"
				if isRawIntegration(subcmd) || subcmd == "push" {
					verb = subcmd
				}
			case "git-kit", "gk":
				sub, gkArgs := gitKitSubcommand(seg.Text)
				if sub == "" {
					continue
				}
				args = gkArgs
				if sub == "switch" && !ev.IsError {
					if b := firstOperand(gkArgs); b != "" && !branchSeen[b] {
						branchSeen[b] = true
						branches = append(branches, b)
					}
				}
				committed = digestCommitGkVerbs[sub]
				if digestIntegrationGkVerbs[sub] {
					verb = sub
				}
			default:
				continue
			}
			if committed && !ev.IsError {
				d.CommitCount++
				if subject := commitSubject(args); subject != "" {
					commits = append(commits, subject)
				}
			}
			if verb != "" {
				integ.Attempts++
				integ.Verbs[verb]++
				if ev.IsError {
					integ.Errored++
					integ.LastError = digestLine(seg.Text)
				}
			}
		}
	}

	d.Repos = topRepos(repoOrder, repoCount)
	if len(branches) > digestMaxBranches {
		branches = branches[:digestMaxBranches]
	}
	d.Branches = branches
	if len(commits) > digestMaxCommits {
		commits = commits[len(commits)-digestMaxCommits:]
	}
	d.Commits = commits
	if integ.Attempts > 0 {
		d.Integration = &integ
	}
	d.Unfinished = unfinishedSignal(events)
	d.Reprobes = reprobeDigests(events)
	return d
}

// topRepos orders the touched repos most-active first (first-seen breaks
// ties) and applies the cap.
func topRepos(order []string, counts map[string]int) []RepoActivity {
	if len(order) == 0 {
		return nil
	}
	out := make([]RepoActivity, 0, len(order))
	for _, repo := range order {
		out = append(out, RepoActivity{Path: repo, Commands: counts[repo]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Commands > out[j].Commands })
	if len(out) > digestMaxRepos {
		out = out[:digestMaxRepos]
	}
	return out
}

// branchFromGitSegment extracts the branch a raw checkout/switch segment
// creates or moves to. "" when the segment is not branch movement: the
// `checkout --` restore form (mirroring isRawBranchSwitch), a detached sha
// checkout, or flags only.
func branchFromGitSegment(subcmd string, args []string) string {
	switch subcmd {
	case "checkout":
		if hasArg(args, "--") {
			return ""
		}
	case "switch":
	default:
		return ""
	}
	for i := 0; i < len(args); i++ {
		a := trimShellToken(args[i])
		switch a {
		case "-b", "-B", "-c", "-C", "--create", "--force-create":
			if i+1 < len(args) {
				return trimShellToken(args[i+1])
			}
			return ""
		}
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		if isHexCommitToken(a) {
			return "" // detached checkout of a sha, not a branch
		}
		return a
	}
	return ""
}

// gitKitSubcommand returns the first subcommand token of a git-kit/gk segment
// and the args after it ("" when only flags follow).
func gitKitSubcommand(text string) (string, []string) {
	fields := shellFields(text)
	for i := 0; i < len(fields); i++ {
		tok := trimShellToken(fields[i])
		if tok != "git-kit" && tok != "gk" {
			continue
		}
		rest := fields[i+1:]
		for j := 0; j < len(rest); j++ {
			a := trimShellToken(rest[j])
			if a == "" || strings.HasPrefix(a, "-") {
				continue
			}
			return a, rest[j+1:]
		}
		return "", nil
	}
	return "", nil
}

// firstOperand returns the first non-flag token of args, unquoted.
func firstOperand(args []string) string {
	for _, a := range args {
		a = trimShellToken(a)
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		return a
	}
	return ""
}

// commitSubject extracts the first line of a commit's -m/--message value.
// Agents conventionally write multi-line messages as -m "$(cat <<'EOF' … EOF)";
// there the subject is the first heredoc body line and everything after it —
// the body — never enters the digest.
func commitSubject(args []string) string {
	msg := ""
	for i := 0; i < len(args); i++ {
		a := trimShellToken(args[i])
		if v, ok := strings.CutPrefix(a, "--message="); ok {
			msg = trimShellToken(v)
			break
		}
		if a == "--message" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.HasSuffix(a, "m")) {
			// -m and short clusters ending in it (-am, -sm) take the next arg.
			if i+1 < len(args) {
				msg = trimShellToken(args[i+1])
			}
			break
		}
	}
	if msg == "" {
		return ""
	}
	lines := strings.Split(msg, "\n")
	if len(lines) > 1 && strings.HasPrefix(lines[0], "$(") && strings.Contains(lines[0], "<<") {
		// The $(cat <<'EOF') scaffolding line — the subject is the line after.
		return digestTruncate(lines[1])
	}
	return digestTruncate(lines[0])
}

// digestGkMutatingVerbs are git-kit verbs (beyond the integration set) that
// unambiguously change repo state. The unfinished signal must see a failed
// `git-kit land` exactly like a failed `git push` — the integration counter
// already tracks those errors, and the two views must not disagree. Verbs
// with common read-only forms (worktree list, stash list) stay out: a failed
// probe is not unfinished work.
var digestGkMutatingVerbs = map[string]bool{
	"commit": true, "switch": true, "apply": true, "resolve": true,
	"continue": true, "abort": true, "undo": true, "clean": true,
}

// commandMutatesForDigest extends commandMutates (raw git only) with the
// git-kit/gk mutating verbs, so the unfinished signal covers exactly the
// commands gk tells agents to use.
func commandMutatesForDigest(cmd string) bool {
	if commandMutates(cmd) {
		return true
	}
	for _, seg := range classifyCommand(cmd).Segments {
		if seg.Tool != "git-kit" && seg.Tool != "gk" {
			continue
		}
		if sub, _ := gitKitSubcommand(seg.Text); digestIntegrationGkVerbs[sub] || digestGkMutatingVerbs[sub] {
			return true
		}
	}
	return false
}

// unfinishedSignal reports the "stopped mid-operation" signal: the most recent
// errored mutating command within the session's final digestUnfinishedWindow
// distinct turns, with no later successful mutating command after it. A failure
// early in the session that later work moved past is not a resume signal, and
// neither is a failure whose retry landed — a `git push` that failed and then
// succeeded needs no resuming.
func unfinishedSignal(events []TurnEvent) *UnfinishedSignal {
	maxTurn := -1
	for _, ev := range events {
		if ev.Turn > maxTurn {
			maxTurn = ev.Turn
		}
	}
	var found *UnfinishedSignal
	for _, ev := range events {
		if maxTurn-ev.Turn >= digestUnfinishedWindow {
			continue
		}
		if !commandMutatesForDigest(ev.Cmd) {
			continue
		}
		if ev.IsError {
			found = &UnfinishedSignal{
				Turn:    ev.Turn,
				Command: digestLine(ev.Cmd),
				Reason:  "errored mutating command near the end of the session",
			}
			continue
		}
		// A later successful mutating command resolved the state the failure
		// left behind (the retry landed) — clear the candidate.
		found = nil
	}
	return found
}

// reprobeDigests aggregates the collapse detector's runs per group: how many
// turns the session spent re-running probes one gk call would have answered.
// It reuses the per-turn attribution (DetectCollapsibleRuns → attributeTurns)
// so the digest and the audit's turn metric never disagree.
func reprobeDigests(events []TurnEvent) []ReprobeDigest {
	saved := map[string]int{}
	for _, run := range DetectCollapsibleRuns(events, collapseMaxGap) {
		saved[run.Group] += run.TurnsSaved
	}
	out := make([]ReprobeDigest, 0, len(saved))
	for group, n := range saved {
		out = append(out, ReprobeDigest{Group: group, TurnsSaved: n, GkCommand: gkForGroup[group]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TurnsSaved != out[j].TurnsSaved {
			return out[i].TurnsSaved > out[j].TurnsSaved
		}
		return out[i].Group < out[j].Group
	})
	if len(out) > digestMaxReprobes {
		out = out[:digestMaxReprobes]
	}
	return out
}

// digestLine applies the digest's privacy/size discipline to one command:
// rebuilt from shell segments (so heredoc bodies, which splitShellSegments
// skips, can never leak through) and truncated to the digest line budget.
func digestLine(cmd string) string {
	parts, _ := splitShellSegments(cmd)
	return digestTruncate(strings.Join(parts, "; "))
}

// digestTruncate collapses whitespace and cuts s to digestTruncateLen bytes,
// backing up to a rune boundary so multi-byte subjects never emit invalid
// UTF-8, marking the cut with "…".
func digestTruncate(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= digestTruncateLen {
		return s
	}
	cut := digestTruncateLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
