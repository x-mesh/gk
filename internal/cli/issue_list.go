package cli

import "github.com/spf13/cobra"

func init() {
	issueCmd := &cobra.Command{
		Use:   "issue",
		Short: "List open issues (current repo, --org, or --mine)",
		Long: `Lists open issues via the GitHub search API.

No flag lists the current repo's issues (owner/repo from origin). --org
lists a whole org/account's issues in one query; --mine restricts to issues
you opened. --state open|closed|all and --json are supported.

Auth comes from GH_TOKEN / GITHUB_TOKEN / a prior 'gh auth login'. Without
a token only public results show, under a lower rate limit.`,
		Args: cobra.MaximumNArgs(1), // permits the `--org acme` space form
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGitHubList(cmd, args, false)
		},
	}
	addGitHubScopeFlags(issueCmd)
	rootCmd.AddCommand(issueCmd)
}
