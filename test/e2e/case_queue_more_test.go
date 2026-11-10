//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestDispatch8SameTeamThreeTasksQueue covers the queue at the size it actually
// shows up in: one response that asks for three things from a team that runs one
// task at a time.
//
// Two tasks prove the mechanism; three prove it still holds when the queue has a
// backlog. The failure this catches is a queue that admits the second waiter and
// drops the third, which looks fine in a two-task case and strands work in a
// real one.
func TestDispatch8SameTeamThreeTasksQueue(t *testing.T) {
	judge(t, "I-DISPATCH-5", "dispatch")

	const workerDelay = 200 * time.Millisecond
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-DISPATCH-5", fixture: "dispatch"})
	e.Mock.script(
		step{match: []string{"You are 接待"}, toolCalls: []mockToolCall{
			{ID: "call_1", Name: "Team", Args: map[string]any{"subagent_type": "worker", "prompt": "任务一", "description": "任务一"}},
			{ID: "call_2", Name: "Team", Args: map[string]any{"subagent_type": "worker", "prompt": "任务二", "description": "任务二"}},
			{ID: "call_3", Name: "Team", Args: map[string]any{"subagent_type": "worker", "prompt": "任务三", "description": "任务三"}},
		}},
		step{match: []string{"You are 执行者"}, delay: workerDelay, text: "完成", repeat: true},
		step{match: []string{"You are 接待"}, text: "三件事都完成", repeat: true},
	)

	result := e.runTurn(map[string]any{"input": "派三件事给同一个 team"})
	waitForEvent(t, e.Root, result.Session.ID, 45*time.Second, func(events []event) bool {
		return completedTeamTasks(t, e.Root) == 3
	})

	if tasks := completedTeamTasks(t, e.Root); tasks != 3 {
		t.Fatalf("completed Team tasks = %d, want 3: one of the three dispatched tasks never finished", tasks)
	}

	worker := requestsOf(e.Mock.requests(), "You are 执行者")
	if len(worker) != 3 {
		t.Fatalf("worker ran %d times, want 3 (one execution per queued task)", len(worker))
	}
	// A resident team is one execution unit: its tasks may queue but must not
	// overlap.
	for i := 1; i < len(worker); i++ {
		if overlaps(worker[i-1], worker[i]) {
			t.Fatalf("task %d and %d ran at the same time (%s..%s / %s..%s): a resident team must serialise its tasks",
				i, i+1,
				worker[i-1].StartedAt.Format("15:04:05.000"), worker[i-1].FinishedAt.Format("15:04:05.000"),
				worker[i].StartedAt.Format("15:04:05.000"), worker[i].FinishedAt.Format("15:04:05.000"))
		}
	}

	// The two waiters have to be visible as waiters: a task that queued logs why.
	queued := 0
	for _, line := range readProcessLog(t, e.Root) {
		if line.Msg == "tool task queued behind a running instance" {
			queued++
		}
	}
	if queued < 2 {
		t.Fatalf("queued log lines = %d, want >= 2: the third task's wait is not observable", queued)
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{"worker_calls": len(worker), "queued_logs": queued})
}

