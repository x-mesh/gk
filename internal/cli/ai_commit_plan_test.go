package cli

import (
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/commitlint"
)

// commitPlanRules mirrors how t6 builds rules from config.Commit: a small set
// of allowed types so the type-enum rule has teeth in the lint cases.
var commitPlanRules = commitlint.Rules{
	AllowedTypes:     []string{"feat", "fix", "chore"},
	MaxSubjectLength: 72,
}

func TestCommitPlan_Read(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr string // substring; "" = expect success
		check   func(t *testing.T, p commitPlanJSON)
	}{
		{
			name:  "valid",
			input: `{"schema":1,"commits":[{"message":"feat(x): subject","files":["a.go"],"allow_empty":false}]}`,
			check: func(t *testing.T, p commitPlanJSON) {
				if p.Schema != 1 || len(p.Commits) != 1 {
					t.Fatalf("got %+v", p)
				}
				if p.Commits[0].Message != "feat(x): subject" || p.Commits[0].Files[0] != "a.go" {
					t.Fatalf("entry not parsed: %+v", p.Commits[0])
				}
			},
		},
		{
			name:  "informational status/kind ignored",
			input: `{"schema":1,"commits":[{"message":"feat(x): s","files":["a.go"],"status":"M","kind":"code"}]}`,
			check: func(t *testing.T, p commitPlanJSON) {
				if p.Commits[0].Status != "M" || p.Commits[0].Kind != "code" {
					t.Fatalf("status/kind not captured: %+v", p.Commits[0])
				}
			},
		},
		{
			name:    "unknown field rejected",
			input:   `{"schema":1,"commits":[{"message":"feat(x): s","files":["a.go"],"bogus":true}]}`,
			wantErr: "invalid plan JSON",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := readCommitPlan(strings.NewReader(tc.input))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.check != nil {
				tc.check(t, p)
			}
		})
	}
}

