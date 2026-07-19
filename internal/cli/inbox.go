package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
	"github.com/x-mesh/gk/internal/ui"
)

func init() {
	inboxCmd := &cobra.Command{
		Use:   "inbox",
		Short: "Everything on GitHub involving you — open PRs and issues across all repos",
		Long: `Lists every open PR and issue that involves you (involves:@me) — items
you authored, are assigned, are requested to review, or are mentioned on —
across every repository, in a single search.

Requires a token (GH_TOKEN / GITHUB_TOKEN / 'gh auth login') to resolve @me.
Use --pr or --issue to narrow the type, --state open|closed|all, --label,
-q for raw qualifiers, --sort/--limit, and --web/--pick/--links/--url/--json.`,
		Args: cobra.NoArgs,
		RunE: runInbox,
	}
	inboxCmd.Flags().Bool("pr", false, "only pull requests")
	inboxCmd.Flags().Bool("issue", false, "only issues")
	inboxCmd.Flags().String("state", "open", "which items: open | closed | all")
	addGitHubQueryFlags(inboxCmd)
	rootCmd.AddCommand(inboxCmd)
}

func runInbox(cmd *cobra.Command, _ []string) error {
	ctx := cmdCtx(cmd)

	token := ghapi.ResolveToken()
	if token == "" {
		return fmt.Errorf("gk inbox needs a GitHub token to resolve @me — set GH_TOKEN / GITHUB_TOKEN or run 'gh auth login'")
	}
	client := &ghapi.Client{Token: token}

	onlyPR := boolFlag(cmd, "pr")
	onlyIssue := boolFlag(cmd, "issue")
	labels, _ := cmd.Flags().GetStringArray("label")
	query, err := inboxSearchQuery(onlyPR, onlyIssue, stringFlag(cmd, "state"), labels, stringFlag(cmd, "query"))
	if err != nil {
		return err
	}

	if boolFlag(cmd, "web") {
		return openGitHubSearch(cmd, query)
	}

	// Interactive by default in a terminal (same gate as gk pr / gk issue).
	explicitPick := boolFlag(cmd, "pick")
	if shouldRunGHPicker(explicitPick, boolFlag(cmd, "list"), promptAllowed()) {
		cfg, _ := config.Load(cmd.Flags())
		if cfg == nil {
			cfg = &config.Config{}
		}
		f := githubSearchFilters{state: stringFlag(cmd, "state"), labels: labels, raw: stringFlag(cmd, "query")}
		switch {
		case onlyPR:
			f.typeFilter = "is:pr"
		case onlyIssue:
			f.typeFilter = "is:issue"
		}
		runner := &git.ExecRunner{Dir: RepoFlag()}
		return newGHPicker(cmd, client, runner, cfg, onlyPR, "inbox", f).runForEnvironment(ctx, explicitPick)
	}

	stop := ui.StartBubbleSpinner("searching GitHub — involves:@me")
	issues, err := client.SearchIssues(ctx, query, stringFlag(cmd, "sort"), intFlag(cmd, "limit"))
	stop()
	if err != nil {
		return fmt.Errorf("github search: %w", err)
	}
	return emitGitHubList(cmd, "involves:@me", query, issues, boolFlag(cmd, "links"), boolFlag(cmd, "url"))
}

// inboxSearchQuery builds the involves:@me query for `gk inbox`, applying the
// --pr/--issue type narrowing plus any label/raw qualifiers. It errors on the
// mutually-exclusive combination. Split out from runInbox so this validation is
// unit-testable without a token or network.
func inboxSearchQuery(onlyPR, onlyIssue bool, state string, labels []string, raw string) (string, error) {
	if onlyPR && onlyIssue {
		return "", fmt.Errorf("--pr and --issue are mutually exclusive")
	}
	f := githubSearchFilters{state: state, labels: labels, raw: raw}
	switch {
	case onlyPR:
		f.typeFilter = "is:pr"
	case onlyIssue:
		f.typeFilter = "is:issue"
	}
	return buildSearchQuery("involves:@me", f), nil
}
