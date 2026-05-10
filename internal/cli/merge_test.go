package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fatih/color"

	"github.com/x-mesh/gk/internal/ai/provider"
	"github.com/x-mesh/gk/internal/git"
)

func TestRunMergeCorePrechecksAndMerges(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --verify main^{commit}": {Stdout: "abc123\n"},
		"status --porcelain=v1 -uno":       {Stdout: ""},
		"merge-base HEAD main":             {Stdout: "base123\n"},
		"merge-tree --write-tree --no-messages --name-only --merge-base base123 HEAD main": {Stdout: "tree123\n"},
		"rev-parse HEAD":                        {Stdout: "old123456\n"},
		"log --oneline HEAD..main":              {Stdout: "abc123 feat: incoming\n"},
		"diff --stat HEAD..main":                {Stdout: " file.go | 2 ++\n"},
		"diff --name-status HEAD..main":         {Stdout: "M\tfile.go\n"},
		"merge --no-edit main":                  {Stdout: "merged\n"},
		"rev-list --count old123456..new123456": {Stdout: "2\n"},
	}}
	runner.Responses["rev-parse HEAD"] = git.FakeResponse{Stdout: "old123456\n"}
	var errOut bytes.Buffer

	err := runMergeCore(context.Background(), mergeDeps{
		Runner: &sequenceRunner{
			FakeRunner: runner,
			sequence: map[string][]git.FakeResponse{
				"rev-parse HEAD": {{Stdout: "old123456\n"}, {Stdout: "new123456\n"}},
			},
		},
		ErrOut: &errOut,
	}, "main", mergeFlags{})
	if err != nil {
		t.Fatalf("runMergeCore: %v", err)
	}

	got := joinedShipCalls(runner.Calls)
	for _, want := range []string{
		"merge-tree --write-tree --no-messages --name-only --merge-base base123 HEAD main",
		"merge --no-edit main",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing call %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(errOut.String(), "merged main") {
		t.Fatalf("expected merge summary, got:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "MERGE PLAN") {
		t.Fatalf("expected merge plan section, got:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "via local git") {
		t.Fatalf("expected local plan source in summary slot, got:\n%s", errOut.String())
	}
}

func TestRunMergeCoreBlocksPrecheckConflicts(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --verify main^{commit}": {Stdout: "abc123\n"},
		"status --porcelain=v1 -uno":       {Stdout: ""},
		"merge-base HEAD main":             {Stdout: "base123\n"},
		"merge-tree --write-tree --no-messages --name-only --merge-base base123 HEAD main": {
			Stdout:   "0123456789abcdef0123456789abcdef01234567\nconflict.go\n",
			ExitCode: 1,
		},
		"log --oneline HEAD..main":      {Stdout: "abc123 feat: incoming\n"},
		"diff --stat HEAD..main":        {Stdout: " conflict.go | 2 ++\n"},
		"diff --name-status HEAD..main": {Stdout: "M\tconflict.go\n"},
	}}
	var errOut bytes.Buffer

	err := runMergeCore(context.Background(), mergeDeps{Runner: runner, ErrOut: &errOut}, "main", mergeFlags{})
	if err == nil {
		t.Fatal("expected precheck conflict error")
	}
	if !strings.Contains(err.Error(), "precheck found 1 conflict") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(joinedShipCalls(runner.Calls), "merge --no-edit main") {
		t.Fatal("merge should not run after precheck conflict")
	}
	if !strings.Contains(errOut.String(), "1 conflict(s)") {
		t.Fatalf("expected conflict count in plan summary, got:\n%s", errOut.String())
	}
}

