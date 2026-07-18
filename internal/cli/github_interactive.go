package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	ghapi "github.com/x-mesh/gk/internal/github"
	"github.com/x-mesh/gk/internal/ui"
)

// ghPicker drives the interactive listing loop for gk pr / issue / inbox.
// It follows the shape gk switch and gk worktree already use: build rows →
// Pick → dispatch on ExtraAction → re-enter with the typed filter preserved.
// Every extra key uses Exit:true (the gk idiom) so the handler runs outside the
// TUI and the loop redraws with fresh data.
type ghPicker struct {
	cmd     *cobra.Command
	client  *ghapi.Client
	runner  *git.ExecRunner
	cfg     *config.Config
	isPR    bool
	kind    string // live scope: "repo" | "org" | "inbox"
	filters githubSearchFilters
	sort    string
	limit   int

	repoPrefix string // resolved lazily, then cached
	orgPrefix  string
}

// scopePrefix resolves the search prefix for the current scope, caching the
// repo/org lookups so a scope cycle doesn't re-resolve them every redraw.
func (p *ghPicker) scopePrefix(ctx context.Context) (string, error) {
	switch p.kind {
	case "inbox":
		return "involves:@me", nil
	case "org":
		if p.orgPrefix == "" {
			owner := p.cfg.GitHub.Owner
			if owner == "" {
				owner = originOwner(ctx, *p.cfg, p.runner)
			}
			if owner == "" {
				return "", errors.New("no org to search: set github.owner or run inside a repo whose origin is on GitHub")
			}
			qualifier := "org"
			if typ, err := p.client.OwnerType(ctx, owner); err == nil && typ == "User" {
				qualifier = "user"
			}
			p.orgPrefix = qualifier + ":" + owner
		}
		return p.orgPrefix, nil
	default:
		if p.repoPrefix == "" {
			owner, repo, err := currentRepoSlug(ctx, *p.cfg, p.runner)
			if err != nil {
				return "", err
			}
			p.repoPrefix = fmt.Sprintf("repo:%s/%s", owner, repo)
		}
		return p.repoPrefix, nil
	}
}

// cycleScope advances repo → org → inbox → repo, skipping inbox when there is
// no token (involves:@me cannot resolve without one).
func (p *ghPicker) cycleScope() {
	switch p.kind {
	case "repo":
		p.kind = "org"
	case "org":
		if p.client.Token == "" {
			p.kind = "repo"
			return
		}
		p.kind = "inbox"
	default:
		p.kind = "repo"
	}
}

func (p *ghPicker) fetch(ctx context.Context) ([]ghapi.Issue, string, error) {
	prefix, err := p.scopePrefix(ctx)
	if err != nil {
		return nil, "", err
	}
	query := buildSearchQuery(prefix, p.filters)
	issues, err := p.client.SearchIssues(ctx, query, p.sort, p.limit)
	if err != nil {
		return nil, query, fmt.Errorf("github search: %w", err)
	}
	return issues, query, nil
}

var ghPickerHeaders = []string{"ITEM", "REPO", "TITLE", "LABELS", "AUTHOR", "AGE"}

func ghPickerColumnPriority() map[string]int {
	return map[string]int{"TITLE": 5, "ITEM": 4, "REPO": 3, "AUTHOR": 2, "AGE": 1, "LABELS": 1}
}

// rows builds picker rows plus a lookup from row key (the item URL) back to the
// issue, so an action key can recover the number/type of the cursor row.
func (p *ghPicker) rows(issues []ghapi.Issue) ([]ui.PickerItem, map[string]ghapi.Issue) {
	byKey := make(map[string]ghapi.Issue, len(issues))
	items := make([]ui.PickerItem, 0, len(issues))
	for _, is := range issues {
		typ := "issue"
		if is.IsPR {
			typ = "PR"
			if is.Draft {
				typ = "draft"
			}
		}
		ref := fmt.Sprintf("%s#%d", typ, is.Number)
		age := "-"
		if !is.UpdatedAt.IsZero() {
			age = relativeTime(time.Since(is.UpdatedAt))
		}
		key := is.URL
		byKey[key] = is
		items = append(items, ui.PickerItem{
			Key:     key,
			Display: fmt.Sprintf("%s %s", ref, is.Title),
			Cells: []string{
				ref,
				is.Owner + "/" + is.Repo,
				is.Title,
				strings.Join(is.Labels, ", "),
				"@" + is.Author,
				age,
			},
		})
	}
	return items, byKey
}