// TestQueue2AttachmentsSurviveQueueing covers the part of a queued message that
// is not text: attachments.
//
// The failure this catches is silent and user-facing. The user pastes an image
// while the previous answer is still being produced; the engine stores the text
// and drops the blocks, so the queued turn runs against a message the user never
// sent and answers as if no image had been attached.
func TestQueue2AttachmentsSurviveQueueing(t *testing.T) {
	judge(t, "I-QUEUE-1", "dispatch")

	const slowTurn = 2 * time.Second
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-QUEUE-1", fixture: "dispatch"})
	e.Mock.script(
		step{match: []string{"You are 接待", "先看这个"}, delay: slowTurn, text: "先答一版"},
		step{match: []string{"You are 接待", "看图"}, text: "图收到了"},
	)

	first := e.runTurnAsync(map[string]any{"input": "先看这个"})
	waitForModelRequest(t, e, "You are 接待", 20*time.Second)
	sessionID := latestSessionID(t, e)
	if sessionID == "" {
		t.Fatal("no session was created for the first input")
	}

	second := e.runTurn(map[string]any{
		"flow_session_id": sessionID,
		"input":           "看图",
		"attachments": []map[string]any{{
			"source_type": "base64", "data_base64": "aGk=",
			"kind": "image", "mime_type": "image/png", "name": "queued.png",
		}},
	})
	if !second.Queued {
		t.Fatalf("second message was not queued while the first turn ran: reply=%q", second.Reply)
	}
	if outcome := <-first; outcome.Status != http.StatusOK {
		t.Fatalf("first turn: status=%d err=%v", outcome.Status, outcome.Err)
	}

	// The queued turn must be the message the user sent: the text, the blocks
	// that carry the attachment, and the attachment itself in the model request.
	waitForModelRequest(t, e, "看图", 45*time.Second)

	carried := false
	for _, req := range e.Mock.requests() {
		if strings.Contains(requestText(req), "看图") && strings.Contains(string(req.Raw), "image_url") {
			carried = true
		}
	}
	if !carried {
		t.Fatal("the queued turn reached the model without the image: queueing kept the text but lost the attachment")
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		sessionID, map[string]any{"attachment_carried": carried})
}

// TestQueue4QueuedInputsRunInOrder covers the order of a backlog: three messages
// arrive mid-turn and must run in the order they were sent.
//
// Out-of-order execution is not a crash, which is why it survives manual testing:
// the user sees answers to their questions, just not in the sequence they asked,
// and any answer that refers to "the previous one" is now wrong.
func TestQueue4QueuedInputsRunInOrder(t *testing.T) {
	judge(t, "I-QUEUE-1", "dispatch")

	const slowTurn = 1500 * time.Millisecond
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-QUEUE-1", fixture: "dispatch"})
	e.Mock.script(
		step{match: []string{"You are 接待", "开场问题"}, delay: slowTurn, text: "开场答复"},
		step{match: []string{"You are 接待", "排队一"}, text: "答复一"},
		step{match: []string{"You are 接待", "排队二"}, text: "答复二"},
		step{match: []string{"You are 接待", "排队三"}, text: "答复三"},
	)

	first := e.runTurnAsync(map[string]any{"input": "开场问题"})
	waitForModelRequest(t, e, "You are 接待", 20*time.Second)
	sessionID := latestSessionID(t, e)
	if sessionID == "" {
		t.Fatal("no session was created for the first input")
	}

	for _, input := range []string{"排队一", "排队二", "排队三"} {
		result := e.runTurn(map[string]any{"flow_session_id": sessionID, "input": input})
		if !result.Queued {
			t.Fatalf("%s was not queued while the first turn ran: reply=%q", input, result.Reply)
		}
	}
	if outcome := <-first; outcome.Status != http.StatusOK {
		t.Fatalf("first turn: status=%d err=%v", outcome.Status, outcome.Err)
	}

	events := waitForEvent(t, e.Root, sessionID, 60*time.Second, func(events []event) bool {
		return countEvents(events, "flow_turn.started") >= 4
	})
	if started := countEvents(events, "flow_turn.started"); started < 4 {
		t.Fatalf("flow_turn.started = %d, want 4 (the opening turn plus three queued ones)", started)
	}

	// The order is read off the model requests: each queued input produced one.
	var order []string
	for _, req := range e.Mock.requests() {
		text := requestText(req)
		for _, input := range []string{"排队一", "排队二", "排队三"} {
			if strings.Contains(text, input) {
				order = appendUnique(order, input)
			}
		}
	}
	want := []string{"排队一", "排队二", "排队三"}
	if len(order) != len(want) {
		t.Fatalf("queued inputs reached the model as %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("queued inputs ran as %v, want %v: a backlog must be served in the order it arrived", order, want)
		}
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		sessionID, map[string]any{"turns_started": 4, "order": order})
}

// appendUnique appends value unless it is already present, keeping the order
// requests were observed in.
func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
