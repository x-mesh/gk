package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/sessionaudit"
)

func init() {
	cmd := &cobra.Command{
		Use:   "hint [command]",
		Short: "Suggest the git-kit replacement for a raw git command",
		Long: `Inspect a shell command and report the git-kit verb that already covers it.

Built to back a PreToolUse hook: pipe the command an agent is about to run into
git-kit hint and, when it reports covered, steer the agent to the git-kit
equivalent instead of raw git. The mapping is the same one git-kit session audit
uses, so guidance stays in one place. The command text comes from the arguments,
or from stdin when none are given:

  git-kit hint "git status --short"
  echo "$TOOL_CMD" | git-kit hint --json

Under --json (or GK_AGENT) it emits {covered, kind, covered_by, suggestion,
matched}. --exit-code exits 1 when a git-kit replacement exists (0 otherwise),
so a hook can branch on the status without parsing output.`,
		Args: cobra.ArbitraryArgs,
		RunE: runHint,
	}
	cmd.Flags().Bool("exit-code", false, "exit 1 when a git-kit replacement exists (for hook scripting)")
	rootCmd.AddCommand(cmd)
}

func runHint(cmd *cobra.Command, args []string) error {
	command := strings.TrimSpace(strings.Join(args, " "))
	if command == "" {
		// No argument → read the command from stdin (the PreToolUse hook path).
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return fmt.Errorf("hint: read stdin: %w", err)
		}
		command = strings.TrimSpace(string(data))
	}

	res := sessionaudit.Hint(command)

	if JSONOut() {
		if err := emitAgentResult(cmd.OutOrStdout(), res); err != nil {
			return err
		}
	} else {
		renderHint(cmd.OutOrStdout(), res)
	}

	if exitCode, _ := cmd.Flags().GetBool("exit-code"); exitCode && res.Covered {
		hintExitFunc(1)
	}
	return nil
}

func renderHint(w io.Writer, res sessionaudit.HintResult) {
	if !res.Covered {
		fmt.Fprintln(w, "ok: no git-kit replacement for this command")
		return
	}
	fmt.Fprintf(w, "use %s instead of raw git\n", strings.Join(res.CoveredBy, " / "))
	if res.Suggestion != "" {
		fmt.Fprintf(w, "  %s\n", res.Suggestion)
	}
}

// hintExitFunc is the indirection used by --exit-code so tests can swap in a
// recorder. Production binds to os.Exit; the flag exists for hook scripts that
// consume the integer exit code directly.
var hintExitFunc = os.Exit