func TestCommitPlan_Validate(t *testing.T) {
	// dirty universe shared by the table; an entry referencing anything else
	// is "no working-tree change".
	dirty := map[string]bool{"a.go": true, "b.go": true, "c.go": true}

	entry := func(msg string, files ...string) commitPlanEntryJSON {
		return commitPlanEntryJSON{Message: msg, Files: files}
	}
	plan := func(schema int, entries ...commitPlanEntryJSON) commitPlanJSON {
		return commitPlanJSON{Schema: schema, Commits: entries}
	}

	cases := []struct {
		name    string
		plan    commitPlanJSON
		wantErr string // substring; "" = expect success
	}{
		{
			name: "valid single commit",
			plan: plan(1, entry("feat(x): subject", "a.go")),
		},
		{
			name: "schema 0 accepted",
			plan: plan(0, entry("feat(x): subject", "a.go")),
		},
		{
			name:    "schema 2 rejected",
			plan:    plan(2, entry("feat(x): subject", "a.go")),
			wantErr: "unsupported plan schema 2",
		},
		{
			name:    "empty plan",
			plan:    plan(1),
			wantErr: "no commits in plan",
		},
		{
			name:    "empty message",
			plan:    plan(1, entry("", "a.go")),
			wantErr: "entry 1: message is required",
		},
		{
			name:    "empty files, allow_empty false",
			plan:    plan(1, entry("feat(x): subject")),
			wantErr: "files is empty (set allow_empty",
		},
		{
			name: "empty files, allow_empty true",
			plan: plan(1, commitPlanEntryJSON{Message: "feat(x): subject", AllowEmpty: true}),
		},
		{
			name: "duplicate file across entries",
			plan: plan(1,
				entry("feat(x): one", "a.go"),
				entry("fix(y): two", "a.go"),
			),
			wantErr: `file "a.go" appears in more than one commit`,
		},
		{
			name:    "file with no working-tree change",
			plan:    plan(1, entry("feat(x): subject", "ghost.go")),
			wantErr: `file "ghost.go" has no working-tree change`,
		},
		{
			name:    "lint violation: malformed header",
			plan:    plan(1, entry("bad subject", "a.go")),
			wantErr: "header-invalid",
		},
		{
			name:    "lint violation: disallowed type",
			plan:    plan(1, entry("wibble(x): subject", "a.go")),
			wantErr: "type-enum",
		},
		{
			name: "informational status field passes through validation",
			plan: plan(1, commitPlanEntryJSON{Message: "feat(x): subject", Files: []string{"a.go"}, Status: "M", Kind: "code"}),
		},
		{
			name: "uncovered dirty files are allowed",
			plan: plan(1, entry("feat(x): subject", "a.go")), // b.go / c.go untouched
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCommitPlan(tc.plan, dirty, commitPlanRules)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestCommitPlan_DuplicateAndMissingHints asserts the WithHint/hintCommand
// decorations the agent envelope surfaces as remediation.
func TestCommitPlan_DuplicateAndMissingHints(t *testing.T) {
	dirty := map[string]bool{"a.go": true}

	dup := commitPlanJSON{Schema: 1, Commits: []commitPlanEntryJSON{
		{Message: "feat(x): one", Files: []string{"a.go"}},
		{Message: "fix(y): two", Files: []string{"a.go"}},
	}}
	if h := HintFrom(validateCommitPlan(dup, dirty, commitPlanRules)); !strings.Contains(h, "exactly once") {
		t.Fatalf("duplicate hint = %q", h)
	}

	missing := commitPlanJSON{Schema: 1, Commits: []commitPlanEntryJSON{
		{Message: "feat(x): one", Files: []string{"ghost.go"}},
	}}
	if h := HintFrom(validateCommitPlan(missing, dirty, commitPlanRules)); !strings.Contains(h, "gk commit --plan-template") {
		t.Fatalf("missing-file hint = %q", h)
	}
}

// TestPlanToMessages covers the plan→[]aicommit.Message conversion: header
// parts split out of the raw message, Files carried from the entry, Breaking
// and Footers preserved, and AllowEmpty mapped through.
func TestPlanToMessages(t *testing.T) {
	plan := commitPlanJSON{Schema: 1, Commits: []commitPlanEntryJSON{
		{
			Message: "feat(api): add v2 endpoint\n\nDetailed body here.\n\nRefs: #42",
			Files:   []string{"api.go", "api_test.go"},
		},
		{
			Message: "feat(core)!: drop legacy mode\n\nBREAKING CHANGE: removes the v1 flag",
			Files:   []string{"core.go"},
		},
		{
			Message:    "chore: trigger ci",
			AllowEmpty: true,
		},
	}}

	msgs := planToMessages(plan)
	if len(msgs) != 3 {
		t.Fatalf("len(msgs) = %d, want 3", len(msgs))
	}

	// Entry 1: type/scope/subject/body/footer, files carried, not breaking.
	m0 := msgs[0]
	if m0.Group.Type != "feat" || m0.Group.Scope != "api" {
		t.Errorf("m0 group = %q(%q), want feat(api)", m0.Group.Type, m0.Group.Scope)
	}
	if m0.Subject != "add v2 endpoint" {
		t.Errorf("m0 subject = %q", m0.Subject)
	}
	if m0.Body != "Detailed body here." {
		t.Errorf("m0 body = %q", m0.Body)
	}
	if len(m0.Group.Files) != 2 || m0.Group.Files[0] != "api.go" {
		t.Errorf("m0 files = %v", m0.Group.Files)
	}
	if len(m0.Footers) != 1 || m0.Footers[0].Token != "Refs" || m0.Footers[0].Value != "#42" {
		t.Errorf("m0 footers = %+v", m0.Footers)
	}
	if m0.Breaking {
		t.Errorf("m0 should not be breaking")
	}
	if m0.AllowEmpty {
		t.Errorf("m0 should not be allow-empty")
	}

	// Entry 2: "!" marker AND BREAKING CHANGE footer both set Breaking; the
	// header must round-trip with the "!".
	m1 := msgs[1]
	if !m1.Breaking {
		t.Errorf("m1 must be breaking (got %+v)", m1)
	}
	if got := m1.Header(); got != "feat(core)!: drop legacy mode" {
		t.Errorf("m1 header = %q, want feat(core)!: drop legacy mode", got)
	}
	// The BREAKING CHANGE footer is preserved verbatim too.
	var sawBreakingFooter bool
	for _, f := range m1.Footers {
		if strings.EqualFold(f.Token, "BREAKING CHANGE") {
			sawBreakingFooter = true
		}
	}
	if !sawBreakingFooter {
		t.Errorf("m1 BREAKING CHANGE footer not preserved: %+v", m1.Footers)
	}

	// Entry 3: empty files + AllowEmpty.
	m2 := msgs[2]
	if !m2.AllowEmpty {
		t.Errorf("m2 must be allow-empty")
	}
	if len(m2.Group.Files) != 0 {
		t.Errorf("m2 files = %v, want empty", m2.Group.Files)
	}
}
