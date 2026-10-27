package cli

import (
	"context"
	"encoding/json"
	"errors"
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
	// onRateLimit reports a wait for the rate limit to reset, so a watch
	// that pauses for minutes does not look hung.
	onRateLimit func(reset time.Time)
}

type actionsClient interface {
	ListWorkflowRuns(context.Context, string, string, string, string) ([]ghapi.WorkflowRun, error)
	GetWorkflowRun(context.Context, string, string, int64) (ghapi.WorkflowRun, error)
	ListWorkflowRunsForSHA(context.Context, string, string, string, string) ([]ghapi.WorkflowRun, string, bool, error)
	GetWorkflowRunConditional(context.Context, string, string, int64, string) (ghapi.WorkflowRun, string, bool, error)
}

func init() {
	actionsCmd := &cobra.Command{Use: "actions", Short: "Inspect GitHub Actions runs"}
	watchCmd := &cobra.Command{
		Use:   "watch",
		Short: "Wait for a GitHub Actions run for the current commit",
		Long: "Waits for a GitHub Actions run without invoking gh.\n\n" +
			"Without options, it resolves the current GitHub remote and HEAD. Set GH_TOKEN or GITHUB_TOKEN. " +
			"gk does not read gh configuration for this command. When several runs match the commit " +
			"(one push can start several workflows, or the same workflow for push and pull_request), " +
			"it waits for all of them and fails if any fails; narrow with --workflow or --run.\n\n" +
			"With --org it instead streams every repository of an organization (or user account): " +
			"repositories are discovered by their newest push, and each push reports ci-expecting, " +
			"ci-start and ci-end per workflow run, or ci-none when no run starts. It runs until " +
			"interrupted and exits 0; failed runs are events, not exit codes. --json (or GK_AGENT) " +
			"emits NDJSON. Pushes are detected from GitHub's pushed_at, so scheduled or manually " +
			"dispatched runs without a push are not reported. Outside a repository with a remote, " +
			"github.owner from config acts as --org when --repo, --sha and --run are not set.",
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
	if opts.org = actionsOrgFallback(cmdCtx(cmd), *cfg, runner, opts); opts.org != "" {
		return runActionsWatchOrg(cmd, &ghapi.Client{Token: token}, opts)
	}
	owner, repo, sha, err := resolveActionsTarget(cmdCtx(cmd), *cfg, runner, opts)
	if err != nil {
		return err
	}
	errOut := cmd.ErrOrStderr()
	opts.onRateLimit = func(reset time.Time) {
		fmt.Fprintf(errOut, "gk actions: GitHub rate limit reached; waiting until %s\n", reset.Local().Format("15:04:05"))
	}
	runs, err := watchActionsRuns(cmdCtx(cmd), &ghapi.Client{Token: token}, owner, repo, sha, opts, waitActionsInterval)
	if err != nil {
		return err
	}
	return emitActionsRuns(cmd, owner, repo, runs)
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

// actionsOrgFallback picks github.owner only where no remote exists, such as
// a workspace directory of clones, so a repository keeps its per-commit watch.
func actionsOrgFallback(ctx context.Context, cfg config.Config, runner git.Runner, opts actionsWatchOptions) string {
	if opts.repo != "" || opts.sha != "" || opts.run > 0 || cfg.GitHub.Owner == "" {
		return ""
	}
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}
	if remoteURL(ctx, runner, remote) != "" {
		return ""
	}
	return cfg.GitHub.Owner
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

// pollActionsRuns refreshes runs in place with one conditional request and
// returns the ETag for the next poll. A watch on --run asks for that run; a
// watch on a commit lists the commit's runs, and asks for a run on its own
// only when it has fallen off that list.
func pollActionsRuns(ctx context.Context, client actionsClient, owner, repo, sha string, opts actionsWatchOptions, runs []ghapi.WorkflowRun, etag string) (string, error) {
	if opts.run > 0 {
		run, newETag, notModified, err := client.GetWorkflowRunConditional(ctx, owner, repo, runs[0].ID, etag)
		if err != nil || notModified {
			return newETag, err
		}
		runs[0] = run
		return newETag, nil
	}
	listed, newETag, notModified, err := client.ListWorkflowRunsForSHA(ctx, owner, repo, sha, etag)
	if err != nil || notModified {
		return newETag, err
	}
	byID := make(map[int64]ghapi.WorkflowRun, len(listed))
	for _, run := range listed {
		byID[run.ID] = run
	}
	for i := range runs {
		if runs[i].Status == "completed" {
			continue
		}
		if run, ok := byID[runs[i].ID]; ok {
			runs[i] = run
			continue
		}
		run, err := client.GetWorkflowRun(ctx, owner, repo, runs[i].ID)
		if err != nil {
			return etag, err
		}
		runs[i] = run
	}
	return newETag, nil
}

// watchActionsRuns waits until every matching run completes. Runs that
// appear after the first listing are not picked up: the set is what GitHub
// had started for the commit when the watch began.
func watchActionsRuns(ctx context.Context, client actionsClient, owner, repo, sha string, opts actionsWatchOptions, wait func(context.Context, time.Duration) error) ([]ghapi.WorkflowRun, error) {
	var runs []ghapi.WorkflowRun
	if opts.run > 0 {
		selected, err := client.GetWorkflowRun(ctx, owner, repo, opts.run)
		if err != nil {
			return nil, err
		}
		runs = []ghapi.WorkflowRun{selected}
	} else {
		listed, err := client.ListWorkflowRuns(ctx, owner, repo, sha, opts.workflow)
		if err != nil {
			return nil, err
		}
		if len(listed) == 0 {
			if opts.workflow != "" {
				return nil, fmt.Errorf("no Actions run for %s at %s with workflow %q", owner+"/"+repo, sha, opts.workflow)
			}
			return nil, fmt.Errorf("no Actions run for %s at %s", owner+"/"+repo, sha)
		}
		runs = listed
	}
	// Each poll is one conditional request: an unchanged answer is a 304,
	// which GitHub does not count against the rate limit. Polling every run
	// separately and unconditionally spent hundreds of requests per watch.
	etag := ""
	delay := opts.interval
	for actionsRunsPending(runs) {
		if err := wait(ctx, delay); err != nil {
			return runs, err
		}
		delay = opts.interval
		newETag, err := pollActionsRuns(ctx, client, owner, repo, sha, opts, runs, etag)
		var rl *ghapi.RateLimitError
		if errors.As(err, &rl) {
			delay = max(time.Until(rl.Reset), opts.interval)
			if opts.onRateLimit != nil {
				opts.onRateLimit(rl.Reset)
			}
			continue
		}
		if err != nil {
			return runs, err
		}
		etag = newETag
	}
	var failed []string
	for _, run := range runs {
		if ciRunFailed(run.Conclusion) {
			failed = append(failed, fmt.Sprintf("%s (run %d) finished with %q: %s", run.Name, run.ID, run.Conclusion, run.HTMLURL))
		}
	}
	if len(failed) > 0 {
		return runs, fmt.Errorf("%d of %d GitHub Actions runs failed: %s", len(failed), len(runs), strings.Join(failed, "; "))
	}
	return runs, nil
}

func actionsRunsPending(runs []ghapi.WorkflowRun) bool {
	for _, run := range runs {
		if run.Status != "completed" {
			return true
		}
	}
	return false
}

// emitActionsRuns keeps "run" for a single match so existing --json readers
// still work; "runs" always lists every watched run.
func emitActionsRuns(cmd *cobra.Command, owner, repo string, runs []ghapi.WorkflowRun) error {
	if JSONOut() {
		out := map[string]any{"repo": owner + "/" + repo, "runs": runs}
		if len(runs) == 1 {
			out["run"] = runs[0]
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
	}
	for _, run := range runs {
		fmt.Fprintf(cmd.OutOrStdout(), "Actions run %s: %s\n%s\n", run.Name, run.Conclusion, run.HTMLURL)
	}
	return nil
}
