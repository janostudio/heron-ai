//go:build e2e

package e2e

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRules1ConditionalRuleInjectedOnMatchingPath covers I-RULES-1 from the
// outside: a rule declaring paths is not in the system prompt, and it enters the
// turn when a tool touches a matching path — recorded as one event naming the
// rule and the path, with the body kept out of the conversation that the next
// turn continues from.
//
// The case runs the real engine, the real tool loop, and the real session files;
// only the model is scripted. A unit test can prove the matcher; only this can
// prove the wiring from config to event.
func TestRules1ConditionalRuleInjectedOnMatchingPath(t *testing.T) {
	judge(t, "I-RULES-1", "rules")

	e := newEnv(t, envOptions{
		caseName: t.Name(),
		intent:   "I-RULES-1",
		fixture:  "rules-conditional",
	})
	e.Mock.script(
		toolStep("Write", map[string]any{"file": "docs/design/note.md", "content": "# note\n"}),
		textStep("写好了"),
	)

	result := e.runTurn(map[string]any{"input": "写一份设计文档"})
	if result.Session.ID == "" {
		t.Fatalf("turn produced no session: %+v", result)
	}

	dir := filepath.Join(e.Root, ".agents", "data", "sessions", result.Session.ID)
	events := readEventsIn(t, dir)

	injected := 0
	for _, event := range events {
		if event.Type != "session.rule.injected" {
			continue
		}
		injected++
		if got, _ := event.Raw["payload"].(map[string]any)["rule_id"].(string); got != "directory-contract" {
			t.Fatalf("rule_id = %q, want directory-contract (event: %s)", got, event.Text)
		}
		if got, _ := event.Raw["payload"].(map[string]any)["trigger_path"].(string); got != "docs/design/note.md" {
			t.Fatalf("trigger_path = %q, want docs/design/note.md", got)
		}
	}
	if injected == 0 {
		t.Fatalf("no session.rule.injected event: the rule neither entered the turn nor said why (dir=%s)", dir)
	}

	// The body is not conversation: replaying it into the next turn would apply a
	// directory contract long after the work left that directory.
	for _, event := range events {
		if event.Type != "session.turn.started" && event.Type != "session.agent.model_response" {
			continue
		}
		if strings.Contains(event.Text, "目录契约（e2e 夹具）") {
			t.Fatalf("rule body was persisted into the conversation: %s", event.Text)
		}
	}

	// A second turn that touches nothing under docs/ must not inject it again.
	e.Mock.script(textStep("这次不写文件"))
	e.runTurn(map[string]any{"input": "解释一下这个项目"})
	after := readEventsIn(t, dir)
	if countType(after, "session.rule.injected") != injected {
		t.Fatalf("a turn that touched no docs path injected the rule again: before=%d after=%d",
			injected, countType(after, "session.rule.injected"))
	}

	writeTrace(t, e, httpTrace{Method: "POST", Path: "/api/run", Status: 200}, result.Session.ID,
		map[string]any{"rule_injected_events": injected})
}

func countType(events []event, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}
