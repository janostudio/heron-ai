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

// TestSpawn1InlineChildCompletes covers the synchronous half of the spawn
// primitive: a child instance runs inline, its answer comes back as the tool
// result, and the parent's turn continues. Spawn is the second collaboration
// primitive next to Team, so it needs its own evidence rather than being assumed
// equivalent.
func TestSpawn1InlineChildCompletes(t *testing.T) {
	judge(t, "I-TOOL-1", "tool")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TOOL-1", extraTools: []string{"Spawn", "Collect"}})
	e.Mock.script(
		step{match: []string{"You are 助手"}, toolCalls: []mockToolCall{{
			ID: "call_spawn_1", Name: "Spawn", Args: map[string]any{"item": "子任务：算一下 1+1", "wait": true},
		}}},
		step{match: []string{"You are 助手"}, text: "子实例的答复"},
		step{match: []string{"You are 助手"}, text: "父实例收尾"},
	)

	result := e.runTurn(map[string]any{"input": "派一个子实例算 1+1"})
	events := waitForEvent(t, e.Root, result.Session.ID, 20*time.Second, turnTerminated)

	spawnEvents := toolEventsOf(events, "Spawn")
	if len(spawnEvents) < 2 {
		t.Fatalf("Spawn did not run to completion; types=%v", typesOf(events))
	}
	requests := e.Mock.requests()
	if len(requests) < 3 {
		t.Fatalf("model called %d times; want parent, child, parent", len(requests))
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{"model_calls": len(requests), "spawn_events": len(spawnEvents)})
}

// TestSpawn2AsyncReturnsAHandle covers the asynchronous half: wait=false returns
// a handle immediately and records a durable task. Without the handle there is
// nothing for Collect to collect, and the async path degrades into a promise
// with no object.
func TestSpawn2AsyncReturnsAHandle(t *testing.T) {
	judge(t, "I-TASK-1", "tasks")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TASK-1", extraTools: []string{"Spawn", "Collect"}})
	e.Mock.script(
		step{match: []string{"You are 助手"}, toolCalls: []mockToolCall{{
			ID: "call_spawn_2", Name: "Spawn", Args: map[string]any{"item": "后台子任务", "wait": false},
		}}},
		step{match: []string{"You are 助手"}, text: "子实例后台答复"},
		step{match: []string{"You are 助手"}, text: "父实例已交付"},
	)

	result := e.runTurn(map[string]any{"input": "后台跑一个子实例"})

	// The parent must be told it has a handle, not a result.
	handleSeen := false
	for _, request := range e.Mock.requests() {
		text := requestText(request)
		if strings.Contains(strings.ToLower(text), "handle") || strings.Contains(text, "task") {
			handleSeen = true
		}
	}

	// A durable task has to exist for the spawned child.
	var tasks []map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tasks = taskRecords(t, e.Root)
		if len(tasks) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{"handle_seen": handleSeen, "tasks": len(tasks)})

	if !handleSeen {
		t.Fatalf("parent never received a handle from the asynchronous spawn; messages=%s", e.Mock.summary())
	}
	if len(tasks) == 0 {
		t.Fatalf("asynchronous spawn recorded no durable task; a handle without a task cannot be collected")
	}
}