func TestRunMergeCorePlanOnlyDoesNotMerge(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --verify main^{commit}": {Stdout: "abc123\n"},
		"symbolic-ref --short HEAD":        {Stdout: "feature/ship\n"},
		"merge-base HEAD main":             {Stdout: "base123\n"},
		"merge-tree --write-tree --no-messages --name-only --merge-base base123 HEAD main": {Stdout: "tree123\n"},
		"log --oneline HEAD..main":      {Stdout: "abc123 feat: incoming\n"},
		"diff --stat HEAD..main":        {Stdout: " file.go | 2 ++\n"},
		"diff --name-status HEAD..main": {Stdout: "M\tfile.go\n"},
	}}
	var errOut bytes.Buffer

	err := runMergeCore(context.Background(), mergeDeps{
		Runner: runner,
		ErrOut: &errOut,
	}, "main", mergeFlags{planOnly: true})
	if err != nil {
		t.Fatalf("runMergeCore: %v", err)
	}
	if strings.Contains(joinedShipCalls(runner.Calls), "merge --no-edit main") {
		t.Fatal("plan-only should not merge")
	}
	if !strings.Contains(errOut.String(), "MERGE PLAN") {
		t.Fatalf("expected merge plan section, got:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "main → feature/ship") {
		t.Fatalf("expected explicit merge direction, got:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "it does NOT merge feature/ship into main") {
		t.Fatalf("expected reverse-direction warning, got:\n%s", errOut.String())
	}
}

func TestRunMergeCorePlanOnlyAllowsDirtyTree(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --verify main^{commit}": {Stdout: "abc123\n"},
		"merge-base HEAD main":             {Stdout: "base123\n"},
		"merge-tree --write-tree --no-messages --name-only --merge-base base123 HEAD main": {Stdout: "tree123\n"},
		"log --oneline HEAD..main":      {Stdout: "abc123 feat: incoming\n"},
		"diff --stat HEAD..main":        {Stdout: " file.go | 2 ++\n"},
		"diff --name-status HEAD..main": {Stdout: "M\tfile.go\n"},
		"status --porcelain=v1 -uno":    {Stdout: " M local.go\n"},
	}}
	var errOut bytes.Buffer

	err := runMergeCore(context.Background(), mergeDeps{
		Runner: runner,
		ErrOut: &errOut,
	}, "main", mergeFlags{planOnly: true})
	if err != nil {
		t.Fatalf("runMergeCore: %v", err)
	}
	calls := joinedShipCalls(runner.Calls)
	if strings.Contains(calls, "status --porcelain=v1 -uno") {
		t.Fatalf("plan-only should not check dirty state, calls:\n%s", calls)
	}
	if strings.Contains(calls, "merge --no-edit main") {
		t.Fatal("plan-only should not merge")
	}
}

func TestRunMergeCorePlanOnlyNoAIUsesLocalPlan(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --verify main^{commit}": {Stdout: "abc123\n"},
		"merge-base HEAD main":             {Stdout: "base123\n"},
		"merge-tree --write-tree --no-messages --name-only --merge-base base123 HEAD main": {Stdout: "tree123\n"},
		"log --oneline HEAD..main":      {Stdout: "abc123 feat: incoming\n"},
		"diff --stat HEAD..main":        {Stdout: " file.go | 2 ++\n"},
		"diff --name-status HEAD..main": {Stdout: "M\tfile.go\n"},
	}}
	var errOut bytes.Buffer

	err := runMergeCore(context.Background(), mergeDeps{
		Runner: runner,
		ErrOut: &errOut,
	}, "main", mergeFlags{planOnly: true, noAI: true})
	if err != nil {
		t.Fatalf("runMergeCore: %v", err)
	}
	if strings.Contains(joinedShipCalls(runner.Calls), "merge --no-edit main") {
		t.Fatal("plan-only should not merge")
	}
	if !strings.Contains(errOut.String(), "MERGE PLAN (LOCAL)") {
		t.Fatalf("expected local merge plan, got:\n%s", errOut.String())
	}
}

func TestRunMergeIntoMergesCurrentBranchInReceiverWorktree(t *testing.T) {
	sourceRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD": {Stdout: "ship\n"},
		"worktree list --porcelain": {Stdout: "worktree /repo/main\nHEAD abc123\nbranch refs/heads/main\n\nworktree /repo/ship\nHEAD def456\nbranch refs/heads/ship\n"},
	}}
	targetFake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":        {Stdout: "main\n"},
		"rev-parse --verify ship^{commit}": {Stdout: "def456\n"},
		"merge-base HEAD ship":             {Stdout: "base123\n"},
		"merge-tree --write-tree --no-messages --name-only --merge-base base123 HEAD ship": {Stdout: "tree123\n"},
		"log --oneline HEAD..ship":              {Stdout: "def456 feat: ship\n"},
		"diff --stat HEAD..ship":                {Stdout: " file.go | 2 ++\n"},
		"diff --name-status HEAD..ship":         {Stdout: "M\tfile.go\n"},
		"status --porcelain=v1 -uno":            {Stdout: ""},
		"merge --no-edit ship":                  {Stdout: "merged\n"},
		"rev-list --count old123456..new123456": {Stdout: "2\n"},
	}}
	targetRunner := &sequenceRunner{
		FakeRunner: targetFake,
		sequence: map[string][]git.FakeResponse{
			"rev-parse HEAD": {{Stdout: "old123456\n"}, {Stdout: "new123456\n"}},
		},
	}
	var errOut bytes.Buffer

	err := runMergeInto(context.Background(), mergeDeps{
		Runner: sourceRunner,
		ErrOut: &errOut,
	}, nil, mergeFlags{into: "main", noAI: true}, func(path string) git.Runner {
		if path != "/repo/main" {
			t.Fatalf("runner path = %q, want /repo/main", path)
		}
		return targetRunner
	})
	if err != nil {
		t.Fatalf("runMergeInto: %v", err)
	}
	if !strings.Contains(joinedShipCalls(targetFake.Calls), "merge --no-edit ship") {
		t.Fatalf("target worktree did not merge source branch, calls:\n%s", joinedShipCalls(targetFake.Calls))
	}
	if !strings.Contains(errOut.String(), "merged ship into main") {
		t.Fatalf("expected direction-aware summary, got:\n%s", errOut.String())
	}
}

