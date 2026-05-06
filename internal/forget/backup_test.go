package forget

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestCreateBackupWritesRefsAndManifest(t *testing.T) {
	r := testutil.NewRepo(t)
	r.WriteFile("a", "x\n")
	r.RunGit("add", ".")
	r.RunGit("commit", "-m", "second")
	r.RunGit("tag", "v1.0")

	runner := &git.ExecRunner{Dir: r.Dir}
	stamp := time.Unix(1700000000, 0)

	backup, err := CreateBackup(context.Background(), runner, r.GitDir, stamp)
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Backup ref for main exists and points at the same commit as main.
	mainSHA := strings.TrimSpace(r.RunGit("rev-parse", "main"))
	backupRef := backup.RefPrefix + "/refs/heads/main"
	got := strings.TrimSpace(r.RunGit("rev-parse", backupRef))
	if got != mainSHA {
		t.Errorf("backup ref %s = %s, want %s", backupRef, got, mainSHA)
	}

	// Tag also captured.
	tagBackup := backup.RefPrefix + "/refs/tags/v1.0"
	if _, _, err := runner.Run(context.Background(), "rev-parse", tagBackup); err != nil {
		t.Errorf("tag backup ref %s missing: %v", tagBackup, err)
	}

	// Manifest exists, includes HEAD line.
	body, err := os.ReadFile(backup.Manifest)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "refs/heads/main "+mainSHA) {
		t.Errorf("manifest missing main entry, got:\n%s", text)
	}
	if !strings.Contains(text, "HEAD ") {
		t.Errorf("manifest missing HEAD entry, got:\n%s", text)
	}
}

func TestCreateBackupStampInPaths(t *testing.T) {
	r := testutil.NewRepo(t)
	runner := &git.ExecRunner{Dir: r.Dir}
	stamp := time.Unix(1700000042, 0)
	backup, err := CreateBackup(context.Background(), runner, r.GitDir, stamp)
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if !strings.Contains(backup.RefPrefix, "1700000042") {
		t.Errorf("RefPrefix = %q, want stamp 1700000042", backup.RefPrefix)
	}
	if !strings.Contains(backup.Manifest, "1700000042") {
		t.Errorf("Manifest = %q, want stamp 1700000042", backup.Manifest)
	}
}
