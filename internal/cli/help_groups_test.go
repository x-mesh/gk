package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Every map entry must name a real root subcommand by its canonical name —
// a renamed verb would otherwise silently fall out of its section.
func TestHelpGroups_EveryMapEntryNamesARealCommand(t *testing.T) {
	for name := range commandGroup {
		cmd, _, err := rootCmd.Find([]string{name})
		if err != nil || cmd == rootCmd || cmd.Name() != name {
			t.Errorf("commandGroup entry %q does not match a root subcommand canonically (found %q, err %v)", name, cmd.Name(), err)
		}
	}
}

// Every group id referenced by the map must be defined, with both titles.
func TestHelpGroups_GroupIDsAreDefined(t *testing.T) {
	ids := map[string]bool{}
	for _, g := range helpGroups {
		ids[g.id] = true
		if g.title == "" || g.koTitle == "" {
			t.Errorf("group %q is missing a title (en %q / ko %q)", g.id, g.title, g.koTitle)
		}
	}
	for name, id := range commandGroup {
		if !ids[id] {
			t.Errorf("command %q references undefined group %q", name, id)
		}
	}
}

// The drift guard: every visible root subcommand must be classified. A new
// verb that forgets its group entry fails here with instructions, instead of
// silently landing in the additional-commands bucket.
func TestHelpGroups_EveryVisibleCommandIsGrouped(t *testing.T) {
	installHelpGroups(rootCmd)
	for _, c := range rootCmd.Commands() {
		switch c.Name() {
		case "help", "completion", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
			continue // cobra's own — deliberately ungrouped
		}
		if !c.IsAvailableCommand() {
			continue // hidden/deprecated commands don't render in help
		}
		if c.GroupID == "" {
			t.Errorf("root command %q has no help group — add it to commandGroup in help_groups.go", c.Name())
		}
	}
}

// The English help must render the grouped sections (cobra's default template
// takes the grouped branch as soon as groups exist).
func TestHelpGroups_RootUsageRendersSections(t *testing.T) {
	installHelpGroups(rootCmd)
	usage := rootCmd.UsageString()
	for _, g := range helpGroups {
		if !strings.Contains(usage, g.title) {
			t.Errorf("root usage is missing the %q section", g.title)
		}
	}
	if strings.Contains(usage, "Available Commands:") {
		t.Error("root usage still renders the flat list — groups not applied")
	}
	// Spot-check one membership: discard under its section header.
	rec := usage[strings.Index(usage, "Recovery & safety nets:"):]
	if end := strings.Index(rec, "\n\n"); end >= 0 {
		rec = rec[:end]
	}
	if !strings.Contains(rec, "discard") {
		t.Errorf("discard is not listed under Recovery & safety nets:\n%s", rec)
	}
}

// The Korean usage template must render the same grouped sections — it is a
// hand-carried copy of cobra's default, so this is the test that catches the
// two drifting apart (the pre-group template silently dropped sections).
func TestKoUsageTemplate_RendersGroups(t *testing.T) {
	root := &cobra.Command{Use: "gk"}
	root.AddGroup(&cobra.Group{ID: "g1", Title: "그룹 하나:"})
	root.AddCommand(
		&cobra.Command{Use: "alpha", Short: "in the group", GroupID: "g1", Run: func(*cobra.Command, []string) {}},
		&cobra.Command{Use: "beta", Short: "ungrouped", Run: func(*cobra.Command, []string) {}},
	)
	root.SetUsageTemplate(koUsageTemplate)

	usage := root.UsageString()
	if !strings.Contains(usage, "그룹 하나:") || !strings.Contains(usage, "alpha") {
		t.Errorf("grouped section missing:\n%s", usage)
	}
	if !strings.Contains(usage, "기타 명령:") || !strings.Contains(usage, "beta") {
		t.Errorf("additional-commands section missing:\n%s", usage)
	}
	if strings.Contains(usage, "사용할 수 있는 명령:") {
		t.Errorf("flat list must not render when groups exist:\n%s", usage)
	}
}

// Without groups (every subcommand help page), the Korean template keeps the
// flat list — the grouped branch must not eat the ordinary case.
func TestKoUsageTemplate_FlatWithoutGroups(t *testing.T) {
	root := &cobra.Command{Use: "gk"}
	root.AddCommand(&cobra.Command{Use: "alpha", Short: "plain", Run: func(*cobra.Command, []string) {}})
	root.SetUsageTemplate(koUsageTemplate)

	usage := root.UsageString()
	if !strings.Contains(usage, "사용할 수 있는 명령:") || !strings.Contains(usage, "alpha") {
		t.Errorf("flat command list missing:\n%s", usage)
	}
}
