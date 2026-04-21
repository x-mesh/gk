package commitlint

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Parse tests
// ---------------------------------------------------------------------------

func TestParse(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		wantType    string
		wantScope   string
		wantBreak   bool
		wantSubject string
		wantValid   bool
		wantBody    string
		checkFooter func(t *testing.T, footers []Footer)
	}{
		{
			name:        "simple feat",
			input:       "feat: add new feature",
			wantType:    "feat",
			wantScope:   "",
			wantBreak:   false,
			wantSubject: "add new feature",
			wantValid:   true,
		},
		{
			name:        "feat with scope",
			input:       "feat(auth): add OAuth support",
			wantType:    "feat",
			wantScope:   "auth",
			wantBreak:   false,
			wantSubject: "add OAuth support",
			wantValid:   true,
		},
		{
			name:        "fix breaking bang",
			input:       "fix!: drop Node 18",
			wantType:    "fix",
			wantScope:   "",
			wantBreak:   true,
			wantSubject: "drop Node 18",
			wantValid:   true,
		},
		{
			name:        "fix with scope and breaking",
			input:       "fix(api)!: remove deprecated endpoint",
			wantType:    "fix",
			wantScope:   "api",
			wantBreak:   true,
			wantSubject: "remove deprecated endpoint",
			wantValid:   true,
		},
		{
			name:        "chore simple",
			input:       "chore: bump deps",
			wantType:    "chore",
			wantScope:   "",
			wantBreak:   false,
			wantSubject: "bump deps",
			wantValid:   true,
		},
		{
			name:        "header only no body",
			input:       "docs: update readme",
			wantType:    "docs",
			wantSubject: "update readme",
			wantValid:   true,
			wantBody:    "",
		},
		{
			name:  "header with body",
			input: "feat: new thing\n\nThis is the body text.\nMore body.",
			wantType:    "feat",
			wantSubject: "new thing",
			wantValid:   true,
			wantBody:    "This is the body text.\nMore body.",
		},
		{
			name: "header body and signed-off-by footer",
			input: "fix: correct typo\n\nDetailed explanation.\n\nSigned-off-by: Alice <alice@example.com>",
			wantType:    "fix",
			wantSubject: "correct typo",
			wantValid:   true,
			checkFooter: func(t *testing.T, footers []Footer) {
				t.Helper()
				if len(footers) != 1 {
					t.Fatalf("expected 1 footer, got %d", len(footers))
				}
				if footers[0].Token != "Signed-off-by" {
					t.Errorf("footer token = %q", footers[0].Token)
				}
			},
		},
		{
			name: "BREAKING CHANGE footer sets Breaking=true",
			input: "feat: new api\n\nSome body.\n\nBREAKING CHANGE: old endpoint removed",
			wantType:    "feat",
			wantSubject: "new api",
			wantValid:   true,
			wantBreak:   true,
			checkFooter: func(t *testing.T, footers []Footer) {
				t.Helper()
				if len(footers) != 1 {
					t.Fatalf("expected 1 footer, got %d", len(footers))
				}
				if !strings.EqualFold(footers[0].Token, "breaking change") {
					t.Errorf("footer token = %q", footers[0].Token)
				}
			},
		},
		{
			name:      "header invalid: no colon",
			input:     "no colon here",
			wantValid: false,
		},
		{
			name:      "header invalid: colon but no type",
			input:     ": no type",
			wantValid: false,
		},
		{
			name:      "header invalid: space instead of colon",
			input:     "feat add X",
			wantValid: false,
		},
		{
			name:      "header invalid: empty",
			input:     "",
			wantValid: false,
		},
		{
			name:        "type with hyphen",
			input:       "build-ci: run pipeline",
			wantType:    "build-ci",
			wantSubject: "run pipeline",
			wantValid:   true,
		},
		{
			name:      "subject missing after colon space",
			input:     "feat: ",
			wantValid: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Parse(tc.input)
			if m.HeaderValid != tc.wantValid {
				t.Fatalf("HeaderValid = %v, want %v (raw=%q)", m.HeaderValid, tc.wantValid, tc.input)
			}
			if !tc.wantValid {
				return
			}
			if m.Type != tc.wantType {
				t.Errorf("Type = %q, want %q", m.Type, tc.wantType)
			}
			if m.Scope != tc.wantScope {
				t.Errorf("Scope = %q, want %q", m.Scope, tc.wantScope)
			}
			if m.Breaking != tc.wantBreak {
				t.Errorf("Breaking = %v, want %v", m.Breaking, tc.wantBreak)
			}
			if m.Subject != tc.wantSubject {
				t.Errorf("Subject = %q, want %q", m.Subject, tc.wantSubject)
			}
			if tc.wantBody != "" && m.Body != tc.wantBody {
				t.Errorf("Body = %q, want %q", m.Body, tc.wantBody)
			}
			if tc.checkFooter != nil {
				tc.checkFooter(t, m.Footers)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Lint tests
// ---------------------------------------------------------------------------

var defaultRules = Rules{
	AllowedTypes:     []string{"feat", "fix", "chore", "docs", "style", "refactor", "perf", "test", "build", "ci", "revert"},
	ScopeRequired:    false,
	MaxSubjectLength: 72,
}

func TestLint_Valid(t *testing.T) {
	m := Parse("feat: add something nice")
	issues := Lint(m, defaultRules)
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestLint_HeaderInvalid(t *testing.T) {
	m := Parse("not a valid header")
	issues := Lint(m, defaultRules)
	if len(issues) == 0 {
		t.Fatal("expected issues, got none")
	}
	if issues[0].Code != "header-invalid" {
		t.Errorf("Code = %q, want header-invalid", issues[0].Code)
	}
	// Must be the only issue (short-circuit).
	if len(issues) != 1 {
		t.Errorf("expected exactly 1 issue, got %d", len(issues))
	}
}

func TestLint_TypeEnum(t *testing.T) {
	m := Parse("random: do something")
	issues := Lint(m, defaultRules)
	if !hasCode(issues, "type-enum") {
		t.Errorf("expected type-enum issue, got %v", issues)
	}
}

func TestLint_TypeEnumCaseInsensitive(t *testing.T) {
	m := Parse("FEAT: do something")
	// FEAT should be allowed even though allowed list has lowercase "feat"
	issues := Lint(m, defaultRules)
	if hasCode(issues, "type-enum") {
		t.Errorf("should accept case-insensitive type match, but got type-enum issue")
	}
}

func TestLint_ScopeRequired(t *testing.T) {
	rules := defaultRules
	rules.ScopeRequired = true
	m := Parse("feat: no scope here")
	issues := Lint(m, rules)
	if !hasCode(issues, "scope-required") {
		t.Errorf("expected scope-required issue, got %v", issues)
	}
}

func TestLint_ScopeRequired_Satisfied(t *testing.T) {
	rules := defaultRules
	rules.ScopeRequired = true
	m := Parse("feat(api): has scope")
	issues := Lint(m, rules)
	if hasCode(issues, "scope-required") {
		t.Errorf("scope-required should not fire when scope is present")
	}
}

func TestLint_SubjectEmpty(t *testing.T) {
	// Constructing a message where subject is empty directly (header regex
	// won't match "feat: " but we can test via an artificially crafted Message).
	m := Message{HeaderValid: true, Type: "feat", Subject: ""}
	issues := Lint(m, defaultRules)
	if !hasCode(issues, "subject-empty") {
		t.Errorf("expected subject-empty issue, got %v", issues)
	}
}

func TestLint_SubjectMaxLength(t *testing.T) {
	long := strings.Repeat("x", 73)
	m := Parse("feat: " + long)
	issues := Lint(m, defaultRules)
	if !hasCode(issues, "subject-max-length") {
		t.Errorf("expected subject-max-length issue, got %v", issues)
	}
}

func TestLint_SubjectMaxLength_Exact(t *testing.T) {
	exact := strings.Repeat("x", 72)
	m := Parse("feat: " + exact)
	issues := Lint(m, defaultRules)
	if hasCode(issues, "subject-max-length") {
		t.Errorf("72-char subject should not exceed max-length of 72")
	}
}

func TestLint_MaxSubjectLength_Zero_NoLimit(t *testing.T) {
	rules := defaultRules
	rules.MaxSubjectLength = 0
	long := strings.Repeat("x", 200)
	m := Parse("feat: " + long)
	issues := Lint(m, rules)
	if hasCode(issues, "subject-max-length") {
		t.Errorf("MaxSubjectLength=0 should impose no limit")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func hasCode(issues []Issue, code string) bool {
	for _, iss := range issues {
		if iss.Code == code {
			return true
		}
	}
	return false
}
