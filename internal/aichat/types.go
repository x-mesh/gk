// Package aichat implements `gk do`, `gk explain`, and `gk ask` —
// AI-powered conversational commands that generate/execute git/gk
// command sequences, diagnose errors, and answer repository-context
// questions.
package aichat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ExecutionPlan is a sequence of commands produced by IntentParser
// from a natural-language input.
type ExecutionPlan struct {
	Commands []PlannedCommand `json:"commands"`
}

// MarshalJSON implements json.Marshaler for ExecutionPlan.
func (p ExecutionPlan) MarshalJSON() ([]byte, error) {
	type Alias ExecutionPlan
	return json.Marshal(Alias(p))
}

// UnmarshalJSON implements json.Unmarshaler for ExecutionPlan.
func (p *ExecutionPlan) UnmarshalJSON(data []byte) error {
	type Alias ExecutionPlan
	var a Alias
	if err := json.Unmarshal(data, &a); err != nil {
		return fmt.Errorf("invalid execution plan JSON: %w", err)
	}
	*p = ExecutionPlan(a)
	return nil
}

// PlannedCommand is a single command inside an ExecutionPlan.
type PlannedCommand struct {
	Command     string    `json:"command"`     // e.g. "gk push"
	Description string    `json:"description"` // one-line explanation
	Dangerous   bool      `json:"dangerous"`   // flagged by AI or SafetyClassifier
	Risk        RiskLevel `json:"-"`           // set by SafetyClassifier
	RiskReason  string    `json:"-"`           // reason for the risk level
}

// allowedGitSubcmds is the set of git subcommands that gk do is
// permitted to generate. Commands outside this set are rejected at
// parse time to prevent AI from generating dangerous git operations
// like `git config` or `git credential`.
var allowedGitSubcmds = map[string]bool{
	"add": true, "commit": true, "push": true, "pull": true,
	"fetch": true, "checkout": true, "switch": true, "branch": true,
	"merge": true, "rebase": true, "reset": true, "stash": true,
	"log": true, "diff": true, "status": true, "tag": true,
	"clean": true, "cherry-pick": true, "revert": true, "show": true,
	"bisect": true, "blame": true, "shortlog": true, "describe": true,
	"am": true, "apply": true, "format-patch": true, "mv": true,
	"rm": true, "restore": true, "sparse-checkout": true,
	"worktree": true, "notes": true, "reflog": true,
	"init": true, "clone": true, "remote": true,
}

// Validate checks that Command starts with "git " or "gk " (case-insensitive)
// and that the git subcommand is in the allowed set.
// Returns an error if the command is not whitelisted.
func (c PlannedCommand) Validate() error {
	lower := strings.ToLower(c.Command)
	if strings.HasPrefix(lower, "git ") {
		// Extract the subcommand (first non-flag token after "git").
		parts := strings.Fields(c.Command)
		if len(parts) < 2 {
			return fmt.Errorf("command %q: missing git subcommand", c.Command)
		}
		// Skip leading flags like -c (which are blocked at execution anyway).
		subcmd := ""
		for _, p := range parts[1:] {
			if !strings.HasPrefix(p, "-") {
				subcmd = strings.ToLower(p)
				break
			}
		}
		if subcmd == "" {
			return fmt.Errorf("command %q: no git subcommand found", c.Command)
		}
		if !allowedGitSubcmds[subcmd] {
			return fmt.Errorf("command %q: git subcommand %q is not allowed", c.Command, subcmd)
		}
		return nil
	}
	if strings.HasPrefix(lower, "gk ") {
		return nil
	}
	return fmt.Errorf("command %q: must start with \"git \" or \"gk \"", c.Command)
}

// RiskLevel indicates how dangerous a command is.
type RiskLevel int

const (
	RiskNone RiskLevel = iota // safe command
	RiskLow                   // use with caution
	RiskHigh                  // destructive — requires extra confirmation
)

// String returns a human-readable label for the risk level.
func (r RiskLevel) String() string {
	switch r {
	case RiskNone:
		return "none"
	case RiskLow:
		return "low"
	case RiskHigh:
		return "high"
	default:
		return fmt.Sprintf("RiskLevel(%d)", int(r))
	}
}

// ExecutionResult is the outcome of CommandExecutor.Execute.
type ExecutionResult struct {
	Executed  []CommandResult // results for each executed command
	BackupRef string         // backup ref created before dangerous commands
	Aborted   bool           // true when user rejected or an error stopped execution
}

// CommandResult is the outcome of a single command execution.
type CommandResult struct {
	Command  string // the command that was run
	Stdout   string // standard output
	Stderr   string // standard error
	ExitCode int    // process exit code
	Error    error  // non-nil on failure
}

// ExecuteOptions controls CommandExecutor.Execute behaviour.
type ExecuteOptions struct {
	Yes    bool // --yes: skip normal confirmation
	Force  bool // --force: skip ALL confirmations including dangerous
	DryRun bool // --dry-run: print plan only, execute nothing
	JSON   bool // --json: output in JSON format
	NonTTY bool // true when stdout is not a terminal
}
