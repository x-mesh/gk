package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
	"github.com/x-mesh/gk/internal/ui"
)

// prCreateFlags captures the CLI flags for `gk pr create`.
type prCreateFlags struct {
	base     string
	head     string
	title    string
	body     string
	bodyFile string
	draft    bool
	ai       bool
	web      bool
	dryRun   bool
}

func readPRCreateFlags(cmd *cobra.Command) prCreateFlags {
	var f prCreateFlags
	f.base, _ = cmd.Flags().GetString("base")
	f.head, _ = cmd.Flags().GetString("head")
	f.title, _ = cmd.Flags().GetString("title")
	f.body, _ = cmd.Flags().GetString("body")
	f.bodyFile, _ = cmd.Flags().GetString("body-file")
	f.draft, _ = cmd.Flags().GetBool("draft")
	f.ai, _ = cmd.Flags().GetBool("ai")
	f.web, _ = cmd.Flags().GetBool("web")
	f.dryRun, _ = cmd.Flags().GetBool("dry-run")
	return f
}

// prCreateJSON is the `--json` / agent-envelope payload for `gk pr create`.
type prCreateJSON struct {
	Number  int    `json:"number"`
	URL     string `json:"url"`
	Title   string `json:"title"`
	Repo    string `json:"repo"` // "owner/name"
	Head    string `json:"head"`
	Base    string `json:"base"`
	Draft   bool   `json:"draft"`
	Created bool   `json:"created"` // false when an open PR already existed
}

// prPlan is everything resolved before the API call: what would be opened,
// against what, with which text. --dry-run prints it and stops.
type prPlan struct {
	owner string
	repo  string
	head  string // local branch name
	base  string
	title string
	body  string
	draft bool
	// commits are the subjects in base..head, most recent first — shown by
	// --dry-run and used to derive a title/body when none was given.
	commits []string
}

func runPRCreate(cmd *cobra.Command, _ []string) error {
	ctx := cmdCtx(cmd)
	flags := readPRCreateFlags(cmd)

	cfg, err := config.Load(cmd.Flags())
	if err != nil {
		return fmt.Errorf("pr create: load config: %w", err)
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}

	// The token gates everything else, so fail on it first: every later step
	// (branch resolution, diff, AI draft) is wasted work without one.
	token := ghapi.ResolveToken()
	if token == "" && !flags.dryRun {
		return WithRemedy(
			errors.New("pr create: opening a pull request needs a GitHub token"),
			"set GH_TOKEN / GITHUB_TOKEN, or run 'gh auth login' — gk reads gh's stored token directly",
			errRemedy{Command: "gh auth login", Safety: "safe"},
		)
	}
	client := &ghapi.Client{Token: token}

	plan, err := buildPRPlan(ctx, cmd, cfg, runner, flags)
	if err != nil {
		return err
	}

	if flags.dryRun {
		return emitPRPlan(cmd.OutOrStdout(), plan)
	}

	stop := ui.StartBubbleSpinner(fmt.Sprintf("opening PR %s → %s", plan.head, plan.base))
	pr, err := client.CreatePullRequest(ctx, plan.owner, plan.repo, ghapi.NewPullRequest{
		Title: plan.title,
		Body:  plan.body,
		Head:  plan.head,
		Base:  plan.base,
		Draft: plan.draft,
	})
	stop()

	created := true
	if errors.Is(err, ghapi.ErrPRAlreadyExists) {
		// Recoverable: report the PR that already covers this branch rather
		// than the collision. A second `gk pr create` after a follow-up push
		// is a normal thing to type.
		existing, found, findErr := client.FindOpenPR(ctx, plan.owner, plan.repo, plan.owner+":"+plan.head, plan.base)
		if findErr != nil || !found {
			return WithHint(
				fmt.Errorf("pr create: an open pull request already exists for %s", plan.head),
				"open it with 'gk pr --mine', or close it before opening a new one",
			)
		}
		pr, created = existing, false
	} else if err != nil {
		return decoratePRCreateError(err, plan)
	}

	return emitPRCreateResult(cmd, plan, pr, created, flags.web)
}

