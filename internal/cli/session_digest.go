package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/sessionaudit"
)

// newSessionDigestCmd builds `gk session digest`; session.go registers it on
// the session command.
func newSessionDigestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "digest [transcript-file]",
		Short: "Compress one session's git activity into a resume/handoff block",
		Long: `Reads a single Claude/Codex JSONL session transcript and compresses its git
activity — repos touched, branches created/switched-to, commit subjects,
integration attempts (and whether any errored), unfinished-work signals, and
the re-probed command groups one gk call would collapse — into a short block
a resumed agent reads instead of re-running the status/log/diff orientation
probes.

Pass an explicit transcript path, or --last to digest the newest session file
under the default session roots (the same roots gk session audit scans).
Run from INSIDE a live agent session, the newest file is that session's own
transcript (it is appended every turn) — use --last=2 for the previous
session's handoff.

The command is local and read-only. With --json or GK_AGENT=1 it emits the
standard machine-readable envelope.`,
		Args: cobra.MaximumNArgs(1),
		RunE: runSessionDigest,
	}
	cmd.Flags().Int("last", 0, "digest the Nth-newest session file under the default session roots (1 = newest; from a live session use --last=2 for the previous one)")
	// Bare --last keeps meaning "the newest" (cobra requires = for a value:
	// --last=2; a space-separated `--last 2` would read 2 as the path arg).
	cmd.Flags().Lookup("last").NoOptDefVal = "1"
	return cmd
}

func runSessionDigest(cmd *cobra.Command, args []string) error {
	last, _ := cmd.Flags().GetInt("last")
	if last > 0 && len(args) > 0 {
		return fmt.Errorf("pass a transcript file or --last, not both")
	}
	if last <= 0 && len(args) == 0 {
		return fmt.Errorf("pass a transcript file or --last")
	}

	var path string
	if last > 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("--last: %w", err)
		}
		path, err = sessionaudit.NthNewestSessionFile(home, last)
		if err != nil {
			return err
		}
	} else {
		path = args[0]
	}

	digest, err := sessionaudit.DigestFile(path)
	if err != nil {
		return err
	}
	if JSONOut() {
		return emitAgentResult(cmd.OutOrStdout(), digest)
	}
	renderSessionDigest(cmd.OutOrStdout(), digest)
	return nil
}

func renderSessionDigest(w io.Writer, d sessionaudit.Digest) {
	fmt.Fprintf(w, "session digest: %s\n", d.File)
	fmt.Fprintf(w, "source: %s, %d turn(s), %d shell command(s)\n", d.Source, d.Turns, d.Commands)
	if len(d.Repos) > 0 {
		parts := make([]string, len(d.Repos))
		for i, r := range d.Repos {
			parts[i] = fmt.Sprintf("%s x%d", r.Path, r.Commands)
		}
		fmt.Fprintf(w, "repos: %s\n", strings.Join(parts, ", "))
	}
	if len(d.Branches) > 0 {
		fmt.Fprintf(w, "branches: %s\n", strings.Join(d.Branches, ", "))
	}
	if d.CommitCount > 0 {
		fmt.Fprintf(w, "commits: %d (most recent last)\n", d.CommitCount)
		for _, subject := range d.Commits {
			fmt.Fprintf(w, "  %s\n", subject)
		}
	}
	if it := d.Integration; it != nil {
		line := fmt.Sprintf("integration: %d attempt(s)", it.Attempts)
		if len(it.Verbs) > 0 {
			line += fmt.Sprintf(" (%s)", formatSubcommandBreakdown(it.Verbs))
		}
		if it.Errored > 0 {
			line += fmt.Sprintf(", %d errored", it.Errored)
		}
		fmt.Fprintln(w, line)
		if it.LastError != "" {
			fmt.Fprintf(w, "  last error: %s\n", it.LastError)
		}
	}
	if u := d.Unfinished; u != nil {
		fmt.Fprintf(w, "unfinished: turn %d — %s (%s)\n", u.Turn, u.Command, u.Reason)
	}
	if len(d.Reprobes) > 0 {
		fmt.Fprintln(w, "re-probed groups (each collapses into one gk call):")
		for _, r := range d.Reprobes {
			fmt.Fprintf(w, "  %s x%d → %s\n", r.Group, r.TurnsSaved, r.GkCommand)
		}
	}
}
