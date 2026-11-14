package gitleaksrules

import (
	"fmt"
	"slices"
	"testing"
)

func servicePasswordLines(t *testing.T, path, content string) []string {
	t.Helper()
	var got []string
	for _, f := range ScanFile(path, content) {
		if f.Kind == "gk-service-password" {
			got = append(got, fmt.Sprintf("%s:%02d", f.File, f.FileLine))
		}
	}
	slices.Sort(got)
	return got
}

// A password key whose service name is only in a parent key is found, at
// its own line. A key under a parent without a service name is not.
func TestYAMLPaths_ParentKeyNamesTheService(t *testing.T) {
	pw := "Zk" + "8q" + "Lm2x"
	content := "spring:\n" + //  1
		"  datasource:\n" + //  2
		"    url: jdbc:mysql://db:3306/app\n" + //  3
		"    username: app\n" + //  4
		"    password: " + pw + "\n" + //  5
		"  data:\n" + //  6
		"    mongodb:\n" + //  7
		"      password: '" + pw + "'\n" + //  8
		"  partner-datasource:\n" + //  9
		"    hikari:\n" + // 10
		"      password: \"" + pw + "\" # dev\n" + // 11
		"other:\n" + // 12
		"  password: " + pw + "\n" + // 13
		"redis:\n" + // 14
		"  password: ${REDIS_PASSWORD}\n" + // 15
		"servers:\n" + // 16
		"  - name: ldap\n" + // 17
		"    ldap:\n" + // 18
		"      password: " + pw + "\n" + // 19
		"mail:\n" + // 20
		"  smtp_password: " + pw + "\n" // 21
	want := []string{"application.yml:05", "application.yml:08", "application.yml:11", "application.yml:19", "application.yml:21"}
	if got := servicePasswordLines(t, "application.yml", content); !slices.Equal(got, want) {
		t.Errorf("findings at %v, want %v", got, want)
	}
	if got := servicePasswordLines(t, "app.jar!BOOT-INF/classes/application-dev.YAML", content); len(got) != len(want) {
		t.Errorf("in an archive: findings at %v, want %d", got, len(want))
	}
	// smtp_password names the service in its own key.
	if got := servicePasswordLines(t, "notes.txt", content); !slices.Equal(got, []string{"notes.txt:21"}) {
		t.Errorf("a file that is not YAML: findings at %v, want only line 21", got)
	}
}

// A key that already names the service is reported once, not again on
// its path. A patch hunk sees only the parents that it adds.
func TestYAMLPaths_OnceAndOnlyParentsInTheHunk(t *testing.T) {
	pw := "Zk" + "8q" + "Lm2x"
	if got := servicePasswordLines(t, "a.yml", "app:\n  db_password: "+pw+"\n"); !slices.Equal(got, []string{"a.yml:02"}) {
		t.Errorf("findings at %v, want one at line 2", got)
	}
	diff := "diff --git a/a.yml b/a.yml\n+++ b/a.yml\n" +
		"@@ -0,0 +1,2 @@\n+datasource:\n+  password: " + pw + "\n" +
		"@@ -9 +10 @@\n+  password: " + pw + "\n"
	var got []int
	for _, f := range ScanDiff(diff) {
		if f.Kind == "gk-service-password" {
			got = append(got, f.FileLine)
		}
	}
	if !slices.Equal(got, []int{2}) {
		t.Errorf("findings at lines %v, want only line 2, whose parent is in its hunk", got)
	}
}

func TestYAMLKey(t *testing.T) {
	for _, c := range []struct {
		line, key, value string
		indent           int
		ok               bool
	}{
		{"spring:", "spring", "", 0, true},
		{"    password: x1 # dev", "password", "x1 # dev", 4, true},
		{"  - name: ldap", "name", "ldap", 4, true},
		{"  - - deep: v", "deep", "v", 6, true},
		{"  url: jdbc:mysql://db:3306/app", "url", "jdbc:mysql://db:3306/app", 2, true},
		{`  "my key": v`, "my key", "v", 2, true},
		{"  'q':", "q", "", 2, true},
		{"# password: x", "", "", 0, false},
		{"  - item", "", "", 0, false},
		{"text without a key", "", "", 0, false},
		{"http://host:80/", "", "", 0, false},
		{"", "", "", 0, false},
	} {
		indent, key, value, ok := yamlKey(c.line)
		if ok != c.ok || ok && (indent != c.indent || key != c.key || value != c.value) {
			t.Errorf("yamlKey(%q) = %d, %q, %q, %v", c.line, indent, key, value, ok)
		}
	}
}

// A parent key can carry an anchor or a tag and still open a block.
func TestYAMLPaths_AnchoredAndTaggedParents(t *testing.T) {
	pw := "Zk" + "8q" + "Lm2x"
	content := "spring:\n" + // 1
		"  datasource: &main\n" + // 2
		"    password: " + pw + "\n" + // 3
		"  redis: !!map\n" + // 4
		"    password: " + pw + "\n" + // 5
		"  mail: &m !!map # shared\n" + // 6
		"    password: " + pw + "\n" + // 7
		"  other: &o\n" + // 8
		"    password: " + pw + "\n" // 9
	want := []string{"a.yml:03", "a.yml:05", "a.yml:07"}
	if got := servicePasswordLines(t, "a.yml", content); !slices.Equal(got, want) {
		t.Errorf("findings at %v, want %v", got, want)
	}
}
