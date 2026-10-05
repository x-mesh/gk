//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/testutil"
)

func runMergeJSON(t *testing.T, dir string, agent bool, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(gkBin, args...)
	cmd.Dir = dir
	mode := "0"
	if agent {
		mode = "1"
	}
	cmd.Env = append(os.Environ(), "GK_AGENT="+mode, "GK_AI_DISABLE=1", "GK_NO_AUTO_CONFIG=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "NO_COLOR=1", "LC_ALL=C", "LANG=C")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func TestE2E_MergeSuccessJSON(t *testing.T) {
	for _, mode := range []string{"agent", "json", "human"} {
		for _, scenario := range []string{"merge", "noop", "into-bare", "into-worktree", "into-noop", "plan", "no-commit", "squash", "autostash"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				r := testutil.NewRepo(t)
				pre := r.RunGit("rev-parse", "HEAD")
				r.CreateBranch("feature")
				if scenario != "noop" && scenario != "into-noop" {
					r.WriteFile("feature.txt", "feature\n")
					r.Commit("feature")
				}
				r.Checkout("main")
				args := []string{"merge", "feature", "--no-ai", "--no-ff"}
				if strings.HasPrefix(scenario, "into-") {
					r.Checkout("feature")
					args = []string{"merge", "--into", "main", "--no-ai", "--no-ff"}
					if scenario != "into-bare" {
						r.RunGit("worktree", "add", filepath.Join(t.TempDir(), "receiver"), "main")
					}
				}
				switch scenario {
				case "plan":
					args = append(args, "--plan-only")
				case "no-commit":
					args = append(args, "--no-commit")
				case "squash":
					args = []string{"merge", "feature", "--no-ai", "--squash"}
				case "autostash":
					r.WriteFile(filepath.Join(".gkkeep", "README"), "local change\n")
					args = append(args, "--autostash")
				}
				if mode == "json" {
					args = append(args, "--json")
				}
				stdout, stderr, err := runMergeJSON(t, r.Dir, mode == "agent", args...)
				if err != nil {
					t.Fatalf("merge failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
				}
				if mode == "human" {
					if stdout != "" || stderr == "" {
						t.Fatalf("human streams: stdout=%q stderr=%q", stdout, stderr)
					}
				} else {
					var payload struct {
						Schema   int             `json:"schema"`
						State    string          `json:"state"`
						OK       bool            `json:"ok"`
						Result   json.RawMessage `json:"result"`
						Source   string          `json:"source"`
						Receiver string          `json:"receiver"`
						PlanOnly bool            `json:"plan_only"`
						NoCommit bool            `json:"no_commit"`
						Squash   bool            `json:"squash"`
						Outcome  string          `json:"-"`
						Noop     bool            `json:"noop"`
						HeadOID  string          `json:"head_oid"`
					}
					if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
						t.Fatalf("invalid stdout JSON: %v\nstdout: %q\nstderr: %s", err, stdout, stderr)
					}
					if payload.Schema != 1 {
						t.Fatalf("schema=%d", payload.Schema)
					}
					if mode == "agent" {
						if payload.State != "ok" || !payload.OK {
							t.Fatalf("expected ok envelope: %s", stdout)
						}
						if err := json.Unmarshal(payload.Result, &payload); err != nil {
							t.Fatal(err)
						}
					}
					if err := json.Unmarshal(payload.Result, &payload.Outcome); err != nil {
						t.Fatalf("result outcome: %v\nstdout: %s", err, stdout)
					}
					wantOutcome := map[string]string{
						"noop": "up-to-date", "into-noop": "up-to-date", "plan": "planned",
						"no-commit": "staged", "squash": "staged",
					}[scenario]
					if wantOutcome == "" {
						wantOutcome = "merged"
					}
					if payload.Outcome != wantOutcome || payload.Noop != (wantOutcome == "up-to-date") {
						t.Fatalf("result=%q noop=%v, want %q: %s", payload.Outcome, payload.Noop, wantOutcome, stdout)
					}
					if payload.HeadOID == "" {
						t.Fatalf("missing head_oid: %s", stdout)
					}
					if payload.Source != "feature" || payload.Receiver != "main" {
						t.Fatalf("wrong merge direction: %s", stdout)
					}
					if payload.PlanOnly != (scenario == "plan") || payload.NoCommit != (scenario == "no-commit") || payload.Squash != (scenario == "squash") {
						t.Fatalf("wrong merge flags: %s", stdout)
					}
				}
				post := r.RunGit("rev-parse", "main")
				unchanged := scenario == "noop" || scenario == "into-noop" || scenario == "plan" || scenario == "no-commit" || scenario == "squash"
				if (pre == post) != unchanged {
					t.Fatalf("unexpected receiver change: before=%s after=%s", pre, post)
				}
				if scenario == "autostash" {
					content, err := os.ReadFile(filepath.Join(r.Dir, ".gkkeep", "README"))
					if err != nil || string(content) != "local change\n" {
						t.Fatalf("autostash did not restore changes: %q %v", content, err)
					}
				}
			})
		}
	}
}

func TestE2E_MergeFailureJSON(t *testing.T) {
	for _, scenario := range []string{"unknown-ref", "conflict", "stash-conflict"} {
		t.Run(scenario, func(t *testing.T) {
			r := testutil.NewRepo(t)
			r.WriteFile("shared.txt", "base\n")
			r.Commit("base")
			r.CreateBranch("feature")
			r.WriteFile("shared.txt", "feature\n")
			r.Commit("feature")
			r.Checkout("main")
			args := []string{"merge", "feature", "--no-ai", "--no-ff", "--skip-precheck"}
			want := "error"
			switch scenario {
			case "unknown-ref":
				args[1] = "missing"
			case "conflict":
				r.WriteFile("shared.txt", "main\n")
				r.Commit("main")
				want = "paused"
			case "stash-conflict":
				r.WriteFile("shared.txt", "local\n")
				args = append(args, "--autostash")
			}
			stdout, stderr, err := runMergeJSON(t, r.Dir, true, args...)
			if err == nil {
				t.Fatalf("expected failure: stdout=%s stderr=%s", stdout, stderr)
			}
			var env struct {
				State string `json:"state"`
				OK    bool   `json:"ok"`
			}
			output := stderr
			if want == "paused" {
				output = stdout
			} else if stdout != "" {
				t.Fatalf("failure emitted success output: %s", stdout)
			}
			start := strings.Index(output, "{")
			if start < 0 {
				t.Fatalf("missing %s envelope: stdout=%s stderr=%s", want, stdout, stderr)
			}
			if err := json.Unmarshal([]byte(output[start:]), &env); err != nil || env.State != want || env.OK {
				t.Fatalf("expected %s envelope: parse=%v stdout=%s stderr=%s", want, err, stdout, stderr)
			}
		})
	}
}
