package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"
	"github.com/x-mesh/gk/internal/git"
)

// stopHookTimeoutSeconds bounds the Stop hook end to end, and is written into
// settings.json as the entry's own timeout so Claude Code enforces the same
// ceiling from its side.
//
// The budget covers one compose round-trip plus the commit. It is generous
// because the alternative — killing a live provider call — throws away the
// summary the call was about to produce; and it is bounded because a hung
// provider must not hold a session open indefinitely. Work is never lost by
// hitting it: the files are still in the working tree, and the next session's
// Stop hook checkpoints them.
const stopHookTimeoutSeconds = 120

// runAgentsHookStop is the Stop-event handler: it writes a `gk commit --wip`
// checkpoint when a Claude Code session ends with uncommitted work.
//
// Fail-open by contract, exactly like the PreToolUse handler. Every problem —
// unreadable stdin, no repo, a provider outage, a timeout — exits 0 without a
// decision payload. A session-end hook that can fail the session is worse than
// one that occasionally skips a checkpoint.
func runAgentsHookStop(cmd *cobra.Command, _ []string) error {
	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return nil
	}
	// stop_hook_active means Claude Code is already inside a Stop-hook-driven
	// continuation. Committing again here would append a checkpoint per loop
	// iteration, so bail before doing any work.
	if gjson.GetBytes(data, "stop_hook_active").Bool() {
		return nil
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}
	if _, _, rerr := runner.Run(ctx, "rev-parse", "--show-toplevel"); rerr != nil {
		return nil // not a repo — nothing to checkpoint
	}

	self, eerr := os.Executable()
	if eerr != nil || self == "" {
		return nil
	}

	// Run the checkpoint as a child process rather than calling runAICommit
	// in-process: cobra flag state is global to the command tree, and this
	// handler shares that tree with the very command it is invoking. A child
	// gets a clean flag set and an enforceable deadline.
	runCtx, cancel := context.WithTimeout(ctx, stopHookTimeoutSeconds*time.Second)
	defer cancel()
	child := exec.CommandContext(runCtx, self, "commit", "--wip")
	child.Stdout = cmd.ErrOrStderr() // progress belongs on stderr for a hook
	child.Stderr = cmd.ErrOrStderr()
	// GK_AGENT would switch the child to the JSON envelope, which is noise in
	// a hook transcript; the human stream is what a session log wants.
	child.Env = append(os.Environ(), "GK_AGENT=")
	if rerr := child.Run(); rerr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "gk: session checkpoint skipped (%v)\n", rerr)
	}
	return nil
}

// stopHookCommandString is the settings.json command that invokes this
// binary's Stop handler. Like the other two it carries an absolute path so it
// survives PATH changes and shell aliases.
func stopHookCommandString() string {
	self, err := os.Executable()
	if err != nil || self == "" {
		self = "git-kit"
	}
	return fmt.Sprintf("%q agents hook run --stop", self)
}

// gkStopHookInstalled reports whether the gk Stop checkpoint hook is
// registered. Like the prefetch hook it carries no enforcement mode, so
// presence is the whole answer.
func gkStopHookInstalled(data []byte) bool {
	installed, _ := gkEventHookMode(data, "Stop")
	return installed
}
