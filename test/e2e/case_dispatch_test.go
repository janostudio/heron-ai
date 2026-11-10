//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// taskRecords reads the durable tool-task files the engine wrote. The engine
// keeps them under tool-tasks/; tasks/ holds the other task flavour, so both
// are scanned rather than assuming one layout.
func taskRecords(t *testing.T, root string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, dir := range []string{"tool-tasks", "tasks"} {
		full := filepath.Join(root, ".agents", "data", dir)
		entries, err := os.ReadDir(full)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(full, entry.Name()))
			if readErr != nil {
				continue
			}
			var record map[string]any
			if json.Unmarshal(data, &record) != nil {
				continue
			}
			record["_file"] = entry.Name()
			out = append(out, record)
		}
	}
	return out
}

func fieldString(record map[string]any, key string) string {
	value, _ := record[key].(string)
	return value
}

// waitForTask polls the durable task store until one task matches.
func waitForTask(t *testing.T, root string, timeout time.Duration, match func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, record := range taskRecords(t, root) {
			if match(record) {
				return record
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no matching durable task within %s; tasks=%v", timeout, taskRecords(t, root))
	return nil
}

// dispatchScript is the healthy handoff: the caller asks for a worker, the
// worker does real work (Read), and the caller turns the result into an answer.
// Steps match on the agent's own prompt, because the caller's follow-up round
// and the worker's first round run concurrently.
func dispatchScript(workerDelay time.Duration, workerUsesTool bool) []step {
	workerFirst := step{match: []string{"You are 执行者"}, delay: workerDelay, text: "worker 结论：v9.9.9"}
	if workerUsesTool {
		workerFirst = step{match: []string{"You are 执行者"}, toolCalls: []mockToolCall{{
			ID: "call_worker_1", Name: "Read", Args: map[string]any{"file": "README.md"},
		}}}
	}
	return []step{
		{match: []string{"You are 接待"}, toolCalls: []mockToolCall{{
			ID: "call_team_1", Name: "Team", Args: map[string]any{
				"subagent_type": "worker",
				"prompt":        "查一下当前版本号",
				"description":   "查版本",
			},
		}}},
		{match: []string{"You are 接待"}, text: "结论：v9.9.9", repeat: true},
		workerFirst,
		{match: []string{"You are 执行者"}, text: "worker 结论：v9.9.9"},
	}
}

// TestDispatch1HandoffLeavesATrace covers I-DISPATCH-3 for the happy path: the
// handoff returns a handle to the caller and writes a durable task that names
// the caller, the callee and the task text. Without those four, "dispatched" is
// a claim with nothing to check afterwards.
func TestDispatch1HandoffLeavesATrace(t *testing.T) {
	judge(t, "I-DISPATCH-3", "dispatch")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-DISPATCH-3", fixture: "dispatch"})
	e.Mock.script(dispatchScript(0, true)...)

	result := e.runTurn(map[string]any{"input": "当前版本是多少"})

	handoff := false
	for _, request := range e.Mock.requests() {
		if strings.Contains(requestText(request), "Handed to worker") {
			handoff = true
			break
		}
	}
	if !handoff {
		t.Fatalf("caller never received a handoff handle; messages=%s", e.Mock.summary())
	}

	task := waitForTask(t, e.Root, 10*time.Second, func(record map[string]any) bool {
		return fieldString(record, "tool_name") == "TeamRun"
	})
	if got := fieldString(recordValue(task, "arguments"), "team_id"); got != "worker" {
		t.Fatalf("task targets %q; want worker (record=%v)", got, task)
	}
	if got := fieldString(recordValue(task, "arguments"), "caller_team"); got != "frontdesk" {
		t.Fatalf("task records caller %q; want frontdesk", got)
	}
	if got := fieldString(recordValue(task, "arguments"), "item"); got != "查一下当前版本号" {
		t.Fatalf("task lost the dispatched text: item=%q", got)
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{"task_file": task["_file"]})
}

// TestDispatch2TurnDoesNotWaitForDownstream covers I-DISPATCH-1: the caller's
// turn should end at the handoff. The worker's model call is delayed 600ms, so
// a request that returns after that waited for work it did not need to wait
// for. Timing is the only evidence that separates "handed off" from "did it
// inline": both end with the same reply.
func TestDispatch2TurnDoesNotWaitForDownstream(t *testing.T) {
	judge(t, "I-DISPATCH-1", "dispatch")

	const workerDelay = 600 * time.Millisecond
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-DISPATCH-1", fixture: "dispatch"})
	e.Mock.script(dispatchScript(workerDelay, true)...)

	startedAt := time.Now()
	result := e.runTurn(map[string]any{"input": "当前版本是多少"})
	elapsed := time.Since(startedAt)

	events := waitForEvent(t, e.Root, result.Session.ID, 20*time.Second, func(events []event) bool {
		return hasEvent(events, "mailbox.message.enqueued") || countEvents(events, "flow_turn.started") >= 2
	})
	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK,
		DurationMS: elapsed.Milliseconds()}, result.Session.ID, map[string]any{
		"turn_ms":   elapsed.Milliseconds(),
		"turns":     countEvents(events, "flow_turn.started"),
		"worker_ms": workerDelay.Milliseconds(),
		"mailbox":   countEvents(events, "mailbox.message.enqueued"),
	})

	if elapsed >= workerDelay {
		t.Fatalf("caller's request returned after %s while the worker takes %s (%d flow_turn.started so far): the turn waited for the downstream team instead of ending at the handoff",
			elapsed, workerDelay, countEvents(events, "flow_turn.started"))
	}
}

