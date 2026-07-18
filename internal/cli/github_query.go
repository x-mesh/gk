package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
)

// orgFlagSentinel is the value pflag records for a bare `--org` (no value).
// GitHub org names can't contain '@', so this can't collide with a real one;
// the handler reads it as "org scope, name unspecified — fall back to
// config/origin". It also renders readably in --help ([="@configured"]).
const orgFlagSentinel = "@configured"

// addGitHubScopeFlags wires the flags shared by `gk pr` and `gk issue`.
// `--org` takes an optional value: bare `--org` scopes to the configured/
// origin org, `--org acme` (or `--org=acme`) names one explicitly.
func addGitHubScopeFlags(cmd *cobra.Command) {
	cmd.Flags().String("org", "", "search the whole org/account instead of the current repo (optional name; defaults to github.owner or origin's owner)")
	cmd.Flags().Lookup("org").NoOptDefVal = orgFlagSentinel
	cmd.Flags().Bool("mine", false, "only items you authored (author:@me; needs a token)")
	cmd.Flags().Bool("review", false, "only PRs awaiting your review (review-requested:@me; needs a token)")
	cmd.Flags().Bool("assigned", false, "only items assigned to you (assignee:@me; needs a token)")
	cmd.Flags().String("author", "", "only items opened by <user>")
	cmd.Flags().String("assignee", "", "only items assigned to <user>")
	cmd.Flags().String("state", "open", "which items: open | closed | all")
	addGitHubQueryFlags(cmd)
}

// addGitHubQueryFlags wires the query/output flags shared by pr, issue, and
// inbox: label/raw-query/sort/limit filters plus the link/url/web/pick output
// options.
func addGitHubQueryFlags(cmd *cobra.Command) {
	cmd.Flags().StringArray("label", nil, "filter by label (repeatable; matches label:<name>)")
	cmd.Flags().StringP("query", "q", "", "extra raw GitHub search qualifiers, appended verbatim (e.g. is:draft)")
	cmd.Flags().String("sort", "updated", "sort key: updated | created | comments")
	cmd.Flags().Int("limit", 0, "cap the number of results (0 = no cap)")
	cmd.Flags().Bool("links", false, "make the PR#/issue# token a clickable terminal hyperlink to its URL")
	cmd.Flags().Bool("url", false, "show the full item URL as a trailing column (bare URLs that most terminals auto-link)")
	cmd.Flags().Bool("web", false, "open the results in the browser (GitHub search) instead of listing")
	cmd.Flags().Bool("pick", false, "force the interactive picker (the default in a terminal)")
	cmd.Flags().Bool("list", false, "print the static list instead of opening the interactive picker")
}

// readGitHubFilters collects the filter flags into a githubSearchFilters.
// isPR sets the type qualifier; scope-only flags absent on a command (e.g. the
// author/review flags on inbox) resolve to their zero value.
func readGitHubFilters(cmd *cobra.Command, isPR bool) githubSearchFilters {
	f := githubSearchFilters{typeFilter: "is:issue"}
	if isPR {
		f.typeFilter = "is:pr"
	}
	f.state, _ = cmd.Flags().GetString("state")
	f.mine, _ = cmd.Flags().GetBool("mine")
	f.review = boolFlag(cmd, "review")
	f.assigned = boolFlag(cmd, "assigned")
	f.author = stringFlag(cmd, "author")
	f.assignee = stringFlag(cmd, "assignee")
	f.labels, _ = cmd.Flags().GetStringArray("label")
	f.raw = stringFlag(cmd, "query")
	return f
}

// boolFlag / stringFlag read a flag that may not be registered on this command
// (inbox omits the scope-relative filters), returning the zero value if absent.
func boolFlag(cmd *cobra.Command, name string) bool {
	if cmd.Flags().Lookup(name) == nil {
		return false
	}
	v, _ := cmd.Flags().GetBool(name)
	return v
}

func stringFlag(cmd *cobra.Command, name string) string {
	if cmd.Flags().Lookup(name) == nil {
		return ""
	}
	v, _ := cmd.Flags().GetString(name)
	return v
}

