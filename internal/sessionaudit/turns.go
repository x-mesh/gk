package sessionaudit

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TurnEvent is one shell command an agent ran, tagged with the turn it ran in.
//
// Turn is the unit that matters for git-kit's value proposition: collapsing
// commands that already share a Turn saves nothing (they ran in one agent
// round-trip), while collapsing commands across distinct Turns is what removes
// real conversation turns. Turn is derived from the assistant message id, so
// parallel tool calls in one message — which the harness runs together — share
// a Turn even though they arrive as separate JSONL records.
type TurnEvent struct {
	Cmd       string // the raw shell command the agent ran
	Turn      int    // dense, monotonic per session in first-seen execution order
	ToolUseID string // the tool_use id, joins to the matching tool_result
	IsError   bool   // resolved from that tool_result: true => the call failed
	Repo      string // working-dir scope inferred from the command (cd / -C), "" if unknown
}

// claudeRecord is the minimal slice of a Claude Code JSONL record the turn
// model needs. The audit's generic ExtractCommands handles both Claude and
// Codex shapes for the occurrence metric; the turn metric needs the message-id
// boundary and the tool_use/tool_result join, which only the structured Claude
// shape carries — Codex sessions are handled by the occurrence path until a
// completion=turn adapter lands (see plan v1.1 Phase 4).
type claudeRecord struct {
	Type    string `json:"type"`
	Message struct {
		ID      string `json:"id"`
		Role    string `json:"role"`
		Content []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`          // tool_use: the toolu_... id
			Name      string          `json:"name"`        // tool_use: Bash, Read, ...
			Input     json.RawMessage `json:"input"`       // tool_use: {"command": "..."}
			ToolUseID string          `json:"tool_use_id"` // tool_result: which call it answers
			IsError   bool            `json:"is_error"`    // tool_result: did that call fail
		} `json:"content"`
	} `json:"message"`
}

type toolInput struct {
	Command string `json:"command"`
	Cmd     string `json:"cmd"`
}

// SessionTurns parses one Claude session's JSONL bytes into the ordered shell
// commands an agent ran, each tagged with its turn, tool_use id, error status,
// and inferred repo. Records that are not Claude assistant tool calls (or carry
// no shell command) produce no events but still advance the turn counter when
// they occupy an agent round-trip, so genuine interleaving is visible to the
// collapse detector. Non-Claude (e.g. Codex) lines are skipped.
func SessionTurns(data []byte) []TurnEvent {
	records := parseClaudeRecords(data)

	// Pass 1: join tool_use id -> did that call error (from tool_result records).
	errByID := map[string]bool{}
	for _, rec := range records {
		for _, c := range rec.Message.Content {
			if c.Type == "tool_result" && c.ToolUseID != "" {
				errByID[c.ToolUseID] = c.IsError
			}
		}
	}

	// Pass 2: emit one event per shell command, tagging the turn from the
	// message id so parallel calls in one message share a turn.
	turnOf := map[string]int{}
	nextTurn := 0
	var events []TurnEvent
	for i, rec := range records {
		toolUses := assistantToolUses(rec)
		if len(toolUses) == 0 {
			continue
		}
		key := rec.Message.ID
		if key == "" {
			// No message id (older shape / fixture): treat the record as its own
			// turn so commands never falsely share one.
			key = fmt.Sprintf("rec#%d", i)
		}
		turn, ok := turnOf[key]
		if !ok {
			turn = nextTurn
			turnOf[key] = turn
			nextTurn++
		}
		for _, tu := range toolUses {
			cmd := commandFromInput(tu.Input)
			if cmd == "" {
				continue
			}
			events = append(events, TurnEvent{
				Cmd:       cmd,
				Turn:      turn,
				ToolUseID: tu.ID,
				IsError:   errByID[tu.ID],
				Repo:      repoScope(cmd),
			})
		}
	}
	return events
}

// codexRecord is the minimal slice of a Codex rollout JSONL record the turn
// model needs. Codex batches a model turn as consecutive function_call records
// terminated by their function_call_output records — the same shape as a
// Claude assistant message's parallel tool calls, so one batch is one turn.
type codexRecord struct {
	Payload struct {
		Type      string `json:"type"`      // function_call | function_call_output | ...
		Name      string `json:"name"`      // exec_command for shell calls
		Arguments string `json:"arguments"` // JSON string: {"cmd","workdir",...}
		CallID    string `json:"call_id"`
		Output    string `json:"output"` // function_call_output text (carries exit code)
	} `json:"payload"`
}

type codexArgs struct {
	Cmd     string `json:"cmd"`
	Workdir string `json:"workdir"`
}

// CodexSessionTurns parses one Codex rollout's JSONL into ordered shell-command
// turn events. A turn is a maximal run of consecutive function_call records (one
// model turn issues a batch, then all outputs return), so parallel calls share a
// turn exactly like Claude's message-id grouping. workdir gives the repo scope
// directly; the exit code embedded in the matching output gives IsError.
func CodexSessionTurns(data []byte) []TurnEvent {
	var records []codexRecord
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec codexRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		records = append(records, rec)
	}

	// Pass 1: join call_id -> did the command exit non-zero.
	errByID := map[string]bool{}
	for _, rec := range records {
		if rec.Payload.Type == "function_call_output" && rec.Payload.CallID != "" {
			errByID[rec.Payload.CallID] = codexOutputErrored(rec.Payload.Output)
		}
	}

	// Pass 2: assign a turn per function_call batch.
	var events []TurnEvent
	turn := -1
	inBatch := false
	for _, rec := range records {
		switch rec.Payload.Type {
		case "function_call":
			if !inBatch {
				turn++
				inBatch = true
			}
			if rec.Payload.Name != "exec_command" {
				continue
			}
			var a codexArgs
			if err := json.Unmarshal([]byte(rec.Payload.Arguments), &a); err != nil || strings.TrimSpace(a.Cmd) == "" {
				continue
			}
			repo := a.Workdir
			if repo == "" {
				repo = repoScope(a.Cmd)
			}
			events = append(events, TurnEvent{
				Cmd:       a.Cmd,
				Turn:      turn,
				ToolUseID: rec.Payload.CallID,
				IsError:   errByID[rec.Payload.CallID],
				Repo:      repo,
			})
		case "function_call_output":
			inBatch = false
		}
	}
	return events
}

// codexOutputErrored best-effort reads the exit code Codex embeds in a
// function_call_output ("Process exited with code N"). Unknown → not errored.
func codexOutputErrored(output string) bool {
	_, rest, found := strings.Cut(output, "exited with code ")
	if !found {
		return false
	}
	return !strings.HasPrefix(rest, "0")
}

type toolUseBlock struct {
	ID    string
	Input json.RawMessage
}

// assistantToolUses returns the tool_use blocks of an assistant record, or nil
// when the record is not an assistant message carrying tool calls.
func assistantToolUses(rec claudeRecord) []toolUseBlock {
	if rec.Type != "assistant" && rec.Message.Role != "assistant" {
		return nil
	}
	var out []toolUseBlock
	for _, c := range rec.Message.Content {
		if c.Type == "tool_use" {
			out = append(out, toolUseBlock{ID: c.ID, Input: c.Input})
		}
	}
	return out
}

func parseClaudeRecords(data []byte) []claudeRecord {
	var records []claudeRecord
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec claudeRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		records = append(records, rec)
	}
	return records
}

func commandFromInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var in toolInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return ""
	}
	if in.Command != "" {
		return in.Command
	}
	return in.Cmd
}

// repoScope infers the working directory a command's git operations ran in, so
// the collapse detector never merges git calls from different repos/worktrees
// into one sequence. It reads a leading `cd <dir>` and a git `-C <dir>`; the
// `-C` form wins when both appear. "" means unknown (single-repo assumption).
func repoScope(cmd string) string {
	segments, _ := splitShellSegments(cmd)
	repo := ""
	for _, seg := range segments {
		fields := shellFields(seg)
		for i := range fields {
			tok := trimShellToken(fields[i])
			switch tok {
			case "cd":
				if i+1 < len(fields) {
					repo = trimShellToken(fields[i+1])
				}
			case "-C":
				if i+1 < len(fields) {
					repo = trimShellToken(fields[i+1])
				}
			}
		}
	}
	return repo
}