// TestDispatch3ResultBecomesTheConclusion covers I-DISPATCH-2: the downstream
// answer reaches the user as the conclusion, exactly once. Two conclusions (the
// caller's guess plus the worker's answer) is the failure this asserts against.
func TestDispatch3ResultBecomesTheConclusion(t *testing.T) {
	judge(t, "I-DISPATCH-2", "dispatch")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-DISPATCH-2", fixture: "dispatch"})
	e.Mock.script(dispatchScript(0, true)...)

	result := e.runTurn(map[string]any{"input": "当前版本是多少"})
	// The handoff turn ends immediately and empty; the conclusion arrives in the
	// turn the engine wakes once the worker finishes, so that is the turn this
	// case judges.
	events := waitForEvent(t, e.Root, result.Session.ID, 25*time.Second, twoTurnsTerminated)

	reply, conclusions := conclusionOf(events)
	if conclusions != 1 {
		t.Fatalf("session produced %d user-facing conclusions; want exactly 1 (events=%v)", conclusions, typesOf(events))
	}
	if reply == "" {
		t.Fatalf("conclusion is empty; events=%v", typesOf(events))
	}

	task := waitForTask(t, e.Root, 10*time.Second, func(record map[string]any) bool {
		return fieldString(record, "tool_name") == "TeamRun" && fieldString(record, "status") == "completed"
	})
	if success, _ := recordValue(task, "result")["success"].(bool); !success {
		t.Fatalf("worker task finished unsuccessfully: %v", recordValue(task, "result"))
	}

	terminals := 0
	for _, ev := range events {
		if strings.HasPrefix(ev.Type, "flow_turn.waiting") || ev.Type == "flow_turn.completed" {
			terminals++
		}
	}
	if terminals == 0 {
		t.Fatalf("no terminal turn event; events=%v", typesOf(events))
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{"reply": reply, "terminals": terminals})
}

