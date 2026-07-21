package provider

import (
	"strings"
	"testing"
)

func TestBuildClassifyUserPrompt_RenamedFileShowsOrig(t *testing.T) {
	in := ClassifyInput{
		Files: []FileChange{
			{Path: "new.go", Status: "renamed", Added: 8, Deleted: 3, OrigPath: "old.go"},
			{Path: "regular.go", Status: "modified", Added: 2, Deleted: 1},
		},
		Lang:         "en",
		AllowedTypes: []string{"feat", "refactor"},
	}
	prompt := buildClassifyUserPrompt(in, "")

	if !strings.Contains(prompt, "1. new.go [renamed, +8/-3 from old.go]") {
		t.Errorf("prompt missing renamed entry: %q", prompt)
	}
	if !strings.Contains(prompt, "2. regular.go [modified, +2/-1]") {
		t.Errorf("prompt missing regular entry: %q", prompt)
	}
	if !strings.Contains(prompt, `"files":[1,2]`) {
		t.Errorf("prompt should ask for index-referenced files: %q", prompt)
	}
}
