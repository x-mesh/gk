package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/git"
)

// followRunner is a FakeRunner whose ls-remote response can change between
// cycles (simulating a remote advancing). It records every call so tests can
// assert the backup→fetch→reset ordering.
type followRunner struct {
	*git.FakeRunner
	// lsRemoteSeq is consumed one entry per ls-remote call; the last entry is
	// reused once exhausted (so a steady-state remote keeps returning it).
	lsRemoteSeq []string
	lsIdx       int
	// dirty controls `git status --porcelain` output.
	dirty bool
	// noHead makes `rev-parse --verify HEAD` fail (HEAD unresolvable).
	noHead bool
	// hasCommits controls the `rev-list -n1 --all` probe: true ⇒ a SHA (repo
	// has history), false ⇒ empty (genuinely empty repo).
	hasCommits bool
}

func newFollowRunner() *followRunner {
	return &followRunner{FakeRunner: &git.FakeRunner{Responses: map[string]git.FakeResponse{}}}
}

func (f *followRunner) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	// Record the call (FakeRunner does this too, but going through it for the
	// special-cased commands below would double-count, so we record here and
	// only delegate for the generic ones).
	switch {
	case len(args) >= 1 && args[0] == "ls-remote":
		f.FakeRunner.Calls = append(f.FakeRunner.Calls, git.FakeCall{Args: append([]string(nil), args...)})
		sha := ""
		if len(f.lsRemoteSeq) > 0 {
			if f.lsIdx >= len(f.lsRemoteSeq) {
				sha = f.lsRemoteSeq[len(f.lsRemoteSeq)-1]
			} else {
				sha = f.lsRemoteSeq[f.lsIdx]
			}
			f.lsIdx++
		}
		if sha == "" {
			return nil, nil, nil
		}
		return []byte(sha + "\trefs/heads/main\n"), nil, nil
	case len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain":
		f.FakeRunner.Calls = append(f.FakeRunner.Calls, git.FakeCall{Args: append([]string(nil), args...)})
		if f.dirty {
			return []byte(" M file.go\n"), nil, nil
		}
		return nil, nil, nil
	case len(args) >= 3 && args[0] == "rev-parse" && args[1] == "--verify":
		// HEAD resolution for the backup ref.
		f.FakeRunner.Calls = append(f.FakeRunner.Calls, git.FakeCall{Args: append([]string(nil), args...)})
		if f.noHead {
			return nil, []byte("fatal: needed a single revision\n"), errors.New("exit status 128")
		}
		return []byte("oldoldoldoldoldoldoldoldoldoldoldoldold0\n"), nil, nil
	case len(args) >= 1 && args[0] == "rev-list":
		// repoHasNoCommits probe: empty output ⇒ no commits (genuinely empty).
		f.FakeRunner.Calls = append(f.FakeRunner.Calls, git.FakeCall{Args: append([]string(nil), args...)})
		if f.hasCommits {
			return []byte("commitcommitcommitcommitcommitcommit0001\n"), nil, nil
		}
		return nil, nil, nil
	case len(args) >= 2 && args[0] == "reset" && args[1] == "--hard":
		f.FakeRunner.Calls = append(f.FakeRunner.Calls, git.FakeCall{Args: append([]string(nil), args...)})
		return nil, nil, nil
	default:
		return f.FakeRunner.Run(ctx, args...)
	}
}