func TestRunMergeIntoUsesExplicitSource(t *testing.T) {
	sourceRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"worktree list --porcelain": {Stdout: "worktree /repo/main\nHEAD abc123\nbranch refs/heads/main\n"},
	}}
	targetRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":             {Stdout: "main\n"},
		"rev-parse --verify feature/x^{commit}": {Stdout: "def456\n"},
		"merge-base HEAD feature/x":             {Stdout: "base123\n"},
		"merge-tree --write-tree --no-messages --name-only --merge-base base123 HEAD feature/x": {Stdout: "tree123\n"},
		"log --oneline HEAD..feature/x":      {Stdout: ""},
		"diff --stat HEAD..feature/x":        {Stdout: ""},
		"diff --name-status HEAD..feature/x": {Stdout: ""},
	}}
	var errOut bytes.Buffer

	err := runMergeInto(context.Background(), mergeDeps{
		Runner: sourceRunner,
		ErrOut: &errOut,
	}, []string{"feature/x"}, mergeFlags{into: "main", planOnly: true, noAI: true}, func(string) git.Runner {
		return targetRunner
	})
	if err != nil {
		t.Fatalf("runMergeInto: %v", err)
	}
	if !strings.Contains(errOut.String(), "feature/x -> main") {
		t.Fatalf("expected explicit source direction, got:\n%s", errOut.String())
	}
}

func TestRunMergeIntoBarePrintsNextStepHint(t *testing.T) {
	// EasyEngine() returns nil in unit tests since rootCmd PersistentPreRun
	// never runs — so this test verifies that absence of EasyEngine is
	// nil-safe (no panic, no hint). The presence-and-content check is
	// covered by TestHintGenerator_MergeIntoNextHint at the easy layer.
	sourceRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":                   {Stdout: "ship\n"},
		"status --porcelain=v1 -uno":                  {Stdout: ""},
		"worktree list --porcelain":                   {Stdout: "worktree /repo/ship\nHEAD def\nbranch refs/heads/ship\n"},
		"rev-parse --verify refs/heads/main^{commit}": {Stdout: "aaa1111\n"},
		"rev-parse --verify ship^{commit}":            {Stdout: "bbb2222\n"},
		"merge-base main ship":                        {Stdout: "aaa1111\n"},
		"update-ref refs/heads/main bbb2222 aaa1111":  {Stdout: ""},
		"rev-list --count aaa1111..bbb2222":           {Stdout: "3\n"},
	}}
	var errOut bytes.Buffer
	err := runMergeInto(context.Background(), mergeDeps{Runner: sourceRunner, ErrOut: &errOut}, nil, mergeFlags{into: "main", noAI: true}, nil)
	if err != nil {
		t.Fatalf("runMergeInto: %v", err)
	}
	// Without EasyEngine wired up, no hint is printed (nil-safe).
	// Hint content is exercised at the easy layer.
	if !strings.Contains(errOut.String(), "merged ship into main") {
		t.Fatalf("expected merge summary, got:\n%s", errOut.String())
	}
}

