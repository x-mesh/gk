package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/spf13/cobra"

	"github.com/x-mesh/gk/internal/testutil"
)

func withAgentMode(t *testing.T, on bool) {
	t.Helper()
	prevA, prevJ := flagAgent, flagJSON
	t.Cleanup(func() { flagAgent, flagJSON = prevA, prevJ })
	flagAgent = on
	if on {
		flagJSON = true
	}
}

// TestEmitAgentResult_GoldenWithoutAgentMode: without GK_AGENT the output
// must be byte-identical to the pre-envelope direct encoding — existing
// --json consumers see no change.
func TestEmitAgentResult_GoldenWithoutAgentMode(t *testing.T) {
	withAgentMode(t, false)
	payload := pullResultJSON{Schema: 1, Result: "up-to-date", Branch: "main"}

	var direct bytes.Buffer
	enc := json.NewEncoder(&direct)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		t.Fatal(err)
	}

	var got bytes.Buffer
	if err := emitAgentResult(&got, payload); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), direct.Bytes()) {
		t.Errorf("non-agent output changed:\ngot:  %q\nwant: %q", got.String(), direct.String())
	}
}

func TestEmitAgentResult_WrapsInAgentMode(t *testing.T) {
	withAgentMode(t, true)
	var got bytes.Buffer
	if err := emitAgentResult(&got, map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Schema int               `json:"schema"`
		OK     bool              `json:"ok"`
		Result map[string]string `json:"result"`
	}
	if err := json.Unmarshal(got.Bytes(), &env); err != nil {
		t.Fatalf("not valid envelope JSON: %v\n%s", err, got.String())
	}
	if env.Schema != 1 || !env.OK || env.Result["k"] != "v" {
		t.Errorf("envelope: %+v", env)
	}
}

func TestFormatErrorJSON(t *testing.T) {
	err := WithRemedy(
		errors.New("working tree has uncommitted changes"),
		"stash or commit first",
		errRemedy{Command: "gk pull --autostash", Safety: "safe"},
	)
	out := FormatErrorJSON(err)

	var env struct {
		Schema int  `json:"schema"`
		OK     bool `json:"ok"`
		Error  struct {
			Code     string      `json:"code"`
			Message  string      `json:"message"`
			Hint     string      `json:"hint"`
			Remedies []errRemedy `json:"remedies"`
		} `json:"error"`
	}
	if uerr := json.Unmarshal([]byte(out), &env); uerr != nil {
		t.Fatalf("not valid JSON: %v\n%s", uerr, out)
	}
	if env.OK || env.Error.Code != "dirty-tree" || env.Error.Message == "" {
		t.Errorf("error envelope: %+v", env)
	}
	if len(env.Error.Remedies) != 1 || env.Error.Remedies[0].Command != "gk pull --autostash" {
		t.Errorf("remedies: %+v", env.Error.Remedies)
	}
	if FormatErrorJSON(nil) != "" {
		t.Error("nil error must yield empty string")
	}
}

// TestEmitPullConflictJSON_DirAware: merge --into pauses in the receiver
// worktree — the conflict contract must probe that directory, not the
// invoking checkout (Codex P2).
func TestEmitPullConflictJSON_DirAware(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	// Repo A: clean (the invoking checkout). Repo B: a real merge conflict.
	clean := testutil.NewRepo(t)
	conflicted := testutil.NewRepo(t)
	conflicted.WriteFile("f.txt", "base\n")
	conflicted.Commit("seed")
	conflicted.RunGit("checkout", "-b", "side")
	conflicted.WriteFile("f.txt", "side\n")
	conflicted.Commit("side edit")
	conflicted.Checkout("main")
	conflicted.WriteFile("f.txt", "main\n")
	conflicted.Commit("main edit")
	if _, err := conflicted.TryGit("merge", "side"); err == nil {
		t.Fatal("fixture: merge must conflict")
	}

	prev := flagRepo
	flagRepo = clean.Dir // invoking checkout is the CLEAN repo
	t.Cleanup(func() { flagRepo = prev })
	prevJ := flagJSON
	flagJSON = true
	t.Cleanup(func() { flagJSON = prevJ })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	out := &bytes.Buffer{}
	cmd.SetOut(out)

	emitPullConflictJSON(cmd, conflicted.Dir)

	var res pullResultJSON
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out.String())
	}
	if res.Result != "conflict" || len(res.Conflict.Files) != 1 || res.Conflict.Files[0] != "f.txt" {
		t.Errorf("conflict contract must come from the paused worktree: %+v", res)
	}
}

// TestDoctorJSONEnveloped: GK_AGENT must cover commands with their own local
// --json flag too (Codex P1) — doctor as the representative case.
func TestDoctorJSONEnveloped(t *testing.T) {
	if testing.Short() {
		t.Skip("skip integration test in short mode")
	}
	repo := testutil.NewRepo(t)
	prev := flagRepo
	flagRepo = repo.Dir
	t.Cleanup(func() { flagRepo = prev })
	withAgentMode(t, true)

	cmd := &cobra.Command{Use: "doctor", RunE: runDoctor, SilenceUsage: true, SilenceErrors: true}
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("fix", false, "")
	cmd.Flags().String("repo", repo.Dir, "")
	cmd.SetContext(context.Background())
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(&bytes.Buffer{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("GK_AGENT doctor must emit the envelope: %v\n%.300s", err, out.String())
	}
	if !env.OK || len(env.Result) == 0 {
		t.Errorf("envelope: ok=%v result=%.80s", env.OK, env.Result)
	}
}