// TestDefine1WritesAnAgentDefinition covers the definition write surface: the
// model can create an agent at runtime, the file lands in the config tree, and
// the next turn can use it. A Define that only reports success leaves the engine
// claiming a definition it does not have.
func TestDefine1WritesAnAgentDefinition(t *testing.T) {
	judge(t, "I-TOOL-1", "tool")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TOOL-1", extraTools: []string{"Define"}})
	e.Mock.script(
		step{match: []string{"You are 助手"}, toolCalls: []mockToolCall{{
			ID: "call_define_1", Name: "Define", Args: map[string]any{
				"action": "create_agent",
				"spec": map[string]any{
					"name":    "e2e-helper",
					"persona": map[string]any{"role": "助手", "goal": "协助"},
					"body":    "你是临时助手。",
				},
			},
		}}},
		textStep("定义已创建"),
	)

	result := e.runTurn(map[string]any{"input": "创建一个临时 agent"})
	events := waitForEvent(t, e.Root, result.Session.ID, 20*time.Second, turnTerminated)

	if len(toolEventsOf(events, "Define")) < 2 {
		t.Fatalf("Define did not run to completion; types=%v", typesOf(events))
	}

	definition := filepath.Join(e.Root, ".agents", "agents", "e2e-helper", "AGENT.md")
	data, err := os.ReadFile(definition)
	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{"definition_written": err == nil})
	if err != nil {
		t.Fatalf("Define reported success but wrote no definition at %s: %v", definition, err)
	}
	if !strings.Contains(string(data), "e2e-helper") {
		t.Fatalf("definition body does not name the agent: %s", truncate(string(data), 120))
	}
}

// TestTodo1WriteThenRead covers the todo tool pair: what TodoWrite records must
// come back from TodoRead. A write that returns success while storing nothing is
// indistinguishable from a working one until the model relies on it.
func TestTodo1WriteThenRead(t *testing.T) {
	judge(t, "I-TOOL-1", "tool")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TOOL-1", extraTools: []string{"TodoWrite", "TodoRead"}})
	e.Mock.script(
		step{match: []string{"You are 助手"}, toolCalls: []mockToolCall{{
			ID: "call_todo_1", Name: "TodoWrite", Args: map[string]any{
				"items": []any{"第一步", "第二步"},
			},
		}}},
		step{match: []string{"You are 助手"}, toolCalls: []mockToolCall{{
			ID: "call_todo_2", Name: "TodoRead", Args: map[string]any{},
		}}},
		textStep("清单读回来了"),
	)

	result := e.runTurn(map[string]any{"input": "建一个待办清单再读回来"})
	waitForEvent(t, e.Root, result.Session.ID, 20*time.Second, turnTerminated)

	requests := e.Mock.requests()
	if len(requests) < 3 {
		t.Fatalf("model called %d times; want write, read, answer", len(requests))
	}
	last := requestText(requests[len(requests)-1])
	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{"read_back": strings.Contains(last, "第一步")})
	if !strings.Contains(last, "第一步") {
		t.Fatalf("TodoRead did not return what TodoWrite recorded; messages=%s", e.Mock.summary())
	}
}

// TestHealth1ReportsReadiness covers the health endpoint as a contract: it is
// what an orchestrator polls, so the fields it promises must be present and
// parse as expected — the harness only uses it internally today.
func TestHealth1ReportsReadiness(t *testing.T) {
	judge(t, "I-AUTH-1", "startup")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-AUTH-1"})

	body, status, err := e.do(http.MethodGet, "/api/health", nil)
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("health: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}
	var health map[string]any
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("health is not JSON: %v body=%s", err, strings.TrimSpace(string(body)))
	}
	ready, ok := health["ready"].(bool)
	version, _ := health["version"].(string)
	commit, _ := health["commit"].(string)
	writeTrace(t, e, httpTrace{Method: http.MethodGet, Path: "/api/health", Status: status},
		"", map[string]any{"ready": ready, "version": version, "commit": commit})
	if !ok || !ready {
		t.Fatalf("health does not report readiness: %s", strings.TrimSpace(string(body)))
	}
	// A deployment that cannot name itself cannot be rolled back with confidence:
	// two restarts look identical, and a rollback that kept the new binary looks
	// like a rollback that worked.
	if strings.TrimSpace(version) != "e2e-build" || strings.TrimSpace(commit) != "e2e-commit" {
		t.Fatalf("health does not identify the build: version=%q commit=%q", version, commit)
	}

	// The static shell has to be served, otherwise the read-only viewer is a 404
	// for the person who needs it most.
	pageBody, pageStatus, err := e.do(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	if pageStatus != http.StatusOK || !strings.Contains(string(pageBody), "<html") {
		t.Fatalf("static page: status=%d body=%s", pageStatus, truncate(string(pageBody), 80))
	}
}
