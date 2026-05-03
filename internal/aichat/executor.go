package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/easy"
	"github.com/x-mesh/gk/internal/git"
)

// SafetyConfig controls safety-related behaviour of CommandExecutor.
type SafetyConfig struct {
	SafetyConfirm bool // when true, dangerous commands require extra confirmation
}

// ConfirmFunc asks the user a yes/no question and returns the answer.
// prompt is the question text. The function should return true for yes.
type ConfirmFunc func(prompt string) (bool, error)

// CommandExecutor previews and executes an ExecutionPlan.
type CommandExecutor struct {
	Runner       git.Runner
	Out          io.Writer
	ErrOut       io.Writer
	EasyEngine   *easy.Engine
	SafetyConfig SafetyConfig
	Dbg          func(string, ...any)

	// ConfirmFunc is called to ask the user for confirmation.
	// If nil, defaults to reading from stdin (y/n/Enter).
	ConfirmFunc ConfirmFunc
}

// dbg is a helper that calls e.Dbg if non-nil.
func (e *CommandExecutor) dbg(format string, args ...any) {
	if e.Dbg != nil {
		e.Dbg(format, args...)
	}
}

// emojiForCommand returns an emoji prefix for a command when Easy Mode
// is active. Returns "" when Easy Mode is disabled.
func (e *CommandExecutor) emojiForCommand(cmd string) string {
	if e.EasyEngine == nil || !e.EasyEngine.IsEnabled() {
		return ""
	}
	em := e.EasyEngine.Emoji()
	if em == nil {
		return ""
	}

	lower := strings.ToLower(cmd)
	switch {
	case strings.Contains(lower, "push"):
		return em.Prefix("push")
	case strings.Contains(lower, "pull"):
		return em.Prefix("pull")
	case strings.Contains(lower, "branch"):
		return em.Prefix("branch")
	case strings.Contains(lower, "merge"):
		return em.Prefix("merge")
	case strings.Contains(lower, "add") || strings.Contains(lower, "stage"):
		return em.Prefix("staged")
	case strings.Contains(lower, "clean") || strings.Contains(lower, "rm"):
		return em.Prefix("deleted")
	default:
		return ""
	}
}

// Preview returns a human-readable preview of the execution plan.
// All commands are numbered. Dangerous commands show a ⚠️ 위험 label
// with an impact description. Easy Mode emoji is inserted when active.
func (e *CommandExecutor) Preview(plan *ExecutionPlan) string {
	if plan == nil || len(plan.Commands) == 0 {
		return ""
	}

	var b strings.Builder
	for i, cmd := range plan.Commands {
		emoji := e.emojiForCommand(cmd.Command)

		if cmd.Risk == RiskHigh || cmd.Dangerous {
			fmt.Fprintf(&b, "  %d. %s%s\n", i+1, emoji, cmd.Command)
			reason := cmd.RiskReason
			if reason == "" && cmd.Description != "" {
				reason = cmd.Description
			}
			if reason != "" {
				fmt.Fprintf(&b, "     ⚠️ 위험: %s\n", reason)
			} else {
				fmt.Fprintf(&b, "     ⚠️ 위험\n")
			}
		} else {
			fmt.Fprintf(&b, "  %d. %s%s\n", i+1, emoji, cmd.Command)
		}

		if cmd.Description != "" && cmd.Risk != RiskHigh && !cmd.Dangerous {
			fmt.Fprintf(&b, "     %s\n", cmd.Description)
		} else if cmd.Description != "" && (cmd.Risk == RiskHigh || cmd.Dangerous) {
			// Description already shown as risk reason or separately
			if cmd.RiskReason != "" && cmd.Description != cmd.RiskReason {
				fmt.Fprintf(&b, "     %s\n", cmd.Description)
			}
		}
	}
	return b.String()
}

