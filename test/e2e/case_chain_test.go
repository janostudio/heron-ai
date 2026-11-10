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

// TestChain1ThreeTeamsInOneTurn covers a chain the two-team cases cannot reach:
// frontdesk dispatches worker, worker dispatches probe, and the conclusion has to
// travel back through both hops to reach the user as one answer.
//
// What this catches: a chain that only works one level deep. The second edge is
// the one that breaks when reach is computed from the wrong team, when a nested
// dispatch overwrites its parent's task, or when the wake-up after the innermost
// team returns to the wrong caller — each of which leaves the user with a
// handoff sentence instead of an answer.
func TestChain1ThreeTeamsInOneTurn(t *testing.T) {
	judge(t, "I-CHAIN-1", "dispatch")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-CHAIN-1", fixture: "chain"})
	e.Mock.script(
		step{
			match: []string{"You are 接待"},
			toolCalls: []mockToolCall{{ID: "call_frontdesk", Name: "Team", Args: map[string]any{
				"subagent_type": "worker", "prompt": "去查当前版本", "description": "查版本",
			}}},
		},
		step{
			match: []string{"You are 执行者"},
			toolCalls: []mockToolCall{{ID: "call_worker", Name: "Team", Args: map[string]any{
				"subagent_type": "probe", "prompt": "探针：查 v9.9.9", "description": "查版本号",
			}}},
		},
		// probe has to do something before it reports: a team that finishes
		// without calling any tool is judged to have done nothing, which is the
		// rule this step accidentally depended on when it was text-only.
		step{match: []string{"You are 探针"}, toolCalls: []mockToolCall{{
			ID: "call_probe", Name: "Read", Args: map[string]any{"file": "README.md"},
		}}},
		step{match: []string{"You are 探针"}, text: "v9.9.9"},
		// worker's closing turn: it now has probe's result and must report it.
		step{match: []string{"You are 执行者"}, text: "结论：v9.9.9"},
		// frontdesk's closing turns: the chain wakes it once when the work comes
		// back and once more to turn that into the user-facing answer, so the step
		// stays available instead of being consumed by the first wake-up.
		step{match: []string{"You are 接待"}, text: "当前版本 v9.9.9", repeat: true},
	)

	result := e.runTurn(map[string]any{"input": "当前版本是多少"})

	events := waitForEvent(t, e.Root, result.Session.ID, 45*time.Second, func(events []event) bool {
		reply, _ := conclusionOf(events)
		return strings.Contains(reply, "v9.9.9")
	})

	reply, conclusions := conclusionOf(events)
	if conclusions != 1 {
		t.Fatalf("session produced %d user-facing conclusions; want exactly 1 (events=%v)", conclusions, typesOf(events))
	}
	if !strings.Contains(reply, "v9.9.9") {
		t.Fatalf("conclusion = %q, want the innermost team's finding carried back through both hops", reply)
	}

	// All three agents ran, and the middle one carried the sub-task down.
	frontdesk := requestsOf(e.Mock.requests(), "You are 接待")
	worker := requestsOf(e.Mock.requests(), "You are 执行者")
	probe := requestsOf(e.Mock.requests(), "You are 探针")
	if len(frontdesk) < 2 {
		t.Fatalf("frontdesk was called %d times; want at least 2 (dispatch + closing turn)", len(frontdesk))
	}
	if len(worker) < 2 {
		t.Fatalf("worker was called %d times; want at least 2 (sub-dispatch + closing turn with probe's result)", len(worker))
	}
	if len(probe) == 0 {
		t.Fatal("probe never ran: the second edge of the chain was not followed")
	}
	if !strings.Contains(requestText(probe[0]), "探针：查 v9.9.9") {
		t.Fatalf("probe did not receive the sub-task worker dispatched: %s", truncate(requestText(probe[0]), 300))
	}
	if !strings.Contains(requestText(worker[len(worker)-1]), "v9.9.9") {
		t.Fatalf("worker's closing turn never saw probe's result: %s", truncate(requestText(worker[len(worker)-1]), 300))
	}

	// Two dispatches happened, one per edge, and both finished.
	if tasks := completedTeamTasks(t, e.Root); tasks != 2 {
		t.Fatalf("completed Team tasks = %d, want 2 (frontdesk→worker and worker→probe)", tasks)
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{
			"reply": reply, "frontdesk_calls": len(frontdesk), "worker_calls": len(worker), "probe_calls": len(probe),
		})
}

// completedTeamTasks counts the durable Team tasks that reached a completed state
// in this workspace.
func completedTeamTasks(t *testing.T, root string) int {
	t.Helper()
	dir := filepath.Join(root, ".agents", "data", "tool-tasks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			continue
		}
		var record map[string]any
		if json.Unmarshal(data, &record) != nil {
			continue
		}
		if status, _ := record["status"].(string); status == "completed" {
			count++
		}
	}
	return count
}