func TestRunMergeIntoBareFastForwardUpdatesRef(t *testing.T) {
	sourceRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":                   {Stdout: "ship\n"},
		"status --porcelain=v1 -uno":                  {Stdout: ""},
		"worktree list --porcelain":                   {Stdout: "worktree /repo/ship\nHEAD def456\nbranch refs/heads/ship\n"},
		"rev-parse --verify refs/heads/main^{commit}": {Stdout: "aaa1111\n"},
		"rev-parse --verify ship^{commit}":            {Stdout: "bbb2222\n"},
		"merge-base main ship":                        {Stdout: "aaa1111\n"},
		"update-ref refs/heads/main bbb2222 aaa1111":  {Stdout: ""},
		"rev-list --count aaa1111..bbb2222":           {Stdout: "3\n"},
	}}
	var errOut bytes.Buffer

	err := runMergeInto(context.Background(), mergeDeps{Runner: sourceRunner, ErrOut: &errOut}, nil, mergeFlags{into: "main", noAI: true}, nil)
	if err != nil {
		t.Fatalf("runMergeInto: %v", err)
	}
	calls := joinedShipCalls(sourceRunner.Calls)
	if !strings.Contains(calls, "update-ref refs/heads/main bbb2222 aaa1111") {
		t.Fatalf("expected fast-forward update-ref, calls:\n%s", calls)
	}
	if strings.Contains(calls, "commit-tree") {
		t.Fatalf("FF path must not run commit-tree, calls:\n%s", calls)
	}
	if !strings.Contains(errOut.String(), "merged ship into main") {
		t.Fatalf("expected summary, got:\n%s", errOut.String())
	}
}

func TestRunMergeIntoBareNonFFCleanCreatesMergeCommit(t *testing.T) {
	sourceRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":                   {Stdout: "ship\n"},
		"status --porcelain=v1 -uno":                  {Stdout: ""},
		"worktree list --porcelain":                   {Stdout: "worktree /repo/ship\nHEAD def456\nbranch refs/heads/ship\n"},
		"rev-parse --verify refs/heads/main^{commit}": {Stdout: "aaa1111\n"},
		"rev-parse --verify ship^{commit}":            {Stdout: "bbb2222\n"},
		"merge-base main ship":                        {Stdout: "ccc3333\n"},
		"merge-tree --write-tree --no-messages --name-only --merge-base ccc3333 main ship": {
			Stdout: "0123456789abcdef0123456789abcdef01234567\n",
		},
		"commit-tree 0123456789abcdef0123456789abcdef01234567 -p aaa1111 -p bbb2222 -m Merge branch 'ship' into main": {
			Stdout: "ddd4444\n",
		},
		"update-ref refs/heads/main ddd4444 aaa1111": {Stdout: ""},
		"rev-list --count aaa1111..ddd4444":          {Stdout: "4\n"},
	}}
	var errOut bytes.Buffer

	err := runMergeInto(context.Background(), mergeDeps{Runner: sourceRunner, ErrOut: &errOut}, nil, mergeFlags{into: "main", noAI: true}, nil)
	if err != nil {
		t.Fatalf("runMergeInto: %v", err)
	}
	calls := joinedShipCalls(sourceRunner.Calls)
	for _, want := range []string{
		"merge-tree --write-tree --no-messages --name-only --merge-base ccc3333 main ship",
		"commit-tree 0123456789abcdef0123456789abcdef01234567 -p aaa1111 -p bbb2222 -m Merge branch 'ship' into main",
		"update-ref refs/heads/main ddd4444 aaa1111",
	} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing call %q in:\n%s", want, calls)
		}
	}
}

func TestRunMergeIntoBareConflictRefusesWithHint(t *testing.T) {
	sourceRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":                   {Stdout: "ship\n"},
		"status --porcelain=v1 -uno":                  {Stdout: ""},
		"worktree list --porcelain":                   {Stdout: "worktree /repo/ship\nHEAD def456\nbranch refs/heads/ship\n"},
		"rev-parse --verify refs/heads/main^{commit}": {Stdout: "aaa1111\n"},
		"rev-parse --verify ship^{commit}":            {Stdout: "bbb2222\n"},
		"merge-base main ship":                        {Stdout: "ccc3333\n"},
		"merge-tree --write-tree --no-messages --name-only --merge-base ccc3333 main ship": {
			Stdout:   "0123456789abcdef0123456789abcdef01234567\nfile.go\n",
			ExitCode: 1,
		},
	}}
	var errOut bytes.Buffer

	err := runMergeInto(context.Background(), mergeDeps{Runner: sourceRunner, ErrOut: &errOut}, nil, mergeFlags{into: "main", noAI: true}, nil)
	if err == nil {
		t.Fatal("expected conflict refusal")
	}
	if !strings.Contains(err.Error(), "precheck found 1 conflict") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(HintFrom(err), "gk worktree add") {
		t.Fatalf("expected worktree hint, got: %q", HintFrom(err))
	}
	if strings.Contains(joinedShipCalls(sourceRunner.Calls), "commit-tree") {
		t.Fatalf("commit-tree must not run on conflict")
	}
	if strings.Contains(joinedShipCalls(sourceRunner.Calls), "update-ref") {
		t.Fatalf("update-ref must not run on conflict")
	}
}

