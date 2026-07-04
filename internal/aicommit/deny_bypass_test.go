package aicommit

import (
	"strings"
	"testing"
)

func TestQuotedNestedAPathBypass(t *testing.T) {
	diff := "diff --git \"a/a/secret/.env\" \"b/a/secret/.env\"\n--- \"a/a/secret/.env\"\n+++ \"b/a/secret/.env\"\n@@ -1 +1 @@\n-old\n+new\n"
	out, dropped := FilterDiffByDeny(diff, []string{"a/secret/.env"})
	if strings.Contains(out, "new") {
		t.Errorf("path-specific deny bypassed: out=%q dropped=%v", out, dropped)
	}
	out2, dropped2 := FilterDiffByDeny(diff, []string{".env"})
	t.Logf(".env deny: dropped=%v contains=%v", dropped2, strings.Contains(out2, "new"))
}
