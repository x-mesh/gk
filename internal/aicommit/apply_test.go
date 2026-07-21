package aicommit

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
)

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestApplyMessagesCreatesCommitPerGroup(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "ai-commit\n"},
			"rev-parse HEAD":                    {Stdout: "abcdef\n"},
			"write-tree":                        {Stdout: "tree123\n"},
			"diff --cached --no-renames --diff-filter=D --name-only -z": {Stdout: ""},
			"diff --cached --name-status -z -M":                         {Stdout: ""},
		},
		DefaultResp: git.FakeResponse{Stdout: "[ai-commit 1111111] feat: subject\n"},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "feat", Files: []string{"a.go"}}, Subject: "add a"},
		{Group: provider.Group{Type: "test", Files: []string{"a_test.go"}}, Subject: "cover a"},
	}
	res, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	if len(res.CommitShas) != 2 {
		t.Fatalf("CommitShas: %+v", res.CommitShas)
	}
	if res.BackupRef == "" || !strings.HasPrefix(res.BackupRef, "refs/gk/ai-commit-backup/") {
		t.Errorf("BackupRef malformed: %q", res.BackupRef)
	}
	if res.TreeBefore != "tree123" {
		t.Errorf("TreeBefore: %q", res.TreeBefore)
	}

	// Verify 2 `git add -A -- a.go` and 2 `git commit -m ... --` calls.
	addCalls, commitCalls := 0, 0
	for _, c := range fake.Calls {
		switch {
		case len(c.Args) >= 3 && c.Args[0] == "add" && c.Args[1] == "-A" && c.Args[2] == "--":
			addCalls++
		case len(c.Args) >= 1 && c.Args[0] == "commit":
			commitCalls++
		}
	}
	if addCalls != 2 || commitCalls != 2 {
		t.Errorf("add=%d commit=%d (want 2,2), calls=%+v", addCalls, commitCalls, fake.Calls)
	}
}

// TestApplyMessagesRecomputesStagedStatePerGroup guards the fix for
// pitfall 1-A: stagedDeletedPaths / stagedRenamePairs must be recomputed
// inside the loop, once per group. If they were captured once before the
// loop, a deletion committed by group 1 would feed stale data into group
// 2's `git add -A` / commit pathspec. We assert the status probes fire
// twice (once per group) and that group 2's `git add -A` still runs.
func TestApplyMessagesRecomputesStagedStatePerGroup(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			// No staged deletions / renames at any point — the key check
			// is that these probes are re-run per group, not the content.
			"diff --cached --no-renames --diff-filter=D --name-only -z": {Stdout: ""},
			"diff --cached --name-status -z -M":                         {Stdout: ""},
		},
		DefaultResp: git.FakeResponse{Stdout: "[main 7777777] chore: x\n"},
	}
	msgs := []Message{
		// Group 1 deletes a file; group 2 adds a new one. Realistically
		// the deletion shifts the staged set, so group 2 must observe the
		// post-commit state — only possible if the probes re-run.
		{Group: provider.Group{Type: "chore", Files: []string{"old.go"}}, Subject: "drop old"},
		{Group: provider.Group{Type: "feat", Files: []string{"new.go"}}, Subject: "add new"},
	}
	if _, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}

	delProbes, renameProbes, group2Add := 0, 0, false
	for _, c := range fake.Calls {
		key := strings.Join(c.Args, " ")
		switch key {
		case "diff --cached --no-renames --diff-filter=D --name-only -z":
			delProbes++
		case "diff --cached --name-status -z -M":
			renameProbes++
		case "add -A -- new.go":
			group2Add = true
		}
	}
	if delProbes != 2 {
		t.Errorf("staged-deletion probe ran %d times, want 2 (once per group)", delProbes)
	}
	if renameProbes != 2 {
		t.Errorf("staged-rename probe ran %d times, want 2 (once per group)", renameProbes)
	}
	if !group2Add {
		t.Errorf("group 2 `git add -A -- new.go` never ran, calls=%+v", fake.Calls)
	}
}