// callArgs flattens recorded calls into "arg0 arg1 ..." strings for order
// assertions.
func (f *followRunner) callArgs() []string {
	out := make([]string, 0, len(f.FakeRunner.Calls))
	for _, c := range f.FakeRunner.Calls {
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}

// indexOfPrefix returns the index of the first recorded call whose joined args
// start with prefix, or -1.
func indexOfPrefix(calls []string, prefix string) int {
	for i, c := range calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func baseOpts(r *followRunner) followOpts {
	return followOpts{
		remote:   "origin",
		branch:   "main",
		interval: time.Millisecond,
		once:     true,
		now:      func() time.Time { return time.Unix(1700000000, 0) },
	}
}

func TestFollow_ChangedTriggersBackupFetchResetHook(t *testing.T) {
	r := newFollowRunner()
	r.lsRemoteSeq = []string{"newnewnewnewnewnewnewnewnewnewnewnewnew1"}

	var hookRan bool
	opts := baseOpts(r)
	opts.hook = func(ctx context.Context) (int, error) { hookRan = true; return 0, nil }

	var buf bytes.Buffer
	if err := followLoop(context.Background(), r, &buf, opts); err != nil {
		t.Fatalf("followLoop returned error: %v", err)
	}

	if !hookRan {
		t.Fatal("hook did not run after a remote change")
	}
	calls := r.callArgs()
	iBackup := indexOfPrefix(calls, "update-ref refs/gk/follow-backup/")
	iFetch := indexOfPrefix(calls, "fetch origin main")
	iReset := indexOfPrefix(calls, "reset --hard newnew")
	if iBackup < 0 || iFetch < 0 || iReset < 0 {
		t.Fatalf("missing expected calls: backup=%d fetch=%d reset=%d in %v", iBackup, iFetch, iReset, calls)
	}
	if !(iBackup < iFetch && iFetch < iReset) {
		t.Fatalf("wrong order: backup=%d fetch=%d reset=%d (want backup<fetch<reset) in %v", iBackup, iFetch, iReset, calls)
	}
}

func TestFollow_OnceExits(t *testing.T) {
	r := newFollowRunner()
	r.lsRemoteSeq = []string{"newnewnewnewnewnewnewnewnewnewnewnewnew1"}
	opts := baseOpts(r)
	opts.hook = func(ctx context.Context) (int, error) { return 0, nil }

	done := make(chan error, 1)
	go func() {
		var buf bytes.Buffer
		done <- followLoop(context.Background(), r, &buf, opts)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("--once loop returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("--once did not exit; followLoop kept polling")
	}
}

func TestFollow_DirtyTreeRefusesReset(t *testing.T) {
	r := newFollowRunner()
	r.lsRemoteSeq = []string{"newnewnewnewnewnewnewnewnewnewnewnewnew1"}
	r.dirty = true

	var hookRan bool
	opts := baseOpts(r)
	opts.hook = func(ctx context.Context) (int, error) { hookRan = true; return 0, nil }

	var buf bytes.Buffer
	err := followLoop(context.Background(), r, &buf, opts)
	if err == nil {
		t.Fatal("expected an error when the working tree is dirty without --discard-dirty")
	}
	if hookRan {
		t.Fatal("hook ran despite the dirty-tree refusal")
	}
	calls := r.callArgs()
	if i := indexOfPrefix(calls, "reset --hard"); i >= 0 {
		t.Fatalf("reset --hard was issued over a dirty tree (call %d): %v", i, calls)
	}
	if i := indexOfPrefix(calls, "update-ref refs/gk/follow-backup/"); i >= 0 {
		t.Fatalf("backup ref written before the dirty refusal (call %d): %v", i, calls)
	}
}

func TestFollow_DirtyTreeResetsWithDiscardDirty(t *testing.T) {
	r := newFollowRunner()
	r.lsRemoteSeq = []string{"newnewnewnewnewnewnewnewnewnewnewnewnew1"}
	r.dirty = true

	opts := baseOpts(r)
	opts.discardDirty = true
	opts.hook = func(ctx context.Context) (int, error) { return 0, nil }

	var buf bytes.Buffer
	if err := followLoop(context.Background(), r, &buf, opts); err != nil {
		t.Fatalf("followLoop returned error: %v", err)
	}
	calls := r.callArgs()
	if i := indexOfPrefix(calls, "reset --hard newnew"); i < 0 {
		t.Fatalf("reset --hard not issued despite --discard-dirty: %v", calls)
	}
	// With --discard-dirty we skip the status probe entirely.
	if i := indexOfPrefix(calls, "status --porcelain"); i >= 0 {
		t.Fatalf("status probe ran despite --discard-dirty (call %d): %v", i, calls)
	}
}

func TestFollow_NoChangeDoesNothing(t *testing.T) {
	r := newFollowRunner()
	r.lsRemoteSeq = []string{"samesamesamesamesamesamesamesamesame0001"}

	// First mirror lastApplied to the current SHA, then assert a second cycle
	// (same SHA) issues no fetch/reset. We test followCycle directly with a
	// pre-seeded lastApplied to model "already applied".
	opts := baseOpts(r)
	opts.hook = func(ctx context.Context) (int, error) { return 0, nil }

	lastApplied := "samesamesamesamesamesamesamesamesame0001"
	res, err := followCycle(context.Background(), r, opts, &lastApplied)
	if err != nil {
		t.Fatalf("followCycle returned error: %v", err)
	}
	if res.Changed || res.Updated || res.Ran {
		t.Fatalf("no-change cycle did work: %+v", res)
	}
	calls := r.callArgs()
	if i := indexOfPrefix(calls, "fetch"); i >= 0 {
		t.Fatalf("fetch issued on a no-change cycle: %v", calls)
	}
	if i := indexOfPrefix(calls, "reset --hard"); i >= 0 {
		t.Fatalf("reset issued on a no-change cycle: %v", calls)
	}
	// Only the cheap ls-remote probe should have run.
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "ls-remote") {
		t.Fatalf("no-change cycle should only ls-remote, got: %v", calls)
	}
}

func TestFollow_HookFailureTripsBackoffAndKeepsAppliedSHA(t *testing.T) {
	r := newFollowRunner()
	r.lsRemoteSeq = []string{"newnewnewnewnewnewnewnewnewnewnewnewnew1"}

	opts := baseOpts(r)
	opts.hook = func(ctx context.Context) (int, error) { return 7, nil }

	lastApplied := ""
	res, err := followCycle(context.Background(), r, opts, &lastApplied)
	if err != nil {
		t.Fatalf("followCycle should not error on a non-zero hook exit: %v", err)
	}
	if !res.Updated || !res.Ran || res.ExitCode != 7 {
		t.Fatalf("expected updated+ran with exit 7, got %+v", res)
	}
	// The mirror is done — lastApplied advanced so the hook (not the reset) is
	// what a backoff retry re-runs.
	if lastApplied != "newnewnewnewnewnewnewnewnewnewnewnewnew1" {
		t.Fatalf("lastApplied not advanced after a successful mirror: %q", lastApplied)
	}
}

func TestFollow_AgentModeEmitsEnvelope(t *testing.T) {
	r := newFollowRunner()
	r.lsRemoteSeq = []string{"newnewnewnewnewnewnewnewnewnewnewnewnew1"}

	// emitAgentResult wraps in the envelope only when the global flagAgent is
	// set (mirroring real GK_AGENT runs); opts.agent gates *whether* we emit.
	prev := flagAgent
	flagAgent = true
	t.Cleanup(func() { flagAgent = prev })

	opts := baseOpts(r)
	opts.agent = true
	opts.hook = func(ctx context.Context) (int, error) { return 0, nil }

	var buf bytes.Buffer
	if err := followLoop(context.Background(), r, &buf, opts); err != nil {
		t.Fatalf("followLoop returned error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"state": "ok"`) {
		t.Fatalf("agent output missing ok envelope: %s", out)
	}
	if !strings.Contains(out, `"updated": true`) || !strings.Contains(out, `"remote_sha"`) {
		t.Fatalf("agent envelope missing expected result fields: %s", out)
	}
}

func TestFollow_LsRemoteErrorIsReported(t *testing.T) {
	r := newFollowRunner()
	// empty seq → ls-remote returns empty output → "no branch" error.
	opts := baseOpts(r)
	var buf bytes.Buffer
	err := followLoop(context.Background(), r, &buf, opts)
	if err == nil {
		t.Fatal("expected an error when the remote has no such branch")
	}
	if !strings.Contains(err.Error(), "no branch") {
		t.Fatalf("unexpected error: %v", err)
	}
	// No mirror should have happened.
	if i := indexOfPrefix(r.callArgs(), "reset --hard"); i >= 0 {
		t.Fatal("reset issued despite ls-remote failure")
	}
}

func TestNextBackoff(t *testing.T) {
	const iv = 2 * time.Second
	const max = 10 * iv
	if got := nextBackoff(0, iv, max); got != iv {
		t.Errorf("first failure: got %v, want %v", got, iv)
	}
	if got := nextBackoff(iv, iv, max); got != 2*iv {
		t.Errorf("second failure doubles: got %v, want %v", got, 2*iv)
	}
	if got := nextBackoff(8*iv, iv, max); got != max {
		t.Errorf("cap: 8iv doubled (16iv) must clamp to %v, got %v", max, got)
	}
	if got := nextBackoff(max, iv, max); got != max {
		t.Errorf("stays capped: got %v, want %v", got, max)
	}
}

// TestFollow_ShutdownReturnsNilOnCancel checks the continuous loop stops cleanly
// (nil, not an error) when its context is cancelled — the SIGINT/SIGTERM path.
func TestFollow_ShutdownReturnsNilOnCancel(t *testing.T) {
	r := newFollowRunner()
	r.lsRemoteSeq = []string{"newnewnewnewnewnewnewnewnewnewnewnewnew1"}
	opts := baseOpts(r)
	opts.once = false
	opts.hook = func(ctx context.Context) (int, error) { return 0, nil }

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // shutdown signalled; loop must stop after the current cycle's wait
	done := make(chan error, 1)
	go func() {
		var buf bytes.Buffer
		done <- followLoop(ctx, r, &buf, opts)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ctx-cancel shutdown should return nil, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("followLoop did not stop on ctx cancel")
	}
}

// TestFollow_AgentOnceErrorDoesNotDoubleEmit guards the contract fix: on agent
// --once with a cycle error, followLoop returns the error to main (one stderr
// envelope) and must NOT also write an envelope to its own stdout.
func TestFollow_AgentOnceErrorDoesNotDoubleEmit(t *testing.T) {
	r := newFollowRunner()
	r.lsRemoteSeq = []string{"newnewnewnewnewnewnewnewnewnewnewnewnew1"}
	r.dirty = true // dirty refusal → cycle error

	prev := flagAgent
	flagAgent = true
	t.Cleanup(func() { flagAgent = prev })

	opts := baseOpts(r) // once: true
	opts.agent = true

	var buf bytes.Buffer
	err := followLoop(context.Background(), r, &buf, opts)
	if err == nil {
		t.Fatal("expected the dirty refusal to surface as an error")
	}
	if buf.Len() != 0 {
		t.Fatalf("followLoop emitted on --once error (double-emit); buf=%q", buf.String())
	}
}

// TestFollow_NoHeadEmptyRepoMirrorsWithoutBackup: a genuinely empty repo (HEAD
// unresolvable AND no commits) skips the backup but still mirrors.
func TestFollow_NoHeadEmptyRepoMirrorsWithoutBackup(t *testing.T) {
	r := newFollowRunner()
	r.lsRemoteSeq = []string{"newnewnewnewnewnewnewnewnewnewnewnewnew1"}
	r.noHead = true
	r.hasCommits = false

	opts := baseOpts(r)
	opts.hook = func(ctx context.Context) (int, error) { return 0, nil }

	var buf bytes.Buffer
	if err := followLoop(context.Background(), r, &buf, opts); err != nil {
		t.Fatalf("empty repo should mirror without error: %v", err)
	}
	calls := r.callArgs()
	if i := indexOfPrefix(calls, "update-ref refs/gk/follow-backup/"); i >= 0 {
		t.Fatalf("backup ref written for an empty repo (nothing to back up): %v", calls)
	}
	if i := indexOfPrefix(calls, "reset --hard newnew"); i < 0 {
		t.Fatalf("empty repo should still mirror (reset --hard): %v", calls)
	}
}

// TestFollow_UnreadableHeadAbortsBeforeReset: the dangerous case — HEAD is
// unreadable but the repo HAS history. A reset with no recovery anchor must be
// refused, not silently performed.
func TestFollow_UnreadableHeadAbortsBeforeReset(t *testing.T) {
	r := newFollowRunner()
	r.lsRemoteSeq = []string{"newnewnewnewnewnewnewnewnewnewnewnewnew1"}
	r.noHead = true
	r.hasCommits = true

	opts := baseOpts(r)
	opts.hook = func(ctx context.Context) (int, error) { return 0, nil }

	var buf bytes.Buffer
	err := followLoop(context.Background(), r, &buf, opts)
	if err == nil {
		t.Fatal("expected refusal when HEAD is unreadable but the repo has history")
	}
	if !strings.Contains(err.Error(), "recovery anchor") {
		t.Fatalf("error should explain the refused destructive reset: %v", err)
	}
	if i := indexOfPrefix(r.callArgs(), "reset --hard"); i >= 0 {
		t.Fatal("reset issued despite unresolvable HEAD with history")
	}
}