func TestRunMergeIntoBareAlreadyMergedNoOp(t *testing.T) {
	sourceRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":                   {Stdout: "ship\n"},
		"status --porcelain=v1 -uno":                  {Stdout: ""},
		"worktree list --porcelain":                   {Stdout: "worktree /repo/ship\nHEAD def456\nbranch refs/heads/ship\n"},
		"rev-parse --verify refs/heads/main^{commit}": {Stdout: "aaa1111\n"},
		"rev-parse --verify ship^{commit}":            {Stdout: "bbb2222\n"},
		"merge-base main ship":                        {Stdout: "bbb2222\n"},
	}}
	var errOut bytes.Buffer

	err := runMergeInto(context.Background(), mergeDeps{Runner: sourceRunner, ErrOut: &errOut}, nil, mergeFlags{into: "main", noAI: true}, nil)
	if err != nil {
		t.Fatalf("runMergeInto: %v", err)
	}
	if !strings.Contains(errOut.String(), "main already contains ship") {
		t.Fatalf("expected already-contains summary, got:\n%s", errOut.String())
	}
	if strings.Contains(joinedShipCalls(sourceRunner.Calls), "update-ref") {
		t.Fatalf("update-ref must not run when already merged")
	}
}

func TestRunMergeIntoBareFFOnlyRejectsNonFF(t *testing.T) {
	sourceRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":                   {Stdout: "ship\n"},
		"status --porcelain=v1 -uno":                  {Stdout: ""},
		"worktree list --porcelain":                   {Stdout: "worktree /repo/ship\nHEAD def456\nbranch refs/heads/ship\n"},
		"rev-parse --verify refs/heads/main^{commit}": {Stdout: "aaa1111\n"},
		"rev-parse --verify ship^{commit}":            {Stdout: "bbb2222\n"},
		"merge-base main ship":                        {Stdout: "ccc3333\n"},
	}}

	err := runMergeInto(context.Background(), mergeDeps{Runner: sourceRunner, ErrOut: &bytes.Buffer{}}, nil, mergeFlags{into: "main", noAI: true, ffOnly: true}, nil)
	if err == nil {
		t.Fatal("expected non-FF refusal under --ff-only")
	}
	if !strings.Contains(err.Error(), "not a fast-forward") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunMergeIntoBareSquashRefused(t *testing.T) {
	sourceRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":  {Stdout: "ship\n"},
		"status --porcelain=v1 -uno": {Stdout: ""},
		"worktree list --porcelain":  {Stdout: "worktree /repo/ship\nHEAD def456\nbranch refs/heads/ship\n"},
	}}

	err := runMergeInto(context.Background(), mergeDeps{Runner: sourceRunner}, nil, mergeFlags{into: "main", squash: true, noAI: true}, nil)
	if err == nil {
		t.Fatal("expected squash refusal")
	}
	if !strings.Contains(err.Error(), "--squash with --into requires a worktree") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunMergeIntoDefaultSourceRejectsDirtySource(t *testing.T) {
	sourceRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":  {Stdout: "ship\n"},
		"status --porcelain=v1 -uno": {Stdout: " M local.go\n"},
	}}

	err := runMergeInto(context.Background(), mergeDeps{Runner: sourceRunner}, nil, mergeFlags{into: "main"}, nil)
	if err == nil {
		t.Fatal("expected dirty source error")
	}
	if !strings.Contains(err.Error(), "source worktree has tracked changes") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(HintFrom(err), "commit or stash") {
		t.Fatalf("expected source dirty hint, got: %q", HintFrom(err))
	}
}

