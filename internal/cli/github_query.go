package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

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
	cmd.Flags().String("state", "open", "which items: open | closed | all")
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

// buildSearchQuery assembles the Search API q= from a scope prefix and the
// shared flags. isPR selects is:pr vs is:issue; empty typeFilter (inbox)
// leaves the type unrestricted.
func buildSearchQuery(prefix, typeFilter, state string, mine bool) string {
	parts := []string{prefix}
	if typeFilter != "" {
		parts = append(parts, typeFilter)
	}
	switch state {
	case "closed":
		parts = append(parts, "is:closed")
	case "all":
		// no is:open/is:closed qualifier
	default: // "open"
		parts = append(parts, "is:open")
	}
	if mine {
		parts = append(parts, "author:@me")
	}
	return strings.Join(parts, " ")
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

	// --mine expands to author:@me, which the search API rejects (422) without
	// a token — guard early with a clear message rather than a raw API error.
	mine, _ := cmd.Flags().GetBool("mine")
	if mine && token == "" {
		return fmt.Errorf("--mine needs a GitHub token to resolve @me — set GH_TOKEN / GITHUB_TOKEN or run 'gh auth login'")
	}
	client := &ghapi.Client{Token: token}

	prefix, label, err := resolveGitHubScope(ctx, cmd, args, *cfg, runner, client)
	if err != nil {
		return err
	}

	typeFilter := "is:issue"
	if isPR {
		typeFilter = "is:pr"
	}
	state, _ := cmd.Flags().GetString("state")
	query := buildSearchQuery(prefix, typeFilter, state, mine)

	issues, err := client.SearchIssues(ctx, query)
	if err != nil {
		return fmt.Errorf("github search: %w", err)
	}

	// warm_on_list: a current-repo, open, non-mine listing already hit the
	// network, so refresh the count cache from it for free (partial — pr sets
	// the PR count, issue the issue count). Skips org/--mine/closed scopes,
	// whose counts don't map to "this repo's open PRs/issues".
	if cfg.GitHub.Counts.WarmOnList && !mine && state == "open" && strings.HasPrefix(label, "repo:") {
		warmGitHubCountFromList(ctx, runner, strings.TrimPrefix(label, "repo:"), isPR, len(issues))
	}

	return emitGitHubList(cmd, label, query, issues)
}

// emitGitHubList renders the result set as JSON (agent envelope) or a table.
func emitGitHubList(cmd *cobra.Command, scope, query string, issues []ghapi.Issue) error {
	out := cmd.OutOrStdout()
	if JSONOut() {
		payload := githubListJSON{Scope: scope, Query: query, Count: len(issues)}
		for _, is := range issues {
			payload.Items = append(payload.Items, toGitHubItemJSON(is))
		}
		return emitAgentResult(out, payload)
	}
	renderGitHubTable(out, scope, issues)
	return nil
}

// renderGitHubTable prints a compact aligned table. Rows carry the repo
// column so it reads correctly for org/inbox scopes that span repos.
func renderGitHubTable(w io.Writer, scope string, issues []ghapi.Issue) {
	if len(issues) == 0 {
		fmt.Fprintf(w, "no matching items (%s)\n", scope)
		return
	}
	fmt.Fprintf(w, "%s — %d item(s)\n", scope, len(issues))
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TYPE\tITEM\tTITLE\tAUTHOR\tUPDATED")
	for _, is := range issues {
		typ := "issue"
		if is.IsPR {
			typ = "PR"
			if is.Draft {
				typ = "PR·draft"
			}
		}
		updated := "-"
		if !is.UpdatedAt.IsZero() {
			updated = is.UpdatedAt.Format("2006-01-02")
		}
		fmt.Fprintf(tw, "%s\t%s#%d\t%s\t@%s\t%s\n",
			typ, is.Owner+"/"+is.Repo, is.Number, truncate(is.Title, 60), is.Author, updated)
	}
	_ = tw.Flush()
}

// truncate shortens s to max runes, adding an ellipsis when cut. max < 1 is
// treated as "no limit" so the ellipsis slice can never go negative.
func truncate(s string, max int) string {
	r := []rune(s)
	if max < 1 || len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
