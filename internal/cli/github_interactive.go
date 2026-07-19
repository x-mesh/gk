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
	runner  git.Runner
	cfg     *config.Config
	isPR    bool
	kind    string // live scope: "repo" | "org" | "inbox"
	filters githubSearchFilters
	sort    string
	limit   int

	// orgName is the account the "org" scope points at. It starts from
	// --org/github.owner/origin and is re-pointed by the scope picker, so an
	// explicit `--org acme` is never silently replaced by origin's owner.
	orgName string
	// orgIsUser marks orgName as a personal account, selecting the user:
	// qualifier instead of org:.
	orgIsUser bool

	repoPrefix string // resolved lazily, then cached
	fetchCache map[string]ghPickerFetch
	openURL    func(string) error // test seam; defaults to openBrowser
}

type ghPickerFetch struct {
	issues []ghapi.Issue
	total  int
}

// ghInteractiveLimit keeps a single picker query inside one Search API page.
// Static --list/JSON output retains its existing pagination behavior.
const ghInteractiveLimit = 100

// scopePrefix resolves the search prefix for the current scope, caching the
// repo/org lookups so a scope cycle doesn't re-resolve them every redraw.
func (p *ghPicker) scopePrefix(ctx context.Context) (string, error) {
	switch p.kind {
	case "inbox":
		return "involves:@me", nil
	case "org":
		if p.orgName == "" {
			p.orgName = p.cfg.GitHub.Owner
			if p.orgName == "" {
				p.orgName = originOwner(ctx, *p.cfg, p.runner)
			}
			if p.orgName == "" {
				return "", errors.New("no org to search: set github.owner or run inside a repo whose origin is on GitHub")
			}
			if typ, err := p.client.OwnerType(ctx, p.orgName); err == nil && typ == "User" {
				p.orgIsUser = true
			}
		}
		qualifier := "org"
		if p.orgIsUser {
			qualifier = "user"
		}
		return qualifier + ":" + p.orgName, nil
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

// chooseScope opens the scope layer: a second picker listing this repository,
// the viewer's own account, every org they belong to, and the inbox. Selecting
// one re-points the listing. Aborting leaves the current scope untouched.
//
// Org discovery needs a token; without one the layer still offers the repo and
// whatever owner config/origin resolved, so it degrades instead of vanishing.
func (p *ghPicker) chooseScope(ctx context.Context) error {
	items := []ui.PickerItem{}
	add := func(key, label, note string) {
		items = append(items, ui.PickerItem{Key: key, Display: label, Cells: []string{label, note}})
	}

	if owner, repo, err := currentRepoSlug(ctx, *p.cfg, p.runner); err == nil {
		add("repo", fmt.Sprintf("%s/%s", owner, repo), "this repository")
	}
	if login := p.client.ViewerLogin(ctx); login != "" {
		add("user:"+login, login, "your account")
	}
	orgs, _ := p.client.ListMyOrgs(ctx) // best-effort: empty without a token
	for _, o := range orgs {
		add("org:"+o, o, "organization")
	}
	// Whatever config/origin points at, in case it is not in the org list
	// (e.g. no token, or an org the viewer is not a member of).
	fallback := p.cfg.GitHub.Owner
	if fallback == "" {
		fallback = originOwner(ctx, *p.cfg, p.runner)
	}
	if fallback != "" && !ghScopeListed(items, fallback) {
		add("org:"+fallback, fallback, "from config/origin")
	}
	if p.client.Token != "" {
		add("inbox", "involves:@me", "everything involving you")
	}
	if len(items) == 0 {
		return errors.New("no scopes available: run inside a GitHub repo or set github.owner")
	}

	picker := &ui.TablePicker{
		Headers:  []string{"SCOPE", "WHAT"},
		Subtitle: "pick a scope — enter select · esc keep current",
	}
	choice, err := picker.Pick(ctx, "scope", items)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return nil // keep the current scope
		}
		return err
	}

	switch {
	case choice.Key == "repo":
		p.kind = "repo"
	case choice.Key == "inbox":
		p.kind = "inbox"
	case strings.HasPrefix(choice.Key, "user:"):
		p.kind, p.orgName, p.orgIsUser = "org", strings.TrimPrefix(choice.Key, "user:"), true
	case strings.HasPrefix(choice.Key, "org:"):
		name := strings.TrimPrefix(choice.Key, "org:")
		p.kind, p.orgName, p.orgIsUser = "org", name, false
		// A config/origin fallback may actually be a personal account.
		if typ, err := p.client.OwnerType(ctx, name); err == nil && typ == "User" {
			p.orgIsUser = true
		}
	}
	return nil
}

// ghScopeListed reports whether a scope row for name is already present.
func ghScopeListed(items []ui.PickerItem, name string) bool {
	for _, it := range items {
		if it.Key == "org:"+name || it.Key == "user:"+name {
			return true
		}
	}
	return false
}

func (p *ghPicker) interactiveLimit() int {
	if p.limit > 0 && p.limit < ghInteractiveLimit {
		return p.limit
	}
	return ghInteractiveLimit
}

