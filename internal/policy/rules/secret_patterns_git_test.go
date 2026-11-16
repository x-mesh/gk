package rules

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/policy"
	"github.com/x-mesh/gk/internal/secrets"
	"github.com/x-mesh/gk/internal/secrets/gitleaksrules"
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

// A history patch is scanned a batch of whole files at a time. The findings
// and their order do not depend on the batch size, also with a line longer
// than the read buffer.
func TestSecretPatternsRule_PatchBatchesGiveTheSameFindings(t *testing.T) {
	repo := testutil.NewRepo(t)
	pw := "Zk" + "8q" + "Lm2x"
	for i := range 4 {
		repo.WriteFile(fmt.Sprintf("svc%d/.env", i), fmt.Sprintf("DB_PASS=%s%d\n", pw, i))
		repo.WriteFile(fmt.Sprintf("svc%d/notes.txt", i), "no secret here\n")
		repo.Commit(fmt.Sprintf("c%d", i))
	}
	repo.WriteFile("dist/app.js.map", strings.Repeat("x", 3<<19)+" REDIS_PASSWORD="+pw+"\n")
	repo.Commit("bundle")

	scan := func(batch int) []policy.Violation {
		r := NewSecretPatternsRule()
		r.runGitleaks = gitleaksMissing
		r.ScanDiff = gitleaksrules.ScanDiff
		r.ScanFile = gitleaksrules.ScanFile
		r.patchBatch = batch
		v, err := r.Evaluate(context.Background(), policy.Input{Runner: &git.ExecRunner{Dir: repo.Dir}, History: true})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	whole, batched := scan(1<<30), scan(1)
	if !slices.EqualFunc(whole, batched, func(a, b policy.Violation) bool {
		return a.File == b.File && a.Line == b.Line && a.Commit == b.Commit && a.Message == b.Message
	}) {
		t.Errorf("batched findings differ:\nwhole   %v\nbatched %v", whole, batched)
	}
	if n := len(whole) - 1; n != 5 {
		t.Errorf("%d findings, want 5: %v", n, whole)
	}
}

func TestSecretPatternsRule_HistoryFindingsCarryCommitSHA(t *testing.T) {
	repo := testutil.NewRepo(t)
	pat := "ghp_" + "wWPw5k4aXcaT4fNP0UcnZwJUVFk6LO0pINUx"
	repo.WriteFile("seed.txt", "seed\n")
	repo.Commit("seed")
	repo.WriteFile("leak.env", "token = \""+pat+"\"\n")
	leakSHA := repo.Commit("add secret")
	repo.WriteFile("seed.txt", "seed\nmore\n")
	repo.Commit("fix: edit seed")
	repo.RunGit("rm", "leak.env")
	repo.Commit("fix: remove secret")

	r := NewSecretPatternsRule()
	r.runGitleaks = gitleaksMissing
	r.ScanDiff = gitleaksrules.ScanDiff
	r.ScanFile = gitleaksrules.ScanFile
	v, err := r.Evaluate(context.Background(), policy.Input{Runner: &git.ExecRunner{Dir: repo.Dir}, History: true})
	if err != nil {
		t.Fatal(err)
	}
	var violations []policy.Violation
	for _, x := range v {
		if x.Severity == policy.SeverityError {
			violations = append(violations, x)
		}
	}
	if len(violations) != 1 {
		t.Fatalf("want 1 error finding, got %d: %+v", len(violations), violations)
	}
	if violations[0].File != "leak.env" {
		t.Fatalf("file = %q, want leak.env", violations[0].File)
	}
	if violations[0].Commit != leakSHA {
		t.Fatalf("Commit = %q, want %q", violations[0].Commit, leakSHA)
	}
}

// failAfterRunner prints part of a patch and then fails, as git does when
// it dies in the middle of a history.
type failAfterRunner struct{}

func (failAfterRunner) Run(context.Context, ...string) ([]byte, []byte, error) {
	return nil, nil, errors.New("unexpected Run")
}

func (failAfterRunner) Stream(_ context.Context, _ io.Reader, w io.Writer, _ ...string) error {
	_, _ = io.WriteString(w, "diff --git a/x b/x\n+++ b/x\n@@ -0,0 +1 @@\n+line\n")
	return errors.New("exit 128")
}

func TestSecretPatternsRule_PatchStreamFailureFailsTheScan(t *testing.T) {
	r := builtinRule(secrets.Finding{Kind: "k", File: "x", FileLine: 1})
	found, err := r.scanPatch(context.Background(), failAfterRunner{}, nil, 0, 1, []string{"log", "-p"})
	if err == nil || !strings.Contains(err.Error(), "exit 128") || found != nil {
		t.Errorf("scanPatch = %v, %v; want no findings and the git error", found, err)
	}
}
