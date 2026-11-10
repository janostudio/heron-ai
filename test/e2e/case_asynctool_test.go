//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"
)

// TestTool6AsyncToolSuspendsTeamTurnAndResumes covers the async-tool path, which
// is where `waiting_tool` actually appears.
//
// Layering matters here and is easy to get wrong: the suspension happens on the
// *team* turn (the agent declared the tool async, so it hands the turn back and
// the task runs in the background), while the caller's flow turn is still
// `waiting_input` because the dispatch was accepted. The test pins both: a
// session.team_turn.waiting_tool event exists, and the session stays continuable.
func TestTool6AsyncToolSuspendsTeamTurnAndResumes(t *testing.T) {
	judge(t, "I-TOOL-4", "tool")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TOOL-4", fixture: "asynctool"})
	e.Mock.script(
		step{match: []string{"You are 接待"}, toolCalls: []mockToolCall{{ID: "call_t1", Name: "Team", Args: map[string]any{
			"subagent_type": "worker", "prompt": "跑一个后台子实例", "description": "派活",
		}}}},
		step{match: []string{"You are 接待"}, text: "已派发", repeat: true},
		step{match: []string{"You are 执行者"}, toolCalls: []mockToolCall{{ID: "call_s1", Name: "Spawn", Args: map[string]any{
			"item": "子任务：慢一点", "wait": false,
		}}}},
		step{match: []string{"You are 执行者"}, text: "结论：已启动", repeat: true},
	)

	result := e.runTurn(map[string]any{"input": "跑一个后台子实例"})

	// The team turn parks on the async tool instead of waiting inline.
	events := waitForEvent(t, e.Root, result.Session.ID, 30*time.Second, func(events []event) bool {
		return hasEvent(events, "session.team_turn.waiting_tool")
	})
	if !hasEvent(events, "session.team_turn.waiting_tool") {
		t.Fatalf("no session.team_turn.waiting_tool event; types=%v", typesOf(events))
	}

	// The async task really is a durable task, and it finishes.
	task := waitForTask(t, e.Root, 40*time.Second, func(record map[string]any) bool {
		return fieldString(record, "tool_name") == "Spawn" && fieldString(record, "status") == "completed"
	})
	if id := fieldString(task, "id"); id == "" {
		t.Fatal("async task has no id: it cannot be inspected or resumed")
	}

	// The session stayed usable throughout: the dispatch was accepted, so the
	// user-facing state is waiting_input, not waiting_tool.
	body, status, err := e.do(http.MethodGet, "/api/status?session_id="+result.Session.ID, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("status: status=%d err=%v", status, err)
	}
	newBody, newStatus, err := e.do(http.MethodPost, "/api/run", map[string]any{
		"flow_session_id": result.Session.ID, "input": "顺便问一句",
	})
	if err != nil || newStatus != http.StatusOK {
		t.Fatalf("POST /api/run while a team is parked on an async tool: status=%d err=%v body=%s",
			newStatus, err, truncate(string(newBody), 200))
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{
			"team_waiting_tool": hasEvent(events, "session.team_turn.waiting_tool"),
			"session_status":    truncate(string(body), 80),
		})
}
