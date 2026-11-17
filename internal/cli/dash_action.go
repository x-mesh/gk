package cli

import (
	"fmt"
	"strings"

	ghapi "github.com/x-mesh/gk/internal/github"
)

// Normalized check states. Any other value is GitHub's raw state in lower
// case, which never counts as success.
const (
	dashCISuccess = "success"
	dashCIFailure = "failure"
	dashCIPending = "pending"
	dashCINone    = "none"
)

// Review verdict sources.
const (
	dashReviewDecision = "decision"
	dashReviewContext  = "context"
	dashReviewNone     = "none"
)

type dashReview struct {
	Source      string `json:"source"`
	State       string `json:"state,omitempty"` // lower case: approved, changes_requested, success, ...
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
}

type dashCheckRef struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

type dashCI struct {
	State  string         `json:"state"`
	Failed []dashCheckRef `json:"failed,omitempty"`
}

// dashAction is the next step of a row. Rule is the number of the rule that
// matched (see prAction); Command is a gk command to run, URL a page to open.
type dashAction struct {
	Rule    int    `json:"rule"`
	Text    string `json:"text,omitempty"`
	Command string `json:"command,omitempty"`
	URL     string `json:"url,omitempty"`
}

// reviewVerdict reads the verdict from reviewDecision, else from the status
// context named by reviewContext. It never reads review bodies.
func reviewVerdict(pr ghapi.DashPR, reviewContext string) dashReview {
	if pr.ReviewDecision != "" {
		return dashReview{Source: dashReviewDecision, State: strings.ToLower(pr.ReviewDecision), URL: pr.URL}
	}
	if reviewContext != "" {
		for _, c := range pr.Checks {
			if c.Kind == "status" && c.Name == reviewContext {
				return dashReview{Source: dashReviewContext, State: strings.ToLower(c.State), Description: c.Description, URL: c.URL}
			}
		}
	}
	return dashReview{Source: dashReviewNone}
}

// passed and waiting split a known verdict. A verdict that is neither (none,
// or an unknown value) gives no merge advice.
func (r dashReview) passed() bool {
	switch r.Source {
	case dashReviewDecision:
		return r.State == "approved"
	case dashReviewContext:
		return r.State == "success"
	}
	return false
}

func (r dashReview) waiting() bool {
	switch r.Source {
	case dashReviewDecision:
		return r.State == "changes_requested" || r.State == "review_required"
	case dashReviewContext:
		return r.State == "failure" || r.State == "error" || r.State == "pending" || r.State == "expected"
	}
	return false
}

func checkState(c ghapi.DashCheck) string {
	if c.Kind == "check_run" {
		if !strings.EqualFold(c.State, "COMPLETED") {
			return dashCIPending
		}
		switch strings.ToUpper(c.Conclusion) {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			return dashCISuccess
		case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
			return dashCIFailure
		}
		return strings.ToLower(c.Conclusion)
	}
	return rollupState(c.State)
}

func rollupState(s string) string {
	switch strings.ToUpper(s) {
	case "SUCCESS":
		return dashCISuccess
	case "PENDING", "EXPECTED":
		return dashCIPending
	case "FAILURE", "ERROR":
		return dashCIFailure
	}
	return strings.ToLower(s)
}

// ciSummary sums the checks of a PR head, without the review context. A
// failure wins, then an unknown state, then a pending one. When GitHub has
// more checks than the page holds, the rollup state over all checks keeps a
// partial page from reading as success.
func ciSummary(pr ghapi.DashPR, reviewContext string) dashCI {
	var ci dashCI
	var unknown, pending, seen bool
	for _, c := range pr.Checks {
		if reviewContext != "" && c.Kind == "status" && c.Name == reviewContext {
			continue
		}
		seen = true
		switch st := checkState(c); st {
		case dashCISuccess:
		case dashCIFailure:
			ci.Failed = append(ci.Failed, dashCheckRef{Name: c.Name, URL: c.URL})
		case dashCIPending:
			pending = true
		default:
			if !unknown {
				ci.State = st
			}
			unknown = true
		}
	}
	switch {
	case len(ci.Failed) > 0:
		ci.State = dashCIFailure
	case unknown:
	case pending:
		ci.State = dashCIPending
	case !seen:
		ci.State = dashCINone
	default:
		ci.State = dashCISuccess
		if pr.ChecksTotal > len(pr.Checks) {
			if r := rollupState(pr.Rollup); r != dashCISuccess {
				ci.State = r
			}
		}
	}
	return ci
}