func TestRunMergeIntoDirtySourceCanCreateWipCommit(t *testing.T) {
	sourceRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":  {Stdout: "ship\n"},
		"status --porcelain=v1 -uno": {Stdout: " M local.go\n"},
		"add -A":                     {Stdout: ""},
		"diff --cached --name-only":  {Stdout: "local.go\n"},
		"commit --no-verify --no-gpg-sign -m --wip-- [skip ci]": {Stdout: "[ship abc123] --wip-- [skip ci]\n"},
		"worktree list --porcelain":                             {Stdout: "worktree /repo/main\nHEAD abc123\nbranch refs/heads/main\n"},
	}}
	targetFake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":        {Stdout: "main\n"},
		"rev-parse --verify ship^{commit}": {Stdout: "abc123\n"},
		"merge-base HEAD ship":             {Stdout: "base123\n"},
		"merge-tree --write-tree --no-messages --name-only --merge-base base123 HEAD ship": {Stdout: "tree123\n"},
		"log --oneline HEAD..ship":              {Stdout: ""},
		"diff --stat HEAD..ship":                {Stdout: ""},
		"diff --name-status HEAD..ship":         {Stdout: ""},
		"status --porcelain=v1 -uno":            {Stdout: ""},
		"merge --no-edit ship":                  {Stdout: "merged\n"},
		"rev-list --count old123456..new123456": {Stdout: "1\n"},
	}}
	targetRunner := &sequenceRunner{
		FakeRunner: targetFake,
		sequence: map[string][]git.FakeResponse{
			"rev-parse HEAD": {{Stdout: "old123456\n"}, {Stdout: "new123456\n"}},
		},
	}
	var out bytes.Buffer

	err := runMergeInto(context.Background(), mergeDeps{
		Runner:  sourceRunner,
		Out:     &out,
		ErrOut:  &bytes.Buffer{},
		Confirm: func(string, bool) (bool, error) { return true, nil },
	}, nil, mergeFlags{into: "main", noAI: true}, func(string) git.Runner {
		return targetRunner
	})
	if err != nil {
		t.Fatalf("runMergeInto: %v", err)
	}
	sourceCalls := joinedShipCalls(sourceRunner.Calls)
	if !strings.Contains(sourceCalls, "commit --no-verify --no-gpg-sign -m --wip-- [skip ci]") {
		t.Fatalf("expected source wip commit, calls:\n%s", sourceCalls)
	}
	if !strings.Contains(out.String(), "wip commit created") {
		t.Fatalf("expected wip output, got:\n%s", out.String())
	}
}

func TestRunMergeIntoExplicitCurrentSourceRejectsDirtySource(t *testing.T) {
	sourceRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":  {Stdout: "ship\n"},
		"status --porcelain=v1 -uno": {Stdout: " M local.go\n"},
	}}

	err := runMergeInto(context.Background(), mergeDeps{Runner: sourceRunner}, []string{"ship"}, mergeFlags{into: "main"}, nil)
	if err == nil {
		t.Fatal("expected dirty source error")
	}
	if !strings.Contains(err.Error(), "source worktree has tracked changes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunMergeIntoReceiverDirtyHintMentionsWorktree(t *testing.T) {
	sourceRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"worktree list --porcelain": {Stdout: "worktree /repo/main\nHEAD abc123\nbranch refs/heads/main\n"},
	}}
	targetRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":        {Stdout: "main\n"},
		"rev-parse --verify ship^{commit}": {Stdout: "def456\n"},
		"merge-base HEAD ship":             {Stdout: "base123\n"},
		"merge-tree --write-tree --no-messages --name-only --merge-base base123 HEAD ship": {Stdout: "tree123\n"},
		"status --porcelain=v1 -uno": {Stdout: " M receiver.go\n"},
	}}

	err := runMergeInto(context.Background(), mergeDeps{Runner: sourceRunner}, []string{"ship"}, mergeFlags{into: "main", noAI: true}, func(string) git.Runner {
		return targetRunner
	})
	if err == nil {
		t.Fatal("expected receiver dirty error")
	}
	if !strings.Contains(err.Error(), "working tree has tracked changes") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(HintFrom(err), "/repo/main") {
		t.Fatalf("expected receiver worktree hint, got: %q", HintFrom(err))
	}
}