// buildPRPlan resolves branch, base, and PR text — everything the API call
// needs — without touching the network beyond git's own remote refs.
func buildPRPlan(ctx context.Context, cmd *cobra.Command, cfg *config.Config, runner git.Runner, flags prCreateFlags) (prPlan, error) {
	owner, repo, err := currentRepoSlug(ctx, *cfg, runner)
	if err != nil {
		return prPlan{}, fmt.Errorf("pr create: %w", err)
	}

	client := git.NewClient(runner)

	head := flags.head
	if head == "" {
		head, err = client.CurrentBranch(ctx)
		if errors.Is(err, git.ErrDetachedHEAD) {
			return prPlan{}, WithHint(
				errors.New("pr create: HEAD is detached — a pull request needs a branch"),
				"create one with 'gk branch new <name>', or pass --head <branch>",
			)
		}
		if err != nil {
			return prPlan{}, fmt.Errorf("pr create: current branch: %w", err)
		}
	}

	base := flags.base
	if base == "" {
		base = cfg.BaseBranch
	}
	if base == "" {
		remote := cfg.Remote
		if remote == "" {
			remote = "origin"
		}
		base, err = client.DefaultBranch(ctx, remote)
		if err != nil {
			return prPlan{}, WithHint(
				fmt.Errorf("pr create: %w", err),
				"pass --base <branch>, or set base_branch in .gk.yaml",
			)
		}
	}
	if head == base {
		return prPlan{}, WithHint(
			fmt.Errorf("pr create: head and base are the same branch (%s)", head),
			"switch to your feature branch first, or pass --base <other-branch>",
		)
	}

	plan := prPlan{owner: owner, repo: repo, head: head, base: base, draft: flags.draft}

	plan.commits, err = prCommitSubjects(ctx, runner, base, head)
	if err != nil {
		return prPlan{}, err
	}
	if len(plan.commits) == 0 {
		return prPlan{}, WithHint(
			fmt.Errorf("pr create: %s has no commits ahead of %s — nothing to review", head, base),
			"commit your work first ('gk commit'), or pick another base with --base",
		)
	}

	// GitHub resolves head against what it has, so an unpushed branch fails
	// with a confusing 422. Check locally where the fix is still obvious.
	if err := requirePushedHead(ctx, runner, *cfg, head); err != nil {
		return prPlan{}, err
	}

	plan.title, plan.body, err = resolvePRText(ctx, cmd, cfg, runner, flags, plan)
	if err != nil {
		return prPlan{}, err
	}
	return plan, nil
}

// prCommitSubjects lists the commit subjects in base..head, most recent first.
func prCommitSubjects(ctx context.Context, runner git.Runner, base, head string) ([]string, error) {
	out, stderr, err := runner.Run(ctx, "log", "--format=%s", base+".."+head)
	if err != nil {
		return nil, fmt.Errorf("pr create: log %s..%s: %s: %w", base, head, strings.TrimSpace(string(stderr)), err)
	}
	var subjects []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			subjects = append(subjects, line)
		}
	}
	return subjects, nil
}

// requirePushedHead blocks unless the local branch and its remote counterpart
// are the same commit.
//
// Both directions matter, because the PR's contents come from the remote
// while its title and body were computed from the local branch:
//
//   - local ahead — the PR would omit work the user just wrote.
//   - remote ahead — the PR would carry commits the title and body never saw.
//
// It stops rather than syncing on the user's behalf: `gk push` carries the
// secret scan and the protected-branch guards, and a PR command that quietly
// reimplements a push would drop both.
func requirePushedHead(ctx context.Context, runner git.Runner, cfg config.Config, head string) error {
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}
	remoteRef := remote + "/" + head

	if _, _, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", "refs/remotes/"+remoteRef); err != nil {
		return WithBlocked(
			fmt.Errorf("pr create: %s is not on %s yet", head, remote),
			"branch_not_pushed",
			"push the branch first — GitHub can only open a PR for a branch it can see",
			errRemedy{Command: "gk push", Safety: "safe"},
		)
	}

	out, _, err := runner.Run(ctx, "rev-list", "--left-right", "--count", remoteRef+"..."+head)
	if err != nil {
		return nil // best-effort: a counting failure must not block the PR
	}
	behind, ahead := parseLeftRightCount(string(out))

	if ahead > 0 {
		return WithBlocked(
			fmt.Errorf("pr create: %s has %d commit(s) not pushed to %s", head, ahead, remote),
			"branch_ahead_remote",
			"push them first, or the PR will omit your latest work",
			errRemedy{Command: "gk push", Safety: "safe"},
		)
	}
	if behind > 0 {
		return WithBlocked(
			fmt.Errorf("pr create: %s is %d commit(s) behind %s", head, behind, remoteRef),
			"branch_behind_remote",
			"pull first — the PR would include commits your title and body were not written from",
			errRemedy{Command: "gk pull", Safety: "safe"},
		)
	}
	return nil
}