// TestDispatch4DownstreamFailureIsReported covers I-DISPATCH-4: when the
// dispatched team fails, the caller must be told, the chain must stop, and the
// reason must be readable. Silence is the worst outcome — the user sees a clean
// turn and has nothing to retry.
func TestDispatch4DownstreamFailureIsReported(t *testing.T) {
	judge(t, "I-DISPATCH-4", "dispatch")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-DISPATCH-4", fixture: "dispatch"})
	// The worker answers without calling any tool, which the engine treats as
	// "did no work" (commit 3a21378).
	e.Mock.script(
		step{match: []string{"You are 接待"}, toolCalls: []mockToolCall{{
			ID: "call_team_1", Name: "Team", Args: map[string]any{
				"subagent_type": "worker", "prompt": "查版本", "description": "查版本",
			},
		}}},
		step{match: []string{"You are 接待"}, text: "已派发", repeat: true},
		step{match: []string{"You are 执行者"}, text: "我没做事"},
	)

	result := e.runTurn(map[string]any{"input": "当前版本是多少"})
	waitForEvent(t, e.Root, result.Session.ID, 25*time.Second, twoTurnsTerminated)

	task := waitForTask(t, e.Root, 10*time.Second, func(record map[string]any) bool {
		return fieldString(record, "tool_name") == "TeamRun"
	})
	success, _ := recordValue(task, "result")["success"].(bool)

	// Give the session a moment to misbehave: a chain that keeps retrying shows
	// up as extra turns or repeated dispatches.
	time.Sleep(1500 * time.Millisecond)
	after := readSession(t, e.Root, result.Session.ID)

	turns := countEvents(after, "flow_turn.started")
	turnError, errorSeen := turnErrorOf(after)
	workerCalls := len(requestsOf(e.Mock.requests(), "You are 执行者"))
	reply, _ := conclusionOf(after)

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{
			"task_success": success,
			"turn_error":   turnError,
			"turns":        turns,
			"worker_calls": workerCalls,
			"reply":        reply,
		})

	if success {
		t.Fatalf("worker did no work yet the task is marked successful; task=%v", task)
	}
	if !errorSeen || strings.TrimSpace(turnError) == "" {
		t.Fatalf("downstream failure is not reported: task_success=false but no turn carries an error (reply=%q turns=%d)", reply, turns)
	}
	if turns > 2 {
		t.Fatalf("chain kept going after the downstream failure: %d turns (want the handoff turn plus one closing turn)", turns)
	}
	if workerCalls > 1 {
		t.Fatalf("worker was dispatched %d times after failing; a failed handoff must not be retried silently", workerCalls)
	}
	if strings.Contains(reply, "已派发") {
		t.Fatalf("failed handoff still ends with the progress sentence %q; the conclusion must state the failure", reply)
	}
}

// turnErrorOf returns the last non-empty turn error in the session.
func turnErrorOf(events []event) (string, bool) {
	last := ""
	for _, ev := range events {
		if !strings.HasPrefix(ev.Type, "flow_turn.waiting") && ev.Type != "flow_turn.completed" {
			continue
		}
		payload, _ := ev.Raw["payload"].(map[string]any)
		if payload == nil {
			continue
		}
		if text, _ := payload["error"].(string); strings.TrimSpace(text) != "" {
			last = strings.TrimSpace(text)
		}
	}
	return last, last != ""
}

func recordValue(record map[string]any, key string) map[string]any {
	value, _ := record[key].(map[string]any)
	return value
}

// terminalCount counts turn-terminal events.
func terminalCount(events []event) int {
	count := 0
	for _, ev := range events {
		if strings.HasPrefix(ev.Type, "flow_turn.waiting") || ev.Type == "flow_turn.completed" {
			count++
		}
	}
	return count
}

// twoTurnsTerminated reports whether the handoff turn and the turn that
// consumes its result have both closed.
func twoTurnsTerminated(events []event) bool {
	return countEvents(events, "flow_turn.started") >= 2 && terminalCount(events) >= 2
}

// conclusionOf returns the last user-facing conclusion and how many there were.
// A conclusion is a terminal turn that is not marked intermediate and carries
// text — the intermediate ones are progress sentences.
func conclusionOf(events []event) (string, int) {
	reply := ""
	count := 0
	for _, ev := range events {
		if !strings.HasPrefix(ev.Type, "flow_turn.waiting") && ev.Type != "flow_turn.completed" {
			continue
		}
		payload, _ := ev.Raw["payload"].(map[string]any)
		if payload == nil {
			continue
		}
		if intermediate, _ := payload["intermediate"].(bool); intermediate {
			continue
		}
		text, _ := payload["reply"].(string)
		if strings.TrimSpace(text) == "" {
			continue
		}
		count++
		reply = text
	}
	return strings.TrimSpace(reply), count
}