// dashPRFacts are what the action rules read about one PR.
type dashPRFacts struct {
	pr           ghapi.DashPR
	review       dashReview
	ci           dashCI
	branches     []dashLocalBranch
	baseConflict []string // files, when the PR conflicts with its base
	baseClean    bool     // the forecast merged the PR with its base cleanly
	pairWith     int      // another open PR it conflicts with, 0 for none
	pairFiles    []string
}

// prAction applies the rules in order and returns the first that matches:
//
//  1. merged, a local branch remains     → gk branch clean
//  2. conflicts with the base            → gk sync --fetch
//  3. CI failed                          → the failed check
//  4. review blocks or waits             → the review
//  5. conflicts with another open PR     → that PR and the files
//  6. draft
//  7. CI running
//  8. review passed, CI passed, base clean → ready to merge
//  9. no verdict, CI passed, base clean  → CI passed, no review verdict
//  10. nothing to suggest
//
// A gk command that runs in the local repository is given only when a local
// branch is linked; otherwise the command is gk pr checkout.
func prAction(f dashPRFacts) dashAction {
	checkout := fmt.Sprintf("gk pr checkout %d", f.pr.Number)
	local := func(cmd string) string {
		if len(f.branches) == 0 {
			return checkout
		}
		return cmd
	}
	where := ""
	if len(f.branches) > 0 && f.branches[0].Worktree != "" && !f.branches[0].Current {
		where = " (in " + f.branches[0].Worktree + ")"
	}
	switch {
	case f.pr.State == "MERGED" && len(f.branches) > 0:
		return dashAction{Rule: 1, Text: "merged; local branch remains" + where, Command: "gk branch clean"}
	case len(f.baseConflict) > 0:
		return dashAction{Rule: 2, Text: fmt.Sprintf("conflicts with %s: %s%s", f.pr.BaseRef, strings.Join(f.baseConflict, ", "), where), Command: local("gk sync --fetch")}
	case f.ci.State == dashCIFailure:
		a := dashAction{Rule: 3, Text: "CI failed: " + f.ci.Failed[0].Name, URL: f.ci.Failed[0].URL}
		if a.URL == "" {
			a.URL = f.pr.URL
		}
		return a
	case f.review.waiting():
		u := f.review.URL
		if u == "" {
			u = f.pr.URL
		}
		return dashAction{Rule: 4, Text: "review: " + f.review.State, URL: u}
	case f.pairWith != 0:
		return dashAction{Rule: 5, Text: fmt.Sprintf("conflicts with #%d: %s", f.pairWith, strings.Join(f.pairFiles, ", "))}
	case f.pr.Draft:
		return dashAction{Rule: 6, Text: "draft"}
	case f.ci.State == dashCIPending:
		return dashAction{Rule: 7, Text: "CI running"}
	case f.review.passed() && f.ci.State == dashCISuccess && f.baseClean:
		return dashAction{Rule: 8, Text: "ready to merge"}
	case f.review.Source == dashReviewNone && f.ci.State == dashCISuccess && f.baseClean:
		return dashAction{Rule: 9, Text: "CI passed, no review verdict"}
	}
	return dashAction{Rule: 10}
}

// prRank orders PR rows: failures and conflicts, then reviews to do, then
// work in progress, then ready, then the rest, and cleanup last.
func prRank(relation string, a dashAction) int {
	if relation == dashRelReview {
		return 1
	}
	switch a.Rule {
	case 2, 3, 5:
		return 0
	case 4:
		return 1
	case 6, 7:
		return 2
	case 8, 9:
		return 3
	case 1:
		return 5
	}
	return 4
}

// issueAction applies the issue rules in order.
func issueAction(relation string, is ghapi.DashIssue) dashAction {
	for _, p := range is.LinkedPRs {
		if p.State == "OPEN" {
			return dashAction{Rule: 1, Text: fmt.Sprintf("PR #%d in review", p.Number)}
		}
	}
	for _, p := range is.LinkedPRs {
		if p.Merged {
			return dashAction{Rule: 2, Text: fmt.Sprintf("PR #%d merged; close the issue?", p.Number), URL: is.URL}
		}
	}
	switch relation {
	case dashRelMentioned:
		return dashAction{Rule: 3, Text: "reply needed", URL: is.URL}
	case dashRelUnassigned:
		return dashAction{Rule: 4, Text: "needs triage", URL: is.URL}
	case dashRelAssigned:
		return dashAction{Rule: 5, Text: "no work branch"}
	}
	return dashAction{Rule: 6}
}
