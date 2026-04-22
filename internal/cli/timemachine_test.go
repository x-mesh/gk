package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/gitsafe"
	"github.com/x-mesh/gk/internal/testutil"
)

// buildTimemachineCmd wires a detached cobra tree that mirrors gk's root
// persistent flags so --repo threads through to RepoFlag().
func buildTimemachineCmd(repoDir string, args ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "")

	tm := &cobra.Command{Use: "timemachine"}
	restore := &cobra.Command{Use: "restore <sha|ref>", Args: cobra.ExactArgs(1), RunE: runTimemachineRestore}
	restore.Flags().String("mode", "auto", "")
	restore.Flags().Bool("dry-run", false, "")
	restore.Flags().Bool("autostash", false, "")
	restore.Flags().Bool("force", false, "")
	tm.AddCommand(restore)
	testRoot.AddCommand(tm)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)
	testRoot.SetArgs(append([]string{"--repo", repoDir, "timemachine", "restore"}, args...))
	return testRoot, buf
}

func TestTimemachineRestore_MovesHEAD_Mixed(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("c1")
	repo.WriteFile("b.txt", "b")
	repo.Commit("c2")

	cmd, buf := buildTimemachineCmd(repo.Dir, sha1, "--mode", "mixed")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("restore: %v (out=%q)", err, buf.String())
	}

	head := repo.RunGit("rev-parse", "HEAD")
	if head != sha1 {
		t.Errorf("HEAD = %q, want %q", head, sha1)
	}
	out := buf.String()
	if !strings.Contains(out, "restored to") {
		t.Errorf("expected 'restored to' in output, got: %q", out)
	}
	if !strings.Contains(out, "refs/gk/timemachine-backup/") {
		t.Errorf("expected backup ref in output, got: %q", out)
	}
	if !strings.Contains(out, "to revert: gk timemachine restore") {
		t.Errorf("expected revert hint, got: %q", out)
	}
}

func TestTimemachineRestore_DryRun_NoMutation(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("c1")
	repo.WriteFile("b.txt", "b")
	sha2 := repo.Commit("c2")

	cmd, buf := buildTimemachineCmd(repo.Dir, sha1, "--mode", "mixed", "--dry-run")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("dry-run: %v", err)
	}

	// HEAD must still be at sha2 (unchanged).
	if head := repo.RunGit("rev-parse", "HEAD"); head != sha2 {
		t.Errorf("HEAD moved during --dry-run: got %q, want %q", head, sha2)
	}

	out := buf.String()
	if !strings.Contains(out, "[dry-run]") {
		t.Errorf("expected [dry-run] banner, got: %q", out)
	}
	if !strings.Contains(out, "no changes made") {
		t.Errorf("expected 'no changes made' footer, got: %q", out)
	}
}

func TestTimemachineRestore_DirtyHard_Refused_WithoutForce(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}

	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("c1")
	repo.WriteFile("b.txt", "b")
	repo.Commit("c2")
	repo.WriteFile("a.txt", "dirty") // dirty tree

	cmd, buf := buildTimemachineCmd(repo.Dir, sha1, "--mode", "hard")
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error on dirty+hard without --force/--autostash, got nil (out=%q)", buf.String())
	}
	// Error should mention force or autostash so user knows the escape hatch.
	msg := err.Error()
	if !strings.Contains(msg, "autostash") && !strings.Contains(msg, "force") {
		t.Errorf("expected error mentioning autostash/force, got: %v", err)
	}
}

func TestTimemachineRestore_UnknownMode_Refused(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("c1")

	cmd, _ := buildTimemachineCmd(repo.Dir, sha1, "--mode", "bogus")
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unknown --mode, got nil")
	}
	if !strings.Contains(err.Error(), "unknown --mode") {
		t.Errorf("expected unknown-mode error, got: %v", err)
	}
}

func TestTimemachineRestore_UnresolvableRef_Refused(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("c1")

	cmd, _ := buildTimemachineCmd(repo.Dir, "does-not-exist", "--mode", "mixed")
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for unresolvable ref, got nil")
	}
}

// --- list-backups -----------------------------------------------------------

// buildListBackupsCmd mirrors buildTimemachineCmd but targets list-backups.
func buildListBackupsCmd(repoDir string, args ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "")

	tm := &cobra.Command{Use: "timemachine"}
	lb := &cobra.Command{Use: "list-backups", RunE: runTimemachineListBackups}
	lb.Flags().Bool("json", false, "")
	lb.Flags().String("kind", "", "")
	tm.AddCommand(lb)
	testRoot.AddCommand(tm)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)
	testRoot.SetArgs(append([]string{"--repo", repoDir, "timemachine", "list-backups"}, args...))
	return testRoot, buf
}

