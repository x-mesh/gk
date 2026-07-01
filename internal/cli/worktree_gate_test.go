package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestParseGateSpec(t *testing.T) {
	tests := []struct {
		name        string
		gate        string
		gateArgs    []string
		panelReview bool
		phase       string
		wantNil     bool
		wantErr     bool
		wantTokens  []string
		wantPhase   string
	}{
		{name: "none", wantNil: true},
		{name: "gate string", gate: "xm panel {patch} --json", phase: "before",
			wantTokens: []string{"xm", "panel", "{patch}", "--json"}, wantPhase: "before"},
		{name: "default phase", gate: "true {patch}", wantTokens: []string{"true", "{patch}"}, wantPhase: "before"},
		{name: "panel-review alias", panelReview: true, phase: "both",
			wantTokens: []string{"xm", "panel", "{patch}", "--json"}, wantPhase: "both"},
		{name: "gate-arg tokens", gateArgs: []string{"sh", "-c", "xm panel {patch} | tee log"}, phase: "after",
			wantTokens: []string{"sh", "-c", "xm panel {patch} | tee log"}, wantPhase: "after"},
		{name: "panel + gate conflict", gate: "x", panelReview: true, wantErr: true},
		{name: "gate + gate-arg conflict", gate: "x", gateArgs: []string{"y"}, wantErr: true},
		{name: "bad phase", gate: "true", phase: "midway", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := parseGateSpec(tc.gate, tc.gateArgs, tc.panelReview, tc.phase, 0, false)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got spec=%+v", spec)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil {
				if spec != nil {
					t.Fatalf("expected nil spec, got %+v", spec)
				}
				return
			}
			if strings.Join(spec.tokens, "\x00") != strings.Join(tc.wantTokens, "\x00") {
				t.Errorf("tokens = %v, want %v", spec.tokens, tc.wantTokens)
			}
			if spec.phase != tc.wantPhase {
				t.Errorf("phase = %q, want %q", spec.phase, tc.wantPhase)
			}
		})
	}
}

func TestSubstituteGateArgv(t *testing.T) {
	vars := map[string]string{
		"patch":  "/tmp/a b.patch", // deliberately contains a space
		"source": "feat/x",
		"target": "develop",
		"phase":  "before",
	}
	got := substituteGateArgv([]string{"xm", "panel", "{patch}", "--src", "{source}", "{unknown}"}, vars)
	want := []string{"xm", "panel", "/tmp/a b.patch", "--src", "feat/x", "{unknown}"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// The space-bearing value must remain a SINGLE argv element (no re-split):
	// that is the property that makes the no-shell contract injection-proof.
	if got[2] != "/tmp/a b.patch" {
		t.Errorf("space value was re-split: %q", got[2])
	}
}

func TestAcquireTargetLock(t *testing.T) {
	dir := t.TempDir()

	// Fresh acquire succeeds and creates the lock file.
	release, err := acquireTargetLock(dir, "feat/x")
	if err != nil {
		t.Fatalf("fresh acquire: %v", err)
	}
	lockPath := filepath.Join(dir, "gk", "locks", "feat%2Fx.lock")
	if _, serr := os.Stat(lockPath); serr != nil {
		t.Fatalf("lock file missing: %v", serr)
	}

	// A live holder (this very process) blocks a second acquire.
	if _, err2 := acquireTargetLock(dir, "feat/x"); err2 == nil {
		t.Fatal("second acquire should block while the holder is alive")
	} else if stateFrom(err2) != envStateBlocked {
		t.Errorf("live-holder error state = %q, want blocked", stateFrom(err2))
	}

	// Release removes the file.
	release()
	if _, serr := os.Stat(lockPath); !os.IsNotExist(serr) {
		t.Fatalf("release did not remove lock (stat err=%v)", serr)
	}

	// A stale holder (dead pid) is reclaimed on the next acquire.
	if werr := os.WriteFile(lockPath, []byte("pid 999999\n"), 0o644); werr != nil {
		t.Fatal(werr)
	}
	release2, err3 := acquireTargetLock(dir, "feat/x")
	if err3 != nil {
		t.Fatalf("stale reclaim: %v", err3)
	}
	release2()
}

// gateBinAvailable skips a test when the shell no-op binaries it relies on are
// absent (true/false are POSIX standard, so this is a belt-and-suspenders guard).
func gateBinAvailable(t *testing.T) {
	t.Helper()
	for _, b := range []string{"true", "false"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not on PATH", b)
		}
	}
}