func (p *ghPicker) extras() []ui.TablePickerExtraKey {
	return []ui.TablePickerExtraKey{
		{Key: "c", Help: "c checkout", Exit: true},
		{Key: "y", Help: "y copy url", Exit: true},
		{Key: "o", Help: "o scope", Exit: true},
		{Key: "a", Help: "a open/all", Exit: true},
	}
}

func (p *ghPicker) subtitle(scope string, n int) string {
	state := p.filters.state
	if state == "" {
		state = "open"
	}
	return fmt.Sprintf("%s · %s · %d item(s)  —  enter open · c checkout · y copy url · o scope · a open/all", scope, state, n)
}

// run is the interactive loop. An aborted picker (Esc/Ctrl-C) exits quietly.
func (p *ghPicker) run(ctx context.Context) error {
	out, errOut := p.cmd.OutOrStdout(), p.cmd.ErrOrStderr()
	filter := ""

	for {
		issues, _, err := p.fetch(ctx)
		if err != nil {
			return err
		}

		items, byKey := p.rows(issues)
		scope, _ := p.scopePrefix(ctx)
		if len(items) == 0 {
			// Empty scope is a dead end in a picker — offer the way out inline
			// instead of just showing nothing.
			items = append(items, ui.PickerItem{
				Key:     "",
				Display: "(nothing here — press o to widen the scope)",
				Cells:   []string{"", "", "(nothing here — press o to widen the scope)", "", "", ""},
			})
		}

		picker := &ui.TablePicker{
			Headers:        ghPickerHeaders,
			Extras:         p.extras(),
			Subtitle:       p.subtitle(scope, len(byKey)),
			InitialFilter:  filter,
			ColumnPriority: ghPickerColumnPriority(),
		}
		choice, err := picker.Pick(ctx, p.title(), items)
		if err != nil {
			if errors.Is(err, ui.ErrPickerAborted) {
				return nil
			}
			return err
		}
		filter = choice.FilterValue
		picked, hasPick := byKey[choice.Key]

		switch choice.ExtraAction {
		case "": // Enter → open in the browser
			if !hasPick {
				continue // placeholder row
			}
			if err := openBrowser(picked.URL); err != nil {
				fmt.Fprintln(out, picked.URL)
			}
			return nil

		case "y":
			if !hasPick {
				continue
			}
			if err := copyToClipboard(picked.URL); err != nil {
				fmt.Fprintln(out, picked.URL)
				return nil
			}
			fmt.Fprintf(errOut, "copied %s\n", picked.URL)
			return nil

		case "c":
			if !hasPick {
				continue
			}
			if !picked.IsPR {
				fmt.Fprintf(errOut, "not a pull request: #%d — checkout applies to PRs only\n", picked.Number)
				continue
			}
			remote := p.cfg.Remote
			if remote == "" {
				remote = "origin"
			}
			msg, err := prCheckoutWith(ctx, p.runner, remote, fmt.Sprintf("pr/%d", picked.Number), picked.Number)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, msg)
			return nil

		case "o":
			p.cycleScope()
			continue

		case "a":
			if p.filters.state == "all" {
				p.filters.state = "open"
			} else {
				p.filters.state = "all"
			}
			continue
		}
	}
}

func (p *ghPicker) title() string {
	switch {
	case p.kind == "inbox":
		return "inbox"
	case p.isPR:
		return "pr"
	default:
		return "issue"
	}
}

// newGHPicker assembles a picker session from the resolved command state.
func newGHPicker(cmd *cobra.Command, client *ghapi.Client, runner *git.ExecRunner, cfg *config.Config, isPR bool, kind string, filters githubSearchFilters) *ghPicker {
	return &ghPicker{
		cmd:     cmd,
		client:  client,
		runner:  runner,
		cfg:     cfg,
		isPR:    isPR,
		kind:    kind,
		filters: filters,
		sort:    stringFlag(cmd, "sort"),
		limit:   intFlag(cmd, "limit"),
	}
}