func TestTimemachineListBackups_Empty(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("init")

	cmd, buf := buildListBackupsCmd(repo.Dir)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list-backups: %v", err)
	}
	if !strings.Contains(buf.String(), "no backup refs found") {
		t.Errorf("empty repo missing empty-state message: %q", buf.String())
	}
}

func TestTimemachineListBackups_AfterRestore_SurfacesBackup(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("c1")
	repo.WriteFile("b.txt", "b")
	repo.Commit("c2")

	// Create a backup via gk timemachine restore.
	restoreCmd, _ := buildTimemachineCmd(repo.Dir, sha1, "--mode", "mixed")
	if err := restoreCmd.Execute(); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// list-backups must surface the backup ref from that restore.
	cmd, buf := buildListBackupsCmd(repo.Dir)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list-backups: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "timemachine") {
		t.Errorf("expected timemachine backup in output, got: %q", out)
	}
	if !strings.Contains(out, "refs/gk/timemachine-backup/") {
		t.Errorf("expected full ref path, got: %q", out)
	}
}

func TestTimemachineListBackups_JSON_NDJSONShape(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("c1")
	repo.WriteFile("b.txt", "b")
	repo.Commit("c2")

	restoreCmd, _ := buildTimemachineCmd(repo.Dir, sha1, "--mode", "mixed")
	if err := restoreCmd.Execute(); err != nil {
		t.Fatalf("restore: %v", err)
	}

	cmd, buf := buildListBackupsCmd(repo.Dir, "--json")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list-backups --json: %v", err)
	}

	out := strings.TrimSpace(buf.String())
	if out == "" {
		t.Fatalf("empty JSON output")
	}

	// Each line must be valid JSON with a required subset of fields.
	lines := strings.Split(out, "\n")
	if len(lines) == 0 {
		t.Fatal("no NDJSON lines")
	}
	for i, line := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("line %d invalid JSON: %v\n  line: %q", i, err, line)
			continue
		}
		for _, k := range []string{"ref", "kind", "branch", "sha"} {
			if _, ok := obj[k]; !ok {
				t.Errorf("line %d missing key %q: %v", i, k, obj)
			}
		}
	}
}

// --- list (unified reflog + backup) ---------------------------------------

func buildListCmd(repoDir string, args ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "")

	tm := &cobra.Command{Use: "timemachine"}
	lc := &cobra.Command{Use: "list", RunE: runTimemachineList}
	lc.Flags().Bool("json", false, "")
	lc.Flags().String("kinds", "reflog,backup", "")
	lc.Flags().Int("limit", 50, "")
	lc.Flags().Bool("all-branches", false, "")
	tm.AddCommand(lc)
	testRoot.AddCommand(tm)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)
	testRoot.SetArgs(append([]string{"--repo", repoDir, "timemachine", "list"}, args...))
	return testRoot, buf
}

func TestTimemachineList_CombinesReflogAndBackup(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("c1")
	repo.WriteFile("b.txt", "b")
	repo.Commit("c2")

	// Create a backup via restore — list should then show both reflog entries and the backup.
	restoreCmd, _ := buildTimemachineCmd(repo.Dir, sha1, "--mode", "mixed")
	if err := restoreCmd.Execute(); err != nil {
		t.Fatalf("restore: %v", err)
	}

	cmd, buf := buildListCmd(repo.Dir, "--json")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list: %v", err)
	}

	out := strings.TrimSpace(buf.String())
	if out == "" {
		t.Fatal("empty output")
	}

	kinds := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		var j map[string]any
		if err := json.Unmarshal([]byte(line), &j); err != nil {
			t.Fatalf("invalid JSON line %q: %v", line, err)
		}
		if k, ok := j["kind"].(string); ok {
			kinds[k]++
		}
	}

	if kinds["reflog"] == 0 {
		t.Errorf("expected reflog events, got kinds=%v", kinds)
	}
	if kinds["backup"] == 0 {
		t.Errorf("expected backup events, got kinds=%v", kinds)
	}
}

func TestTimemachineList_KindsFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("c1")
	repo.WriteFile("b.txt", "b")
	repo.Commit("c2")

	restoreCmd, _ := buildTimemachineCmd(repo.Dir, sha1, "--mode", "mixed")
	if err := restoreCmd.Execute(); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Only reflog → no backup kinds in output.
	cmd, buf := buildListCmd(repo.Dir, "--json", "--kinds", "reflog")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var j map[string]any
		_ = json.Unmarshal([]byte(line), &j)
		if j["kind"] == "backup" {
			t.Errorf("backup event leaked through --kinds=reflog: %v", j)
		}
	}

	// Only backup → no reflog kinds.
	cmd2, buf2 := buildListCmd(repo.Dir, "--json", "--kinds", "backup")
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(buf2.String()), "\n") {
		var j map[string]any
		_ = json.Unmarshal([]byte(line), &j)
		if j["kind"] == "reflog" {
			t.Errorf("reflog event leaked through --kinds=backup: %v", j)
		}
	}
}

