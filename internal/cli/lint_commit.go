package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/commitlint"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:     "lint-commit [<rev-range>]",
		Aliases: []string{"lint-commits", "commitlint"},
		Short:   "Validate commit messages against Conventional Commits",
		Long: "Checks one or more commit messages against Conventional Commits rules.\n\n" +
			"Modes:\n" +
			"  gk lint-commit                    — last commit (HEAD)\n" +
			"  gk lint-commit HEAD~3..HEAD       — rev-range\n" +
			"  gk lint-commit --file <path>      — a single message from file (for commit-msg hook)\n" +
			"  gk lint-commit --staged           — reads .git/COMMIT_EDITMSG",
		RunE: runLintCommit,
	}
	cmd.Flags().String("file", "", "validate a single message stored at <path>")
	cmd.Flags().Bool("staged", false, "validate the prepared .git/COMMIT_EDITMSG")
	rootCmd.AddCommand(cmd)
}

func runLintCommit(cmd *cobra.Command, args []string) error {
	cfg, _ := config.Load(cmd.Flags())
	rules := commitlint.Rules{
		AllowedTypes:     cfg.Commit.Types,
		ScopeRequired:    cfg.Commit.ScopeRequired,
		MaxSubjectLength: cfg.Commit.MaxSubjectLength,
	}

	file, _ := cmd.Flags().GetString("file")
	staged, _ := cmd.Flags().GetBool("staged")

	// File / staged path
	if file != "" || staged {
		path := file
		if staged {
			runner := &git.ExecRunner{Dir: RepoFlag()}
			stdout, _, err := runner.Run(cmd.Context(), "rev-parse", "--absolute-git-dir")
			if err != nil {
				return err
			}
			path = strings.TrimSpace(string(stdout)) + "/COMMIT_EDITMSG"
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		msg := commitlint.Parse(cleanRawMessage(string(raw)))
		issues := commitlint.Lint(msg, rules)
		return reportIssues(cmd.OutOrStdout(), "(file)", msg, issues)
	}

	// rev-range (default HEAD)
	rev := "HEAD"
	if len(args) > 0 {
		rev = args[0]
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}
	stdout, stderr, err := runner.Run(cmd.Context(), "log", "--format=%H%x00%B%x1e", rev)
	if err != nil {
		return fmt.Errorf("git log: %s: %w", strings.TrimSpace(string(stderr)), err)
	}
	records := strings.Split(strings.TrimRight(string(stdout), "\x1e\n"), "\x1e")
	fails := 0
	for _, rec := range records {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\x00", 2)
		if len(parts) < 2 {
			continue
		}
		sha := parts[0]
		if len(sha) > 8 {
			sha = sha[:8]
		}
		msg := commitlint.Parse(parts[1])
		issues := commitlint.Lint(msg, rules)
		if err := reportIssues(cmd.OutOrStdout(), sha, msg, issues); err != nil {
			fails++
		}
	}
	if fails > 0 {
		return fmt.Errorf("%d commit message(s) failed linting", fails)
	}
	return nil
}

// cleanRawMessage strips comment lines (# ...) that git commit editor may include.
func cleanRawMessage(s string) string {
	var b strings.Builder
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func reportIssues(w io.Writer, label string, _ commitlint.Message, issues []commitlint.Issue) error {
	if len(issues) == 0 {
		fmt.Fprintf(w, "✓ %s\n", label)
		return nil
	}
	fmt.Fprintf(w, "✗ %s\n", label)
	for _, iss := range issues {
		fmt.Fprintf(w, "    [%s] %s\n", iss.Code, iss.Message)
	}
	return fmt.Errorf("lint failed")
}
