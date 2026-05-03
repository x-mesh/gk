package aichat

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// codeBlockRe matches a markdown fenced code block (```json ... ``` or ``` ... ```).
var codeBlockRe = regexp.MustCompile("(?s)```(?:json)?\\s*\n?(.*?)\\s*```")

// maxCommandsPerPlan is the maximum number of commands allowed in a
// single execution plan. This prevents AI from generating excessively
// large plans that could be used for abuse.
const maxCommandsPerPlan = 20

// ParseExecutionPlan parses a raw AI response string into an ExecutionPlan.
//
// It handles:
//   - Plain JSON: {"commands":[...]}
//   - JSON wrapped in markdown code fences: ```json ... ```
//   - Whitelist validation: commands must start with "git " or "gk "
//   - Field validation: Command and Description must be non-empty
//   - Maximum command count: capped at maxCommandsPerPlan
//
// Commands that violate the whitelist or have empty fields are silently
// skipped (with a warning appended to the returned plan). The function
// returns an error only when the input is fundamentally unparseable.
func ParseExecutionPlan(raw string) (*ExecutionPlan, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("parse: empty AI response")
	}

	// Try to extract JSON from markdown code fences first.
	jsonStr := extractJSON(trimmed)

	// Attempt to unmarshal into the plan structure.
	var plan ExecutionPlan
	if err := json.Unmarshal([]byte(jsonStr), &plan); err != nil {
		return nil, fmt.Errorf("parse: invalid JSON in AI response: %w", err)
	}

	// Filter commands: skip those with empty fields or non-whitelisted prefixes.
	var valid []PlannedCommand
	for _, cmd := range plan.Commands {
		if strings.TrimSpace(cmd.Command) == "" || strings.TrimSpace(cmd.Description) == "" {
			continue
		}
		if err := cmd.Validate(); err != nil {
			// Non-whitelisted prefix — skip with warning (logged via caller).
			continue
		}
		valid = append(valid, cmd)
	}

	if len(valid) == 0 {
		return nil, fmt.Errorf("parse: no valid commands in AI response (all commands were empty or non-whitelisted)")
	}

	// Cap the number of commands to prevent abuse.
	if len(valid) > maxCommandsPerPlan {
		valid = valid[:maxCommandsPerPlan]
	}

	plan.Commands = valid
	return &plan, nil
}

// extractJSON attempts to extract a JSON string from the raw input.
// If the input contains a markdown code fence, the content inside is returned.
// Otherwise the original (trimmed) input is returned as-is.
func extractJSON(s string) string {
	if m := codeBlockRe.FindStringSubmatch(s); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return s
}