// TestApplyMessagesStaleDeleteNoLongerBreaksLaterGroup exercises the
// concrete failure the per-group recompute prevents: a path that is a
// fully-staged deletion only at entry (so the pre-loop capture would skip
// it forever) but is a normal modification by the time group 2 runs. With
// per-group recompute group 2 sees an empty deletion set and stages its
// file normally.
func TestApplyMessagesStaleDeleteNoLongerBreaksLaterGroup(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			"diff --cached --no-renames --diff-filter=D --name-only -z": {Stdout: ""},
			"diff --cached --name-status -z -M":                         {Stdout: ""},
		},
		DefaultResp: git.FakeResponse{Stdout: "[main 8888888] x\n"},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "chore", Files: []string{"a.go"}}, Subject: "first"},
		{Group: provider.Group{Type: "feat", Files: []string{"b.go"}}, Subject: "second"},
	}
	if _, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	// Both groups must stage their own file.
	var sawA, sawB bool
	for _, c := range fake.Calls {
		switch strings.Join(c.Args, " ") {
		case "add -A -- a.go":
			sawA = true
		case "add -A -- b.go":
			sawB = true
		}
	}
	if !sawA || !sawB {
		t.Errorf("both groups must run their own add: sawA=%v sawB=%v calls=%+v", sawA, sawB, fake.Calls)
	}
}

// TestApplyMessagesAllowEmptyCommitsWithFlag verifies that an empty-file
// group with AllowEmpty commits via `git commit --allow-empty` and never
// invokes `git add` (a zero-pathspec add would stage the whole repo).
func TestApplyMessagesAllowEmptyCommitsWithFlag(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			// Clean index — the allow_empty guard must see no staged paths.
			"diff --cached --name-only": {Stdout: ""},
		},
		DefaultResp: git.FakeResponse{Stdout: "[main 9999999] chore: empty\n"},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "chore"}, Subject: "trigger ci", AllowEmpty: true},
	}
	if _, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	var sawAllowEmpty, sawAdd bool
	for _, c := range fake.Calls {
		if len(c.Args) >= 2 && c.Args[0] == "commit" && contains(c.Args, "--allow-empty") {
			sawAllowEmpty = true
		}
		if len(c.Args) >= 1 && c.Args[0] == "add" {
			sawAdd = true
		}
	}
	if !sawAllowEmpty {
		t.Errorf("expected `git commit --allow-empty`, calls=%+v", fake.Calls)
	}
	if sawAdd {
		t.Errorf("AllowEmpty must not invoke git add, calls=%+v", fake.Calls)
	}
}

// TestApplyMessagesAllowEmptyRefusesStagedIndex — Codex review P1: a
// pathspec-less `git commit --allow-empty` consumes the INDEX, so staged
// files outside the plan would ride into the "empty" commit unscanned.
// The guard must refuse instead of committing.
func TestApplyMessagesAllowEmptyRefusesStagedIndex(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			"diff --cached --name-only":         {Stdout: "secret.txt\n"},
		},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "chore"}, Subject: "trigger ci", AllowEmpty: true},
	}
	_, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "refusing allow_empty") {
		t.Fatalf("want refusing allow_empty error, got %v", err)
	}
	for _, c := range fake.Calls {
		if len(c.Args) > 0 && c.Args[0] == "commit" {
			t.Errorf("must not commit over a dirty index, calls=%+v", fake.Calls)
		}
	}
}