// setupGateWorktree builds a repo + feature worktree on feat-x (parent main)
// with one committed change, and stubs the promote/land child so the merge
// step is observable without a real gk binary. Returns the worktree path and a
// pointer to the recorded child invocations.
func setupGateWorktree(t *testing.T) (repo *testutil.Repo, wtPath string, childCalls *[][]string) {
	t.Helper()
	repo = testutil.NewRepo(t)
	wtPath = filepath.Join(t.TempDir(), "feat-wt")
	root, buf := buildWorktreeCmd(repo.Dir, "add", "--no-init", "-b", wtPath, "feat-x")
	if err := root.Execute(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, buf.String())
	}
	// One committed change so the gate patch is non-empty.
	if werr := os.WriteFile(filepath.Join(wtPath, "f.txt"), []byte("hello\n"), 0o644); werr != nil {
		t.Fatal(werr)
	}
	gitIn(t, wtPath, "add", "f.txt")
	gitIn(t, wtPath, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "feat work")

	calls := &[][]string{}
	prev := landRunChild
	landRunChild = func(_ context.Context, _, _ string, _ bool, args ...string) error {
		*calls = append(*calls, args)
		return nil
	}
	t.Cleanup(func() { landRunChild = prev })
	return repo, wtPath, calls
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestWorktreeFinish_BeforeGateBlocks(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	gateBinAvailable(t)
	_, wtPath, calls := setupGateWorktree(t)

	fin, buf := buildWorktreeCmd(wtPath, "finish", "--json", "--gate", "false {patch}")
	err := fin.Execute()
	if err == nil {
		t.Fatalf("expected blocked error, got nil\n%s", buf.String())
	}
	if stateFrom(err) != envStateBlocked {
		t.Errorf("state = %q, want blocked (%v)", stateFrom(err), err)
	}
	if len(*calls) != 0 {
		t.Errorf("before-gate failure still ran the merge child: %v", *calls)
	}
}

