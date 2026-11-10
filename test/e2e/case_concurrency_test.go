//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// requestsOf returns the model requests whose context names one agent.
func requestsOf(requests []mockRequest, needle string) []mockRequest {
	var out []mockRequest
	for _, request := range requests {
		if strings.Contains(requestText(request), needle) {
			out = append(out, request)
		}
	}
	return out
}

// overlaps reports whether two model calls were in flight at the same time.
func overlaps(a, b mockRequest) bool {
	if a.StartedAt.IsZero() || b.StartedAt.IsZero() {
		return false
	}
	aEnd := a.FinishedAt
	if aEnd.IsZero() {
		aEnd = a.StartedAt
	}
	bEnd := b.FinishedAt
	if bEnd.IsZero() {
		bEnd = b.StartedAt
	}
	return a.StartedAt.Before(bEnd) && b.StartedAt.Before(aEnd)
}

// twoTeamCallsScript hands two tasks out in one model response.
func twoTeamCallsScript(first, second string, workerDelay time.Duration) []step {
	return []step{
		{match: []string{"You are 接待"}, toolCalls: []mockToolCall{
			{ID: "call_team_1", Name: "Team", Args: map[string]any{
				"subagent_type": first, "prompt": "任务一", "description": "任务一",
			}},
			{ID: "call_team_2", Name: "Team", Args: map[string]any{
				"subagent_type": second, "prompt": "任务二", "description": "任务二",
			}},
		}},
		{match: []string{"You are 接待"}, text: "已派发两个任务", repeat: true},
		{match: []string{"You are 执行者"}, delay: workerDelay, text: "worker 结论"},
		{match: []string{"You are 第二执行者"}, delay: workerDelay, text: "worker-b 结论"},
	}
}

// TestDispatch5SameTeamTasksQueueAndFinish covers I-DISPATCH-5: two tasks for
// one resident Team queue behind each other and both finish.
//
// A resident Team is one execution unit, so the second task is not an error —
// it is work that has to wait. The failure this guards is the refusal: "already
// running" arriving as a failed task tells the caller nothing happened, when the
// only thing that happened is that it had to wait its turn.
func TestDispatch5SameTeamTasksQueueAndFinish(t *testing.T) {
	judge(t, "I-DISPATCH-5", "dispatch")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-DISPATCH-5", fixture: "dispatch"})
	e.Mock.script(
		step{match: []string{"You are 接待"}, toolCalls: []mockToolCall{
			{ID: "call_team_1", Name: "Team", Args: map[string]any{
				"subagent_type": "worker", "prompt": "任务一", "description": "任务一",
			}},
			{ID: "call_team_2", Name: "Team", Args: map[string]any{
				"subagent_type": "worker", "prompt": "任务二", "description": "任务二",
			}},
		}},
		step{match: []string{"You are 接待"}, text: "结论：两件都办完了", repeat: true},
		// Serial execution makes the consumption order deterministic: task one
		// takes the first pair, task two the second pair.
		step{match: []string{"You are 执行者"}, toolCalls: []mockToolCall{{
			ID: "call_w1", Name: "Read", Args: map[string]any{"file": "README.md"},
		}}},
		step{match: []string{"You are 执行者"}, text: "任务一完成"},
		step{match: []string{"You are 执行者"}, toolCalls: []mockToolCall{{
			ID: "call_w2", Name: "Read", Args: map[string]any{"file": "README.md"},
		}}},
		step{match: []string{"You are 执行者"}, text: "任务二完成"},
	)

	result := e.runTurn(map[string]any{"input": "派两个任务给同一个 team"})
	waitForEvent(t, e.Root, result.Session.ID, 25*time.Second, func(events []event) bool {
		return terminalCount(events) >= 2
	})
	time.Sleep(1500 * time.Millisecond)

	tasks := taskRecords(t, e.Root)
	if len(tasks) != 2 {
		t.Fatalf("dispatched two tasks but found %d durable tasks", len(tasks))
	}
	var problems []string
	for _, task := range tasks {
		id := fieldString(task, "id")
		status := fieldString(task, "status")
		resultField, _ := task["result"].(map[string]any)
		success, _ := resultField["success"].(bool)
		errText, _ := resultField["error"].(string)
		if status != "completed" || !success {
			problems = append(problems, fmt.Sprintf("%s status=%s success=%v err=%s", id, status, success, errText))
		}
	}
	if len(problems) > 0 {
		t.Fatalf("queued tasks did not both finish: %v", problems)
	}

	workerCalls := requestsOf(e.Mock.requests(), "You are 执行者")
	if len(workerCalls) != 4 {
		t.Fatalf("worker ran %d model rounds for 2 two-hop tasks; want 4", len(workerCalls))
	}
	// Serial evidence: the second task may not start before the first finished.
	if workerCalls[2].StartedAt.Before(workerCalls[1].FinishedAt) {
		t.Fatalf("two tasks for one resident Team ran at the same time; one instance means one execution")
	}

	// The queue has to be visible as a queue: a task that waited logs why.
	queued := 0
	for _, line := range readProcessLog(t, e.Root) {
		if line.Msg == "tool task queued behind a running instance" {
			queued++
		}
	}
	if queued == 0 {
		t.Fatalf("no log line records the task waiting for its instance; a silent wait is indistinguishable from a hang")
	}

	writeTrace(t, e, httpTrace{Method: "POST", Path: "/api/run", Status: 200},
		result.Session.ID, map[string]any{"tasks": len(tasks), "worker_calls": len(workerCalls), "queued_logs": queued})
}

