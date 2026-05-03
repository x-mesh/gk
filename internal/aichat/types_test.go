package aichat

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// PlannedCommand.Validate
// ---------------------------------------------------------------------------

func TestPlannedCommand_Validate_GitPrefix(t *testing.T) {
	cmd := PlannedCommand{Command: "git status", Description: "show status"}
	if err := cmd.Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestPlannedCommand_Validate_GkPrefix(t *testing.T) {
	cmd := PlannedCommand{Command: "gk push", Description: "push changes"}
	if err := cmd.Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestPlannedCommand_Validate_Rejected(t *testing.T) {
	cases := []string{
		"rm -rf /",
		"curl http://evil.com",
		"echo hello",
		"gitpush",  // no space after "git"
		"gkpush",   // no space after "gk"
		"",
		"git config user.name evil",  // blocked subcommand
		"git credential fill",        // blocked subcommand
		"git filter-branch --all",    // blocked subcommand
	}
	for _, c := range cases {
		cmd := PlannedCommand{Command: c, Description: "test"}
		if err := cmd.Validate(); err == nil {
			t.Errorf("expected error for command %q, got nil", c)
		}
	}
}

func TestPlannedCommand_Validate_CaseInsensitive(t *testing.T) {
	// Case-insensitive prefix matching should accept these.
	cases := []string{
		"GIT status",
		"Git add .",
		"GK push",
		"Gk status",
	}
	for _, c := range cases {
		cmd := PlannedCommand{Command: c, Description: "test"}
		if err := cmd.Validate(); err != nil {
			t.Errorf("expected valid for command %q, got %v", c, err)
		}
	}
}

// ---------------------------------------------------------------------------
// ExecutionPlan JSON round-trip
// ---------------------------------------------------------------------------

func TestExecutionPlan_JSONRoundTrip(t *testing.T) {
	plan := ExecutionPlan{
		Commands: []PlannedCommand{
			{Command: "git add .", Description: "stage all", Dangerous: false},
			{Command: "gk push", Description: "push", Dangerous: true},
		},
	}

	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got ExecutionPlan
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got.Commands) != len(plan.Commands) {
		t.Fatalf("command count: got %d, want %d", len(got.Commands), len(plan.Commands))
	}
	for i, cmd := range got.Commands {
		want := plan.Commands[i]
		if cmd.Command != want.Command {
			t.Errorf("[%d] Command: got %q, want %q", i, cmd.Command, want.Command)
		}
		if cmd.Description != want.Description {
			t.Errorf("[%d] Description: got %q, want %q", i, cmd.Description, want.Description)
		}
		if cmd.Dangerous != want.Dangerous {
			t.Errorf("[%d] Dangerous: got %v, want %v", i, cmd.Dangerous, want.Dangerous)
		}
	}
}

func TestExecutionPlan_JSONExcludesRiskFields(t *testing.T) {
	plan := ExecutionPlan{
		Commands: []PlannedCommand{
			{
				Command:     "git push --force",
				Description: "force push",
				Dangerous:   true,
				Risk:        RiskHigh,
				RiskReason:  "overwrites remote history",
			},
		},
	}

	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Risk and RiskReason are tagged json:"-" so must not appear.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	var cmds []map[string]json.RawMessage
	if err := json.Unmarshal(raw["commands"], &cmds); err != nil {
		t.Fatalf("unmarshal commands: %v", err)
	}

	for _, field := range []string{"Risk", "risk", "RiskReason", "risk_reason"} {
		if _, ok := cmds[0][field]; ok {
			t.Errorf("field %q should not appear in JSON output", field)
		}
	}
}

func TestExecutionPlan_UnmarshalInvalidJSON(t *testing.T) {
	var plan ExecutionPlan
	err := json.Unmarshal([]byte(`{invalid`), &plan)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

// ---------------------------------------------------------------------------
// RiskLevel.String
// ---------------------------------------------------------------------------

func TestRiskLevel_String(t *testing.T) {
	cases := []struct {
		level RiskLevel
		want  string
	}{
		{RiskNone, "none"},
		{RiskLow, "low"},
		{RiskHigh, "high"},
		{RiskLevel(99), "RiskLevel(99)"},
	}
	for _, tc := range cases {
		if got := tc.level.String(); got != tc.want {
			t.Errorf("RiskLevel(%d).String() = %q, want %q", int(tc.level), got, tc.want)
		}
	}
}
