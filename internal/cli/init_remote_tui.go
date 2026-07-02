package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/ui"
)

// Sentinel picker keys for the two non-profile rows. NUL-prefixed so
// they can never collide with a user-defined clone.hosts alias.
const (
	remotePickDirect = "\x00direct"
	remotePickSkip   = "\x00skip"
)

// remoteNameToken stands in for the not-yet-chosen project name inside
// the per-profile URL previews. Replaced with "<name>" for display.
const remoteNameToken = "__gk_name__"

// remoteTUIInput carries what the interactive remote step needs.
// Plan, when non-nil, was already decided (a --remote flag or an
// existing origin) and the TUI only echoes it in the summary; nil means
// the account picker + name prompt run.
type remoteTUIInput struct {
	Cfg        config.CloneConfig
	Dir        string
	Plan       *remotePlan
	ForceSSH   bool
	ForceHTTPS bool
}

// remoteAccountItems builds the account-picker rows: one per
// owner-bearing clone.hosts profile (in the order the user declared
// them in config, with a resolved URL preview), then "direct…" and
// "skip". With no usable profiles the order flips so "skip" sits under
// the cursor and a bare Enter is a no-op — existing users who never
// registered profiles pay one keystroke, not a detour. Ownerless
// aliases are host shorthands, not account profiles, so they are
// omitted (direct input still covers them). Pure — safe to unit test
// without a TTY.
func remoteAccountItems(cfg config.CloneConfig, forceSSH, forceHTTPS bool) []ui.PickerItem {
	aliases := orderedProfileAliases(cfg)

	items := make([]ui.PickerItem, 0, len(aliases)+2)
	for _, name := range aliases {
		url, _, err := cfg.ResolveURL(name+":"+remoteNameToken, forceSSH, forceHTTPS)
		if err != nil {
			continue
		}
		preview := strings.Replace(url, remoteNameToken, "<name>", 1)
		items = append(items, ui.PickerItem{
			Display: fmt.Sprintf("%-12s %s", name, preview),
			Key:     name,
			Cells:   []string{name, preview},
		})
	}

	direct := ui.PickerItem{
		Display: fmt.Sprintf("%-12s %s", "direct…", "(owner/repo or URL)"),
		Key:     remotePickDirect,
		Cells:   []string{"direct…", "(owner/repo or URL)"},
	}
	skip := ui.PickerItem{
		Display: fmt.Sprintf("%-12s %s", "skip", "(no remote)"),
		Key:     remotePickSkip,
		Cells:   []string{"skip", "(no remote)"},
	}
	if len(items) == 0 {
		return []ui.PickerItem{skip, direct}
	}
	return append(items, direct, skip)
}

// orderedProfileAliases returns the owner-bearing clone.hosts aliases in
// presentation order: config declaration order first (cfg.HostsOrder),
// then any stragglers the order list does not know about (aliases from
// env/git-config layers, or an unreadable file) alphabetically.
func orderedProfileAliases(cfg config.CloneConfig) []string {
	isProfile := func(name string) bool {
		a, ok := cfg.Hosts[name]
		return ok && a.Owner != ""
	}

	seen := map[string]bool{}
	aliases := make([]string, 0, len(cfg.Hosts))
	for _, name := range cfg.HostsOrder {
		if isProfile(name) && !seen[name] {
			seen[name] = true
			aliases = append(aliases, name)
		}
	}

	var rest []string
	for name := range cfg.Hosts {
		if isProfile(name) && !seen[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(aliases, rest...)
}

// promptRemoteTUI runs the interactive account + name steps and returns
// the resulting plan. A nil plan with nil error means the user skipped
// or cancelled the step — per init's convention, cancellation is a
// harmless skip, never an error.
func promptRemoteTUI(ctx context.Context, in *remoteTUIInput) (*remotePlan, error) {
	defaultName := sanitizeRepoName(filepath.Base(in.Dir))

	picked, err := ui.NewPicker().Pick(ctx, "connect origin to", remoteAccountItems(in.Cfg, in.ForceSSH, in.ForceHTTPS))
	if errors.Is(err, ui.ErrPickerAborted) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	switch picked.Key {
	case remotePickSkip:
		return nil, nil

	case remotePickDirect:
		// The direct spec carries its own repo name, so no name prompt.
		// Invalid input re-prompts (the error names what was wrong);
		// Esc backs out of the whole step.
		placeholder := "owner/repo or URL"
		for {
			spec, err := ui.PromptTextTUI(ctx, "remote", placeholder, "")
			if errors.Is(err, ui.ErrPickerAborted) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			plan, berr := buildRemotePlan(in.Cfg, spec, defaultName, in.ForceSSH, in.ForceHTTPS)
			if berr != nil {
				fmt.Fprintf(os.Stderr, "  %v\n", berr)
				continue
			}
			plan.Direct = true
			return plan, nil
		}

	default: // a clone.hosts profile
		name, err := ui.PromptTextTUI(ctx, "project name", "", defaultName)
		if errors.Is(err, ui.ErrPickerAborted) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		plan, berr := buildRemotePlan(in.Cfg, picked.Key, sanitizeRepoName(name), in.ForceSSH, in.ForceHTTPS)
		if berr != nil {
			// A profile that resolved for the preview should not fail
			// here; if it somehow does, degrade to a skipped step.
			fmt.Fprintf(os.Stderr, "  %v\n", berr)
			return nil, nil
		}
		return plan, nil
	}
}

// offerProfileSave proposes storing a direct-entered account as a
// clone.hosts profile in the global config, so the next init is a pick
// instead of typing. Runs only after the remote add succeeded, TTY only.
// Nothing here can fail init — the remote is already wired; persistence
// is a convenience, so every failure degrades to a warning.
func offerProfileSave(ctx context.Context, cmd *cobra.Command, cfg config.CloneConfig, plan *remotePlan, w io.Writer) {
	if plan == nil || !plan.Direct || plan.Meta.Host == "" || plan.Meta.Owner == "" {
		return
	}
	ok, err := ui.ConfirmTUI(ctx, "save this account to config?",
		fmt.Sprintf("%s/%s → clone.hosts (global config)", plan.Meta.Host, plan.Meta.Owner), false)
	if err != nil || !ok {
		return
	}

	for {
		alias, err := ui.PromptTextTUI(ctx, "profile alias", "personal", "")
		if err != nil {
			return // Esc → drop the offer, keep the remote
		}
		if !validProfileAlias(alias) {
			fmt.Fprintln(os.Stderr, "  alias must be ASCII letters, digits, '-' or '_'")
			continue
		}
		if _, exists := cfg.Hosts[alias]; exists {
			fmt.Fprintf(os.Stderr, "  alias %q already exists in clone.hosts\n", alias)
			continue
		}

		path, _, _, perr := configWritePath(cmd, false, true)
		if perr != nil {
			fmt.Fprintf(w, "warn: cannot resolve global config path: %v\n", perr)
			return
		}
		if serr := saveRemoteProfile(path, alias, plan); serr != nil {
			fmt.Fprintf(w, "warn: profile not saved: %v\n", serr)
			return
		}
		fmt.Fprintln(w, successLine("saved", fmt.Sprintf("clone.hosts.%s → %s/%s", alias, plan.Meta.Host, plan.Meta.Owner)))
		return
	}
}