// TestDispatch6DifferentTeamsRunInParallel covers the other half of
// I-DISPATCH-4: independent teams must be able to run at the same time,
// otherwise a coordinator handing out three tasks pays for them one by one.
func TestDispatch6DifferentTeamsRunInParallel(t *testing.T) {
	judge(t, "I-DISPATCH-6", "dispatch")

	const delay = 300 * time.Millisecond
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-DISPATCH-6", fixture: "dispatch"})
	e.Mock.script(twoTeamCallsScript("worker", "worker-b", delay)...)

	result := e.runTurn(map[string]any{"input": "派两个任务给不同 team"})
	waitForEvent(t, e.Root, result.Session.ID, 25*time.Second, func(events []event) bool {
		return terminalCount(events) >= 2
	})

	first := requestsOf(e.Mock.requests(), "You are 执行者")
	second := requestsOf(e.Mock.requests(), "You are 第二执行者")
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected both workers to run; worker=%d worker-b=%d", len(first), len(second))
	}
	if !overlaps(first[0], second[0]) {
		t.Fatalf("the two teams ran one after the other (worker %s..%s, worker-b %s..%s); independent teams must be able to overlap",
			first[0].StartedAt.Format("15:04:05.000"), first[0].FinishedAt.Format("15:04:05.000"),
			second[0].StartedAt.Format("15:04:05.000"), second[0].FinishedAt.Format("15:04:05.000"))
	}

	writeTrace(t, e, httpTrace{Method: "POST", Path: "/api/run", Status: 200},
		result.Session.ID, map[string]any{"worker": len(first), "worker_b": len(second)})
}

// TestDispatch7PerTaskInstancesRunConcurrently covers I-DISPATCH-7: a team that
// declares lifecycle per_task gets one instance per dispatched task, so two
// tasks in one response run at the same time instead of one being refused.
//
// The failure this guards is silent: if per_task degrades into a single
// instance, the second dispatch comes back as "already running" and the
// declaration stops meaning anything — the flow says concurrent, the engine
// serialises.
func TestDispatch7PerTaskInstancesRunConcurrently(t *testing.T) {
	judge(t, "I-DISPATCH-7", "dispatch")

	const delay = 400 * time.Millisecond
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-DISPATCH-7", fixture: "dispatch"})
	e.Mock.script(
		step{match: []string{"You are 接待"}, toolCalls: []mockToolCall{
			// A per_task team is addressed by task_key: it is what lets a later
			// dispatch find the instance instead of starting another one.
			{ID: "call_probe_1", Name: "Team", Args: map[string]any{
				"subagent_type": "probe", "prompt": "任务一", "description": "任务一", "task_key": "probe-1",
			}},
			{ID: "call_probe_2", Name: "Team", Args: map[string]any{
				"subagent_type": "probe", "prompt": "任务二", "description": "任务二", "task_key": "probe-2",
			}},
		}},
		step{match: []string{"You are 接待"}, text: "已派发", repeat: true},
		// Two scripted worker steps: the concurrent instances each take one.
		step{match: []string{"You are 执行者"}, delay: delay, text: "任务一结论"},
		step{match: []string{"You are 执行者"}, delay: delay, text: "任务二结论"},
	)

	result := e.runTurn(map[string]any{"input": "分别办两件事"})
	waitForEvent(t, e.Root, result.Session.ID, 25*time.Second, func(events []event) bool {
		return terminalCount(events) >= 2
	})
	time.Sleep(1200 * time.Millisecond)

	tasks := taskRecords(t, e.Root)
	if len(tasks) != 2 {
		t.Fatalf("two dispatches to a per_task team produced %d durable tasks; each task needs its own instance", len(tasks))
	}

	var refused []string
	for _, task := range tasks {
		id := fieldString(task, "id")
		status := fieldString(task, "status")
		resultField, _ := task["result"].(map[string]any)
		errText, _ := resultField["error"].(string)
		if strings.Contains(errText, "already running") {
			refused = append(refused, id+": "+errText)
		}
		if status != "completed" {
			refused = append(refused, id+" status="+status)
		}
	}
	if len(refused) > 0 {
		t.Fatalf("per_task dispatch did not get its own instance: %v", refused)
	}

	workerCalls := requestsOf(e.Mock.requests(), "You are 执行者")
	if len(workerCalls) != 2 {
		t.Fatalf("worker ran %d times for 2 tasks; want 2", len(workerCalls))
	}
	if !overlaps(workerCalls[0], workerCalls[1]) {
		t.Fatalf("two per_task instances ran one after another; the lifecycle declares them concurrent")
	}

	writeTrace(t, e, httpTrace{Method: "POST", Path: "/api/run", Status: 200},
		result.Session.ID, map[string]any{"tasks": len(tasks), "worker_calls": len(workerCalls), "overlapping": true})
}
