package cli

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/config"
	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/ui"
)

// wipCheckpointScope derives the (single) scope for a checkpoint commit from
// the files in play: the top-level directory that owns the most of them.
//
// A checkpoint is one commit spanning whatever the session touched, so unlike
// the classify path there is no per-group scope to inherit. Picking the
// dominant directory keeps "WIP(remote): …" readable at a glance in `git log`
// without pretending the commit is narrower than it is. Ties break
// alphabetically so the same file set always yields the same scope.
func wipCheckpointScope(files []aicommit.FileChange) string {
	counts := map[string]int{}
	for _, f := range files {
		td := topLevelDirSimple(f.Path)
		if td == "." {
			continue
		}
		counts[td]++
	}
	if len(counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	best := keys[0]
	for _, k := range keys {
		if counts[k] > counts[best] {
			best = k
		}
	}
	return best
}

// wipCheckpointGroup folds every in-scope file into one group.
//
// The type is a placeholder: MarkAsWIP overwrites it with "WIP" after compose
// runs. It still has to be a type the configured commitlint rules accept,
// because Compose validates its own output against them — "chore" is the
// safest member of every default type list.
func wipCheckpointGroup(files []aicommit.FileChange, scope string) provider.Group {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return provider.Group{
		Type:  "chore",
		Scope: scope,
		Files: paths,
	}
}

// composeWIPCheckpoint produces the message list for `gk commit --wip`.
//
// It NEVER returns an error: every AI failure mode — no provider, a compose
// timeout, a malformed response — degrades to FallbackWIPMessage instead of
// propagating. That is the mode's central promise. `gk commit --wip` is built
// to run unattended from an agent Stop hook, where "the model was down so I
// declined to save your work" is the one outcome worse than a dull message.
//
// prov may be nil, which means the provider could not be built at all; the
// compose call is skipped entirely in that case.
func composeWIPCheckpoint(
	ctx context.Context,
	cmd *cobra.Command,
	runner git.Runner,
	prov provider.Provider,
	files []aicommit.FileChange,
	wipCommit wipCommitForAICommit,
	cfg *config.Config,
	ai config.AIConfig,
	aiErr error,
) []aicommit.Message {
	errOut := cmd.ErrOrStderr()
	scope := wipCheckpointScope(files)
	fallback := func(reason string) []aicommit.Message {
		fmt.Fprintf(errOut, "commit: WIP checkpoint without an AI summary (%s)\n", reason)
		return []aicommit.Message{aicommit.FallbackWIPMessage(files, scope)}
	}

	if prov == nil {
		return fallback(fmt.Sprintf("no usable provider: %v", aiErr))
	}

	group := wipCheckpointGroup(files, scope)
	groups := []provider.Group{group}

	diffs, err := collectGroupDiffs(ctx, runner, groups, wipCommit)
	if err != nil {
		return fallback(fmt.Sprintf("diff collection failed: %v", err))
	}
	// Redact before the diff leaves for a remote provider. Local providers
	// no-op inside the gate, so this costs nothing on the common path.
	for k, d := range diffs {
		red, _, pgErr := applyPrivacyGate(cmd, prov, d, ai)
		if pgErr != nil {
			return fallback(fmt.Sprintf("privacy gate: %v", pgErr))
		}
		diffs[k] = red
	}

	fmt.Fprintf(errOut, "commit: summarising %d file(s) as one checkpoint via %s...\n",
		len(files), providerLabel(prov))
	stop := ui.StartBubbleSpinner(fmt.Sprintf("wip compose — %s", providerLabel(prov)))
	messages, err := aicommit.ComposeAll(ctx, prov, groups, diffs, aicommit.ComposeOptions{
		MaxAttempts:      2, // one retry: a checkpoint is not worth three round-trips
		AllowedTypes:     cfg.Commit.Types,
		ScopeRequired:    cfg.Commit.ScopeRequired,
		MaxSubjectLength: cfg.Commit.MaxSubjectLength,
		Lang:             ai.Lang,
		Concurrency:      1, // single group — a worker pool would just add overhead
	})
	stop()
	if err != nil || len(messages) == 0 {
		reason := "provider returned no message"
		if err != nil {
			reason = err.Error()
		}
		return fallback(reason)
	}
	return aicommit.MarkAsWIP(messages)
}