func TestRunMergeIntoConflictHintMentionsReceiverWorktree(t *testing.T) {
	sourceRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD": {Stdout: "other\n"},
		"worktree list --porcelain": {Stdout: "worktree /repo/main\nHEAD abc123\nbranch refs/heads/main\n"},
	}}
	targetRunner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"symbolic-ref --short HEAD":        {Stdout: "main\n"},
		"rev-parse --verify ship^{commit}": {Stdout: "def456\n"},
		"status --porcelain=v1 -uno":       {Stdout: ""},
		"rev-parse HEAD":                   {Stdout: "old123456\n"},
		"merge --no-edit ship": {
			Stdout:   "CONFLICT (content): Merge conflict in file.go\n",
			ExitCode: 1,
		},
	}}

	err := runMergeInto(context.Background(), mergeDeps{Runner: sourceRunner}, []string{"ship"}, mergeFlags{into: "main", noAI: true, skipPrecheck: true}, func(string) git.Runner {
		return targetRunner
	})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConflictError, got %T: %v", err, err)
	}
	if !strings.Contains(HintFrom(err), "/repo/main") {
		t.Fatalf("expected receiver worktree hint, got: %q", HintFrom(err))
	}
}

func TestRunMergeCorePlanLabelsNonSummarizerProvider(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --verify main^{commit}": {Stdout: "abc123\n"},
		"merge-base HEAD main":             {Stdout: "base123\n"},
		"merge-tree --write-tree --no-messages --name-only --merge-base base123 HEAD main": {Stdout: "tree123\n"},
		"log --oneline HEAD..main":      {Stdout: "abc123 feat: incoming\n"},
		"diff --stat HEAD..main":        {Stdout: " file.go | 2 ++\n"},
		"diff --name-status HEAD..main": {Stdout: "M\tfile.go\n"},
	}}
	var errOut bytes.Buffer

	err := runMergeCore(context.Background(), mergeDeps{
		Runner:   runner,
		Provider: mergeNonSummarizer{name: "gemini"},
		ErrOut:   &errOut,
	}, "main", mergeFlags{planOnly: true})
	if err != nil {
		t.Fatalf("runMergeCore: %v", err)
	}
	if !strings.Contains(errOut.String(), `provider "gemini" does not support merge-plan summaries`) {
		t.Fatalf("expected provider capability reason, got:\n%s", errOut.String())
	}
}

func TestRunMergeCorePlanLabelsProviderInitError(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --verify main^{commit}": {Stdout: "abc123\n"},
		"merge-base HEAD main":             {Stdout: "base123\n"},
		"merge-tree --write-tree --no-messages --name-only --merge-base base123 HEAD main": {Stdout: "tree123\n"},
		"log --oneline HEAD..main":      {Stdout: "abc123 feat: incoming\n"},
		"diff --stat HEAD..main":        {Stdout: " file.go | 2 ++\n"},
		"diff --name-status HEAD..main": {Stdout: "M\tfile.go\n"},
	}}
	var errOut bytes.Buffer

	err := runMergeCore(context.Background(), mergeDeps{
		Runner:      runner,
		ProviderErr: errors.New("no AI providers available"),
		ErrOut:      &errOut,
	}, "main", mergeFlags{planOnly: true})
	if err != nil {
		t.Fatalf("runMergeCore: %v", err)
	}
	if !strings.Contains(errOut.String(), "no AI providers available") {
		t.Fatalf("expected provider init reason, got:\n%s", errOut.String())
	}
}