// TestMessageHeaderBreakingMarker pins that Breaking renders the "!" in
// the header so a plan's "feat(x)!: ..." round-trips without losing the
// breaking marker.
func TestMessageHeaderBreakingMarker(t *testing.T) {
	cases := []struct {
		name string
		m    Message
		want string
	}{
		{
			name: "type+scope+breaking",
			m:    Message{Group: provider.Group{Type: "feat", Scope: "api"}, Subject: "drop v1", Breaking: true},
			want: "feat(api)!: drop v1",
		},
		{
			name: "bare type+breaking",
			m:    Message{Group: provider.Group{Type: "feat"}, Subject: "rip out legacy", Breaking: true},
			want: "feat!: rip out legacy",
		},
		{
			name: "not breaking — no marker",
			m:    Message{Group: provider.Group{Type: "feat"}, Subject: "add thing"},
			want: "feat: add thing",
		},
		{
			name: "duplicated breaking prefix on subject collapses",
			m:    Message{Group: provider.Group{Type: "feat"}, Subject: "feat!: rip out legacy", Breaking: true},
			want: "feat!: rip out legacy",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.Header(); got != tc.want {
				t.Errorf("Header() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestApplyMessagesDryRunMakesNoCommits(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			"diff --cached --no-renames --diff-filter=D --name-only -z": {Stdout: ""},
			"diff --cached --name-status -z -M":                         {Stdout: ""},
		},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "feat", Files: []string{"a.go"}}, Subject: "add a"},
	}
	res, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	for _, c := range fake.Calls {
		if len(c.Args) > 0 && c.Args[0] == "commit" {
			t.Error("DryRun must not invoke git commit")
		}
		// Codex review P2: a preview must not write refs — a dry-run
		// backup would become the LATEST ref and retarget --abort.
		if len(c.Args) > 0 && c.Args[0] == "update-ref" {
			t.Error("DryRun must not write a backup ref")
		}
	}
	if res.BackupRef != "" {
		t.Errorf("BackupRef in dry-run must be empty, got %q", res.BackupRef)
	}
	if len(res.CommitShas) != 1 || res.CommitShas[0] != "" {
		t.Errorf("CommitShas in dry-run: %+v", res.CommitShas)
	}
}

func TestApplyMessagesAddFailureAborts(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			"diff --cached --no-renames --diff-filter=D --name-only -z": {Stdout: ""},
			"diff --cached --name-status -z -M":                         {Stdout: ""},
			"add -A -- bad.go": {
				Stderr:   "fatal: pathspec 'bad.go' did not match any files",
				ExitCode: 128,
			},
		},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "feat", Files: []string{"bad.go"}}, Subject: "x"},
	}
	_, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{})
	if err == nil {
		t.Fatal("want error when git add fails")
	}
	if !strings.Contains(err.Error(), "pathspec") {
		t.Errorf("error should include stderr: %v", err)
	}
}

// TestApplyMessagesStagesUnstagedDeletion covers the path where the
// user removed a tracked file from disk but hadn't run `git rm` — the
// index still has the entry, working tree doesn't (porcelain " D"). The
// file is tracked, so it stages via `git add -u -- <path>`, which records
// deletions of tracked entries just as well as modifications. The earlier
// bug silently lost the deletion; this guards the fix.
func TestApplyMessagesStagesUnstagedDeletion(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			"diff --cached --no-renames --diff-filter=D --name-only -z": {Stdout: ""},
			// gone.go still has an index entry → git tracks it → `git add -u`.
			"ls-files -z -- gone.go": {Stdout: "gone.go\x00"},
		},
		DefaultResp: git.FakeResponse{Stdout: "[main 2222222] chore: drop\n"},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "chore", Files: []string{"gone.go"}}, Subject: "drop unused"},
	}
	if _, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	var saw bool
	for _, c := range fake.Calls {
		if len(c.Args) >= 4 && c.Args[0] == "add" && c.Args[1] == "-u" && c.Args[2] == "--" && c.Args[3] == "gone.go" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected `git add -u -- gone.go`, got calls=%+v", fake.Calls)
	}
}

