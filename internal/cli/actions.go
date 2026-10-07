package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
)

const defaultActionsWatchInterval = 3 * time.Second

type actionsWatchOptions struct {
	repo     string
	sha      string
	workflow string
	run      int64
	org      string
	interval time.Duration
}

type actionsClient interface {
	ListWorkflowRuns(context.Context, string, string, string, string) ([]ghapi.WorkflowRun, error)
	GetWorkflowRun(context.Context, string, string, int64) (ghapi.WorkflowRun, error)
}

func init() {
	actionsCmd := &cobra.Command{Use: "actions", Short: "Inspect GitHub Actions runs"}
	watchCmd := &cobra.Command{
		Use:   "watch",
		Short: "Wait for a GitHub Actions run for the current commit",
		Long: "Waits for a GitHub Actions run without invoking gh.\n\n" +
			"Without options, it resolves the current GitHub remote and HEAD. Set GH_TOKEN or GITHUB_TOKEN. " +
			"gk does not read gh configuration for this command. If more than one workflow run matches, " +
			"select one with --workflow or --run.\n\n" +
			"With --org it instead streams every repository of an organization (or user account): " +
			"repositories are discovered by their newest push, and each push reports ci-expecting, " +
			"ci-start and ci-end per workflow run, or ci-none when no run starts. It runs until " +
			"interrupted and exits 0; failed runs are events, not exit codes. --json (or GK_AGENT) " +
			"emits NDJSON. Pushes are detected from GitHub's pushed_at, so scheduled or manually " +
			"dispatched runs without a push are not reported.",
		Args: cobra.NoArgs,
		RunE: runActionsWatch,
	}
	addActionsWatchFlags(watchCmd)
	actionsCmd.AddCommand(watchCmd)
	rootCmd.AddCommand(actionsCmd)
}

func addActionsWatchFlags(cmd *cobra.Command) {
	cmd.Flags().String("repo", "", "GitHub repository as owner/repo (default: current remote)")
	cmd.Flags().String("sha", "", "commit SHA (default: HEAD)")
	cmd.Flags().String("workflow", "", "exact workflow display name")
	cmd.Flags().Int64("run", 0, "GitHub Actions run ID")
	cmd.Flags().String("org", "", "stream Actions runs across every repository of this organization or user")
	cmd.Flags().Duration("interval", defaultActionsWatchInterval, "poll interval")
}

func runActionsWatch(cmd *cobra.Command, _ []string) error {
	token := actionsToken()
	if token == "" {
		return fmt.Errorf("gk actions watch needs GH_TOKEN or GITHUB_TOKEN")
	}
	opts, err := readActionsWatchOptions(cmd)
	if err != nil {
		return err
	}
	if opts.org != "" {
		return runActionsWatchOrg(cmd, &ghapi.Client{Token: token}, opts)
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}
	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	owner, repo, sha, err := resolveActionsTarget(cmdCtx(cmd), *cfg, runner, opts)
	if err != nil {
		return err
	}
	run, err := watchActionsRun(cmdCtx(cmd), &ghapi.Client{Token: token}, owner, repo, sha, opts, waitActionsInterval)
	if err != nil {
		return err
	}
	return emitActionsRun(cmd, owner, repo, run)
}

// actionsToken deliberately accepts only explicit environment variables. This
// command must not reuse gh's stored token or execute the gh binary.
func actionsToken() string {
	if token := os.Getenv("GH_TOKEN"); token != "" {
		return token
	}
	return os.Getenv("GITHUB_TOKEN")
}

