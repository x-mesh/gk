package resolve

import (
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/gitstate"
)

func TestFormatStuckGuidance_None(t *testing.T) {
	got := FormatStuckGuidance(gitstate.RebaseStuck{Reason: gitstate.RebaseStuckNone})
	if got != "" {
		t.Errorf("None reason: want empty string, got %q", got)
	}
}

func TestFormatStuckGuidance_PrefixAlwaysPresent(t *testing.T) {
	cases := []gitstate.RebaseStuckReason{
		gitstate.RebaseStuckEmptyCommit,
		gitstate.RebaseStuckEdit,
		gitstate.RebaseStuckExec,
		gitstate.RebaseStuckUnknown,
	}
	const prefix = "rebase is in progress but no conflicted files found."
	for _, r := range cases {
		got := FormatStuckGuidance(gitstate.RebaseStuck{Reason: r})
		if !strings.HasPrefix(got, prefix) {
			t.Errorf("Reason=%s: missing prefix\n--- got ---\n%s", r, got)
		}
	}
}

func TestFormatStuckGuidance_EmptyCommitRecommendsSkip(t *testing.T) {
	stuck := gitstate.RebaseStuck{
		Reason:     gitstate.RebaseStuckEmptyCommit,
		StoppedSHA: "c613c80fc327d1c90c36c93259332ecb202f79d0",
	}
	got := FormatStuckGuidance(stuck)
	if !strings.Contains(got, "empty/redundant commit at c613c80") {
		t.Errorf("missing reason detail with short sha:\n%s", got)
	}
	if !strings.Contains(got, "git rebase --skip") {
		t.Errorf("missing --skip option:\n%s", got)
	}
	if !strings.Contains(got, "git rebase --skip      # drop the current commit and move on  (recommended)") {
		t.Errorf("--skip should be marked recommended:\n%s", got)
	}
	// All three resume commands plus gk continue must appear.
	for _, want := range []string{"git rebase --skip", "git rebase --continue", "git rebase --abort", "gk continue"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
}

func TestFormatStuckGuidance_EditRecommendsContinue(t *testing.T) {
	stuck := gitstate.RebaseStuck{
		Reason:     gitstate.RebaseStuckEdit,
		StoppedSHA: "abcdef1234567890",
		LastDoneOp: "edit",
	}
	got := FormatStuckGuidance(stuck)
	if !strings.Contains(got, "paused for editing at abcdef1 (edit)") {
		t.Errorf("missing edit reason detail:\n%s", got)
	}
	if !strings.Contains(got, "git rebase --continue") {
		t.Errorf("missing --continue option:\n%s", got)
	}
	// Recommended marker must be on --continue, not --skip.
	if strings.Contains(got, "git rebase --skip") && strings.Contains(got, "--skip      # drop the current commit and move on  (recommended)") {
		t.Errorf("--skip should not be recommended for Edit reason:\n%s", got)
	}
	wantRecommended := "git rebase --continue  # use the current state as-is (creates an empty commit if needed)  (recommended)"
	if !strings.Contains(got, wantRecommended) {
		t.Errorf("--continue should be recommended for Edit:\n%s", got)
	}
}

// TestFormatStuckGuidance_EditBreakOp — `git rebase --break` produces a
// done entry with no operand, so LastDoneOp is empty. displayOp must render
// "edit/break" rather than the empty string.
func TestFormatStuckGuidance_EditBreakOp(t *testing.T) {
	stuck := gitstate.RebaseStuck{
		Reason:     gitstate.RebaseStuckEdit,
		LastDoneOp: "",
	}
	got := FormatStuckGuidance(stuck)
	if !strings.Contains(got, "(edit/break)") {
		t.Errorf("empty LastDoneOp should render as edit/break:\n%s", got)
	}
	if !strings.Contains(got, "git rebase --continue  # use the current state as-is (creates an empty commit if needed)  (recommended)") {
		t.Errorf("--continue should be recommended for break:\n%s", got)
	}
}

func TestFormatStuckGuidance_ExecIncludesCommand(t *testing.T) {
	stuck := gitstate.RebaseStuck{
		Reason:      gitstate.RebaseStuckExec,
		LastDoneArg: "make test",
	}
	got := FormatStuckGuidance(stuck)
	if !strings.Contains(got, "exec failed: make test") {
		t.Errorf("missing exec command in reason:\n%s", got)
	}
	if !strings.Contains(got, "git rebase --continue  # use the current state as-is (creates an empty commit if needed)  (recommended)") {
		t.Errorf("--continue should be recommended for Exec:\n%s", got)
	}
}

func TestFormatStuckGuidance_UnknownNoRecommendation(t *testing.T) {
	stuck := gitstate.RebaseStuck{Reason: gitstate.RebaseStuckUnknown}
	got := FormatStuckGuidance(stuck)
	if !strings.Contains(got, "unrecognized reason") {
		t.Errorf("missing unrecognized hint:\n%s", got)
	}
	if strings.Contains(got, "(recommended)") {
		t.Errorf("Unknown should not mark any option as recommended:\n%s", got)
	}
	for _, want := range []string{"git rebase --skip", "git rebase --continue", "git rebase --abort", "gk continue"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestStuckError_WrapsGuidance(t *testing.T) {
	stuck := gitstate.RebaseStuck{
		Reason:     gitstate.RebaseStuckEmptyCommit,
		StoppedSHA: "deadbeef0000",
	}
	err := &StuckError{Stuck: stuck}
	got := err.Error()
	if !strings.HasPrefix(got, "rebase is in progress but no conflicted files found.") {
		t.Errorf("StuckError.Error must start with prefix, got:\n%s", got)
	}
	if strings.HasSuffix(got, "\n") {
		t.Errorf("StuckError.Error must not have trailing newline (cli.FormatError adds one)")
	}
	if !strings.Contains(got, "deadbee") {
		t.Errorf("StuckError.Error must include short sha, got:\n%s", got)
	}
}

func TestCheckStuck(t *testing.T) {
	if err := CheckStuck(nil); err != nil {
		t.Errorf("nil state: want nil err, got %v", err)
	}

	// Non-rebase states never trigger.
	for _, k := range []gitstate.StateKind{gitstate.StateNone, gitstate.StateMerge, gitstate.StateCherryPick} {
		if err := CheckStuck(&gitstate.State{Kind: k}); err != nil {
			t.Errorf("Kind=%s: want nil err, got %v", k, err)
		}
	}
}

func TestFormatStuckGuidance_HandlesShortSHA(t *testing.T) {
	cases := []struct {
		sha  string
		want string
	}{
		{"", "empty/redundant commit — its changes are already in the new base"},
		{"abc", "empty/redundant commit at abc"},
		{"abcdef1", "empty/redundant commit at abcdef1"},
		{"abcdef1234567890", "empty/redundant commit at abcdef1"},
	}
	for _, c := range cases {
		got := FormatStuckGuidance(gitstate.RebaseStuck{
			Reason: gitstate.RebaseStuckEmptyCommit, StoppedSHA: c.sha,
		})
		if !strings.Contains(got, c.want) {
			t.Errorf("sha=%q: missing %q in:\n%s", c.sha, c.want, got)
		}
	}
}