func TestTimemachineList_Limit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	for i := 0; i < 5; i++ {
		repo.WriteFile("f.txt", strings.Repeat("x", i+1))
		repo.Commit("c" + string(rune('1'+i)))
	}

	cmd, buf := buildListCmd(repo.Dir, "--json", "--limit", "2", "--kinds", "reflog")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) > 2 {
		t.Errorf("--limit=2 produced %d lines", len(lines))
	}
}

// --- show -----------------------------------------------------------------

func buildShowCmd(repoDir string, args ...string) (*cobra.Command, *bytes.Buffer) {
	testRoot := &cobra.Command{Use: "gk", SilenceUsage: true, SilenceErrors: true}
	testRoot.PersistentFlags().StringVar(&flagRepo, "repo", repoDir, "")
	testRoot.PersistentFlags().BoolVar(&flagVerbose, "verbose", false, "")
	testRoot.PersistentFlags().BoolVar(&flagDryRun, "dry-run", false, "")
	testRoot.PersistentFlags().BoolVar(&flagJSON, "json", false, "")
	testRoot.PersistentFlags().BoolVar(&flagNoColor, "no-color", true, "")

	tm := &cobra.Command{Use: "timemachine"}
	show := &cobra.Command{Use: "show <sha|ref>", Args: cobra.ExactArgs(1), RunE: runTimemachineShow}
	show.Flags().Bool("patch", false, "")
	tm.AddCommand(show)
	testRoot.AddCommand(tm)

	buf := &bytes.Buffer{}
	testRoot.SetOut(buf)
	testRoot.SetErr(buf)
	testRoot.SetArgs(append([]string{"--repo", repoDir, "timemachine", "show"}, args...))
	return testRoot, buf
}

func TestTimemachineShow_BasicCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha := repo.Commit("test commit message")

	cmd, buf := buildShowCmd(repo.Dir, sha)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("show: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "commit ") {
		t.Errorf("missing commit header: %q", out)
	}
	if !strings.Contains(out, "subject:") || !strings.Contains(out, "test commit message") {
		t.Errorf("missing subject/commit message: %q", out)
	}
	if !strings.Contains(out, "author:") {
		t.Errorf("missing author line: %q", out)
	}
}

func TestTimemachineShow_BackupRef_AddsContext(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("c1")
	repo.WriteFile("b.txt", "b")
	repo.Commit("c2")

	// Create a backup via restore.
	restoreCmd, _ := buildTimemachineCmd(repo.Dir, sha1, "--mode", "mixed")
	if err := restoreCmd.Execute(); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Resolve the backup ref name.
	runner := &git.ExecRunner{Dir: repo.Dir}
	backups, err := gitsafe.ListBackups(context.Background(), runner)
	if err != nil || len(backups) != 1 {
		t.Fatalf("ListBackups failed or empty: err=%v backups=%v", err, backups)
	}
	backupRef := backups[0].Ref

	cmd, buf := buildShowCmd(repo.Dir, backupRef)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("show backup: %v (out=%q)", err, buf.String())
	}

	out := buf.String()
	if !strings.Contains(out, "gk backup:") {
		t.Errorf("missing backup descriptor line: %q", out)
	}
	if !strings.Contains(out, "kind=timemachine") {
		t.Errorf("missing kind=timemachine: %q", out)
	}
}

func TestTimemachineShow_InvalidRef(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.Commit("c1")

	cmd, _ := buildShowCmd(repo.Dir, "does-not-exist")
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for unresolvable ref, got nil")
	}
}

func TestTimemachineListBackups_KindFilter(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	sha1 := repo.Commit("c1")
	repo.WriteFile("b.txt", "b")
	repo.Commit("c2")

	// Create a timemachine-kind backup.
	restoreCmd, _ := buildTimemachineCmd(repo.Dir, sha1, "--mode", "mixed")
	if err := restoreCmd.Execute(); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Filter to kind=undo → should find nothing.
	cmd, buf := buildListBackupsCmd(repo.Dir, "--kind", "undo")
	if err := cmd.Execute(); err != nil {
		t.Fatalf("list-backups: %v", err)
	}
	if !strings.Contains(buf.String(), "no backup refs found") {
		t.Errorf("filter=undo expected empty, got: %q", buf.String())
	}

	// Filter to kind=timemachine → should find the backup.
	cmd2, buf2 := buildListBackupsCmd(repo.Dir, "--kind", "timemachine")
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("list-backups: %v", err)
	}
	if !strings.Contains(buf2.String(), "refs/gk/timemachine-backup/") {
		t.Errorf("filter=timemachine expected match, got: %q", buf2.String())
	}
}
