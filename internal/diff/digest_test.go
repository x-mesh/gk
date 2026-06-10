package diff

import (
	"strings"
	"testing"
)

const digestFixture = `diff --git a/pull.go b/pull.go
index 1111111..2222222 100644
--- a/pull.go
+++ b/pull.go
@@ -10,2 +10,3 @@ func runPull(cmd *cobra.Command) error {
-	old line
+	new line
+	another
@@ -40,1 +41,1 @@ func runPull(cmd *cobra.Command) error {
-	x
+	y
@@ -80,1 +81,1 @@ func helper() {
-	a
+	b
diff --git a/old.go b/new.go
similarity index 95%
rename from old.go
rename to new.go
index 3333333..4444444 100644
--- a/old.go
+++ b/new.go
@@ -1,1 +1,1 @@
-var name = "old"
+var name = "new"
diff --git a/img.png b/img.png
index 5555555..6666666 100644
Binary files a/img.png and b/img.png differ
`

func TestParseUnifiedDiff_FuncName(t *testing.T) {
	res, err := ParseUnifiedDiff(strings.NewReader(digestFixture))
	if err != nil {
		t.Fatal(err)
	}
	h := res.Files[0].Hunks
	if len(h) != 3 || h[0].FuncName != "func runPull(cmd *cobra.Command) error" || h[2].FuncName != "func helper()" {
		t.Errorf("funcnames: %+v", h)
	}
	// 컨텍스트 없는 hunk → 빈 FuncName
	if res.Files[1].Hunks[0].FuncName != "" {
		t.Errorf("empty context must stay empty, got %q", res.Files[1].Hunks[0].FuncName)
	}
}

func TestBuildDigest(t *testing.T) {
	res, err := ParseUnifiedDiff(strings.NewReader(digestFixture))
	if err != nil {
		t.Fatal(err)
	}
	d := BuildDigest(res)

	if d.Stat.Files != 3 || d.Stat.Hunks != 4 || d.Stat.Added != 5 || d.Stat.Deleted != 4 {
		t.Errorf("stat: %+v", d.Stat)
	}
	f0 := d.Files[0]
	// 같은 함수의 hunk 2개 → 심볼 dedupe, 순서 유지
	if len(f0.Symbols) != 2 || f0.Symbols[0] != "func runPull(cmd *cobra.Command) error" || f0.Symbols[1] != "func helper()" {
		t.Errorf("symbols: %v", f0.Symbols)
	}
	if f0.Hunks != 3 || f0.Added != 4 || f0.Deleted != 3 {
		t.Errorf("file0: %+v", f0)
	}
	f1 := d.Files[1]
	if f1.Status != StatusRenamed || f1.OldPath != "old.go" || f1.Path != "new.go" {
		t.Errorf("rename: %+v", f1)
	}
	f2 := d.Files[2]
	if !f2.Binary || f2.Hunks != 0 {
		t.Errorf("binary: %+v", f2)
	}
}
