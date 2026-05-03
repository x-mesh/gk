package aichat

import (
	"context"
	"fmt"
	"strings"

	"github.com/x-mesh/gk/internal/git"
)

// RepoContextCollector gathers repository state for AI prompt context.
type RepoContextCollector struct {
	Runner      git.Runner
	TokenBudget int // default 2000; Format() output limited to this
	Dbg         func(string, ...any)
}

// RepoContext holds the collected repository state.
type RepoContext struct {
	Branch       string   // current branch name
	HeadSHA      string   // HEAD commit hash (short)
	Upstream     string   // upstream tracking ref (e.g. "origin/main")
	Status       string   // git status --porcelain=v2 output
	RecentReflog []string // recent reflog entries (up to 10)
	BranchList   []string // all local branches (for gk ask)
	IsRepo       bool     // true when inside a git repository
	IsDetached   bool     // true when HEAD is detached (not on a branch)
	tokenBudget  int      // set by collector; used by Format()
}

// dbg is a helper that calls c.Dbg if non-nil.
func (c *RepoContextCollector) dbg(format string, args ...any) {
	if c.Dbg != nil {
		c.Dbg(format, args...)
	}
}

// tokenBudget returns the effective token budget, defaulting to 2000.
func (c *RepoContextCollector) tokenBudget() int {
	if c.TokenBudget > 0 {
		return c.TokenBudget
	}
	return 2000
}

// runGit executes a git command and returns trimmed stdout.
// On error the result is empty string; errors are silently ignored.
func (c *RepoContextCollector) runGit(ctx context.Context, args ...string) string {
	stdout, _, err := c.Runner.Run(ctx, args...)
	if err != nil {
		c.dbg("context: git %s: %v", strings.Join(args, " "), err)
		return ""
	}
	return strings.TrimSpace(string(stdout))
}

// Collect gathers repository state. Individual git command failures leave
// the corresponding field empty; the method never returns an error.
// A non-git directory yields IsRepo=false with all fields empty.
func (c *RepoContextCollector) Collect(ctx context.Context) *RepoContext {
	rc := &RepoContext{}

	// Check if we're in a git repo by asking for HEAD.
	branch := c.runGit(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if branch == "" {
		// Not a git repo or bare/detached — mark and return.
		rc.IsRepo = false
		return rc
	}
	rc.IsRepo = true
	rc.tokenBudget = c.tokenBudget()

	// Detect detached HEAD state.
	if branch == "HEAD" {
		rc.IsDetached = true
		rc.Branch = "(detached)"
	} else {
		rc.Branch = branch
	}

	// HEAD SHA (short).
	rc.HeadSHA = c.runGit(ctx, "rev-parse", "--short", "HEAD")

	// Upstream tracking ref.
	rc.Upstream = c.runGit(ctx, "rev-parse", "--abbrev-ref", "@{u}")

	// Working tree status.
	rc.Status = c.runGit(ctx, "status", "--porcelain=v2")

	// Recent reflog (up to 10 entries).
	reflog := c.runGit(ctx, "reflog", "-10", "--format=%h %gs")
	if reflog != "" {
		rc.RecentReflog = strings.Split(reflog, "\n")
	}

	return rc
}

// CollectForQuestion collects additional info related to the question,
// including the branch list for gk ask.
func (c *RepoContextCollector) CollectForQuestion(ctx context.Context, question string) *RepoContext {
	rc := c.Collect(ctx)
	if !rc.IsRepo {
		return rc
	}

	// Collect branch list for question-answering context.
	branches := c.runGit(ctx, "branch", "--format=%(refname:short)")
	if branches != "" {
		rc.BranchList = strings.Split(branches, "\n")
	}

	return rc
}

// Format converts RepoContext into a prompt-insertion string.
// The output is limited to the token budget set during collection
// (default 2000).
func (rc *RepoContext) Format() string {
	budget := rc.tokenBudget
	if budget <= 0 {
		budget = 2000
	}
	return rc.formatWithBudget(budget)
}

// FormatWithBudget converts RepoContext into a prompt-insertion string
// limited to the given token budget.
func (rc *RepoContext) formatWithBudget(budget int) string {
	if !rc.IsRepo {
		return "Not a git repository."
	}

	maxChars := budget * 4 // rough token estimate: 1 token ≈ 4 chars
	var b strings.Builder

	// Priority 1: Branch + HEAD SHA + upstream (~50 tokens)
	fmt.Fprintf(&b, "Branch: %s\n", rc.Branch)
	if rc.HeadSHA != "" {
		fmt.Fprintf(&b, "HEAD: %s\n", rc.HeadSHA)
	}
	if rc.Upstream != "" {
		fmt.Fprintf(&b, "Upstream: %s\n", rc.Upstream)
	}

	// Priority 2: git status summary (~200 tokens)
	if rc.Status != "" {
		section := fmt.Sprintf("Status:\n%s\n", rc.Status)
		if b.Len()+len(section) <= maxChars {
			b.WriteString(section)
		} else {
			// Truncate status to fit budget.
			remaining := maxChars - b.Len() - len("Status:\n") - len("...\n")
			if remaining > 0 {
				truncated := rc.Status
				if len(truncated) > remaining {
					truncated = truncated[:remaining] + "..."
				}
				fmt.Fprintf(&b, "Status:\n%s\n", truncated)
			}
		}
	}

	// Priority 3: Recent reflog (~500 tokens)
	if len(rc.RecentReflog) > 0 {
		section := "Reflog:\n" + strings.Join(rc.RecentReflog, "\n") + "\n"
		if b.Len()+len(section) <= maxChars {
			b.WriteString(section)
		} else {
			// Add as many reflog entries as fit.
			header := "Reflog:\n"
			if b.Len()+len(header) < maxChars {
				b.WriteString(header)
				for _, entry := range rc.RecentReflog {
					line := entry + "\n"
					if b.Len()+len(line) > maxChars {
						break
					}
					b.WriteString(line)
				}
			}
		}
	}

	// Priority 5: Branch list (~200 tokens, gk ask only)
	if len(rc.BranchList) > 0 {
		section := "Branches:\n" + strings.Join(rc.BranchList, "\n") + "\n"
		if b.Len()+len(section) <= maxChars {
			b.WriteString(section)
		} else {
			header := "Branches:\n"
			if b.Len()+len(header) < maxChars {
				b.WriteString(header)
				for _, br := range rc.BranchList {
					line := br + "\n"
					if b.Len()+len(line) > maxChars {
						break
					}
					b.WriteString(line)
				}
			}
		}
	}

	result := b.String()
	// Final safety: hard-truncate if somehow over budget.
	if len(result) > maxChars {
		result = result[:maxChars]
	}
	return result
}

// FormatForCollector formats with the given collector's token budget.
func (rc *RepoContext) FormatForCollector(c *RepoContextCollector) string {
	return rc.formatWithBudget(c.tokenBudget())
}
