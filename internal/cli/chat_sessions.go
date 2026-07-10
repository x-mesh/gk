package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/chat"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
)

// newChatSessionsCmd builds `gk chat sessions` (list) and its `prune`
// subcommand. There is no separate index file (v1 scope): both scan
// .git/gk-chat/sessions/*.jsonl directly via chat.ListSessions.
func newChatSessionsCmd() *cobra.Command {
	sessions := &cobra.Command{
		Use:   "sessions",
		Short: "List gk chat sessions",
		Long: `Lists every gk chat session recorded under .git/gk-chat/sessions/,
newest first: id, when it started, a title (an explicit /rename, else the
first user message truncated), and how many turns it holds.

Resume one with: gk chat --session <id>`,
		// Not NoArgs: bare `gk chat sessions` lists, but a one-shot question
		// that happens to START with the word "sessions" (e.g. `gk chat
		// sessions where are they stored`, unquoted) is routed here by cobra
		// as if "sessions" were the subcommand. Rejecting the extra args
		// would turn that question into an error; instead, treat any args as
		// a question and delegate to the ordinary one-shot path with the
		// "sessions" token restored, so the subcommand shadows nothing.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return runChat(cmd, append([]string{"sessions"}, args...))
			}
			return runChatSessionsList(cmd, args)
		},
	}

	prune := &cobra.Command{
		Use:   "prune",
		Short: "Expire gk chat sessions older than a retention window",
		Long: `Deletes session JSONL files under .git/gk-chat/sessions/ whose last
activity (file mtime) is older than the retention window.

Off by default: pass --keep-days N or set ai.chat.session_retention_days in
.gk.yaml to opt in — mirrors gk snapshot prune's retention pattern, except
prune never runs with an implicit non-zero window. The session --continue
currently points at is never pruned.`,
		Args: cobra.NoArgs,
		RunE: runChatSessionsPrune,
	}
	prune.Flags().Int("keep-days", 0, "expire sessions inactive for this many days (falls back to ai.chat.session_retention_days; 0 = do nothing)")
	sessions.AddCommand(prune)

	return sessions
}

// chatSessionJSON is one entry in `GK_AGENT=1 gk chat sessions`'s array.
type chatSessionJSON struct {
	ID        string `json:"id"`
	StartedAt string `json:"started_at,omitempty"`
	Title     string `json:"title,omitempty"`
	Turns     int    `json:"turns"`
	// Current marks the session `gk chat --continue` would resume.
	Current bool `json:"current,omitempty"`
}

func runChatSessionsList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}

	metas, err := chat.ListSessions(ctx, runner)
	if err != nil {
		return fmt.Errorf("chat: list sessions: %w", err)
	}
	current := chat.LastSessionID(ctx, runner)

	if JSONOut() {
		out := make([]chatSessionJSON, 0, len(metas))
		for _, m := range metas {
			j := chatSessionJSON{ID: m.ID, Title: m.Title, Turns: m.TurnCount, Current: m.ID == current}
			if !m.StartedAt.IsZero() {
				j.StartedAt = m.StartedAt.Format(time.RFC3339)
			}
			out = append(out, j)
		}
		return emitAgentResult(cmd.OutOrStdout(), out)
	}

	w := cmd.OutOrStdout()
	if len(metas) == 0 {
		fmt.Fprintln(w, "no gk chat sessions yet")
		fmt.Fprintln(w, stylizeHintLine("hint: gk chat   # start one"))
		return nil
	}
	for _, m := range metas {
		mark := " "
		if m.ID == current {
			mark = "*"
		}
		title := m.Title
		if title == "" {
			title = cellFaint("(no title)")
		}
		fmt.Fprintf(w, "%s %s  %s  %s  %d turn(s)\n",
			mark, cellCyan(m.ID), stashRelative(m.StartedAt), title, m.TurnCount)
	}
	fmt.Fprintln(w, stylizeHintLine("hint: gk chat --session <id>   # resume one"))
	return nil
}

// chatSessionsPruneJSON backs GK_AGENT=1 gk chat sessions prune.
type chatSessionsPruneJSON struct {
	Pruned   []string `json:"pruned"`
	KeepDays int      `json:"keep_days"`
}

func runChatSessionsPrune(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	w := cmd.OutOrStdout()

	days, _ := cmd.Flags().GetInt("keep-days")
	if !cmd.Flags().Changed("keep-days") {
		if cfg, cErr := config.Load(cmd.Flags()); cErr == nil && cfg.AI.Chat.SessionRetentionDays > 0 {
			days = cfg.AI.Chat.SessionRetentionDays
		}
	}
	if days <= 0 {
		if JSONOut() {
			return emitAgentResult(w, chatSessionsPruneJSON{Pruned: []string{}, KeepDays: 0})
		}
		fmt.Fprintln(w, "no retention window set — nothing pruned")
		fmt.Fprintln(w, stylizeHintLine("hint: gk chat sessions prune --keep-days 30   # or set ai.chat.session_retention_days in .gk.yaml"))
		return nil
	}

	runner := &git.ExecRunner{Dir: RepoFlag()}
	metas, err := chat.ListSessions(ctx, runner)
	if err != nil {
		return fmt.Errorf("chat: list sessions: %w", err)
	}
	// The session `gk chat --continue` would resume is never pruned, even
	// if its last activity falls outside the window — deleting the file a
	// bare --continue is about to reopen would surprise the user more than
	// leaving one stale-looking entry behind.
	keep := chat.LastSessionID(ctx, runner)
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	pruned := make([]string, 0)
	for _, m := range metas {
		if m.ID == keep {
			continue
		}
		info, statErr := os.Stat(m.Path)
		if statErr != nil || info.ModTime().After(cutoff) {
			continue
		}
		if rmErr := os.Remove(m.Path); rmErr != nil {
			return fmt.Errorf("chat: prune session %s: %w", m.ID, rmErr)
		}
		pruned = append(pruned, m.ID)
	}

	if JSONOut() {
		return emitAgentResult(w, chatSessionsPruneJSON{Pruned: pruned, KeepDays: days})
	}
	if len(pruned) == 0 {
		fmt.Fprintf(w, "nothing to prune — no sessions inactive for %d+ days\n", days)
		return nil
	}
	fmt.Fprintln(w, successLinef("pruned", "%d session(s) inactive for %d+ days", len(pruned), days))
	return nil
}
