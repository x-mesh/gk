package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
)

// dashAPIBase points the dashboard client at a test server. Empty uses
// api.github.com.
var dashAPIBase = ""

func init() {
	rootCmd.AddCommand(newDashCmd())
}

func newDashCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dash",
		Short: "Show your PRs and issues with local branch state and merge-conflict forecasts",
		Long: `Shows the open pull requests and issues that involve you, joined with the
local repository: the branch and worktree of each PR, whether the local base
trails the remote, and which open PRs would conflict with each other or with
their base if merged.

PR rows: mine, review (requested from you), cleanup (merged, local branch
left). Issue rows: assigned, mentioned, authored, unassigned (repository
scope only); issues assigned to someone else are counted, not listed. Each
row has a next action, with the gk command to run when there is one.

The scope is the current repository; --org reads a whole org or account,
without the local columns and the forecast. The forecast uses the remote
branches as of the last fetch; --fetch fetches first.

gk dash only reads: it sends GraphQL queries and runs git merge-tree, and it
changes no ref, index, or worktree. The one exception is --fetch, which runs
git fetch and so updates the remote-tracking branches. It needs a token: GH_TOKEN, GITHUB_TOKEN,
or 'gh auth login'.

In a terminal, gk dash opens a screen that reads once; press w to turn
auto-refresh on or off, r to refresh now. --watch starts with auto-refresh
on. Piped, in agent mode, or with --json it prints one snapshot.

The review verdict is GitHub's reviewDecision. When the repository has no
review rule, set github.dash.review_context to a commit status context (for
example a review bot's) to show its state instead.`,
		Args: cobra.MaximumNArgs(1), // permits the `--org acme` space form
		RunE: runDash,
	}
	cmd.Flags().String("org", "", "read the whole org/account instead of the current repo (optional name; defaults to github.owner or origin's owner)")
	cmd.Flags().Lookup("org").NoOptDefVal = orgFlagSentinel
	cmd.Flags().Bool("fetch", false, "fetch the remote before the forecast (no network git call by default)")
	cmd.Flags().Bool("watch", false, "start the screen with auto-refresh on (press w on the screen to toggle it)")
	cmd.Flags().Duration("interval", dashDefaultInterval, "auto-refresh period on the screen, at least 30s")
	return cmd
}

// dashEnv is what one dashboard read needs. collectDash uses it for the first
// read and for every refresh.
type dashEnv struct {
	client        *ghapi.Client
	runner        *git.ExecRunner
	prefix        string
	remote        string
	reviewContext string
	warmCounts    bool
}

func runDash(cmd *cobra.Command, args []string) error {
	ctx := cmdCtx(cmd)
	if len(args) > 0 && !cmd.Flags().Changed("org") {
		return fmt.Errorf("unexpected argument %q — did you mean `--org %s`?", args[0], args[0])
	}
	interval, _ := cmd.Flags().GetDuration("interval")
	if interval < dashMinInterval {
		return fmt.Errorf("--interval %s is below the minimum of %s", interval, dashMinInterval)
	}
	screen := promptAllowed()
	if boolFlag(cmd, "watch") && !screen {
		return errors.New("--watch needs a terminal; without one gk dash prints one snapshot")
	}
	env, err := newDashEnv(ctx, cmd, args)
	if err != nil {
		return err
	}
	if boolFlag(cmd, "fetch") && strings.HasPrefix(env.prefix, "repo:") {
		if _, stderr, err := env.runner.Run(ctx, "fetch", "--quiet", env.remote); err != nil {
			return fmt.Errorf("fetch %s: %w", env.remote, dashGitErr("git fetch", stderr, err))
		}
	}
	if screen {
		return runDashTUI(ctx, env, interval, boolFlag(cmd, "watch"))
	}
	snap, err := collectDash(ctx, env)
	if err != nil {
		return err
	}
	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), snap)
	}
	renderDash(cmd.OutOrStdout(), snap, dashWidth())
	return nil
}

func newDashEnv(ctx context.Context, cmd *cobra.Command, args []string) (dashEnv, error) {
	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return dashEnv{}, fmt.Errorf("load config: %w", err)
	}
	token := ghapi.ResolveToken()
	if token == "" {
		return dashEnv{}, errors.New("gk dash needs a GitHub token: set GH_TOKEN or GITHUB_TOKEN, or run 'gh auth login'")
	}
	env := dashEnv{
		client:        &ghapi.Client{Token: token, APIBase: dashAPIBase},
		runner:        &git.ExecRunner{Dir: RepoFlag()},
		remote:        cfg.Remote,
		reviewContext: cfg.GitHub.Dash.ReviewContext,
		warmCounts:    cfg.GitHub.Counts.WarmOnList,
	}
	if env.remote == "" {
		env.remote = "origin"
	}
	prefix, _, err := resolveGitHubScope(ctx, cmd, args, *cfg, env.runner, env.client)
	if err != nil {
		return dashEnv{}, err
	}
	env.prefix = prefix
	return env, nil
}

// collectDash reads GitHub, then the local repository and the forecast for a
// repo: scope. A failed GitHub read is an error, never an empty snapshot.
func collectDash(ctx context.Context, env dashEnv) (dashSnapshot, error) {
	q := dashQuery(env.prefix)
	q.ReviewContext = env.reviewContext
	res, err := env.client.Dashboard(ctx, q)
	if err != nil {
		return dashSnapshot{}, fmt.Errorf("gk dash: %w", err)
	}
	in := dashInputs{Scope: env.prefix, Now: time.Now(), Res: res, ReviewContext: env.reviewContext}
	if res.HasRepo {
		bases := dashBases(res)
		loc, err := collectDashLocal(ctx, env.runner, env.remote, bases)
		if err != nil {
			res.Notes = append(res.Notes, "local state: "+err.Error())
		} else {
			in.Local = &loc
			in.Forecast = forecastPRConflicts(ctx, env.runner, env.remote, forecastInputs(res), dashForecastLimit, dashForecastBudget)
		}
		if env.warmCounts && res.PRErr == nil {
			warmGitHubCountFromList(ctx, env.runner, strings.TrimPrefix(env.prefix, "repo:"), true, res.RepoOpen.Total)
		}
	}
	return buildDashSnapshot(in), nil
}

// dashBases lists the base branches of the repository's open PRs.
func dashBases(res *ghapi.DashResult) []string {
	set := map[string]bool{}
	for _, p := range res.RepoOpen.Items {
		set[p.BaseRef] = true
	}
	out := make([]string, 0, len(set))
	for b := range set {
		if b != "" {
			out = append(out, b)
		}
	}
	sort.Strings(out)
	return out
}

// dashWidth is the terminal width, or 120 when stdout is not a terminal.
func dashWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 20 {
		return w
	}
	return 120
}