// TestApplyMessagesStagesTrackedFileInIgnoredDir guards the 2026-06-15
// fix: a file git already tracks but that lives inside a gitignored
// directory (e.g. a config force-added under an ignored data/ dir) makes
// `git add -A -- <path>` fail with "paths are ignored, use -f". gk must
// stage it via `git add -u` (tracked-only, ignore-blind) so the commit
// still lands. The fake fails on -A to prove gk never takes that path.
func TestApplyMessagesStagesTrackedFileInIgnoredDir(t *testing.T) {
	const path = "data/space_names.json"
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			"diff --cached --no-renames --diff-filter=D --name-only -z": {Stdout: ""},
			"diff --cached --name-status -z -M":                         {Stdout: ""},
			// git tracks the file despite its ignored parent dir.
			"ls-files -z -- " + path: {Stdout: path + "\x00"},
			// Plain `git add -A` would reject the ignored path — if gk ever
			// routed a tracked file here again, this turns the test red.
			"add -A -- " + path: {
				Stderr:   "The following paths are ignored by one of your .gitignore files:\ndata\nhint: Use -f if you really want to add them.",
				ExitCode: 1,
			},
		},
		DefaultResp: git.FakeResponse{Stdout: "[main 4444444] chore: x\n"},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "chore", Files: []string{path}}, Subject: "update mappings"},
	}
	if _, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyMessages must succeed for a tracked file in an ignored dir: %v", err)
	}
	var sawUpdate, sawForbiddenA bool
	for _, c := range fake.Calls {
		switch strings.Join(c.Args, " ") {
		case "add -u -- " + path:
			sawUpdate = true
		case "add -A -- " + path:
			sawForbiddenA = true
		}
	}
	if !sawUpdate {
		t.Errorf("expected `git add -u -- %s`, calls=%+v", path, fake.Calls)
	}
	if sawForbiddenA {
		t.Errorf("must not run the ignore-rejecting `git add -A -- %s`, calls=%+v", path, fake.Calls)
	}
}

// TestApplyMessagesSkipsAddForFullyStagedDeletion guards the
// 2026-04-29 regression: when a tracked file is already fully staged
// for deletion (porcelain "D " — gone from working tree AND from
// index), `git add -A -- <path>` fails with "pathspec did not match
// any files". The fix excludes such paths from the add invocation;
// `git commit -- <path>` still picks up the deletion via HEAD diff.
func TestApplyMessagesSkipsAddForFullyStagedDeletion(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			"diff --cached --no-renames --diff-filter=D --name-only -z": {
				Stdout: "removed.go\x00",
			},
		},
		DefaultResp: git.FakeResponse{Stdout: "[main 3333333] chore: drop\n"},
	}
	// Group mixes a fully-staged-deletion path with a normal one. Only
	// the normal path should reach `git add`; both reach `git commit`.
	msgs := []Message{
		{
			Group:   provider.Group{Type: "chore", Files: []string{"removed.go", "kept.go"}},
			Subject: "tidy",
		},
	}
	if _, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	var addArgs, commitArgs []string
	for _, c := range fake.Calls {
		if len(c.Args) >= 1 && c.Args[0] == "add" {
			addArgs = c.Args
		}
		if len(c.Args) >= 1 && c.Args[0] == "commit" {
			commitArgs = c.Args
		}
	}
	// add must be called with kept.go only — never removed.go.
	want := []string{"add", "-A", "--", "kept.go"}
	if !reflect.DeepEqual(addArgs, want) {
		t.Errorf("add args = %v, want %v", addArgs, want)
	}
	// commit still references both paths.
	if !contains(commitArgs, "removed.go") || !contains(commitArgs, "kept.go") {
		t.Errorf("commit args missing one of the files: %v", commitArgs)
	}
}

// TestApplyMessagesSkipsAddEntirelyWhenAllStagedDeleted exercises the
// edge case where every file in a group is already fully staged for
// deletion — `git add` should not be invoked at all (zero pathspecs
// would otherwise stage every dirty file in the repo).
func TestApplyMessagesSkipsAddEntirelyWhenAllStagedDeleted(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			"diff --cached --no-renames --diff-filter=D --name-only -z": {
				Stdout: "a\x00b\x00",
			},
		},
		DefaultResp: git.FakeResponse{Stdout: "[main 4444444] chore: drop\n"},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "chore", Files: []string{"a", "b"}}, Subject: "drop"},
	}
	if _, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	for _, c := range fake.Calls {
		if len(c.Args) >= 1 && c.Args[0] == "add" {
			t.Errorf("git add must not be invoked when all files are fully staged-deleted, got %v", c.Args)
		}
	}
}

