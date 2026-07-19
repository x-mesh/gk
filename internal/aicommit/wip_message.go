package aicommit

import (
	"fmt"

	"github.com/x-mesh/gk/internal/ai/provider"
)

// WIPCommitType is the Conventional-Commit type used for checkpoint commits
// created by `gk commit --wip`. It is deliberately the bare "WIP" spelling
// that DetectWIPChain's baked-in `^[Ww][Ii][Pp]\b` pattern matches, so a
// later plain `gk commit` folds the whole checkpoint chain into real commits.
// That round trip is the entire point of the mode: cheap saves now, one
// semantic history later.
const WIPCommitType = "WIP"

// MarkAsWIP rewrites composed messages so their header reads
// "WIP(scope): <summary>" instead of "feat(scope): <summary>".
//
// Why rewrite instead of asking the model for a WIP header directly: Compose
// runs the full commitlint retry loop against the configured type list, which
// does not (and should not) contain "WIP". Letting the model produce a normal
// Conventional Commit and swapping the type afterwards keeps the prompt, the
// validation, and every provider adapter untouched — the checkpoint mode is a
// post-processing step, not a second prompt dialect.
//
// The subject is stripped of its original "<type>(<scope>): " prefix first.
// Header() strips only a prefix matching the CURRENT type, so without this a
// subject the model spelled out in full would surface twice:
// "WIP(remote): feat(remote): add the thing".
func MarkAsWIP(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		m.Subject = stripConventionalPrefix(m.Subject, m.Group.Type, m.Group.Scope)
		m.Group.Type = WIPCommitType
		// A checkpoint is not a release-visible change, so a breaking marker
		// on it would be noise — and worse, it would survive into the unwrap
		// where the real commit's own analysis decides breaking-ness.
		m.Breaking = false
		out = append(out, m)
	}
	return out
}

// FallbackWIPMessage builds the checkpoint commit used when the AI path is
// unavailable — no provider configured, preflight failed, or compose errored
// out. It carries no summary beyond the file count because there is nothing
// trustworthy to summarise with.
//
// The contract this exists to uphold: `gk commit --wip` NEVER loses work to a
// provider outage. An automated checkpoint that refuses to commit because a
// model was down is strictly worse than one with a dull message.
func FallbackWIPMessage(files []FileChange, scope string) Message {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	noun := "files"
	if len(paths) == 1 {
		noun = "file"
	}
	return Message{
		Group: provider.Group{
			Type:  WIPCommitType,
			Scope: scope,
			Files: paths,
		},
		Subject: fmt.Sprintf("checkpoint — %d %s (no AI summary)", len(paths), noun),
	}
}