func TestRunMergeCorePlanUsesAISummary(t *testing.T) {
	runner := &git.FakeRunner{Responses: map[string]git.FakeResponse{
		"rev-parse --verify main^{commit}": {Stdout: "abc123\n"},
		"merge-base HEAD main":             {Stdout: "base123\n"},
		"merge-tree --write-tree --no-messages --name-only --merge-base base123 HEAD main": {Stdout: "tree123\n"},
		"log --oneline HEAD..main":      {Stdout: "abc123 feat: incoming\n"},
		"diff --stat HEAD..main":        {Stdout: " file.go | 2 ++\n"},
		"diff --name-status HEAD..main": {Stdout: "M\tfile.go\n"},
	}}
	fake := provider.NewFake()
	fake.NameVal = "nvidia"
	fake.SummarizeResponses = []provider.SummarizeResult{{Text: "AI says merge is low risk."}}
	var errOut bytes.Buffer

	err := runMergeCore(context.Background(), mergeDeps{
		Runner:   runner,
		Provider: fake,
		ErrOut:   &errOut,
	}, "main", mergeFlags{planOnly: true})
	if err != nil {
		t.Fatalf("runMergeCore: %v", err)
	}
	if !strings.Contains(errOut.String(), "MERGE PLAN (AI)") {
		t.Fatalf("expected AI plan section banner, got:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "main → HEAD") {
		t.Fatalf("expected target → current in summary slot, got:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "AI says merge is low risk.") {
		t.Fatalf("expected AI summary, got:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "VERDICT") {
		t.Fatalf("expected VERDICT footer section, got:\n%s", errOut.String())
	}
}

func TestRenderAIMergePlanHeaderNoColor(t *testing.T) {
	prev := color.NoColor
	color.NoColor = true
	t.Cleanup(func() { color.NoColor = prev })

	got := renderAIMergePlanHeader("main", "feature", "gemini", 0, false)
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("header should not contain ANSI escapes: %q", got)
	}
	// New bar-section format: title is uppercased, target → current
	// + clean state + provider live in the inline summary slot.
	for _, want := range []string{
		"MERGE PLAN (AI)",
		"main → feature",
		"clean",
		"via gemini",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("header missing %q; full output:\n%s", want, got)
		}
	}
}

func TestRenderMergeSummaryNoCommitHeadUnchanged(t *testing.T) {
	runner := &git.FakeRunner{}
	var out bytes.Buffer

	renderMergeSummary(context.Background(), &out, runner, "abc123456", "abc123456", "main", "feature", mergeFlags{noCommit: true})

	if strings.Contains(out.String(), "already contains") {
		t.Fatalf("no-commit merge should not report already contains: %q", out.String())
	}
	if !strings.Contains(out.String(), "merged main into feature index/worktree") {
		t.Fatalf("expected index/worktree summary, got: %q", out.String())
	}
}

func TestCleanMergePlanSummaryRemovesMarkdownArtifacts(t *testing.T) {
	got := cleanMergePlanSummary(">\n\n# Merge Plan\n\n## Risk\n```bash\nmake test\n```\nNEXT\n")
	if strings.Contains(got, ">") {
		t.Fatalf("summary still contains prompt marker: %q", got)
	}
	if strings.Contains(got, "```") {
		t.Fatalf("summary still contains code fences: %q", got)
	}
	if !strings.Contains(got, "Merge Plan") || !strings.Contains(got, "Risk") {
		t.Fatalf("summary lost headings: %q", got)
	}
}

func TestMergeArgs(t *testing.T) {
	tests := []struct {
		name  string
		flags mergeFlags
		want  string
	}{
		{name: "default", want: "merge --no-edit main"},
		{name: "ff only", flags: mergeFlags{ffOnly: true}, want: "merge --ff-only --no-edit main"},
		{name: "no commit", flags: mergeFlags{noCommit: true}, want: "merge --no-commit main"},
		{name: "squash", flags: mergeFlags{squash: true}, want: "merge --squash main"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(mergeArgs("main", tc.flags), " "); got != tc.want {
				t.Fatalf("mergeArgs = %q, want %q", got, tc.want)
			}
		})
	}
}

type mergeNonSummarizer struct {
	name string
}

func (m mergeNonSummarizer) Name() string                { return m.name }
func (m mergeNonSummarizer) Locality() provider.Locality { return provider.LocalityLocal }
func (m mergeNonSummarizer) Available(_ context.Context) error {
	return nil
}
func (m mergeNonSummarizer) Classify(_ context.Context, _ provider.ClassifyInput) (provider.ClassifyResult, error) {
	return provider.ClassifyResult{}, nil
}
func (m mergeNonSummarizer) Compose(_ context.Context, _ provider.ComposeInput) (provider.ComposeResult, error) {
	return provider.ComposeResult{}, nil
}

type sequenceRunner struct {
	*git.FakeRunner
	sequence map[string][]git.FakeResponse
}

func (s *sequenceRunner) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	key := strings.Join(args, " ")
	if seq := s.sequence[key]; len(seq) > 0 {
		resp := seq[0]
		s.sequence[key] = seq[1:]
		s.Calls = append(s.Calls, git.FakeCall{Args: append([]string(nil), args...)})
		if resp.ExitCode != 0 {
			return []byte(resp.Stdout), []byte(resp.Stderr), &git.ExitError{Code: resp.ExitCode, Args: args, Stderr: resp.Stderr}
		}
		return []byte(resp.Stdout), []byte(resp.Stderr), resp.Err
	}
	return s.FakeRunner.Run(ctx, args...)
}