// TestApplyMessagesExpandsRenamePairsInCommitPathspec guards the fix for
// dangling staged deletions when a grouper emits only the new path of a
// staged rename. The orig (deletion) side must be included in the commit
// pathspec so both sides land in the same commit.
func TestApplyMessagesExpandsRenamePairsInCommitPathspec(t *testing.T) {
	// R100 means 100% rename similarity: old.go -> new.go
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			"diff --cached --no-renames --diff-filter=D --name-only -z": {Stdout: ""},
			"diff --cached --name-status -z -M":                         {Stdout: "R100\x00old.go\x00new.go\x00"},
		},
		DefaultResp: git.FakeResponse{Stdout: "[main 5555555] feat: rename\n"},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "feat", Files: []string{"new.go"}}, Subject: "rename old to new"},
	}
	if _, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	var commitArgs []string
	for _, c := range fake.Calls {
		if len(c.Args) >= 1 && c.Args[0] == "commit" {
			commitArgs = c.Args
		}
	}
	if !contains(commitArgs, "new.go") {
		t.Errorf("commit args missing new.go: %v", commitArgs)
	}
	if !contains(commitArgs, "old.go") {
		t.Errorf("commit args missing orig path old.go: %v", commitArgs)
	}
}

// TestApplyMessagesNoRenamePairsLeavesPathspecAsIs checks that when there
// are no staged renames the commit pathspec matches Group.Files exactly.
func TestApplyMessagesNoRenamePairsLeavesPathspecAsIs(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"rev-parse HEAD":                    {Stdout: "abc\n"},
			"write-tree":                        {Stdout: "t\n"},
			"diff --cached --no-renames --diff-filter=D --name-only -z": {Stdout: ""},
			"diff --cached --name-status -z -M":                         {Stdout: ""},
		},
		DefaultResp: git.FakeResponse{Stdout: "[main 6666666] feat: add\n"},
	}
	msgs := []Message{
		{Group: provider.Group{Type: "feat", Files: []string{"a.go", "b.go"}}, Subject: "add files"},
	}
	if _, err := ApplyMessages(context.Background(), fake, msgs, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyMessages: %v", err)
	}
	var commitArgs []string
	for _, c := range fake.Calls {
		if len(c.Args) >= 1 && c.Args[0] == "commit" {
			commitArgs = c.Args
		}
	}
	// Expect exactly: commit -m <msg> -- a.go b.go (no extras)
	var pathArgs []string
	pastSep := false
	for _, a := range commitArgs {
		if a == "--" {
			pastSep = true
			continue
		}
		if pastSep {
			pathArgs = append(pathArgs, a)
		}
	}
	want := []string{"a.go", "b.go"}
	if !reflect.DeepEqual(pathArgs, want) {
		t.Errorf("commit path args = %v, want %v", pathArgs, want)
	}
}

