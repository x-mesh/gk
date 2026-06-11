package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/aicommit"
	"github.com/x-mesh/gk/internal/commitlint"
)

// gk commit --plan is the declarative counterpart to the AI-driven commit:
// the caller (LLM or human) hands gk a JSON plan that groups working-tree
// files into commits with pre-written messages, instead of letting gk
// classify and compose. Judgment stays with the caller, but gk still
// validates the plan against the real working tree and commitlint rules
// before anything is committed — a plan is checked all-or-nothing at the
// input boundary, mirroring batch/rebase --plan.

// commitPlanJSON is the on-the-wire plan: a schema marker plus the ordered
// commits to create.
type commitPlanJSON struct {
	Schema  int                   `json:"schema"`
	Commits []commitPlanEntryJSON `json:"commits"`
}

// commitPlanEntryJSON is one commit in the plan.
type commitPlanEntryJSON struct {
	// Message is the full commit message header (and optional body) in
	// Conventional Commits form — validated by commitlint, not gk.
	Message string `json:"message"`
	// Files are the working-tree paths this commit should capture. Each
	// file may appear in exactly one entry across the whole plan.
	Files []string `json:"files"`
	// AllowEmpty permits an entry with no files (an empty commit). Off by
	// default so a missing/empty files list is treated as a mistake.
	AllowEmpty bool `json:"allow_empty,omitempty"`

	// Status and Kind are informational fields emitted by --plan-template
	// (file change kind, etc.). They are accepted on input but ignored, so a
	// template can round-trip straight back as a plan without DisallowUnknownFields
	// rejecting it (same pattern as rebase --plan's subject/pushed).
	Status string `json:"status,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

// readCommitPlan decodes a commit plan from r. The plan is object-only and
// unknown fields are rejected, so a typo'd key is a hard error rather than a
// silently dropped instruction. The caller (t6) is responsible for opening
// the "-" (stdin) / file path before handing us the reader.
func readCommitPlan(r io.Reader) (commitPlanJSON, error) {
	var plan commitPlanJSON
	raw, err := io.ReadAll(r)
	if err != nil {
		return plan, fmt.Errorf("commit plan: read plan: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if derr := dec.Decode(&plan); derr != nil {
		return plan, WithHint(
			fmt.Errorf("commit plan: invalid plan JSON: %v", derr),
			"expected {\"schema\":1,\"commits\":[{\"message\":\"feat(x): subject\",\"files\":[\"a.go\"]}]} — draft one with gk commit --plan-template",
		)
	}
	return plan, nil
}

// validateCommitPlan rejects a plan that could not commit as written. dirty is
// the set of paths with a working-tree change (the universe the plan may draw
// from); rules are the commitlint rules built from config.Commit. The checks:
//
//   - the plan must contain at least one commit;
//   - every entry needs a non-empty message and (unless allow_empty) files;
//   - a file may appear in at most one entry — overlapping commits are a mistake;
//   - every named file must actually have a working-tree change;
//   - every message must pass commitlint (header parse + rules).
//
// Files in dirty that the plan never mentions are left alone on purpose: the
// dirty set is an open universe, so unlike rebase --plan there is no
// "address every entry exactly once" invariant — uncovered changes simply stay
// in the working tree.
func validateCommitPlan(plan commitPlanJSON, dirty map[string]bool, rules commitlint.Rules) error {
	if plan.Schema != 0 && plan.Schema != 1 {
		return fmt.Errorf("commit plan: unsupported plan schema %d (want 1)", plan.Schema)
	}
	if len(plan.Commits) == 0 {
		return fmt.Errorf("commit plan: no commits in plan")
	}

	// Track the owning entry of each file so a duplicate across entries is
	// reported as the file conflict it is, not a vague count.
	seen := make(map[string]bool)
	for i, e := range plan.Commits {
		if e.Message == "" {
			return fmt.Errorf("commit plan: entry %d: message is required", i+1)
		}
		if len(e.Files) == 0 && !e.AllowEmpty {
			return fmt.Errorf("commit plan: entry %d: files is empty (set allow_empty to commit anyway)", i+1)
		}
		for _, f := range e.Files {
			if seen[f] {
				return WithHint(
					fmt.Errorf("commit plan: file %q appears in more than one commit", f),
					"each file must appear exactly once across the plan",
				)
			}
			seen[f] = true
			if !dirty[f] {
				return WithHint(
					fmt.Errorf("commit plan: file %q has no working-tree change", f),
					hintCommand("gk commit --plan-template"),
				)
			}
		}

		// Message must be a clean Conventional Commit per the repo's rules.
		msg := commitlint.Parse(e.Message)
		if issues := commitlint.Lint(msg, rules); len(issues) > 0 {
			iss := issues[0]
			return fmt.Errorf("commit plan: entry %d: message %s: %s", i+1, iss.Code, iss.Message)
		}
	}
	return nil
}

// planToMessages turns a validated commit plan into the []aicommit.Message that
// aicommit.ApplyMessages consumes. Each entry's raw message is split with
// commitlint.Parse — ApplyMessages re-assembles the header from
// Group.Type/Scope (+ Breaking) and Subject via formatCommitMessage, so the raw
// message can't be injected verbatim; the parsed parts must be mapped instead.
//
// What carries through Parse:
//   - Type/Scope/Subject → provider.Group + Message.Subject.
//   - Breaking ("!" marker OR a "BREAKING CHANGE" footer) → Message.Breaking,
//     which Header() re-emits as "!"; without this the "!" would be dropped and
//     the committed header would differ from the plan.
//   - Body → Message.Body.
//   - Footers (commitlint.Footer{Token,Value}) → []provider.Footer (identical
//     shape). The "BREAKING CHANGE" footer is preserved verbatim in Footers in
//     addition to setting Breaking, matching how a hand-written message reads.
//
// AllowEmpty maps to Message.AllowEmpty: ApplyMessages honours it only for an
// empty Files list, committing with `git commit --allow-empty`. A non-empty
// Files list ignores the flag and stages/commits the pathspecs as usual.
//
// Callers must validate the plan (validateCommitPlan) before converting; this
// function assumes messages already parse cleanly and does no error reporting.
func planToMessages(plan commitPlanJSON) []aicommit.Message {
	msgs := make([]aicommit.Message, 0, len(plan.Commits))
	for _, e := range plan.Commits {
		m := commitlint.Parse(e.Message)
		footers := make([]provider.Footer, 0, len(m.Footers))
		for _, f := range m.Footers {
			footers = append(footers, provider.Footer{Token: f.Token, Value: f.Value})
		}
		msgs = append(msgs, aicommit.Message{
			Group: provider.Group{
				Type:  m.Type,
				Scope: m.Scope,
				Files: e.Files,
			},
			Subject:    m.Subject,
			Body:       m.Body,
			Footers:    footers,
			Breaking:   m.Breaking,
			AllowEmpty: e.AllowEmpty,
		})
	}
	return msgs
}
