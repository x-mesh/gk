package cli

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:     "branch-check",
		Aliases: []string{"check-branch"},
		Short:   "Validate the current (or a given) branch name against config patterns",
		Long: `Validate the current (or a given) branch name against the patterns
configured in branch.patterns. Protected branches (branch.protected) are
always allowed without pattern matching.

Exit code 0 on success, non-zero on failure (cobra default).

Example usage as a prepare-commit-msg hook:
  gk branch-check`,
		RunE: runBranchCheck,
	}
	cmd.Flags().String("branch", "", "validate this name instead of the current branch")
	cmd.Flags().StringSlice("patterns", nil, "override regex patterns (comma-separated)")
	cmd.Flags().Bool("quiet", false, "suppress output; rely on exit code")
	rootCmd.AddCommand(cmd)
}

// branchCheckResult is pure data returned by checkBranch for testability.
type branchCheckResult struct {
	Branch   string
	Patterns []string
	Matched  bool
	Skipped  bool // true when the branch is on the protected allowlist
	Reason   string
}

func runBranchCheck(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cmd.Flags())
	if err != nil || cfg == nil {
		d := config.Defaults()
		cfg = &d
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)

	override, _ := cmd.Flags().GetString("branch")
	patOverride, _ := cmd.Flags().GetStringSlice("patterns")
	quiet, _ := cmd.Flags().GetBool("quiet")

	branch := override
	if branch == "" {
		b, err := client.CurrentBranch(cmd.Context())
		if err != nil {
			if errors.Is(err, git.ErrDetachedHEAD) {
				if cfg.Branch.AllowDetached {
					return nil
				}
				return fmt.Errorf("detached HEAD (set branch.allow_detached=true to skip)")
			}
			return err
		}
		branch = b
	}

	patterns := cfg.Branch.Patterns
	if len(patOverride) > 0 {
		patterns = patOverride
	}

	res := checkBranch(branch, patterns, cfg.Branch.Protected)

	if res.Skipped {
		if !quiet {
			fmt.Fprintf(cmd.OutOrStdout(), "✓ %s (protected)\n", branch)
		}
		return nil
	}
	if res.Matched {
		if !quiet {
			fmt.Fprintf(cmd.OutOrStdout(), "✓ %s\n", branch)
		}
		return nil
	}

	if !quiet {
		fmt.Fprintf(cmd.ErrOrStderr(), "✗ %s — %s\n", branch, res.Reason)
		if suggestion := suggestBranchName(patterns); suggestion != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "  example: %s\n", suggestion)
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "  configured patterns:")
		for _, p := range patterns {
			fmt.Fprintf(cmd.ErrOrStderr(), "    - %s\n", p)
		}
	}
	return fmt.Errorf("branch %q did not match any configured pattern", branch)
}

// checkBranch validates name against patterns and the protected allowlist.
// It is a pure function with no I/O, making it easy to unit-test.
func checkBranch(name string, patterns, protected []string) branchCheckResult {
	for _, p := range protected {
		if p == name {
			return branchCheckResult{Branch: name, Patterns: patterns, Skipped: true}
		}
	}
	if len(patterns) == 0 {
		// No patterns configured — treat as pass (users haven't opted in).
		return branchCheckResult{
			Branch:   name,
			Patterns: patterns,
			Matched:  true,
			Reason:   "no patterns configured",
		}
	}
	for _, raw := range patterns {
		re, err := regexp.Compile(raw)
		if err != nil {
			// Invalid regex — skip this rule; don't block on misconfiguration.
			continue
		}
		if re.MatchString(name) {
			return branchCheckResult{Branch: name, Patterns: patterns, Matched: true}
		}
	}
	return branchCheckResult{
		Branch:   name,
		Patterns: patterns,
		Matched:  false,
		Reason:   "did not match any configured pattern",
	}
}

// suggestBranchName returns a simple example for the first pattern that looks
// like a standard prefix alternation (e.g. `^(feat|fix|...)/...`).
// Returns "" when the first pattern is too exotic to hint for.
func suggestBranchName(patterns []string) string {
	if len(patterns) == 0 {
		return ""
	}
	re := regexp.MustCompile(`\(([a-zA-Z0-9_|]+)\)`)
	for _, pat := range patterns {
		if m := re.FindStringSubmatch(pat); m != nil {
			first := strings.SplitN(m[1], "|", 2)[0]
			return first + "/topic-name"
		}
	}
	return ""
}