// Execute runs the plan's commands sequentially after user confirmation.
//
// Confirmation flow:
//  1. NonTTY + no --yes/--dry-run → return error (exit 2)
//  2. --dry-run → print plan, return without executing
//  3. --json → marshal plan to JSON, return without executing
//  4. Normal: show preview + ask y/n (Enter = yes)
//  5. Dangerous commands: extra per-command confirmation (SafetyConfirm=true)
//  6. --yes: skip normal confirmation, dangerous still confirmed
//  7. --force: skip ALL confirmations
//  8. Create backup ref before first dangerous command
//  9. Execute commands sequentially; stop on error
func (e *CommandExecutor) Execute(ctx context.Context, plan *ExecutionPlan, opts ExecuteOptions) (*ExecutionResult, error) {
	if plan == nil || len(plan.Commands) == 0 {
		return &ExecutionResult{}, nil
	}

	// NonTTY without --yes or --dry-run → exit 2
	if opts.NonTTY && !opts.Yes && !opts.DryRun && !opts.Force {
		return nil, &NonTTYError{}
	}

	// --json mode: marshal plan to JSON and return
	if opts.JSON {
		data, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("do: failed to marshal plan to JSON: %w", err)
		}
		fmt.Fprintln(e.Out, string(data))
		return &ExecutionResult{}, nil
	}

	// --dry-run mode: print preview and return
	if opts.DryRun {
		preview := e.Preview(plan)
		fmt.Fprint(e.Out, preview)
		return &ExecutionResult{}, nil
	}

	// Show preview
	preview := e.Preview(plan)
	fmt.Fprint(e.Out, preview)

	// Normal confirmation (unless --yes or --force)
	if !opts.Yes && !opts.Force {
		ok, err := e.confirm("실행할까요? (y/n) ")
		if err != nil {
			return nil, fmt.Errorf("do: confirmation error: %w", err)
		}
		if !ok {
			return &ExecutionResult{Aborted: true}, nil
		}
	}

	result := &ExecutionResult{}

	// Check if plan has dangerous commands
	hasDangerous := false
	for _, cmd := range plan.Commands {
		if cmd.Risk == RiskHigh || cmd.Dangerous {
			hasDangerous = true
			break
		}
	}

	// Create backup ref before first dangerous command
	backupCreated := false

	// Execute commands sequentially
	for i, cmd := range plan.Commands {
		// Check context cancellation (Ctrl+C)
		select {
		case <-ctx.Done():
			result.Aborted = true
			fmt.Fprintf(e.ErrOut, "do: interrupted — %d of %d commands executed\n", len(result.Executed), len(plan.Commands))
			return result, nil
		default:
		}

		isDangerous := cmd.Risk == RiskHigh || cmd.Dangerous

		// Create backup ref before first dangerous command
		if isDangerous && !backupCreated && hasDangerous {
			ref, err := e.createBackupRef(ctx)
			if err != nil {
				e.dbg("do: failed to create backup ref: %v", err)
			} else if ref != "" {
				result.BackupRef = ref
				e.dbg("do: backup ref created: %s", ref)
			}
			backupCreated = true
		}

		// Extra confirmation for dangerous commands.
		// --force skips all confirmations.
		// --yes skips normal confirmation but dangerous commands ALWAYS
		// require confirmation regardless of SafetyConfirm setting.
		if isDangerous && !opts.Force {
			reason := cmd.RiskReason
			if reason == "" {
				reason = cmd.Description
			}
			prompt := fmt.Sprintf("⚠️ 위험 명령어: %s (%s)\n정말 실행할까요? (y/n) ", cmd.Command, reason)
			ok, err := e.confirm(prompt)
			if err != nil {
				return nil, fmt.Errorf("do: confirmation error: %w", err)
			}
			if !ok {
				result.Aborted = true
				fmt.Fprintf(e.ErrOut, "do: 위험 명령어 거부됨 — %d of %d commands executed\n", len(result.Executed), len(plan.Commands))
				return result, nil
			}
		}

		// Execute the command
		fmt.Fprintf(e.Out, "\n[%d/%d] %s\n", i+1, len(plan.Commands), cmd.Command)

		cmdResult := e.runCommand(ctx, cmd.Command)
		result.Executed = append(result.Executed, cmdResult)

		if cmdResult.Stdout != "" {
			fmt.Fprint(e.Out, cmdResult.Stdout)
			if !strings.HasSuffix(cmdResult.Stdout, "\n") {
				fmt.Fprintln(e.Out)
			}
		}
		if cmdResult.Stderr != "" {
			fmt.Fprint(e.ErrOut, cmdResult.Stderr)
			if !strings.HasSuffix(cmdResult.Stderr, "\n") {
				fmt.Fprintln(e.ErrOut)
			}
		}

		if cmdResult.Error != nil {
			result.Aborted = true
			remaining := len(plan.Commands) - i - 1
			fmt.Fprintf(e.ErrOut, "do: command failed: %s\n", cmd.Command)
			if remaining > 0 {
				fmt.Fprintf(e.ErrOut, "do: %d remaining command(s) skipped\n", remaining)
			}
			if result.BackupRef != "" {
				fmt.Fprintf(e.ErrOut, "hint: restore with: git update-ref HEAD %s\n", result.BackupRef)
			}
			return result, nil
		}

		// Show success indicator
		fmt.Fprintf(e.Out, "  ✓ done\n")
	}

	return result, nil
}

// confirm asks the user for confirmation using the ConfirmFunc.
func (e *CommandExecutor) confirm(prompt string) (bool, error) {
	if e.ConfirmFunc != nil {
		return e.ConfirmFunc(prompt)
	}
	// Default: print prompt and read from stdin (not used in tests)
	fmt.Fprint(e.Out, prompt)
	return false, fmt.Errorf("no ConfirmFunc set and stdin reading not implemented")
}

