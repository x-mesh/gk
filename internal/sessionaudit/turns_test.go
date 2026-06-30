package sessionaudit

import (
	"strings"
	"testing"
)

func session(lines ...string) []byte {
	return []byte(strings.Join(lines, "\n") + "\n")
}

// asst builds an assistant record with one Bash tool_use.
func asst(msgID, toolID, cmd string) string {
	return `{"type":"assistant","message":{"id":"` + msgID + `","role":"assistant","content":[{"type":"tool_use","id":"` + toolID + `","name":"Bash","input":{"command":` + jsonStr(cmd) + `}}]}}`
}

// result builds a tool_result user record for one tool_use id.
func result(toolID string, isErr bool) string {
	e := "false"
	if isErr {
		e = "true"
	}
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + toolID + `","is_error":` + e + `}]}}`
}

func jsonStr(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func TestSessionTurns_SequentialMessagesAreDistinctTurns(t *testing.T) {
	data := session(
		asst("msg_A", "t_a", "git status"),
		result("t_a", false),
		asst("msg_B", "t_b", "git log --oneline -5"),
		asst("msg_C", "t_c", "git diff"),
	)
	events := SessionTurns(data)
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3: %+v", len(events), events)
	}
	turns := []int{events[0].Turn, events[1].Turn, events[2].Turn}
	if turns[0] == turns[1] || turns[1] == turns[2] || turns[0] == turns[2] {
		t.Fatalf("sequential commands must be distinct turns, got %v", turns)
	}
	if turns[0] >= turns[1] || turns[1] >= turns[2] {
		t.Fatalf("turns must follow execution order, got %v", turns)
	}
}

func TestSessionTurns_ParallelToolUseSharesOneTurn(t *testing.T) {
	// Two tool_use blocks in ONE assistant message (same message id) — the
	// harness runs these in one round-trip, so they MUST share a turn.
	parallel := `{"type":"assistant","message":{"id":"msg_P","role":"assistant","content":[` +
		`{"type":"tool_use","id":"p1","name":"Bash","input":{"command":"git status"}},` +
		`{"type":"tool_use","id":"p2","name":"Bash","input":{"command":"git diff"}}]}}`
	data := session(parallel)
	events := SessionTurns(data)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2: %+v", len(events), events)
	}
	if events[0].Turn != events[1].Turn {
		t.Fatalf("parallel tool calls must share a turn, got %d and %d", events[0].Turn, events[1].Turn)
	}
}

func TestSessionTurns_SameMessageIDAcrossRecordsSharesTurn(t *testing.T) {
	// Newer Claude logs split content blocks into separate records that still
	// carry the same message id — they are one turn.
	data := session(
		asst("msg_X", "t1", "git status"),
		asst("msg_X", "t2", "git diff"),
		asst("msg_Y", "t3", "git log"),
	)
	events := SessionTurns(data)
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	if events[0].Turn != events[1].Turn {
		t.Fatalf("same message id must share a turn, got %d and %d", events[0].Turn, events[1].Turn)
	}
	if events[2].Turn == events[0].Turn {
		t.Fatalf("different message id must be a new turn")
	}
}

func TestSessionTurns_JoinsIsErrorFromToolResult(t *testing.T) {
	data := session(
		asst("msg_A", "t_ok", "git push"),
		result("t_ok", false),
		asst("msg_B", "t_fail", "git push"),
		result("t_fail", true),
	)
	events := SessionTurns(data)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].IsError {
		t.Errorf("event 0 should be IsError=false")
	}
	if !events[1].IsError {
		t.Errorf("event 1 should be IsError=true (joined from tool_result)")
	}
}

func TestSessionTurns_InfersRepoScope(t *testing.T) {
	data := session(
		asst("m1", "t1", "cd /work/repoA && git status"),
		asst("m2", "t2", "git -C /work/repoB status"),
		asst("m3", "t3", "git status"),
	)
	events := SessionTurns(data)
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	if events[0].Repo != "/work/repoA" {
		t.Errorf("event 0 repo = %q, want /work/repoA", events[0].Repo)
	}
	if events[1].Repo != "/work/repoB" {
		t.Errorf("event 1 repo = %q, want /work/repoB", events[1].Repo)
	}
	if events[2].Repo != "" {
		t.Errorf("event 2 repo = %q, want empty", events[2].Repo)
	}
}

func TestSessionTurns_NonCommandToolStillAdvancesTurn(t *testing.T) {
	// A Read tool call between two git commands occupies a turn (interleave) but
	// emits no event — the git events must keep non-adjacent turn indices.
	readRec := `{"type":"assistant","message":{"id":"msg_R","role":"assistant","content":[{"type":"tool_use","id":"r1","name":"Read","input":{"file_path":"/a.go"}}]}}`
	data := session(
		asst("msg_A", "t_a", "git status"),
		readRec,
		asst("msg_B", "t_b", "git diff"),
	)
	events := SessionTurns(data)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 (Read emits none): %+v", len(events), events)
	}
	if events[1].Turn-events[0].Turn != 2 {
		t.Fatalf("interleaved Read turn must leave a gap of 2, got turns %d and %d", events[0].Turn, events[1].Turn)
	}
}
