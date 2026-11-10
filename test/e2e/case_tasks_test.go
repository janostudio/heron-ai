//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// dispatchToSlowWorker starts one handoff whose worker takes a while, so the
// task is observably non-terminal for long enough to query and cancel.
func dispatchToSlowWorker(t *testing.T, e *env, delay time.Duration) turnResult {
	t.Helper()
	e.Mock.script(
		step{match: []string{"You are 接待"}, toolCalls: []mockToolCall{{
			ID: "call_team_1", Name: "Team", Args: map[string]any{
				"subagent_type": "worker", "prompt": "跑一个慢任务", "description": "慢任务",
			},
		}}},
		step{match: []string{"You are 接待"}, text: "已派发", repeat: true},
		step{match: []string{"You are 执行者"}, delay: delay, text: "慢任务完成"},
	)
	return e.runTurn(map[string]any{"input": "跑个慢任务"})
}

// runningTaskID waits until a durable task exists and returns its id.
func runningTaskID(t *testing.T, root string, timeout time.Duration, wantStatus string) string {
	t.Helper()
	task := waitForTask(t, root, timeout, func(record map[string]any) bool {
		return fieldString(record, "tool_name") == "TeamRun" && fieldString(record, "status") == wantStatus
	})
	return fieldString(task, "id")
}

// TestTask1StatusIsQueryable covers I-TASK-1: the task a dispatch created can be
// looked up over HTTP by the id the engine assigned. A dispatch whose state is
// only visible in files is not operable by a client.
func TestTask1StatusIsQueryable(t *testing.T) {
	judge(t, "I-TASK-1", "tasks")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TASK-1", fixture: "dispatch"})
	result := dispatchToSlowWorker(t, e, 2*time.Second)
	taskID := runningTaskID(t, e.Root, 5*time.Second, "running")

	body, status, err := e.do(http.MethodGet, "/api/tasks?task_id="+taskID, nil)
	if err != nil {
		t.Fatalf("GET /api/tasks: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("GET /api/tasks: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}
	var task struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &task); err != nil {
		t.Fatalf("decode task: %v body=%s", err, strings.TrimSpace(string(body)))
	}
	if task.ID != taskID {
		t.Fatalf("task id mismatch: got %q want %q", task.ID, taskID)
	}
	if task.Status == "" {
		t.Fatalf("task response carries no status: %s", strings.TrimSpace(string(body)))
	}

	writeTrace(t, e, httpTrace{Method: http.MethodGet, Path: "/api/tasks", Status: status},
		result.Session.ID, map[string]any{"task_id": taskID, "task_status": task.Status})
}

// TestTask2CancelStopsTheExecution covers I-TASK-2: cancelling reaches the
// execution, not just a record. The second half of the promise is that no
// conclusion is delivered for work that was cancelled.
func TestTask2CancelStopsTheExecution(t *testing.T) {
	judge(t, "I-TASK-2", "tasks")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TASK-2", fixture: "dispatch"})
	result := dispatchToSlowWorker(t, e, 3*time.Second)
	taskID := runningTaskID(t, e.Root, 5*time.Second, "running")

	body, status, err := e.do(http.MethodPost, "/api/tasks/cancel?task_id="+taskID, nil)
	if err != nil {
		t.Fatalf("POST /api/tasks/cancel: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("cancel: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}

	cancelled := waitForTask(t, e.Root, 5*time.Second, func(record map[string]any) bool {
		return fieldString(record, "id") == taskID && fieldString(record, "status") == "cancelled"
	})
	if fieldString(cancelled, "status") != "cancelled" {
		t.Fatalf("task did not reach cancelled: %v", cancelled)
	}

	events := readSession(t, e.Root, result.Session.ID)
	reply, _ := conclusionOf(events)
	if strings.Contains(reply, "慢任务完成") {
		t.Fatalf("cancelled work still produced a conclusion: %q", reply)
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/tasks/cancel", Status: status},
		result.Session.ID, map[string]any{"task_id": taskID, "reply": reply})
}

// TestTask3StreamEndsAtTerminalState covers I-TASKSTREAM-1: the task stream
// carries frames and closes once the task is terminal. A stream that stays open
// on a finished task keeps clients waiting on a state that already arrived.
func TestTask3StreamEndsAtTerminalState(t *testing.T) {
	judge(t, "I-TASKSTREAM-1", "tasks")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TASKSTREAM-1", fixture: "dispatch"})
	result := dispatchToSlowWorker(t, e, time.Second)
	taskID := runningTaskID(t, e.Root, 5*time.Second, "running")

	subscription := startStreamPath(t, e, "/api/tasks/stream?task_id="+taskID)
	frames := subscription.waitUntil(15*time.Second, func(frame sseFrame) bool {
		var task map[string]any
		if json.Unmarshal([]byte(frame.Data), &task) != nil {
			return false
		}
		switch fieldString(task, "status") {
		case "completed", "failed", "cancelled":
			return true
		}
		return false
	})

	if len(frames) == 0 {
		t.Fatalf("task stream delivered no frames for %s", taskID)
	}
	last := frames[len(frames)-1]
	var task map[string]any
	if err := json.Unmarshal([]byte(last.Data), &task); err != nil {
		t.Fatalf("task frame is not JSON: %v payload=%s", err, last.Data)
	}
	status := fieldString(task, "status")
	switch status {
	case "completed", "failed", "cancelled":
	default:
		t.Fatalf("task stream ended at status %q; want a terminal status (frames=%d)", status, len(frames))
	}

	writeTrace(t, e, httpTrace{Method: http.MethodGet, Path: "/api/tasks/stream", Status: http.StatusOK},
		result.Session.ID, map[string]any{"task_id": taskID, "frames": len(frames), "final": status})
}

// TestTask4CancelIsIdempotent covers the repeat half of I-TASK-2: cancelling an
// already-cancelled task is a no-op, not an error. Clients retry cancels.
func TestTask4CancelIsIdempotent(t *testing.T) {
	judge(t, "I-TASK-2", "tasks")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TASK-2", fixture: "dispatch"})
	dispatchToSlowWorker(t, e, 3*time.Second)
	taskID := runningTaskID(t, e.Root, 5*time.Second, "running")

	firstBody, firstStatus, err := e.do(http.MethodPost, "/api/tasks/cancel?task_id="+taskID, nil)
	if err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	secondBody, secondStatus, err := e.do(http.MethodPost, "/api/tasks/cancel?task_id="+taskID, nil)
	if err != nil {
		t.Fatalf("second cancel: %v", err)
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/tasks/cancel", Status: secondStatus},
		"", map[string]any{"first": firstStatus, "second": secondStatus})

	if firstStatus >= 500 || secondStatus >= 500 {
		t.Fatalf("cancel returned 5xx (first=%d %s, second=%d %s)",
			firstStatus, strings.TrimSpace(string(firstBody)), secondStatus, strings.TrimSpace(string(secondBody)))
	}
	if secondStatus != http.StatusOK {
		t.Fatalf("repeat cancel answered %d (%s); cancelling a terminal task is a no-op",
			secondStatus, strings.TrimSpace(string(secondBody)))
	}
}