// parseLeftRightCount reads `rev-list --left-right --count A...B` output,
// which is a tab-separated "left right" pair: commits only in A, then commits
// only in B. Unparsable output counts as zero on both sides — the caller
// treats a failed measurement as "no objection", not as a block.
func parseLeftRightCount(out string) (left, right int) {
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return 0, 0
	}
	left, _ = strconv.Atoi(fields[0])
	right, _ = strconv.Atoi(fields[1])
	return left, right
}

// resolvePRText settles the title and body.
//
// Title priority: --title > the single commit's subject > the branch name
// rendered as a Conventional Commit header. Body priority: --body-file >
// --body > an AI draft (--ai) > the commit list. The defaults mirror what a
// human types by hand for the common cases, and every one of them is
// overridable, so nothing here is a guess the user is stuck with.
func resolvePRText(ctx context.Context, cmd *cobra.Command, cfg *config.Config, runner git.Runner, flags prCreateFlags, plan prPlan) (title, body string, err error) {
	title = flags.title
	if title == "" {
		if len(plan.commits) == 1 {
			title = plan.commits[0]
		} else {
			title = titleFromBranch(plan.head)
		}
	}

	switch {
	case flags.bodyFile != "":
		raw, rErr := readPRBodyFile(cmd, flags.bodyFile)
		if rErr != nil {
			return "", "", fmt.Errorf("pr create: --body-file: %w", rErr)
		}
		body = raw
	case flags.body != "":
		body = flags.body
	case flags.ai:
		body, err = draftPRBodyWithAI(ctx, cmd, cfg, runner, plan.base, plan.head)
		if err != nil {
			return "", "", err
		}
	default:
		body = bodyFromCommits(plan.commits)
	}
	return title, strings.TrimSpace(body), nil
}

// titleFromBranch renders a branch name as a PR title: a Conventional Commit
// prefix is kept as one ("feat/pr-create" → "feat: pr create"), anything else
// just loses its separators. Used only when a branch carries several commits
// and the user named no title.
func titleFromBranch(branch string) string {
	name := branch
	prefix := ""
	if i := strings.Index(branch, "/"); i > 0 && isConventionalType(branch[:i]) {
		prefix, name = branch[:i], branch[i+1:]
	}
	name = strings.NewReplacer("-", " ", "_", " ", "/", " ").Replace(name)
	name = strings.TrimSpace(name)
	if name == "" {
		return branch
	}
	if prefix != "" {
		return prefix + ": " + name
	}
	return name
}

// isConventionalType reports whether s is a Conventional Commit type, so a
// branch prefix that is one survives into the title as a type.
func isConventionalType(s string) bool {
	switch s {
	case "feat", "fix", "chore", "docs", "refactor", "test", "perf", "build", "ci", "style", "revert":
		return true
	}
	return false
}

// bodyFromCommits is the no-flags default body: the commit subjects as a
// list, so a reviewer sees the shape of the branch without an AI call.
func bodyFromCommits(commits []string) string {
	if len(commits) == 1 {
		return ""
	}
	var b strings.Builder
	for _, c := range commits {
		b.WriteString("- ")
		b.WriteString(c)
		b.WriteString("\n")
	}
	return b.String()
}

