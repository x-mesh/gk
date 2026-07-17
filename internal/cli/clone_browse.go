package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/x-mesh/gk/internal/config"
	ghapi "github.com/x-mesh/gk/internal/github"
	"github.com/x-mesh/gk/internal/ui"
)

// browseCloneCandidates powers `gk clone` with no arguments: it lists the
// repos under every owner-bearing clone.hosts profile and lets the user
// pick one interactively, returning a spec ready for resolveCloneURL
// (an "alias:owner/repo" string, so the alias's own protocol/ssh_host
// settings are honored same as if the user had typed it).
//
// ui.ErrPickerAborted comes back unwrapped so callers can treat Esc/Ctrl-C
// as a quiet no-op, matching every other picker flow in this package.
func browseCloneCandidates(ctx context.Context, cfg config.CloneConfig, errOut io.Writer) (string, error) {
	profiles := githubProfileAliases(cfg)
	if len(profiles) == 0 {
		return "", fmt.Errorf(
			"gk clone: no clone.hosts entry has both `owner` and a github.com host — " +
				"pass <owner>/<repo> directly, or add one under clone.hosts")
	}

	client := &ghapi.Client{Token: ghapi.ResolveToken()}
	items, warnings := cloneCandidateItems(ctx, client, profiles)
	for _, w := range warnings {
		fmt.Fprintf(errOut, "warn: gk clone: %s\n", w)
	}
	if len(items) == 0 {
		return "", fmt.Errorf("gk clone: no repositories found under %s (check network/auth, or pass <owner>/<repo> directly)",
			strings.Join(profileOwners(profiles), ", "))
	}

	picked, err := ui.NewPicker().Pick(ctx, "clone which repo?", items)
	if err != nil {
		return "", err
	}
	return picked.Key, nil
}

// hostProfile is one clone.hosts alias resolved down to what browse mode
// needs: the alias name (to build "alias:owner/repo" specs) and the owner
// to list repos for.
type hostProfile struct {
	alias string
	owner string
}

func profileOwners(profiles []hostProfile) []string {
	out := make([]string, len(profiles))
	for i, p := range profiles {
		out[i] = p.owner
	}
	return out
}

// githubProfileAliases returns the clone.hosts aliases that have both an
// owner configured and resolve to github.com — the only host this
// package's REST client can talk to. Order follows orderedProfileAliases,
// the same presentation order gk init's account picker uses, so the two
// pickers list profiles consistently.
func githubProfileAliases(cfg config.CloneConfig) []hostProfile {
	var out []hostProfile
	for _, name := range orderedProfileAliases(cfg) {
		alias := cfg.Hosts[name]
		host := alias.Host
		if host == "" {
			host = cfg.DefaultHost
		}
		if host == "" {
			host = "github.com"
		}
		if !strings.EqualFold(host, "github.com") {
			continue
		}
		out = append(out, hostProfile{alias: name, owner: alias.Owner})
	}
	return out
}

// cloneCandidateItems fetches every profile's repo list concurrently
// (network-bound, so serial would pay one round-trip per owner for no
// reason) and flattens the result into sorted picker rows. A failure on
// one profile is a warning, not a hard error — the other profiles' repos
// are still worth showing.
func cloneCandidateItems(ctx context.Context, client *ghapi.Client, profiles []hostProfile) ([]ui.PickerItem, []string) {
	type result struct {
		profile hostProfile
		repos   []ghapi.Repo
		err     error
	}
	results := make([]result, len(profiles))
	var wg sync.WaitGroup
	for i, p := range profiles {
		wg.Add(1)
		go func(i int, p hostProfile) {
			defer wg.Done()
			repos, err := client.ListRepos(ctx, p.owner)
			results[i] = result{profile: p, repos: repos, err: err}
		}(i, p)
	}
	wg.Wait()

	var items []ui.PickerItem
	var warnings []string
	for _, r := range results {
		if r.err != nil {
			warnings = append(warnings, fmt.Sprintf("%s (%s): %v", r.profile.alias, r.profile.owner, r.err))
			continue
		}
		for _, repo := range r.repos {
			fullName := repo.Owner + "/" + repo.Name
			desc := repo.Description
			if desc == "" {
				desc = "-"
			}
			updated := "-"
			if !repo.UpdatedAt.IsZero() {
				updated = repo.UpdatedAt.Format("2006-01-02")
			}
			visibility := "public"
			if repo.Private {
				visibility = "private"
			}
			items = append(items, ui.PickerItem{
				Display: fmt.Sprintf("%-10s %-40s %s", r.profile.alias, fullName, desc),
				Key:     r.profile.alias + ":" + fullName,
				Cells:   []string{r.profile.alias, fullName, visibility, updated, desc},
			})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, warnings
}