func intFlag(cmd *cobra.Command, name string) int {
	if cmd.Flags().Lookup(name) == nil {
		return 0
	}
	v, _ := cmd.Flags().GetInt(name)
	return v
}

// githubItemJSON is the per-row shape emitted by `gk pr/issue/inbox --json`.
type githubItemJSON struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Repo      string   `json:"repo"` // "owner/name"
	Author    string   `json:"author"`
	URL       string   `json:"url"`
	IsPR      bool     `json:"is_pr"`
	Draft     bool     `json:"draft,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

// githubListJSON is the top-level `--json` payload for the listing commands.
type githubListJSON struct {
	Scope string           `json:"scope"` // "repo:owner/name" | "org:acme" | "involves:@me"
	Query string           `json:"query"` // the raw Search API q= sent
	Count int              `json:"count"`
	Items []githubItemJSON `json:"items"`
}

func toGitHubItemJSON(is ghapi.Issue) githubItemJSON {
	item := githubItemJSON{
		Number: is.Number,
		Title:  is.Title,
		State:  is.State,
		Repo:   is.Owner + "/" + is.Repo,
		Author: is.Author,
		URL:    is.URL,
		IsPR:   is.IsPR,
		Draft:  is.Draft,
		Labels: is.Labels,
	}
	if !is.UpdatedAt.IsZero() {
		item.UpdatedAt = is.UpdatedAt.Format("2006-01-02")
	}
	return item
}

// cmdCtx returns the command's context, defaulting to Background — the same
// nil-guard every long-running gk handler uses.
func cmdCtx(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// resolveGitHubScope turns the shared flags into a Search API scope prefix
// (e.g. "repo:x-mesh/gk", "org:acme", "user:octocat") plus a human label.
//
// No --org  → current repo, from origin.
// --org     → org/account scope. Owner priority: explicit --org value >
//
//	positional arg (the `--org acme` space form) > config
//	github.owner > origin's owner. org: vs user: qualifier is
//	chosen via a /users lookup (defaulting to org: on failure).
func resolveGitHubScope(ctx context.Context, cmd *cobra.Command, args []string, cfg config.Config, runner git.Runner, client *ghapi.Client) (prefix, label string, err error) {
	if !cmd.Flags().Changed("org") {
		owner, repo, err := currentRepoSlug(ctx, cfg, runner)
		if err != nil {
			return "", "", err
		}
		s := fmt.Sprintf("repo:%s/%s", owner, repo)
		return s, s, nil
	}

	owner, _ := cmd.Flags().GetString("org")
	if owner == orgFlagSentinel {
		owner = ""
	}
	if owner == "" && len(args) == 1 {
		owner = args[0] // `--org acme` (space form); NoOptDefVal sends acme to args
	}
	if owner == "" {
		owner = cfg.GitHub.Owner
	}
	if owner == "" {
		owner = originOwner(ctx, cfg, runner)
	}
	if owner == "" {
		return "", "", fmt.Errorf("no org to search: pass --org <name>, set github.owner in config, or run inside a repo whose origin is on GitHub")
	}

	qualifier := "org"
	if typ, err := client.OwnerType(ctx, owner); err == nil && typ == "User" {
		qualifier = "user"
	}
	s := qualifier + ":" + owner
	return s, s, nil
}

// currentRepoSlug parses owner/repo from the configured remote's URL,
// erroring clearly when there is no GitHub origin to read.
func currentRepoSlug(ctx context.Context, cfg config.Config, runner git.Runner) (owner, repo string, err error) {
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}
	url := remoteURL(ctx, runner, remote)
	if url == "" {
		return "", "", fmt.Errorf("no %s remote to read — pass --org <name> to search an org instead", remote)
	}
	meta := config.ParseRemoteMeta(url)
	if meta.Owner == "" || meta.Repo == "" {
		return "", "", fmt.Errorf("could not parse owner/repo from %s URL %q", remote, url)
	}
	if !isGitHubHost(meta.Host) {
		return "", "", fmt.Errorf("%s host %q is not github.com — GitHub listing works on github.com only", remote, meta.Host)
	}
	return meta.Owner, meta.Repo, nil
}

// originOwner returns just the owner from the configured remote, or "" when
// it can't be read (used as the last fallback for org scope).
func originOwner(ctx context.Context, cfg config.Config, runner git.Runner) string {
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}
	url := remoteURL(ctx, runner, remote)
	if url == "" {
		return ""
	}
	meta := config.ParseRemoteMeta(url)
	if !isGitHubHost(meta.Host) {
		return ""
	}
	return meta.Owner
}

// githubSearchFilters is the resolved set of qualifiers a listing applies on
// top of its scope prefix. typeFilter is is:pr / is:issue / "" (inbox).
type githubSearchFilters struct {
	typeFilter string
	state      string // open | closed | all
	mine       bool   // author:@me
	review     bool   // review-requested:@me
	assigned   bool   // assignee:@me
	author     string // author:<user>
	assignee   string // assignee:<user>
	labels     []string
	raw        string // -q: appended verbatim
}

// needsToken reports whether any @me-relative qualifier is set — those require
// a token for the search API to resolve @me.
func (f githubSearchFilters) needsToken() bool {
	return f.mine || f.review || f.assigned
}

// isPlainOpen reports whether the filters are just "open items, no narrowing" —
// the only case where the result count equals the repo's true open PR/issue
// count and is safe to warm the count cache from.
func (f githubSearchFilters) isPlainOpen() bool {
	return f.state == "open" && !f.mine && !f.review && !f.assigned &&
		f.author == "" && f.assignee == "" && len(f.labels) == 0 && f.raw == ""
}

// buildSearchQuery assembles the Search API q= from a scope prefix and filters.
func buildSearchQuery(prefix string, f githubSearchFilters) string {
	parts := []string{prefix}
	if f.typeFilter != "" {
		parts = append(parts, f.typeFilter)
	}
	switch f.state {
	case "closed":
		parts = append(parts, "is:closed")
	case "all":
		// no is:open/is:closed qualifier
	default: // "open"
		parts = append(parts, "is:open")
	}
	if f.mine {
		parts = append(parts, "author:@me")
	}
	if f.review {
		parts = append(parts, "review-requested:@me")
	}
	if f.assigned {
		parts = append(parts, "assignee:@me")
	}
	if f.author != "" {
		parts = append(parts, "author:"+f.author)
	}
	if f.assignee != "" {
		parts = append(parts, "assignee:"+f.assignee)
	}
	for _, l := range f.labels {
		parts = append(parts, "label:"+quoteQualifier(l))
	}
	if f.raw != "" {
		parts = append(parts, f.raw)
	}
	return strings.Join(parts, " ")
}

// quoteQualifier wraps a value containing whitespace in double quotes so a
// multi-word label (e.g. "good first issue") stays one qualifier.
func quoteQualifier(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}

// runGitHubList backs `gk pr` and `gk issue`: resolve scope, run one search,
// print. isPR chooses which type the command lists.
func runGitHubList(cmd *cobra.Command, args []string, isPR bool) error {
	ctx := cmdCtx(cmd)

	// A bare positional (`gk pr acme`) without --org is a mistake — the
	// `--org acme` space form sets --org too, so a positional here means the
	// user forgot the flag. Fail with the likely intent instead of silently
	// listing the current repo.
	if len(args) > 0 && !cmd.Flags().Changed("org") {
		return fmt.Errorf("unexpected argument %q — did you mean `--org %s`?", args[0], args[0])
	}

	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}
	token := ghapi.ResolveToken()

	filters := readGitHubFilters(cmd, isPR)
	// @me qualifiers (--mine/--review/--assigned) need a token, or the search
	// API rejects them (422) — guard early with a clear message.
	if filters.needsToken() && token == "" {
		return fmt.Errorf("--mine/--review/--assigned need a GitHub token to resolve @me — set GH_TOKEN / GITHUB_TOKEN or run 'gh auth login'")
	}
	client := &ghapi.Client{Token: token}

	prefix, label, err := resolveGitHubScope(ctx, cmd, args, *cfg, runner, client)
	if err != nil {
		return err
	}
	query := buildSearchQuery(prefix, filters)

	// --web opens the GitHub search in the browser instead of listing.
	if boolFlag(cmd, "web") {
		return openGitHubSearch(cmd, query)
	}

	// Interactive by default in a terminal — the same convention as gk switch /
	// gk worktree / gk clone. promptAllowed() keeps agent/--json/CI/piped runs
	// on the static list; --list forces it off, --pick forces it on.
	explicitPick := boolFlag(cmd, "pick")
	if shouldRunGHPicker(explicitPick, boolFlag(cmd, "list"), promptAllowed()) {
		p := newGHPicker(cmd, client, runner, cfg, isPR, "repo", filters)
		p.setScopeFromPrefix(prefix) // reuse the scope already resolved above
		return p.runForEnvironment(ctx, explicitPick)
	}

	issues, total, err := client.SearchIssuesWithTotal(ctx, query, stringFlag(cmd, "sort"), intFlag(cmd, "limit"))
	if err != nil {
		return fmt.Errorf("github search: %w", err)
	}

	// warm_on_list: a plain current-repo open listing already hit the network,
	// so refresh the count cache from it for free (partial — pr sets the PR
	// count, issue the issue count). Any extra filter (label/author/@me/limit)
	// makes the count non-representative, so skip warming then.
	if cfg.GitHub.Counts.WarmOnList && filters.isPlainOpen() && intFlag(cmd, "limit") == 0 && strings.HasPrefix(label, "repo:") {
		warmGitHubCountFromList(ctx, runner, strings.TrimPrefix(label, "repo:"), isPR, total)
	}

	return emitGitHubList(cmd, label, query, issues, boolFlag(cmd, "links"), boolFlag(cmd, "url"))
}

// emitGitHubList renders the result set as JSON (agent envelope) or a table.
// links makes the ref token a clickable terminal hyperlink; showURL adds a
// trailing full-URL column (text mode only; the URL is always in JSON).
func emitGitHubList(cmd *cobra.Command, scope, query string, issues []ghapi.Issue, links, showURL bool) error {
	out := cmd.OutOrStdout()
	if JSONOut() {
		payload := githubListJSON{Scope: scope, Query: query, Count: len(issues)}
		for _, is := range issues {
			payload.Items = append(payload.Items, toGitHubItemJSON(is))
		}
		return emitAgentResult(out, payload)
	}
	renderGitHubTable(out, scope, issues, links, showURL)
	return nil
}

// maxGitHubTitleWidth caps the title column's display width so the author and
// age columns stay aligned even when a title is very long.
const maxGitHubTitleWidth = 72

// maxGitHubLabelWidth caps the joined labels column so a heavily-labeled item
// cannot blow the row width.
const maxGitHubLabelWidth = 30

// ghCol is one output column: the plain cell text (for width math) paired with
// its colored rendering (which carries invisible ANSI bytes).
type ghCol struct {
	rightAlign bool
	plain      []string
	colored    []string
}

func (c *ghCol) add(plain, colored string) {
	c.plain = append(c.plain, plain)
	c.colored = append(c.colored, colored)
}

func (c *ghCol) width() int {
	max := 0
	for _, s := range c.plain {
		if w := runewidth.StringWidth(s); w > max {
			max = w
		}
	}
	return max
}

// renderGitHubTable prints an aligned, colored list. Alignment is computed on
// the PLAIN text (runewidth, ANSI-blind) and the color is applied afterward, so
// the escape bytes never skew the columns. The repo column is shown only for
// org/inbox scopes that span repositories — it is redundant under a repo scope.
func renderGitHubTable(w io.Writer, scope string, issues []ghapi.Issue, links, showURL bool) {
	if len(issues) == 0 {
		fmt.Fprintln(w, cellFaint(fmt.Sprintf("no matching items · %s", scope)))
		return
	}

	fmt.Fprintf(w, "%s  %s\n\n", cellBold(humanGitHubScope(scope)), cellFaint(fmt.Sprintf("· %d item(s)", len(issues))))

	showRepo := !strings.HasPrefix(scope, "repo:")
	refCol := &ghCol{}
	repoCol := &ghCol{}
	titleCol := &ghCol{}
	labelCol := &ghCol{}
	authorCol := &ghCol{}
	ageCol := &ghCol{}
	urlCol := &ghCol{}
	anyLabels := false

	for _, is := range issues {
		closed := is.State == "closed"

		// One copy-friendly token: <type>#<num> (e.g. PR#4, issue#128). Type
		// keeps its color, the #number is cyan; a closed item dims the whole.
		typ := "issue"
		typColored := cellCyan("issue")
		if is.IsPR {
			if is.Draft {
				typ, typColored = "draft", cellFaint("draft")
			} else {
				typ, typColored = "PR", cellGreen("PR")
			}
		}
		num := "#" + strconv.Itoa(is.Number)
		ref := typ + num
		refColored := typColored + cellCyan(num)
		if closed {
			refColored = cellFaint(ref)
		}
		if links {
			refColored = osc8Link(is.URL, refColored)
		}
		refCol.add(ref, refColored)

		if showRepo {
			repo := is.Owner + "/" + is.Repo
			repoCol.add(repo, cellFaint(repo))
		}

		title := runewidth.Truncate(is.Title, maxGitHubTitleWidth, "…")
		titleColored := title
		if closed {
			titleColored = cellFaint(title)
		}
		titleCol.add(title, titleColored)

		label := runewidth.Truncate(strings.Join(is.Labels, ", "), maxGitHubLabelWidth, "…")
		if label != "" {
			anyLabels = true
		}
		labelColored := cellYellow(label)
		if closed {
			labelColored = cellFaint(label)
		}
		labelCol.add(label, labelColored)

		author := "@" + is.Author
		authorCol.add(author, cellFaint(author))

		age := "-"
		if !is.UpdatedAt.IsZero() {
			age = relativeTime(time.Since(is.UpdatedAt))
		}
		ageCol.add(age, cellFaint(age))

		// Full URL as bare text — most terminals (incl. Warp) auto-link a bare
		// https:// URL, so this works where OSC 8 (--links) does not.
		urlCol.add(is.URL, cellFaint(is.URL))
	}

	cols := []*ghCol{refCol}
	if showRepo {
		cols = append(cols, repoCol)
	}
	cols = append(cols, titleCol)
	if anyLabels { // only reserve the label column when something has labels
		cols = append(cols, labelCol)
	}
	cols = append(cols, authorCol, ageCol)
	if showURL { // trailing (ragged) column, so it never skews the ones before it
		cols = append(cols, urlCol)
	}

	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = c.width()
	}

	for r := range issues {
		var b strings.Builder
		b.WriteString("  ")
		for i, c := range cols {
			b.WriteString(padGitHubCell(c.colored[r], runewidth.StringWidth(c.plain[r]), widths[i], c.rightAlign))
			if i < len(cols)-1 {
				b.WriteString("   ")
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}

// padGitHubCell pads a colored cell with plain spaces so its VISIBLE width
// (plainWidth) reaches width. Padding is added after (left-align) or before
// (right-align) the colored content, never inside it, so ANSI resets stay put.
func padGitHubCell(colored string, plainWidth, width int, rightAlign bool) string {
	if width <= plainWidth {
		return colored
	}
	pad := strings.Repeat(" ", width-plainWidth)
	if rightAlign {
		return pad + colored
	}
	return colored + pad
}

// humanGitHubScope trims the "repo:" prefix for the header (the repo is obvious
// from the single-repo listing); org:/user:/involves: scopes are kept verbatim.
func humanGitHubScope(scope string) string {
	if s, ok := strings.CutPrefix(scope, "repo:"); ok {
		return s
	}
	return scope
}