// readPRBodyFile reads the body from a file, or from stdin for "-".
func readPRBodyFile(cmd *cobra.Command, path string) (string, error) {
	if path == "-" {
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// draftPRBodyWithAI reuses the `gk pr new` pipeline, capturing its output
// instead of printing it — the description that command writes to stdout is
// exactly the body this one wants.
//
// base and head are the already-resolved pair, passed explicitly: the AI path
// would otherwise re-detect its own base and summarize the checkout, which is
// the wrong branch whenever --head names another one.
func draftPRBodyWithAI(ctx context.Context, cmd *cobra.Command, cfg *config.Config, runner git.Runner, base, head string) (string, error) {
	prov, err := buildPRProvider(ctx, cfg.AI)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	err = runAIPRCore(ctx, aiPRDeps{
		Runner:   runner,
		Provider: prov,
		Lang:     cfg.AI.Lang,
		BaseCfg:  base,
		Remote:   cfg.Remote,
		Head:     head,
		AI:       cfg.AI,
		Cmd:      cmd,
		Out:      &buf,
		ErrOut:   cmd.ErrOrStderr(),
	}, aiPRFlags{output: "stdout"})
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

// decoratePRCreateError attaches the fix to the API failures whose cause is
// something the user can act on.
func decoratePRCreateError(err error, plan prPlan) error {
	if errors.Is(err, ghapi.ErrNoCommitsBetween) {
		return WithHint(
			fmt.Errorf("pr create: GitHub sees no commits between %s and %s", plan.base, plan.head),
			"push the branch again ('gk push') — GitHub compares what it has, not your local history",
		)
	}
	return WithHint(
		fmt.Errorf("pr create: %w", err),
		"check that your token can write to "+plan.owner+"/"+plan.repo+" (a fine-grained token needs Pull requests: write)",
	)
}

// emitPRPlan renders --dry-run: what would be opened, without opening it.
func emitPRPlan(out io.Writer, plan prPlan) error {
	if JSONOut() {
		return emitAgentResult(out, prCreateJSON{
			Title: plan.title,
			Repo:  plan.owner + "/" + plan.repo,
			Head:  plan.head,
			Base:  plan.base,
			Draft: plan.draft,
		})
	}
	fmt.Fprintf(out, "--- dry-run: the pull request that would be opened ---\n")
	fmt.Fprintf(out, "repo:   %s/%s\n", plan.owner, plan.repo)
	fmt.Fprintf(out, "head:   %s\n", plan.head)
	fmt.Fprintf(out, "base:   %s\n", plan.base)
	fmt.Fprintf(out, "draft:  %v\n", plan.draft)
	fmt.Fprintf(out, "title:  %s\n", plan.title)
	fmt.Fprintf(out, "commits (%d):\n", len(plan.commits))
	for _, c := range plan.commits {
		fmt.Fprintf(out, "  %s\n", c)
	}
	if plan.body != "" {
		fmt.Fprintf(out, "body:\n%s\n", plan.body)
	}
	return nil
}

func emitPRCreateResult(cmd *cobra.Command, plan prPlan, pr ghapi.PullRequest, created, web bool) error {
	out := cmd.OutOrStdout()
	if JSONOut() {
		if err := emitAgentResult(out, prCreateJSON{
			Number:  pr.Number,
			URL:     pr.URL,
			Title:   pr.Title,
			Repo:    plan.owner + "/" + plan.repo,
			Head:    plan.head,
			Base:    plan.base,
			Draft:   pr.Draft,
			Created: created,
		}); err != nil {
			return err
		}
	} else {
		verb := "opened"
		if !created {
			verb = "already open"
		}
		draft := ""
		if pr.Draft {
			draft = " (draft)"
		}
		fmt.Fprintf(out, "PR #%d %s%s: %s → %s\n", pr.Number, verb, draft, plan.head, plan.base)
		fmt.Fprintf(out, "%s\n", pr.URL)
	}

	if web && pr.URL != "" {
		if err := openBrowser(pr.URL); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "pr create: could not open a browser (%v)\n", err)
		}
	}
	return nil
}