func readActionsWatchOptions(cmd *cobra.Command) (actionsWatchOptions, error) {
	opts := actionsWatchOptions{interval: defaultActionsWatchInterval}
	var err error
	if opts.repo, err = cmd.Flags().GetString("repo"); err != nil {
		return opts, err
	}
	if opts.sha, err = cmd.Flags().GetString("sha"); err != nil {
		return opts, err
	}
	if opts.workflow, err = cmd.Flags().GetString("workflow"); err != nil {
		return opts, err
	}
	if opts.run, err = cmd.Flags().GetInt64("run"); err != nil {
		return opts, err
	}
	if opts.org, err = cmd.Flags().GetString("org"); err != nil {
		return opts, err
	}
	if opts.interval, err = cmd.Flags().GetDuration("interval"); err != nil {
		return opts, err
	}
	if opts.interval <= 0 {
		return opts, fmt.Errorf("--interval must be greater than zero")
	}
	if opts.run < 0 {
		return opts, fmt.Errorf("--run must be greater than zero")
	}
	if opts.run > 0 && (opts.sha != "" || opts.workflow != "") {
		return opts, fmt.Errorf("--run cannot be combined with --sha or --workflow")
	}
	if opts.org != "" && (opts.repo != "" || opts.sha != "" || opts.run > 0) {
		return opts, fmt.Errorf("--org cannot be combined with --repo, --sha or --run")
	}
	return opts, nil
}

func resolveActionsTarget(ctx context.Context, cfg config.Config, runner git.Runner, opts actionsWatchOptions) (owner, repo, sha string, err error) {
	if opts.repo != "" {
		parts := strings.Split(opts.repo, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", "", fmt.Errorf("--repo must be owner/repo")
		}
		owner, repo = parts[0], parts[1]
	} else {
		owner, repo, err = currentRepoSlug(ctx, cfg, runner)
		if err != nil {
			return "", "", "", err
		}
	}
	if opts.run > 0 {
		return owner, repo, "", nil
	}
	sha = opts.sha
	if sha == "" {
		out, _, runErr := runner.Run(ctx, "rev-parse", "HEAD")
		if runErr != nil {
			return "", "", "", fmt.Errorf("resolve HEAD: %w", runErr)
		}
		sha = strings.TrimSpace(string(out))
	}
	if sha == "" {
		return "", "", "", fmt.Errorf("commit SHA is empty")
	}
	return owner, repo, sha, nil
}

func waitActionsInterval(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func watchActionsRun(ctx context.Context, client actionsClient, owner, repo, sha string, opts actionsWatchOptions, wait func(context.Context, time.Duration) error) (ghapi.WorkflowRun, error) {
	var run ghapi.WorkflowRun
	if opts.run > 0 {
		selected, err := client.GetWorkflowRun(ctx, owner, repo, opts.run)
		if err != nil {
			return run, err
		}
		run = selected
	} else {
		runs, err := client.ListWorkflowRuns(ctx, owner, repo, sha, opts.workflow)
		if err != nil {
			return run, err
		}
		if len(runs) == 0 {
			if opts.workflow != "" {
				return run, fmt.Errorf("no Actions run for %s at %s with workflow %q", owner+"/"+repo, sha, opts.workflow)
			}
			return run, fmt.Errorf("no Actions run for %s at %s", owner+"/"+repo, sha)
		}
		if len(runs) > 1 {
			return run, fmt.Errorf("%d Actions runs match %s at %s; pass --workflow or --run", len(runs), owner+"/"+repo, sha)
		}
		run = runs[0]
	}
	for run.Status != "completed" {
		if err := wait(ctx, opts.interval); err != nil {
			return run, err
		}
		updated, err := client.GetWorkflowRun(ctx, owner, repo, run.ID)
		if err != nil {
			return run, err
		}
		run = updated
	}
	if run.Conclusion != "success" {
		return run, fmt.Errorf("GitHub Actions run %d finished with %q: %s", run.ID, run.Conclusion, run.HTMLURL)
	}
	return run, nil
}

func emitActionsRun(cmd *cobra.Command, owner, repo string, run ghapi.WorkflowRun) error {
	if JSONOut() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"repo": owner + "/" + repo, "run": run})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Actions run succeeded: %s\n%s\n", run.Name, run.HTMLURL)
	return nil
}
