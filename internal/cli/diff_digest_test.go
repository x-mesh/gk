package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/diff"
	"github.com/x-mesh/gk/internal/testutil"
)

func TestDigestToJSON_KindAndRename(t *testing.T) {
	d := diff.Digest{
		Files: []diff.FileDigest{
			{Path: "internal/cli/pull.go", Status: diff.StatusModified, Hunks: 2, Added: 10, Deleted: 3, Symbols: []string{"func runPull()"}},
			{Path: "internal/cli/pull_test.go", Status: diff.StatusAdded, Hunks: 1, Added: 50},
			{Path: "docs/commands.md", Status: diff.StatusModified, Hunks: 1, Added: 4},
			{Path: "new.go", OldPath: "old.go", Status: diff.StatusRenamed, Hunks: 1, Added: 1, Deleted: 1},
		},
		Stat: diff.DigestStat{Files: 4, Hunks: 5, Added: 65, Deleted: 4},
	}
	out := digestToJSON(d)
	if out.Schema != 1 || len(out.Files) != 4 {
		t.Fatalf("envelope: %+v", out)
	}
	if out.Files[0].Kind != "" {
		t.Errorf("source file must have empty kind, got %q", out.Files[0].Kind)
	}
	if out.Files[1].Kind != "test" || out.Files[2].Kind != "docs" {
		t.Errorf("kinds: %q %q", out.Files[1].Kind, out.Files[2].Kind)
	}
	if out.Files[3].OldPath != "old.go" || out.Files[3].Status != "renamed" {
		t.Errorf("rename: %+v", out.Files[3])
	}
}

func TestIntegration_DiffDigest(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	resetDiffFlags(t)
	repo := testutil.NewRepo(t)
	// Greet 본문이 파일 중간에 오도록 충분히 길게 — git의 funcname
	// 휴리스틱은 hunk 시작 이전의 컨텍스트 줄에서 찾으므로, 파일 전체를
	// 덮는 hunk에는 funcname이 붙지 않는다.
	pad := strings.Repeat("// filler\n", 10)
	inner := strings.Repeat("\t_ = 0\n", 5) // 선언과 변경 줄 사이를 -U3 밖으로
	base := "package main\n\n" + pad + "func Greet() string {\n" + inner + "\treturn \"hi\"\n}\n"
	repo.WriteFile("app.go", base)
	repo.WriteFile("app_test.go", "package main\n")
	repo.Commit("seed")

	// Working tree: modify inside Greet, add a doc.
	repo.WriteFile("app.go", strings.Replace(base, "return \"hi\"", "return \"hello\"", 1))
	repo.WriteFile("README.md", "# readme\n")
	repo.RunGit("add", "README.md") // untracked는 diff에 안 잡히므로 stage

	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })
	prevJ := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = prevJ })
	prevDigest := diffFlagDigest
	diffFlagDigest = true
	t.Cleanup(func() { diffFlagDigest = prevDigest })

	cmd := diffTestCmd(t)
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("diff --digest: %v", err)
	}

	var res diffDigestJSON
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, stdout.String())
	}
	if res.Stat.Files != 1 || len(res.Files) != 1 {
		t.Fatalf("unstaged digest: %+v", res)
	}
	f := res.Files[0]
	if f.Path != "app.go" || f.Status != "modified" || f.Hunks != 1 {
		t.Errorf("file: %+v", f)
	}
	if len(f.Symbols) != 1 || !strings.Contains(f.Symbols[0], "func Greet()") {
		t.Errorf("symbols: %v", f.Symbols)
	}

	// 사람용 출력 — staged 포함해 두 파일이 보이도록 HEAD 기준.
	flagJSON = false
	cmd2 := diffTestCmd(t)
	cmd2.SetArgs([]string{"HEAD"})
	human := &bytes.Buffer{}
	cmd2.SetOut(human)
	cmd2.SetErr(&bytes.Buffer{})
	if err := cmd2.Execute(); err != nil {
		t.Fatalf("human digest: %v", err)
	}
	out := human.String()
	// 사람용 표는 bare name으로 축약("func Greet() string" → "Greet");
	// JSON 계약은 위에서 시그니처 원문을 검증했다.
	if !strings.Contains(out, "app.go") || !strings.Contains(out, "Greet") {
		t.Errorf("human view missing symbol:\n%s", out)
	}
	if !strings.Contains(out, "README.md") || !strings.Contains(out, "[docs]") {
		t.Errorf("human view missing docs kind:\n%s", out)
	}
	if !strings.Contains(out, "2 files") {
		t.Errorf("summary line:\n%s", out)
	}
}

func TestIntegration_DiffRawPatchJSONScopesPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	resetDiffFlags(t)
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "one\n")
	repo.WriteFile("b.txt", "one\n")
	repo.Commit("seed files")
	repo.WriteFile("a.txt", "two\n")
	repo.WriteFile("b.txt", "two\n")

	prevRepo, prevJSON := flagRepo, flagJSON
	flagRepo, flagJSON = repo.Dir, true
	t.Cleanup(func() { flagRepo, flagJSON = prevRepo, prevJSON })
	diffFlagRawPatch = true

	cmd := diffTestCmd(t)
	cmd.SetArgs([]string{"--", "a.txt"})
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("diff --raw-patch --json -- a.txt: %v", err)
	}

	var res diffPatchJSON
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, stdout.String())
	}
	if res.Schema != 1 || res.Parsed == nil {
		t.Fatalf("payload: %+v", res)
	}
	if !strings.Contains(res.Patch, "diff --git a/a.txt b/a.txt") {
		t.Fatalf("raw patch missing selected file:\n%s", res.Patch)
	}
	if strings.Contains(res.Patch, "b.txt") {
		t.Fatalf("raw patch leaked unselected file:\n%s", res.Patch)
	}
	if res.Parsed.Stat.TotalFiles != 1 || len(res.Parsed.Files) != 1 || res.Parsed.Files[0].Path != "a.txt" {
		t.Fatalf("parsed diff = %+v", res.Parsed)
	}
}

