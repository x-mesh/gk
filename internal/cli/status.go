package cli

import (
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"st"},
		Short:   "Show concise working tree status",
		RunE:    runStatus,
	}
	rootCmd.AddCommand(cmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	if NoColorFlag() {
		color.NoColor = true
	}
	runner := &git.ExecRunner{Dir: RepoFlag()}
	client := git.NewClient(runner)
	st, err := client.Status(cmd.Context())
	if err != nil {
		return err
	}

	w := cmd.OutOrStdout()
	bold := color.New(color.Bold).SprintFunc()
	cyan := color.CyanString
	faint := color.New(color.Faint).SprintFunc()

	// branch line
	line := fmt.Sprintf("%s %s", faint("branch:"), bold(st.Branch))
	if st.Upstream != "" {
		line += fmt.Sprintf("  %s %s", faint("⇄"), cyan(st.Upstream))
	}
	if st.Ahead != 0 || st.Behind != 0 {
		line += fmt.Sprintf("  ↑%d ↓%d", st.Ahead, st.Behind)
	}
	fmt.Fprintln(w, line)

	// group entries by Kind
	grouped := groupEntries(st.Entries)
	if len(grouped.Unmerged) > 0 {
		fmt.Fprintln(w, color.New(color.FgRed, color.Bold).Sprint("conflicts:"))
		for _, e := range grouped.Unmerged {
			fmt.Fprintf(w, "  %s %s\n", color.RedString(e.XY), e.Path)
		}
	}
	if len(grouped.Staged) > 0 {
		fmt.Fprintln(w, color.GreenString("staged:"))
		for _, e := range grouped.Staged {
			fmt.Fprintf(w, "  %s %s\n", color.GreenString(e.XY), displayPath(e))
		}
	}
	if len(grouped.Modified) > 0 {
		fmt.Fprintln(w, color.YellowString("modified:"))
		for _, e := range grouped.Modified {
			fmt.Fprintf(w, "  %s %s\n", color.YellowString(e.XY), displayPath(e))
		}
	}
	if len(grouped.Untracked) > 0 {
		fmt.Fprintln(w, color.New(color.FgHiBlack).Sprint("untracked:"))
		for _, e := range grouped.Untracked {
			fmt.Fprintf(w, "  %s %s\n", color.New(color.FgHiBlack).Sprint("??"), e.Path)
		}
	}
	if len(st.Entries) == 0 {
		fmt.Fprintln(w, faint("working tree clean"))
	}
	return nil
}

type groupedEntries struct {
	Modified, Staged, Unmerged, Untracked []git.StatusEntry
}

func groupEntries(entries []git.StatusEntry) groupedEntries {
	var g groupedEntries
	for _, e := range entries {
		switch e.Kind {
		case git.KindUnmerged:
			g.Unmerged = append(g.Unmerged, e)
		case git.KindUntracked:
			g.Untracked = append(g.Untracked, e)
		default:
			if len(e.XY) >= 2 {
				x, y := e.XY[0], e.XY[1]
				switch {
				case x != '.' && x != ' ' && (y == '.' || y == ' '):
					g.Staged = append(g.Staged, e)
				default:
					g.Modified = append(g.Modified, e)
				}
			}
		}
	}
	return g
}

func displayPath(e git.StatusEntry) string {
	if e.Orig != "" {
		return fmt.Sprintf("%s → %s", e.Orig, e.Path)
	}
	return e.Path
}

// keep strings imported via usage
var _ = strings.Builder{}