// TestExpandRenamePairsHelper unit-tests the helper directly:
// dedup, input-order preservation, and orig insertion position.
func TestExpandRenamePairsHelper(t *testing.T) {
	t.Run("empty pairs returns input unchanged", func(t *testing.T) {
		in := []string{"a.go", "b.go"}
		got := expandRenamePairs(in, nil)
		if !reflect.DeepEqual(got, in) {
			t.Errorf("got %v, want %v", got, in)
		}
	})

	t.Run("orig inserted directly after new", func(t *testing.T) {
		pairs := map[string]string{"new.go": "old.go"}
		got := expandRenamePairs([]string{"x.go", "new.go", "y.go"}, pairs)
		want := []string{"x.go", "new.go", "old.go", "y.go"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("dedup: orig already present not duplicated", func(t *testing.T) {
		pairs := map[string]string{"new.go": "old.go"}
		got := expandRenamePairs([]string{"old.go", "new.go"}, pairs)
		// old.go appears first (input order); new.go follows; old.go not re-added
		want := []string{"old.go", "new.go"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("dedup: new appears twice in input", func(t *testing.T) {
		pairs := map[string]string{"new.go": "old.go"}
		got := expandRenamePairs([]string{"new.go", "new.go"}, pairs)
		want := []string{"new.go", "old.go"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

// TestStagedRenamePairsParsesNZFormat checks that the -z parser correctly
// handles mixed (M, R, A, D) output from git diff --cached --name-status -z -M.
func TestStagedRenamePairsParsesNZFormat(t *testing.T) {
	// Simulate: M modified.go, R100 old.go->new.go, A added.go, D deleted.go
	nzOutput := "M\x00modified.go\x00R100\x00old.go\x00new.go\x00A\x00added.go\x00D\x00deleted.go\x00"
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"diff --cached --name-status -z -M": {Stdout: nzOutput},
		},
	}
	pairs, err := stagedRenamePairs(context.Background(), fake)
	if err != nil {
		t.Fatalf("stagedRenamePairs: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("expected 1 pair, got %d: %v", len(pairs), pairs)
	}
	if pairs["new.go"] != "old.go" {
		t.Errorf("pairs[new.go] = %q, want %q", pairs["new.go"], "old.go")
	}
	// Non-rename keys must not appear.
	for _, k := range []string{"modified.go", "added.go", "deleted.go"} {
		if _, ok := pairs[k]; ok {
			t.Errorf("unexpected key %q in pairs", k)
		}
	}
}

func TestAbortRestoreEmptyBackupIsNoop(t *testing.T) {
	fake := &git.FakeRunner{}
	result, err := AbortRestore(context.Background(), fake, "")
	if err != nil {
		t.Errorf("empty backup should be no-op, got %v", err)
	}
	if result.SafetyRef != "" {
		t.Errorf("empty backup should return no safety ref, got %q", result.SafetyRef)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("expected no git calls, got %+v", fake.Calls)
	}
}

func TestAbortRestoreRunsMixedReset(t *testing.T) {
	fake := &git.FakeRunner{
		Responses: map[string]git.FakeResponse{
			"symbolic-ref --quiet --short HEAD": {Stdout: "main\n"},
			"status --porcelain=v1 -uno":        {Stdout: ""},
			"rev-parse --verify HEAD^{commit}":  {Stdout: "deadbeef\n"},
		},
	}
	result, err := AbortRestore(context.Background(), fake, "refs/gk/ai-commit-backup/main/123")
	if err != nil {
		t.Errorf("AbortRestore: %v", err)
	}
	if result.SafetyRef == "" {
		t.Error("expected a safety-net ref to be written before the hard reset")
	}
	if result.StashConflict != "" {
		t.Errorf("clean tree should not report a stash conflict, got %q", result.StashConflict)
	}
	var resetCall, stashCall *git.FakeCall
	for i := range fake.Calls {
		switch fake.Calls[i].Args[0] {
		case "reset":
			resetCall = &fake.Calls[i]
		case "stash":
			stashCall = &fake.Calls[i]
		}
	}
	if resetCall == nil {
		t.Fatalf("no reset call among: %+v", fake.Calls)
	}
	args := resetCall.Args
	want := []string{"reset", "--mixed", "refs/gk/ai-commit-backup/main/123"}
	for i, w := range want {
		if args[i] != w {
			t.Errorf("args[%d]: want %q, got %q", i, w, args[i])
		}
	}
	// Mixed reset preserves the working tree and needs no stash dance.
	if stashCall != nil {
		t.Errorf("expected no stash call on a clean tree, got %+v", *stashCall)
	}
	// The safety ref must point at the PRE-reset HEAD (deadbeef), not the
	// backup target — otherwise it would be redundant with backupRef itself.
	var updateRefCall *git.FakeCall
	for i := range fake.Calls {
		if fake.Calls[i].Args[0] == "update-ref" {
			updateRefCall = &fake.Calls[i]
		}
	}
	if updateRefCall == nil {
		t.Fatalf("no update-ref call among: %+v", fake.Calls)
	}
	if updateRefCall.Args[1] != result.SafetyRef {
		t.Errorf("update-ref target = %q, want returned safety ref %q", updateRefCall.Args[1], result.SafetyRef)
	}
	if updateRefCall.Args[2] != "HEAD" {
		t.Errorf("update-ref value = %q, want literal %q (resolved by git itself)", updateRefCall.Args[2], "HEAD")
	}
}

// TestAbortRestorePreservesDirtyWorkingTree proves mixed abort preserves an
// unrelated uncommitted edit even when no AI commit was created.
func TestAbortRestorePreservesDirtyWorkingTree(t *testing.T) {
	dir := t.TempDir()
	runner := &git.ExecRunner{Dir: dir}
	ctx := context.Background()
	run := func(args ...string) string {
		t.Helper()
		out, stderr, err := runner.Run(ctx, args...)
		if err != nil {
			t.Fatalf("git %s: %v (stderr=%s)", strings.Join(args, " "), err, stderr)
		}
		return string(out)
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(dir+"/tracked.txt", []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked.txt")
	run("commit", "-q", "-m", "init")

	// Simulate EnsureBackupRef's snapshot at the start of an ai-commit run
	// that then fails before creating any commit — backupRef == HEAD.
	backupRef, err := EnsureBackupRef(ctx, runner)
	if err != nil {
		t.Fatalf("EnsureBackupRef: %v", err)
	}

	// The user's uncommitted edit, unrelated to the failed run, must
	// survive the abort.
	if err := os.WriteFile(dir+"/tracked.txt", []byte("v1 + my uncommitted edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := AbortRestore(ctx, runner, backupRef)
	if err != nil {
		t.Fatalf("AbortRestore: %v", err)
	}
	if result.StashConflict != "" {
		t.Fatalf("unexpected stash conflict: %q", result.StashConflict)
	}

	got, err := os.ReadFile(dir + "/tracked.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "v1 + my uncommitted edit\n" {
		t.Fatalf("uncommitted edit lost across abort: got %q", string(got))
	}

	head := strings.TrimSpace(run("rev-parse", "HEAD"))
	backupSHA := strings.TrimSpace(run("rev-parse", backupRef))
	if head != backupSHA {
		t.Fatalf("HEAD = %s, want backup ref target %s", head, backupSHA)
	}
}

// TestAbortRestoreResetsPastCommitAndKeepsUnrelatedEdit covers the case
// where the AI commit DID land (HEAD moved past backupRef) and the user then
// made an uncommitted edit to a file the AI commit never touched — abort must
// land HEAD back at backupRef while preserving both sets of work unstaged.
func TestAbortRestoreResetsPastCommitAndKeepsUnrelatedEdit(t *testing.T) {
	dir := t.TempDir()
	runner := &git.ExecRunner{Dir: dir}
	ctx := context.Background()
	run := func(args ...string) string {
		t.Helper()
		out, stderr, err := runner.Run(ctx, args...)
		if err != nil {
			t.Fatalf("git %s: %v (stderr=%s)", strings.Join(args, " "), err, stderr)
		}
		return string(out)
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(dir+"/committed.txt", []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/other.txt", []byte("other v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "committed.txt", "other.txt")
	run("commit", "-q", "-m", "init")

	backupRef, err := EnsureBackupRef(ctx, runner)
	if err != nil {
		t.Fatalf("EnsureBackupRef: %v", err)
	}

	// The AI commit lands, touching only committed.txt.
	if err := os.WriteFile(dir+"/committed.txt", []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/new-from-ai.txt", []byte("new work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "committed.txt", "new-from-ai.txt")
	run("commit", "-q", "-m", "ai commit")

	// Separately, an uncommitted edit to the untouched file.
	if err := os.WriteFile(dir+"/other.txt", []byte("other v1 + my edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := AbortRestore(ctx, runner, backupRef)
	if err != nil {
		t.Fatalf("AbortRestore: %v", err)
	}
	if result.StashConflict != "" {
		t.Fatalf("unexpected stash conflict: %q", result.StashConflict)
	}

	gotCommitted, err := os.ReadFile(dir + "/committed.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCommitted) != "v2\n" {
		t.Fatalf("AI commit content must remain unstaged after abort: got %q", string(gotCommitted))
	}
	gotNew, err := os.ReadFile(dir + "/new-from-ai.txt")
	if err != nil {
		t.Fatalf("new file from AI commit was removed: %v", err)
	}
	if string(gotNew) != "new work\n" {
		t.Fatalf("new file content = %q, want preserved AI work", string(gotNew))
	}
	gotOther, err := os.ReadFile(dir + "/other.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(gotOther) != "other v1 + my edit\n" {
		t.Fatalf("unrelated uncommitted edit lost across abort: got %q", string(gotOther))
	}

	head := strings.TrimSpace(run("rev-parse", "HEAD"))
	backupSHA := strings.TrimSpace(run("rev-parse", backupRef))
	if head != backupSHA {
		t.Fatalf("HEAD = %s, want backup ref target %s", head, backupSHA)
	}
}

func TestFormatCommitMessageJoinsCleanly(t *testing.T) {
	m := Message{
		Group:   provider.Group{Type: "feat", Scope: "ai"},
		Subject: "add provider factory",
		Body:    "Autodetects gemini, qwen, kiro.",
		Footers: []provider.Footer{{Token: "Refs", Value: "#42"}},
	}
	got := formatCommitMessage(m, "gemini@0.38.2")
	wantHeader := "feat(ai): add provider factory"
	wantBody := "Autodetects gemini, qwen, kiro."
	wantFoot := "Refs: #42"
	wantTrailer := "AI-Assisted-By: gemini@0.38.2"
	for _, want := range []string{wantHeader, wantBody, wantFoot, wantTrailer} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted message missing %q:\n%s", want, got)
		}
	}
	// Trailer is last.
	if idx := strings.Index(got, wantTrailer); idx == -1 || idx+len(wantTrailer) != len(got) {
		t.Errorf("trailer should be at end, got:\n%s", got)
	}
}

// TestFormatCommitMessage_StripsDuplicatedPrefix guards against the
// "build: build: ..." style header that surfaces when the LLM tucks the
// Conventional Commits header onto Subject and the formatter prepends
// it again.
func TestFormatCommitMessage_StripsDuplicatedPrefix(t *testing.T) {
	cases := []struct {
		name    string
		group   provider.Group
		subject string
		want    string
	}{
		{
			name:    "type only",
			group:   provider.Group{Type: "build"},
			subject: "build: embed branch and worktree name",
			want:    "build: embed branch and worktree name",
		},
		{
			name:    "type with scope",
			group:   provider.Group{Type: "refactor", Scope: "internal"},
			subject: "refactor(internal): improve dirty-tree UX",
			want:    "refactor(internal): improve dirty-tree UX",
		},
		{
			name:    "scope mismatch still strips",
			group:   provider.Group{Type: "feat", Scope: "ai"},
			subject: "feat(internal): add provider factory",
			want:    "feat(ai): add provider factory",
		},
		{
			name:    "case-insensitive type",
			group:   provider.Group{Type: "fix"},
			subject: "Fix: handle nil pointer",
			want:    "fix: handle nil pointer",
		},
		{
			name:    "no prefix to strip",
			group:   provider.Group{Type: "feat"},
			subject: "add provider factory",
			want:    "feat: add provider factory",
		},
		{
			name:    "subject is *only* prefix — keep as-is",
			group:   provider.Group{Type: "feat"},
			subject: "feat:",
			want:    "feat: feat:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatCommitMessage(Message{Group: tc.group, Subject: tc.subject}, "")
			// Compare just the header (first line).
			line := strings.SplitN(got, "\n", 2)[0]
			if line != tc.want {
				t.Errorf("header = %q, want %q", line, tc.want)
			}
		})
	}
}

// TestMessageHeader pins the shared header renderer used by both the commit
// preview (printSummary / the interactive picker) and the committed message.
// The regression: the LLM tucked the full Conventional-Commits prefix onto
// Subject, so the preview doubled up to "feat(internal): feat(internal): ..."
// while the committed message (which already stripped it) read correctly —
// preview and reality disagreed. Header() must emit the prefix exactly once.
func TestMessageHeader(t *testing.T) {
	cases := []struct {
		name    string
		group   provider.Group
		subject string
		want    string
	}{
		{
			name:    "duplicated type+scope prefix on subject",
			group:   provider.Group{Type: "feat", Scope: "internal"},
			subject: "feat(internal): link git-kit alias after binary upgrade",
			want:    "feat(internal): link git-kit alias after binary upgrade",
		},
		{
			name:    "duplicated bare type prefix on subject",
			group:   provider.Group{Type: "build"},
			subject: "build: add ALT_NAME variable",
			want:    "build: add ALT_NAME variable",
		},
		{
			name:    "clean subject gets the prefix prepended",
			group:   provider.Group{Type: "docs"},
			subject: "document git-kit alias guarantee",
			want:    "docs: document git-kit alias guarantee",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Message{Group: tc.group, Subject: tc.subject}).Header(); got != tc.want {
				t.Errorf("Header() = %q, want %q", got, tc.want)
			}
		})
	}
}
