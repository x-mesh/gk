package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
)

func init() {
	cmd := &cobra.Command{
		Use:     "local",
		Aliases: []string{"lo"},
		Short:   "Show only what exists locally (uncommitted + unpushed + stash)",
		Long: "Roll up everything that lives only on this machine into one screen:\n" +
			"working-tree changes (unstaged/staged/conflicts), commits not on any\n" +
			"remote (unpushed — uses @{upstream}, falling back to any remote-tracking\n" +
			"ref when no upstream is set), and stash entries.",
		RunE: runLocal,
	}
	cmd.Flags().IntP("limit", "n", 10, "max commits/files to list per section (0 = unlimited)")
	rootCmd.AddCommand(cmd)
}

// localCommit is one unpushed commit row in --json output.
type localCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Age     string `json:"age"`
}

type localReport struct {
	Branch        string        `json:"branch"`
	Upstream      string        `json:"upstream,omitempty"`
	Unstaged      int           `json:"unstaged"`
	Staged        int           `json:"staged"`
	Conflicts     int           `json:"conflicts"`
	Unpushed      int           `json:"unpushed"`
	UnpushedKnown bool          `json:"unpushed_known"`
	Stash         int           `json:"stash"`
	Commits       []localCommit `json:"unpushed_commits,omitempty"`
	Clean         bool          `json:"clean"`
}

func runLocal(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	runner := &git.ExecRunner{Dir: RepoFlag()}
	limit, _ := cmd.Flags().GetInt("limit")

	st, err := git.NewClient(runner).Status(ctx)
	if err != nil {
		return fmt.Errorf("gk local: %w", err)
	}
	g := groupEntries(st.Entries)
	unstaged := len(g.Modified) + len(g.Untracked)
	staged := len(g.Staged)
	conflicts := len(g.Unmerged)

	commits, unpushedOK := unpushedCommits(ctx, runner, limitArg(limit))
	stashN := stashCount(ctx, runner)

	// "clean" requires that we could actually determine push state — with no
	// remote to compare against we cannot assert "nothing local-only."
	clean := unpushedOK && unstaged == 0 && staged == 0 && conflicts == 0 && len(commits) == 0 && stashN == 0

	if JSONOut() {
		rep := localReport{
			Branch:        st.Branch,
			Upstream:      st.Upstream,
			Unstaged:      unstaged,
			Staged:        staged,
			Conflicts:     conflicts,
			Unpushed:      len(commits),
			UnpushedKnown: unpushedOK,
			Stash:         stashN,
			Commits:       commits,
			Clean:         clean,
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}

	w := cmd.OutOrStdout()
	faint := color.New(color.Faint).SprintFunc()
	bold := color.New(color.Bold).SprintFunc()
	head := faint("LOCAL") + "  " + faint("— only on this machine")
	if st.Branch != "" {
		head = bold(st.Branch) + "  " + faint("— local only")
	}
	fmt.Fprintln(w, head)

	// Working tree.
	wtParts := []string{}
	if unstaged > 0 {
		wtParts = append(wtParts, fmt.Sprintf("%d unstaged", unstaged))
	}
	if staged > 0 {
		wtParts = append(wtParts, color.GreenString("%d staged", staged))
	}
	if conflicts > 0 {
		wtParts = append(wtParts, color.RedString("%d conflicts", conflicts))
	}
	if len(wtParts) == 0 {
		fmt.Fprintf(w, "  %s  %s\n", faint("working tree"), faint("clean"))
	} else {
		fmt.Fprintf(w, "  %s  %s\n", faint("working tree"), strings.Join(wtParts, faint(" · ")))
		shown := 0
		for _, e := range committableEntries(st.Entries) {
			if limit > 0 && shown >= limit {
				fmt.Fprintf(w, "      %s\n", faint(fmt.Sprintf("… and %d more", countCommittable(st.Entries)-shown)))
				break
			}
			fmt.Fprintf(w, "      %s %s\n", localStateGlyph(e), e.Path)
			shown++
		}
	}

	// Unpushed commits.
	switch {
	case !unpushedOK:
		fmt.Fprintf(w, "  %s  %s\n", faint("unpushed"), faint("no remote to compare against"))
	case len(commits) == 0:
		fmt.Fprintf(w, "  %s  %s\n", faint("unpushed"), faint("in sync — nothing local-only"))
	default:
		label := fmt.Sprintf("%d commit", len(commits))
		if len(commits) != 1 {
			label += "s"
		}
		fmt.Fprintf(w, "  %s  %s\n", faint("unpushed"), label)
		for _, c := range commits {
			fmt.Fprintf(w, "      %s %s  %s\n",
				color.YellowString("◇"),
				color.YellowString(c.SHA),
				c.Subject+"  "+faint("("+c.Age+")"))
		}
	}

	// Stash.
	if stashN > 0 {
		if summary := renderStashSummary(ctx, runner); summary != "" {
			fmt.Fprintf(w, "  %s  %s\n", faint("stash"), summary)
		} else {
			fmt.Fprintf(w, "  %s  %d entries\n", faint("stash"), stashN)
		}
	}

	if clean {
		fmt.Fprintln(w, faint("  ✓ nothing local-only — everything is committed and pushed"))
	}
	return nil
}

// limitArg converts the user's 0-means-unlimited limit into a git -n value:
// returns 0 (no -n flag) when unlimited, else the limit.
func limitArg(limit int) int {
	if limit < 0 {
		return 0
	}
	return limit
}

// unpushedCommits lists commits that exist on no remote. Resolution mirrors
// collectPushedShas / sincePushSuffix: @{upstream} first, then any
// remote-tracking ref (--remotes) when no upstream is configured. ok=false
// means there is no remote to compare against at all.
func unpushedCommits(ctx context.Context, runner *git.ExecRunner, limit int) ([]localCommit, bool) {
	revArgs, ok := unpushedRevArgs(ctx, runner)
	if !ok {
		return nil, false
	}
	args := append([]string{"log"}, revArgs...)
	args = append(args, "--format=%h%x1f%s%x1f%cr")
	if limit > 0 {
		args = append(args, fmt.Sprintf("-n%d", limit))
	}
	out, _, err := runner.Run(ctx, args...)
	if err != nil {
		return nil, false
	}
	var commits []localCommit
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\x1f", 3)
		if len(f) != 3 {
			continue
		}
		commits = append(commits, localCommit{SHA: f[0], Subject: f[1], Age: f[2]})
	}
	return commits, true
}