func TestIntegration_DiffJSONAgentEnvelope(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	resetDiffFlags(t)
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "one\n")
	repo.Commit("seed file")
	repo.WriteFile("a.txt", "two\n")

	prevRepo := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prevRepo })
	withAgentMode(t, true)

	cmd := diffTestCmd(t)
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent diff --json: %v", err)
	}

	var env struct {
		Schema int           `json:"schema"`
		State  string        `json:"state"`
		OK     bool          `json:"ok"`
		Result diff.DiffJSON `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("not valid envelope JSON: %v\n%s", err, stdout.String())
	}
	if env.Schema != 1 || env.State != "ok" || !env.OK {
		t.Fatalf("envelope: %+v", env)
	}
	if env.Result.Stat.TotalFiles != 1 || len(env.Result.Files) != 1 || env.Result.Files[0].Path != "a.txt" {
		t.Fatalf("result: %+v", env.Result)
	}
}

func TestIntegration_DiffCheckJSONAgentEnvelope(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	resetDiffFlags(t)
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "ok\n")
	repo.Commit("seed file")
	repo.WriteFile("a.txt", "bad   \n")

	prevRepo := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prevRepo })
	withAgentMode(t, true)
	diffFlagCheck = true

	exitCode := -1
	prevExit := diffExitFunc
	diffExitFunc = func(code int) { exitCode = code }
	t.Cleanup(func() { diffExitFunc = prevExit })

	cmd := diffTestCmd(t)
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent diff --check --json: %v", err)
	}
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}

	var env struct {
		Schema int           `json:"schema"`
		State  string        `json:"state"`
		OK     bool          `json:"ok"`
		Result diffCheckJSON `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("not valid envelope JSON: %v\n%s", err, stdout.String())
	}
	if env.Schema != 1 || env.State != "blocked" || env.OK {
		t.Fatalf("envelope: %+v", env)
	}
	if env.Result.Clean || env.Result.Count != 1 || len(env.Result.Problems) != 1 {
		t.Fatalf("result: %+v", env.Result)
	}
	p := env.Result.Problems[0]
	if p.Path != "a.txt" || p.Line != 1 || p.Kind != "trailing-whitespace" || !strings.Contains(p.Content, "bad") {
		t.Fatalf("problem: %+v", p)
	}
}

func TestIntegration_DiffCheckJSONClean(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	resetDiffFlags(t)
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "ok\n")
	repo.Commit("seed file")
	repo.WriteFile("a.txt", "better\n")

	prevRepo, prevJSON := flagRepo, flagJSON
	flagRepo, flagJSON = repo.Dir, true
	t.Cleanup(func() { flagRepo, flagJSON = prevRepo, prevJSON })
	diffFlagCheck = true

	exitCode := -1
	prevExit := diffExitFunc
	diffExitFunc = func(code int) { exitCode = code }
	t.Cleanup(func() { diffExitFunc = prevExit })

	cmd := diffTestCmd(t)
	stdout := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("diff --check --json clean: %v", err)
	}
	if exitCode != -1 {
		t.Fatalf("exit called with %d on clean diff", exitCode)
	}

	var res diffCheckJSON
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, stdout.String())
	}
	if !res.Clean || res.Result != "clean" || res.Count != 0 || len(res.Problems) != 0 {
		t.Fatalf("result: %+v", res)
	}
}

// diffTestCmd wires a cobra command around runDiff with the flags it reads.
func diffTestCmd(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "diff", RunE: runDiff, SilenceUsage: true, SilenceErrors: true}
	cmd.SetContext(context.Background())
	return cmd
}

func resetDiffFlags(t *testing.T) {
	t.Helper()
	prevStaged := diffFlagStaged
	prevStat := diffFlagStat
	prevDigest := diffFlagDigest
	prevInteract := diffFlagInteract
	prevNoPager := diffFlagNoPager
	prevNoWordDiff := diffFlagNoWordDiff
	prevContext := diffFlagContext
	prevNoRefLabels := diffFlagNoRefLabels
	prevConflicts := diffFlagConflicts
	prevRawPatch := diffFlagRawPatch
	prevCheck := diffFlagCheck
	diffFlagStaged = false
	diffFlagStat = false
	diffFlagDigest = false
	diffFlagInteract = false
	diffFlagNoPager = false
	diffFlagNoWordDiff = false
	diffFlagContext = 3
	diffFlagNoRefLabels = false
	diffFlagConflicts = false
	diffFlagRawPatch = false
	diffFlagCheck = false
	t.Cleanup(func() {
		diffFlagStaged = prevStaged
		diffFlagStat = prevStat
		diffFlagDigest = prevDigest
		diffFlagInteract = prevInteract
		diffFlagNoPager = prevNoPager
		diffFlagNoWordDiff = prevNoWordDiff
		diffFlagContext = prevContext
		diffFlagNoRefLabels = prevNoRefLabels
		diffFlagConflicts = prevConflicts
		diffFlagRawPatch = prevRawPatch
		diffFlagCheck = prevCheck
	})
}
