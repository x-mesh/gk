package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

// --- gk find: one-call history search -----------------------------------------
//
// The gap this closes is not "git log has flags gk lacks" — adding --grep to gk
// log would be a 1:1 swap that saves no turns. It is that an agent hunting for a
// commit does not KNOW which query will hit, so it tries them one at a time:
//
//	git log --all --oneline --grep="OTLP exporter"      # turn 1 — nothing
//	git log --all --oneline | grep -i otlp              # turn 2 — nothing
//	git log --all -p -S "OTLPExporter" -- internal/     # turn 3 — found it
//
// Measured across the local session corpus, that shape is the single largest
// class of raw git left (session audit: raw-history-search). gk find runs all
// three searches at once and says WHICH one matched, so the hunt is one call.

const (
	findModeMessage = "message" // --grep: the commit message says it
	findModeContent = "content" // -S pickaxe: the commit changed an occurrence of it
	findModePath    = "path"    // the commit touched a file whose path matches
)

func init() {
	cmd := &cobra.Command{
		Use:     "find [query]",
		Aliases: []string{"search"},
		Short:   "Search history in one call — commit messages, changed content, and paths at once",
		Long: `Finds the commits behind "when did this get added / who changed this / where
did this file come from", without the try-one-query-at-a-time loop.

The query runs against three things simultaneously, across every ref (not just
the current branch):

  message   the commit message mentions it        (git log --grep)
  content   the commit added or removed it        (git log -S, the "pickaxe")
  path      the commit touched a matching file    (git log -- '*<query>*')

Each result says which of the three matched, so "the message never mentions it
but the code changed here" is visible rather than something you re-derive with a
second query. Commits that match more than one way rank first, then by recency.

  gk find OTLPExporter              # all three searches, every ref
  gk find "fleet watch" --since 2w  # narrow by time
  gk find tildePath --path internal/cli   # narrow to a subtree
  gk find --path docs/commands.md   # no query: the history of a path
  gk find OTLP --json               # agent contract

--no-content skips the pickaxe, which is the slow one on large repos (it must
diff every commit); --no-path and --no-message likewise narrow the fan-out.

What gk find does NOT answer: "what is in B that is not in A" (git log A..B).
That is a range comparison, not a search — see gk log --ahead/--behind --base
for the upstream/base cases.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runFind,
	}
	cmd.Flags().IntP("limit", "n", 20, "최대 결과 개수")
	cmd.Flags().String("since", "", "이 시점 이후의 커밋만 (예: 2w, 2026-06-01)")
	cmd.Flags().String("author", "", "작성자로 좁히기")
	cmd.Flags().String("path", "", "이 경로(pathspec) 안으로 좁히기")
	cmd.Flags().String("ref", "", "이 ref만 검색 (기본: 모든 ref)")
	cmd.Flags().Bool("no-message", false, "커밋 메시지 검색 끄기")
	cmd.Flags().Bool("no-content", false, "내용(pickaxe) 검색 끄기 — 큰 저장소에서 가장 느린 모드")
	cmd.Flags().Bool("no-path", false, "경로 검색 끄기")
	cmd.Flags().Bool("json", false, "기계가 읽는 JSON으로 출력")
	rootCmd.AddCommand(cmd)
}

// findMatch is one commit and the reason(s) it surfaced.
type findMatch struct {
	Hash    string   `json:"hash"`
	Short   string   `json:"short"`
	Date    string   `json:"date"`
	Author  string   `json:"author"`
	Subject string   `json:"subject"`
	Matched []string `json:"matched"` // message | content | path

	when time.Time // ranking only
}

// findResult is the JSON contract. Fields are append-only.
type findResult struct {
	Query   string      `json:"query"`
	Scope   string      `json:"scope"` // "all-refs" or the ref searched
	Modes   []string    `json:"modes"` // which searches actually ran
	Count   int         `json:"count"`
	Matches []findMatch `json:"matches"`
	// Failed records a mode that errored (e.g. a bad --since). The other modes
	// still return: a partial answer beats no answer, but it must not look complete.
	Failed map[string]string `json:"failed,omitempty"`
}

func runFind(cmd *cobra.Command, args []string) error {
	query := ""
	if len(args) == 1 {
		query = args[0]
	}
	limit, _ := cmd.Flags().GetInt("limit")
	since, _ := cmd.Flags().GetString("since")
	author, _ := cmd.Flags().GetString("author")
	path, _ := cmd.Flags().GetString("path")
	ref, _ := cmd.Flags().GetString("ref")
	noMsg, _ := cmd.Flags().GetBool("no-message")
	noContent, _ := cmd.Flags().GetBool("no-content")
	noPath, _ := cmd.Flags().GetBool("no-path")
	asJSON, _ := cmd.Flags().GetBool("json")
	asJSON = asJSON || JSONOut()

	if query == "" && path == "" {
		return fmt.Errorf("gk find: 검색어 또는 --path 중 하나는 필요합니다")
	}
	if limit <= 0 {
		limit = 20
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}
	res := findCommits(cmd.Context(), runner, findQuery{
		query: query, limit: limit, since: since, author: author,
		path: path, ref: ref,
		message: !noMsg && query != "",
		content: !noContent && query != "",
		// With no query, a path search IS the request ("history of this path").
		pathMode: !noPath && (query != "" || path != ""),
	})

	if asJSON {
		return emitAgentResult(cmd.OutOrStdout(), res)
	}
	renderFind(cmd.OutOrStdout(), res)
	return nil
}

// findQuery is one resolved request — the flags after defaulting, so findCommits
// is testable without a cobra command.
type findQuery struct {
	query    string
	limit    int
	since    string
	author   string
	path     string
	ref      string
	message  bool
	content  bool
	pathMode bool
}

// findCommits fans the query out across the enabled modes CONCURRENTLY. The
// whole point of the verb is that the agent does not pay a turn per guess, so it
// must not pay a serial git run per guess either — the pickaxe alone can take
// seconds on a large repo, and running it after the message search would make
// the common case (message hits) wait for the slow one.
func findCommits(ctx context.Context, runner *git.ExecRunner, q findQuery) findResult {
	scope := "all-refs"
	if q.ref != "" {
		scope = q.ref
	}
	res := findResult{Query: q.query, Scope: scope}

	type modeResult struct {
		mode    string
		commits []findMatch
		err     error
	}
	var modes []string
	if q.message {
		modes = append(modes, findModeMessage)
	}
	if q.content {
		modes = append(modes, findModeContent)
	}
	if q.pathMode {
		modes = append(modes, findModePath)
	}
	res.Modes = modes

	out := make([]modeResult, len(modes))
	var wg sync.WaitGroup
	for i, mode := range modes {
		wg.Add(1)
		go func(i int, mode string) {
			defer wg.Done()
			commits, err := runFindMode(ctx, runner, q, mode)
			out[i] = modeResult{mode: mode, commits: commits, err: err}
		}(i, mode)
	}
	wg.Wait()

	// Merge by commit: a commit found by two modes is ONE result that matched
	// twice, and that is the strongest signal there is — it ranks first.
	byHash := map[string]*findMatch{}
	var order []string
	for _, mr := range out {
		if mr.err != nil {
			if res.Failed == nil {
				res.Failed = map[string]string{}
			}
			res.Failed[mr.mode] = mr.err.Error()
			continue
		}
		for _, c := range mr.commits {
			existing, ok := byHash[c.Hash]
			if !ok {
				cp := c
				cp.Matched = []string{mr.mode}
				byHash[c.Hash] = &cp
				order = append(order, c.Hash)
				continue
			}
			existing.Matched = append(existing.Matched, mr.mode)
		}
	}

	matches := make([]findMatch, 0, len(order))
	for _, h := range order {
		m := byHash[h]
		sort.Strings(m.Matched)
		matches = append(matches, *m)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if len(matches[i].Matched) != len(matches[j].Matched) {
			return len(matches[i].Matched) > len(matches[j].Matched) // multi-mode first
		}
		return matches[i].when.After(matches[j].when) // then newest
	})
	if len(matches) > q.limit {
		matches = matches[:q.limit]
	}
	res.Matches = matches
	res.Count = len(matches)
	return res
}

// findFormat is the NUL-separated record the parser reads. NUL cannot appear in
// any of these fields, so a subject containing tabs/pipes/quotes stays intact.
const findFormat = "--format=%H%x00%h%x00%ct%x00%an%x00%s"

// runFindMode runs one search. Every mode shares the narrowing flags, so
// `--since`/`--author`/`--path` mean the same thing whichever way a commit is
// found — otherwise a hit in one mode and a miss in another would be an artifact
// of the mode, not of the history.
func runFindMode(ctx context.Context, runner *git.ExecRunner, q findQuery, mode string) ([]findMatch, error) {
	args := []string{"--no-optional-locks", "log", findFormat}
	if q.ref != "" {
		args = append(args, q.ref)
	} else {
		args = append(args, "--all")
	}
	// A generous per-mode cap: the merge ranks and then trims to --limit, so a
	// mode must be allowed to return more than the final limit or a strong
	// multi-mode hit could be cut before it is ever compared.
	args = append(args, "-n", strconv.Itoa(q.limit*5))
	if q.since != "" {
		args = append(args, "--since="+q.since)
	}
	if q.author != "" {
		args = append(args, "--author="+q.author)
	}

	var pathspec []string
	switch mode {
	case findModeMessage:
		args = append(args, "--regexp-ignore-case", "--grep="+q.query)
	case findModeContent:
		args = append(args, "-S"+q.query)
	case findModePath:
		// With an explicit --path the request is "history of this path". With a
		// query and no --path, the query itself is the path fragment to look for.
		switch {
		case q.path != "" && q.query != "":
			pathspec = []string{q.path}
		case q.path != "":
			pathspec = []string{q.path}
		default:
			pathspec = []string{"*" + q.query + "*"}
		}
	}
	if q.path != "" && mode != findModePath {
		pathspec = []string{q.path}
	}
	if len(pathspec) > 0 {
		args = append(args, "--")
		args = append(args, pathspec...)
	}

	stdout, stderr, err := runner.Run(ctx, args...)
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return parseFindLog(string(stdout)), nil
}

func parseFindLog(out string) []findMatch {
	var matches []findMatch
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x00")
		if len(f) < 5 {
			continue
		}
		m := findMatch{Hash: f[0], Short: f[1], Author: f[3], Subject: f[4]}
		if secs, err := strconv.ParseInt(f[2], 10, 64); err == nil {
			m.when = time.Unix(secs, 0)
			m.Date = m.when.Format("2006-01-02")
		}
		matches = append(matches, m)
	}
	return matches
}

func renderFind(w io.Writer, res findResult) {
	for mode, msg := range res.Failed {
		fmt.Fprintf(w, "! %s 검색 실패: %s\n", mode, msg)
	}
	if res.Count == 0 {
		fmt.Fprintf(w, "no commits match %q (searched: %s)\n", res.Query, strings.Join(res.Modes, ", "))
		return
	}
	for _, m := range res.Matches {
		fmt.Fprintf(w, "%s  %s  %-22s  %s\n",
			m.Short, m.Date, "["+strings.Join(m.Matched, "+")+"]", m.Subject)
	}
	fmt.Fprintf(w, "\n%d commits · searched %s across %s\n",
		res.Count, strings.Join(res.Modes, " + "), res.Scope)
}