// createBackupRef creates a backup ref at HEAD before dangerous commands.
// Returns the ref path or empty string on failure.
func (e *CommandExecutor) createBackupRef(ctx context.Context) (string, error) {
	// Get current branch name
	branchOut, _, err := e.Runner.Run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("failed to get branch: %w", err)
	}
	branch := strings.TrimSpace(string(branchOut))
	if branch == "" {
		branch = "detached"
	}

	unix := time.Now().Unix()
	refPath := fmt.Sprintf("refs/gk/do-backup/%s/%d", branch, unix)

	_, _, err = e.Runner.Run(ctx, "update-ref", refPath, "HEAD")
	if err != nil {
		return "", fmt.Errorf("failed to create backup ref: %w", err)
	}

	return refPath, nil
}

// blockedGitArgs are git arguments that must not appear in AI-generated
// commands because they can override git behaviour in dangerous ways
// (e.g. -c core.sshCommand="malicious").
var blockedGitArgs = []string{"-c", "--config", "--exec-path", "--git-dir", "--work-tree"}

// allowedGkSubcmds maps gk subcommands to their git equivalents.
// Only listed subcommands are executed; unknown gk subcommands are rejected.
var allowedGkSubcmds = map[string]string{
	"sync":   "", // no direct git equivalent — skip execution
	"push":   "push",
	"pull":   "pull",
	"commit": "commit",
	"status": "status",
	"log":    "log",
	"diff":   "diff",
	"merge":  "merge",
	"clone":  "clone",
	"ship":   "", // no direct git equivalent — skip execution
	"st":     "status",
	"slog":   "log",
}

// shellSplit splits a command string respecting single and double quotes.
// Unlike strings.Fields, it correctly handles: git commit -m "fix: bug"
func shellSplit(s string) []string {
	var parts []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'' && !inDouble:
			inSingle = !inSingle
		case ch == '"' && !inSingle:
			inDouble = !inDouble
		case ch == ' ' && !inSingle && !inDouble:
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(ch)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// validateGitArgs checks that none of the args contain blocked options
// that could override git behaviour (e.g. -c for config injection).
func validateGitArgs(args []string) error {
	for _, arg := range args {
		for _, blocked := range blockedGitArgs {
			if arg == blocked || strings.HasPrefix(arg, blocked+"=") {
				return fmt.Errorf("blocked git argument: %s", arg)
			}
		}
	}
	return nil
}

// runCommand parses and executes a single command string via git.Runner.
func (e *CommandExecutor) runCommand(ctx context.Context, command string) CommandResult {
	cr := CommandResult{Command: command}

	parts := shellSplit(command)
	if len(parts) == 0 {
		cr.Error = fmt.Errorf("empty command")
		cr.ExitCode = 1
		return cr
	}

	prefix := parts[0]
	args := parts[1:]

	switch strings.ToLower(prefix) {
	case "git":
		// Validate: block dangerous git arguments like -c.
		if err := validateGitArgs(args); err != nil {
			cr.Error = fmt.Errorf("do: security: %w", err)
			cr.ExitCode = 1
			return cr
		}
		stdout, stderr, err := e.Runner.Run(ctx, args...)
		cr.Stdout = string(stdout)
		cr.Stderr = string(stderr)
		if err != nil {
			cr.Error = err
			if exitErr, ok := err.(*git.ExitError); ok {
				cr.ExitCode = exitErr.Code
			} else {
				cr.ExitCode = 1
			}
		}
	case "gk":
		// Map gk subcommands to git equivalents via whitelist.
		if len(args) == 0 {
			cr.Error = fmt.Errorf("gk command requires a subcommand")
			cr.ExitCode = 1
			return cr
		}
		subcmd := strings.ToLower(args[0])
		gitEquiv, ok := allowedGkSubcmds[subcmd]
		if !ok {
			cr.Error = fmt.Errorf("unsupported gk subcommand: %s", subcmd)
			cr.ExitCode = 1
			return cr
		}
		if gitEquiv == "" {
			// No direct git equivalent — report as not executable.
			cr.Error = fmt.Errorf("gk %s cannot be executed via git runner; run it directly", subcmd)
			cr.ExitCode = 1
			return cr
		}
		// Replace gk subcommand with git equivalent, keep remaining args.
		gitArgs := append([]string{gitEquiv}, args[1:]...)
		if err := validateGitArgs(gitArgs); err != nil {
			cr.Error = fmt.Errorf("do: security: %w", err)
			cr.ExitCode = 1
			return cr
		}
		stdout, stderr, err := e.Runner.Run(ctx, gitArgs...)
		cr.Stdout = string(stdout)
		cr.Stderr = string(stderr)
		if err != nil {
			cr.Error = err
			if exitErr, ok := err.(*git.ExitError); ok {
				cr.ExitCode = exitErr.Code
			} else {
				cr.ExitCode = 1
			}
		}
	default:
		cr.Error = fmt.Errorf("unsupported command prefix: %s", prefix)
		cr.ExitCode = 1
	}

	return cr
}

// NonTTYError is returned when Execute is called in a non-TTY environment
// without --yes or --dry-run.
type NonTTYError struct{}

func (e *NonTTYError) Error() string {
	return "do: non-interactive mode requires --yes or --dry-run"
}

// ExitCode returns 2 for NonTTYError.
func (e *NonTTYError) ExitCode() int {
	return 2
}
