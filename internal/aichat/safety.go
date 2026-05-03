package aichat

import (
	"regexp"
	"strings"
)

// dangerousPattern pairs a compiled regex with a human-readable reason
// explaining why the matched command is dangerous, and the risk level.
type dangerousPattern struct {
	re     *regexp.Regexp
	reason string
	risk   RiskLevel // defaults to RiskHigh when zero
}

// dangerousPatterns is the ordered list of patterns that classify a
// command as RiskHigh.  Order does not matter for correctness but
// keeps the table easy to scan.
var dangerousPatterns = []dangerousPattern{
	// --- destructive git operations ---
	{
		// --force-with-lease is classified separately (RiskLow, not RiskHigh).
		re:     regexp.MustCompile(`(?i)^git\s+push\s+.*--force-with-lease\b`),
		reason: "force push with lease (safer)",
		risk:   RiskLow,
	},
	{
		// --force / -f (but not --force-with-lease, matched above).
		re:     regexp.MustCompile(`(?i)^git\s+push\s+.*(?:--force|-f)\b`),
		reason: "overwrites remote history",
		risk:   RiskHigh,
	},
	{
		re:     regexp.MustCompile(`(?i)^git\s+reset\s+.*--hard\b`),
		reason: "resets working tree and index",
	},
	{
		re:     regexp.MustCompile(`(?i)^git\s+clean\s+.*-f`),
		reason: "deletes untracked files",
	},
	{
		// -D is case-sensitive: -D = force delete, -d = safe delete.
		re:     regexp.MustCompile(`^git\s+branch\s+.*-D\b`),
		reason: "force deletes branch",
	},
	{
		re:     regexp.MustCompile(`(?i)^git\s+rebase\b`),
		reason: "rewrites history",
	},
	{
		re:     regexp.MustCompile(`(?i)^git\s+checkout\s+--\s+\.`),
		reason: "discards working tree changes",
	},
	// --- configuration / credential manipulation ---
	{
		re:     regexp.MustCompile(`(?i)^git\s+config\b`),
		reason: "modifies git configuration",
	},
	{
		re:     regexp.MustCompile(`(?i)^git\s+credential\b`),
		reason: "accesses stored credentials",
	},
	{
		re:     regexp.MustCompile(`(?i)^git\s+remote\s+(set-url|add|rename|remove)\b`),
		reason: "modifies remote configuration",
	},
	{
		re:     regexp.MustCompile(`(?i)^git\s+filter-branch\b`),
		reason: "rewrites entire repository history",
	},
	{
		re:     regexp.MustCompile(`(?i)^git\s+replace\b`),
		reason: "replaces git objects",
	},
	{
		re:     regexp.MustCompile(`(?i)^git\s+submodule\s+add\b`),
		reason: "adds external dependency",
	},
	// --- gk destructive operations ---
	{
		re:     regexp.MustCompile(`(?i)^gk\s+wipe\b`),
		reason: "hard reset and clean",
	},
	{
		re:     regexp.MustCompile(`(?i)^gk\s+reset\b`),
		reason: "branch reset",
	},
}

// SafetyClassifier classifies commands by risk level.
type SafetyClassifier struct{}

// Classify returns the risk level and a human-readable reason for the
// given command string. Commands matching a known dangerous pattern
// return the pattern's risk level (default RiskHigh); everything else
// returns RiskNone. Patterns are checked in order; the first match wins.
func (s *SafetyClassifier) Classify(command string) (RiskLevel, string) {
	trimmed := strings.TrimSpace(command)
	for _, dp := range dangerousPatterns {
		if dp.re.MatchString(trimmed) {
			risk := dp.risk
			if risk == RiskNone {
				risk = RiskHigh // default for patterns without explicit risk
			}
			return risk, dp.reason
		}
	}
	return RiskNone, ""
}

// ClassifyPlan reports whether the plan contains at least one
// dangerous (RiskHigh) command.  It also populates each
// PlannedCommand's Risk and RiskReason fields as a side effect.
func (s *SafetyClassifier) ClassifyPlan(plan *ExecutionPlan) bool {
	if plan == nil {
		return false
	}
	hasDangerous := false
	for i := range plan.Commands {
		risk, reason := s.Classify(plan.Commands[i].Command)
		plan.Commands[i].Risk = risk
		plan.Commands[i].RiskReason = reason
		if risk == RiskHigh {
			hasDangerous = true
		}
	}
	return hasDangerous
}