// unpushedRevArgs returns the rev range that selects local-only commits.
func unpushedRevArgs(ctx context.Context, runner *git.ExecRunner) ([]string, bool) {
	if _, _, err := runner.Run(ctx, "rev-parse", "--verify", "--quiet", "@{u}"); err == nil {
		return []string{"@{u}..HEAD"}, true
	}
	if anyRemoteTrackingRef(ctx, runner) {
		return []string{"HEAD", "--not", "--remotes"}, true
	}
	return nil, false
}

// stashCount returns the number of stash entries (0 on error / none).
func stashCount(ctx context.Context, runner *git.ExecRunner) int {
	out, _, err := runner.Run(ctx, "stash", "list", "--format=%gd")
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// countCommittable counts non-submodule entries (the rows `gk local` lists).
func countCommittable(entries []git.StatusEntry) int {
	n := 0
	for _, e := range entries {
		if e.Kind != git.KindSubmodule {
			n++
		}
	}
	return n
}

// localStateGlyph maps an entry's state to a short colored marker for the
// working-tree list, reusing statusEntryState's classification.
func localStateGlyph(e git.StatusEntry) string {
	switch statusEntryState(e) {
	case "staged":
		return color.GreenString("+")
	case "conflict":
		return color.RedString("⚔")
	case "untracked":
		return color.New(color.Faint).Sprint("?")
	default:
		return color.YellowString("~")
	}
}
