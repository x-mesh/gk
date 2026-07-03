package aicommit

import (
	"strings"
	"testing"
)

func TestFilterDiffByDenyRenameOldPath(t *testing.T) {
	diff := "diff --git a/.env b/renamed.txt\nsimilarity index 90%\nrename from .env\nrename to renamed.txt\n--- a/.env\n+++ b/renamed.txt\n@@ -1 +1 @@\n-SECRET=old\n+SECRET=new\n"
	out, dropped := FilterDiffByDeny(diff, []string{".env"})
	if strings.Contains(out, "SECRET=") {
		t.Errorf("rename pre-image .env must be dropped:\n%s", out)
	}
	if len(dropped) != 1 {
		t.Errorf("dropped = %v", dropped)
	}
}

func TestFilterDiffByDenyQuotedPath(t *testing.T) {
	diff := "diff --git \"a/secret env/.env\" \"b/secret env/.env\"\n--- \"a/secret env/.env\"\n+++ \"b/secret env/.env\"\n@@ -1 +1 @@\n-A=1\n+A=2\n"
	out, dropped := FilterDiffByDeny(diff, []string{".env"})
	if strings.Contains(out, "A=2") {
		t.Errorf("quoted path must still match deny:\n%s", out)
	}
	if len(dropped) != 1 {
		t.Errorf("dropped = %v", dropped)
	}
}

func TestFilterDiffByDenyCombinedDiff(t *testing.T) {
	diff := "commit abc\nMerge: 1 2\n\n    merge msg\n\ndiff --cc .env\nindex 1,2..3\n--- a/.env\n+++ b/.env\n@@@ -1,1 -1,1 +1,1 @@@\n- SECRET=a\n +SECRET=merged\ndiff --cc ok.go\nindex 4,5..6\n--- a/ok.go\n+++ b/ok.go\n@@@ -1,1 -1,1 +1,1 @@@\n +package ok\n"
	out, dropped := FilterDiffByDeny(diff, []string{".env"})
	if strings.Contains(out, "SECRET=merged") {
		t.Errorf("diff --cc block for denied file must be dropped:\n%s", out)
	}
	if !strings.Contains(out, "package ok") {
		t.Errorf("allowed --cc block must survive:\n%s", out)
	}
	if !strings.Contains(out, "merge msg") {
		t.Errorf("commit header must survive:\n%s", out)
	}
	if len(dropped) != 1 {
		t.Errorf("dropped = %v", dropped)
	}
}
