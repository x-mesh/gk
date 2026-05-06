package forget

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
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

	// Backup ref for main matches the gitsafe <kind>-backup/<branch>/<unix>
	// grammar so ListBackups can pick it up alongside undo/wipe entries.
	mainSHA := strings.TrimSpace(r.RunGit("rev-parse", "main"))
	mainBackup := fmt.Sprintf("refs/gk/forget-backup/main/%d", backup.Stamp)
	got := strings.TrimSpace(r.RunGit("rev-parse", mainBackup))
	if got != mainSHA {
		t.Errorf("backup ref %s = %s, want %s", mainBackup, got, mainSHA)
	}

	// Tag captured under "tag-<name>" so a same-named branch and tag never
	// collide in the namespace.
	tagBackup := fmt.Sprintf("refs/gk/forget-backup/tag-v1.0/%d", backup.Stamp)
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

// TestForgetBackupVisibleInListBackups locks in the contract that forget
// backups appear in `gk timemachine list` (which delegates to
// gitsafe.ListBackups). Without the gitsafe-compatible ref shape this
// would silently regress.
func TestForgetBackupVisibleInListBackups(t *testing.T) {
	r := testutil.NewRepo(t)
	r.RunGit("tag", "v1.0")

	runner := &git.ExecRunner{Dir: r.Dir}
	ctx := context.Background()

	if _, err := CreateBackup(ctx, runner, r.GitDir, time.Unix(1700000000, 0)); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	refs, err := gitsafe.ListBackups(ctx, runner)
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}

	var sawMain, sawTag bool
	for _, b := range refs {
		if b.Kind != "forget" {
			continue
		}
		switch b.Branch {
		case "main":
			sawMain = true
		case "tag-v1.0":
			sawTag = true
		}
	}
	if !sawMain {
		t.Errorf("forget-backup for main not found in ListBackups")
	}
	if !sawTag {
		t.Errorf("forget-backup for tag-v1.0 not found in ListBackups")
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
	if backup.Stamp != 1700000042 {
		t.Errorf("Stamp = %d, want 1700000042", backup.Stamp)
	}
	if !strings.Contains(backup.Manifest, "1700000042") {
		t.Errorf("Manifest = %q, want stamp 1700000042", backup.Manifest)
	}
}
