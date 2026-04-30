package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestDefaultWorkflows_Count verifies exactly 5 default workflows exist.
// Validates: Requirements 6.2
func TestDefaultWorkflows_Count(t *testing.T) {
	if got := len(defaultWorkflows); got != 5 {
		t.Errorf("defaultWorkflows count = %d, want 5", got)
	}
}

// TestDefaultWorkflows_RequiredNames verifies the 5 required workflow names.
// Validates: Requirements 6.2
func TestDefaultWorkflows_RequiredNames(t *testing.T) {
	required := []string{"save", "update", "branch-work", "resolve-conflict", "undo"}
	names := make(map[string]bool, len(defaultWorkflows))
	for _, wf := range defaultWorkflows {
		names[wf.Name] = true
	}
	for _, name := range required {
		if !names[name] {
			t.Errorf("required workflow %q not found in defaultWorkflows", name)
		}
	}
}

// TestFindWorkflow_Exists verifies findWorkflow returns the correct workflow
// for each valid name.
// Validates: Requirements 6.7
func TestFindWorkflow_Exists(t *testing.T) {
	for _, wf := range defaultWorkflows {
		got, err := findWorkflow(wf.Name)
		if err != nil {
			t.Errorf("findWorkflow(%q) returned error: %v", wf.Name, err)
			continue
		}
		if got.Name != wf.Name {
			t.Errorf("findWorkflow(%q).Name = %q, want %q", wf.Name, got.Name, wf.Name)
		}
		if got.DisplayName != wf.DisplayName {
			t.Errorf("findWorkflow(%q).DisplayName = %q, want %q", wf.Name, got.DisplayName, wf.DisplayName)
		}
	}
}

// TestFindWorkflow_NotFound verifies findWorkflow returns an error with
// available workflow list for an invalid name.
// Validates: Requirements 6.7
func TestFindWorkflow_NotFound(t *testing.T) {
	_, err := findWorkflow("nonexistent")
	if err == nil {
		t.Fatal("findWorkflow(\"nonexistent\") expected error, got nil")
	}
	msg := err.Error()
	// Error should mention the invalid name.
	if !strings.Contains(msg, "nonexistent") {
		t.Errorf("error should mention invalid name, got: %s", msg)
	}
	// Error should list available workflow names.
	for _, wf := range defaultWorkflows {
		if !strings.Contains(msg, wf.Name) {
			t.Errorf("error should list available workflow %q, got: %s", wf.Name, msg)
		}
	}
}

// TestPrintWorkflowListText verifies non-TTY text output contains all
// workflow names.
// Validates: Requirements 6.5
func TestPrintWorkflowListText(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	printWorkflowListText(cmd)

	output := buf.String()
	for _, wf := range defaultWorkflows {
		if !strings.Contains(output, wf.Name) {
			t.Errorf("text output should contain workflow name %q, got:\n%s", wf.Name, output)
		}
	}
	// Should contain usage hint.
	if !strings.Contains(output, "gk guide") {
		t.Errorf("text output should contain usage hint 'gk guide', got:\n%s", output)
	}
}
