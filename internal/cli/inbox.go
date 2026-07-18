package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	ghapi "github.com/x-mesh/gk/internal/github"
)

func init() {
	inboxCmd := &cobra.Command{
		Use:   "inbox",
		Short: "Everything on GitHub involving you — open PRs and issues across all repos",
		Long: `Lists every open PR and issue that involves you (involves:@me) — items
you authored, are assigned, are requested to review, or are mentioned on —
across every repository, in a single search.

Requires a token (GH_TOKEN / GITHUB_TOKEN / 'gh auth login') to resolve @me.
Use --pr or --issue to narrow the type, --state open|closed|all, and --json.`,
		Args: cobra.NoArgs,
		RunE: runInbox,
	}
	inboxCmd.Flags().Bool("pr", false, "only pull requests")
	inboxCmd.Flags().Bool("issue", false, "only issues")
	inboxCmd.Flags().String("state", "open", "which items: open | closed | all")
	inboxCmd.Flags().Bool("links", false, "make the PR#/issue# token a clickable terminal hyperlink to its URL")
	rootCmd.AddCommand(inboxCmd)
}

func runInbox(cmd *cobra.Command, _ []string) error {
	ctx := cmdCtx(cmd)

	token := ghapi.ResolveToken()
	if token == "" {
		return fmt.Errorf("gk inbox needs a GitHub token to resolve @me — set GH_TOKEN / GITHUB_TOKEN or run 'gh auth login'")
	}
	client := &ghapi.Client{Token: token}

	onlyPR, _ := cmd.Flags().GetBool("pr")
	onlyIssue, _ := cmd.Flags().GetBool("issue")
	state, _ := cmd.Flags().GetString("state")
	query, err := inboxSearchQuery(onlyPR, onlyIssue, state)
	if err != nil {
		return err
	}

	issues, err := client.SearchIssues(ctx, query)
	if err != nil {
		return fmt.Errorf("github search: %w", err)
	}
	links, _ := cmd.Flags().GetBool("links")
	return emitGitHubList(cmd, "involves:@me", query, issues, links)
}

// inboxSearchQuery builds the involves:@me query for `gk inbox`, applying the
// --pr/--issue type narrowing. It errors on the mutually-exclusive
// combination. Split out from runInbox so this validation is unit-testable
// without a token or network.
func inboxSearchQuery(onlyPR, onlyIssue bool, state string) (string, error) {
	if onlyPR && onlyIssue {
		return "", fmt.Errorf("--pr and --issue are mutually exclusive")
	}
	typeFilter := ""
	switch {
	case onlyPR:
		typeFilter = "is:pr"
	case onlyIssue:
		typeFilter = "is:issue"
	}
	return buildSearchQuery("involves:@me", typeFilter, state, false), nil
}