func TestWorktreeFinish_BeforeGatePassesThenMerges(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	gateBinAvailable(t)
	_, wtPath, calls := setupGateWorktree(t)

	fin, buf := buildWorktreeCmd(wtPath, "finish", "--json", "--gate", "true {patch}")
	if err := fin.Execute(); err != nil {
		t.Fatalf("finish: %v\n%s", err, buf.String())
	}
	var res worktreeFinishJSON
	if err := json.Unmarshal(buf.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if res.Gate == nil || res.Gate.Before != "passed" || !res.Gate.Merged {
		t.Fatalf("unexpected gate result: %+v", res.Gate)
	}
	if len(*calls) != 1 || len(((*calls)[0])) == 0 || (*calls)[0][0] != "promote" {
		t.Errorf("expected one promote child call, got %v", *calls)
	}
}

func TestWorktreeFinish_AfterGatePauses(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	gateBinAvailable(t)
	_, wtPath, calls := setupGateWorktree(t)

	fin, buf := buildWorktreeCmd(wtPath, "finish", "--json", "--cleanup",
		"--gate", "false {patch}", "--gate-phase", "after")
	err := fin.Execute()
	// Paused finish exits 3 via ExitError.
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 {
		t.Fatalf("expected ExitError{3}, got %v", err)
	}
	var res worktreeFinishJSON
	if uerr := json.Unmarshal(buf.Bytes(), &res); uerr != nil {
		t.Fatalf("unmarshal: %v\n%s", uerr, buf.String())
	}
	if res.Gate == nil || !res.Gate.Paused || !res.Gate.Merged || res.Gate.After != "failed" {
		t.Fatalf("expected paused after-gate with merge intact: %+v", res.Gate)
	}
	if len(res.Gate.Recover) == 0 {
		t.Error("paused result carries no recover[] commands")
	}
	// The merge ran once; cleanup was held (worktree still present, not removed).
	if len(*calls) != 1 {
		t.Errorf("expected one merge child call, got %v", *calls)
	}
	if res.Removed {
		t.Error("paused finish must not remove the worktree")
	}
	if _, serr := os.Stat(wtPath); serr != nil {
		t.Errorf("worktree gone after pause: %v", serr)
	}
}

func TestWorktreeFinish_DirtyBlocks(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	gateBinAvailable(t)
	_, wtPath, calls := setupGateWorktree(t)

	// Leave an uncommitted change so the gate cannot certify what merges.
	if werr := os.WriteFile(filepath.Join(wtPath, "dirty.txt"), []byte("wip\n"), 0o644); werr != nil {
		t.Fatal(werr)
	}
	fin, buf := buildWorktreeCmd(wtPath, "finish", "--json", "--gate", "true {patch}")
	err := fin.Execute()
	if err == nil || stateFrom(err) != envStateBlocked {
		t.Fatalf("expected blocked for dirty tree, got %v\n%s", err, buf.String())
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("error should mention uncommitted changes: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("dirty block still ran the merge: %v", *calls)
	}
}

func TestWorktreeFinish_ResumeAcceptGuardsUnmerged(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	// A --resume-accept --cleanup on a branch that was never merged must NOT
	// delete the worktree/branch — it blocks instead (data-loss guard).
	_, wtPath, _ := setupGateWorktree(t)
	fin, buf := buildWorktreeCmd(wtPath, "finish", "--json", "--to", "main", "--resume-accept", "--cleanup")
	err := fin.Execute()
	if err == nil || stateFrom(err) != envStateBlocked {
		t.Fatalf("expected blocked (unmerged), got %v\n%s", err, buf.String())
	}
	if _, serr := os.Stat(wtPath); serr != nil {
		t.Errorf("resume-accept removed the worktree despite no merge: %v", serr)
	}
}

func TestWorktreeFinish_PushWithAfterGateRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	_, wtPath, _ := setupGateWorktree(t)
	fin, buf := buildWorktreeCmd(wtPath, "finish", "--json", "--push",
		"--gate", "true {patch}", "--gate-phase", "both")
	err := fin.Execute()
	if err == nil || !strings.Contains(err.Error(), "--push") {
		t.Fatalf("expected --push+after rejection, got %v\n%s", err, buf.String())
	}
}

func TestWorktreeFinish_StateFileWritten(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in short mode")
	}
	gateBinAvailable(t)
	repo, wtPath, _ := setupGateWorktree(t)

	fin, buf := buildWorktreeCmd(wtPath, "finish", "--json", "--gate", "true {patch}")
	if err := fin.Execute(); err != nil {
		t.Fatalf("finish: %v\n%s", err, buf.String())
	}
	// State lives under the SHARED common dir, resolvable from the main repo.
	commonDir := gitCommonDir(context.Background(), &git.ExecRunner{Dir: repo.Dir})
	stateDir := filepath.Join(commonDir, "gk", "worktree-gate")
	entries, rerr := os.ReadDir(stateDir)
	if rerr != nil {
		t.Fatalf("read state dir %s: %v", stateDir, rerr)
	}
	if len(entries) == 0 {
		t.Fatalf("no gate state file written under %s", stateDir)
	}
	data, _ := os.ReadFile(filepath.Join(stateDir, entries[0].Name()))
	var st gateStateFile
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal state: %v\n%s", err, data)
	}
	if st.Target != "main" || st.Phase != "before" || st.Source != "feat-x" {
		t.Errorf("unexpected state record: %+v", st)
	}
	if st.TargetBeforeSHA == "" {
		t.Error("state record missing target_before_sha")
	}
}
