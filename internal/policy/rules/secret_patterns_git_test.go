package rules

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/policy"
	"github.com/x-mesh/gk/internal/secrets"
	"github.com/x-mesh/gk/internal/testutil"
)

// Each patch the scan reads leaves out the archives, in any case and
// directory, and keeps a text file in a directory with an archive's name.
// Git runs in a subdirectory, where a pathspec is relative to it unless it
// names the top.
func TestSecretPatternsRule_PatchesLeaveOutArchives(t *testing.T) {
	repo := testutil.NewRepo(t)
	base := strings.TrimSpace(repo.RunGit("rev-parse", "HEAD"))
	writeArchives := func(n string) {
		for _, p := range []string{"lib/app.jar", "UPPER.JAR", "web/site.war", "a.ear", "dist/b.zip"} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(repo.Dir, p)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo.Dir, p), []byte("PK\x00"+n), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeArchives("1")
	repo.WriteFile("conf.jar/app.yml", "kept: 1\n")
	repo.Commit("first")
	writeArchives("2")
	repo.WriteFile("conf.jar/app.yml", "kept: 2\n")
	repo.RunGit("add", "-A")

	for name, in := range map[string]policy.Input{
		"history": {History: true},
		"staged":  {Staged: true},
		"push":    {PushRange: base + "..HEAD"},
	} {
		t.Run(name, func(t *testing.T) {
			r := builtinRule()
			var patches []string
			r.ScanDiff = func(d string) []secrets.Finding {
				patches = append(patches, d)
				return nil
			}
			in.Runner = &git.ExecRunner{Dir: filepath.Join(repo.Dir, "lib")}
			in.WorkDir = repo.Dir
			if _, err := r.Evaluate(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			all := strings.Join(patches, "\n")
			if !strings.Contains(all, "+++ b/conf.jar/app.yml") {
				t.Errorf("the text file is missing from the patches:\n%s", all)
			}
			for _, p := range []string{"app.jar", "UPPER.JAR", "site.war", "a.ear", "b.zip"} {
				if strings.Contains(all, p) {
					t.Errorf("the patches show %s:\n%s", p, all)
				}
			}
		})
	}
}