func (p *ghPicker) fetch(ctx context.Context) ([]ghapi.Issue, string, int, error) {
	prefix, err := p.scopePrefix(ctx)
	if err != nil {
		return nil, "", 0, err
	}
	query := buildSearchQuery(prefix, p.filters)
	limit := p.interactiveLimit()
	key := fmt.Sprintf("%s\x00%s\x00%d", query, p.sort, limit)
	if cached, ok := p.fetchCache[key]; ok {
		return cached.issues, query, cached.total, nil
	}

	issues, total, err := p.client.SearchIssuesWithTotal(ctx, query, p.sort, limit)
	if err != nil {
		return nil, query, 0, fmt.Errorf("github search: %w", err)
	}
	if p.fetchCache == nil {
		p.fetchCache = make(map[string]ghPickerFetch)
	}
	p.fetchCache[key] = ghPickerFetch{issues: issues, total: total}

	// The picker fetch is intentionally capped, so len(issues) is not a safe
	// repository count. total_count remains exact and lets the default
	// interactive path warm the same cache as a static plain-open listing.
	if p.cfg != nil && p.cfg.GitHub.Counts.WarmOnList && p.limit == 0 &&
		p.filters.isPlainOpen() && strings.HasPrefix(prefix, "repo:") {
		warmGitHubCountFromList(ctx, p.runner, strings.TrimPrefix(prefix, "repo:"), p.isPR, total)
	}
	return issues, query, total, nil
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

func (p *ghPicker) subtitle(scope string, shown, total int) string {
	state := p.filters.state
	if state == "" {
		state = "open"
	}
	count := fmt.Sprintf("%d item(s)", shown)
	if total > shown {
		count = fmt.Sprintf("%d of %d item(s) · capped at %d", shown, total, p.interactiveLimit())
	}
	return fmt.Sprintf("%s · %s · %s  —  enter open · c checkout · y copy url · o scope · a open/all", scope, state, count)
}

func shouldRunGHPicker(explicitPick, list, promptsAllowed bool) bool {
	return explicitPick || (promptsAllowed && !list)
}

// runForEnvironment preserves the default non-TTY list behavior while making
// an explicit --pick request use the numbered fallback picker.
func (p *ghPicker) runForEnvironment(ctx context.Context, explicit bool) error {
	if explicit && !ui.IsTerminal() {
		return p.runFallback(ctx)
	}
	return p.run(ctx)
}

func (p *ghPicker) runFallback(ctx context.Context) error {
	// Non-TUI path, so a spinner is safe here (inside p.run the bubbletea
	// program owns the screen and would fight it).
	stop := ui.StartBubbleSpinner("searching GitHub")
	issues, _, _, err := p.fetch(ctx)
	stop()
	if err != nil {
		return err
	}
	items, byKey := p.rows(issues)
	picker := &ui.FallbackPicker{In: p.cmd.InOrStdin(), Out: p.cmd.ErrOrStderr()}
	choice, err := picker.Pick(ctx, p.title(), items)
	if err != nil {
		if errors.Is(err, ui.ErrPickerAborted) {
			return nil
		}
		return err
	}
	picked, ok := byKey[choice.Key]
	if !ok {
		return nil
	}
	return p.openPicked(picked)
}

func (p *ghPicker) openPicked(picked ghapi.Issue) error {
	opener := p.openURL
	if opener == nil {
		opener = openBrowser
	}
	if err := opener(picked.URL); err != nil {
		fmt.Fprintln(p.cmd.OutOrStdout(), picked.URL)
	}
	return nil
}

// run is the interactive loop. An aborted picker (Esc/Ctrl-C) exits quietly.
func (p *ghPicker) run(ctx context.Context) error {
	out, errOut := p.cmd.OutOrStdout(), p.cmd.ErrOrStderr()
	filter := ""

	for {
		issues, _, total, err := p.fetch(ctx)
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
			Subtitle:       p.subtitle(scope, len(byKey), total),
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
			return p.openPicked(picked)

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
			source, local, err := prCheckoutTarget(ctx, p.runner, *p.cfg, picked.Owner, picked.Repo, picked.Number)
			if err != nil {
				return err
			}
			msg, err := prCheckoutWith(ctx, p.runner, source, local, picked.Number)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, msg)
			return nil

		case "o":
			if err := p.chooseScope(ctx); err != nil {
				return err
			}
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

// setScopeFromPrefix seeds the live scope from an already-resolved search
// prefix (what resolveGitHubScope produced for this invocation). Using the
// resolved value — instead of re-deriving it — is what keeps an explicit
// `--org acme` from being replaced by config/origin's owner.
func (p *ghPicker) setScopeFromPrefix(prefix string) {
	switch {
	case strings.HasPrefix(prefix, "org:"):
		p.kind, p.orgName, p.orgIsUser = "org", strings.TrimPrefix(prefix, "org:"), false
	case strings.HasPrefix(prefix, "user:"):
		p.kind, p.orgName, p.orgIsUser = "org", strings.TrimPrefix(prefix, "user:"), true
	case strings.HasPrefix(prefix, "repo:"):
		p.kind, p.repoPrefix = "repo", prefix
	case prefix == "involves:@me":
		p.kind = "inbox"
	}
}

// newGHPicker assembles a picker session from the resolved command state.
func newGHPicker(cmd *cobra.Command, client *ghapi.Client, runner git.Runner, cfg *config.Config, isPR bool, kind string, filters githubSearchFilters) *ghPicker {
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
