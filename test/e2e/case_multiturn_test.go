//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// multiTurnScript builds one text step per turn.
func multiTurnScript(turns int) []step {
	steps := make([]step, 0, turns)
	for index := 1; index <= turns; index++ {
		steps = append(steps, textStep(fmt.Sprintf("答复%d", index)))
	}
	return steps
}

// runTurns drives N turns in one session and returns every result.
func runTurns(t *testing.T, e *env, questions ...string) []turnResult {
	t.Helper()
	results := make([]turnResult, 0, len(questions))
	sessionID := ""
	for index, question := range questions {
		payload := map[string]any{"input": question}
		if sessionID != "" {
			payload["flow_session_id"] = sessionID
		}
		result := e.runTurn(payload)
		if index == 0 {
			sessionID = result.Session.ID
		}
		if result.Session.ID != sessionID {
			t.Fatalf("turn %d moved to session %q; want %q", index+1, result.Session.ID, sessionID)
		}
		results = append(results, result)
	}
	return results
}

func replyOf(result turnResult) string {
	return strings.TrimSpace(firstNonEmpty(result.FinalReply, result.Reply))
}

// TestTurn1EachTurnKeepsItsOwnReply covers I-TURN-1 for a single session: N
// turns produce N answers, each one its own. A session that leaks the previous
// answer into the next reply looks fine in a single-turn test and is unusable
// in a conversation.
func TestTurn1EachTurnKeepsItsOwnReply(t *testing.T) {
	judge(t, "I-TURN-1", "loop")

	const turns = 4
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TURN-1"})
	e.Mock.script(multiTurnScript(turns)...)

	questions := make([]string, 0, turns)
	for index := 1; index <= turns; index++ {
		questions = append(questions, fmt.Sprintf("第%d轮问题", index))
	}
	results := runTurns(t, e, questions...)

	for index, result := range results {
		want := fmt.Sprintf("答复%d", index+1)
		if got := replyOf(result); got != want {
			t.Fatalf("turn %d reply=%q; want %q (replies must not bleed across turns)", index+1, got, want)
		}
	}

	sessionID := results[0].Session.ID
	events := readSession(t, e.Root, sessionID)
	if started := countEvents(events, "session.turn.started"); started != turns {
		t.Fatalf("session has %d session.turn.started events; want %d", started, turns)
	}
	terminal := 0
	for _, ev := range events {
		if (strings.HasPrefix(ev.Type, "session.turn.waiting") || ev.Type == "session.turn.idle") || ev.Type == "session.turn.completed" {
			terminal++
		}
	}
	if terminal != turns {
		t.Fatalf("session has %d terminal turn events; want %d (each turn must close)", terminal, turns)
	}
	if requests := e.Mock.callCount(); requests != turns {
		t.Fatalf("model called %d times for %d text-only turns; want %d", requests, turns, turns)
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		sessionID, map[string]any{"turns": turns, "events": len(events)})
}

// TestTurn2HistoryAccumulatesExactlyOnce covers I-TURN-2: every earlier user
// input is in the context once. Asking for the same question twice or dropping
// one are both invisible from the reply; only the request shows it.
func TestTurn2HistoryAccumulatesExactlyOnce(t *testing.T) {
	judge(t, "I-TURN-2", "loop")

	const turns = 3
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TURN-2"})
	e.Mock.script(multiTurnScript(turns)...)

	results := runTurns(t, e, "第1轮问题", "第2轮问题", "第3轮问题")

	requests := e.Mock.requests()
	if len(requests) != turns {
		t.Fatalf("model called %d times; want %d", len(requests), turns)
	}
	// The last request has seen every earlier turn.
	last := requestText(requests[len(requests)-1])
	for index := 1; index <= turns; index++ {
		question := fmt.Sprintf("第%d轮问题", index)
		if got := strings.Count(last, question); got != 1 {
			t.Fatalf("last request contains %q %d times; want exactly 1 (duplicate history inflates the prompt and can bias the model)", question, got)
		}
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		results[0].Session.ID, map[string]any{"turns": turns, "model_calls": len(requests)})
}

// TestTurn3ToolContextSurvivesLaterTurns covers I-TURN-3: what a tool returned
// in an earlier turn is still available later. This is the difference between a
// session and a sequence of unrelated requests — and the failure is silent,
// because the model simply answers from memory or guesses.
func TestTurn3ToolContextSurvivesLaterTurns(t *testing.T) {
	judge(t, "I-TURN-3", "loop")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TURN-3"})
	e.Mock.script(
		toolStep("Read", map[string]any{"file": "package.json"}),
		textStep("答复1"),
		textStep("答复2"),
	)

	first := e.runTurn(map[string]any{"input": "看看 package.json"})
	second := e.runTurn(map[string]any{"flow_session_id": first.Session.ID, "input": "刚才那个包叫什么"})

	requests := e.Mock.requests()
	if len(requests) < 3 {
		t.Fatalf("model called %d times; want 3 (tool, answer, follow-up)", len(requests))
	}
	followUp := requestText(requests[len(requests)-1])
	if !strings.Contains(followUp, "simple-qa-fixture") {
		t.Fatalf("the follow-up request no longer carries the tool output from turn 1; messages=%s", e.Mock.summary())
	}
	if reply := replyOf(second); reply == "" {
		t.Fatalf("follow-up turn produced no reply")
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		first.Session.ID, map[string]any{"model_calls": len(requests)})
}

// TestTurn4EarlierAnswerIsVisible covers I-CTX-1: a follow-up can only build on
// the previous answer if the previous answer is in its context. The engine has
// two legitimate channels for that — the assistant message in the transcript,
// or a declared record — so the case accepts either and only fails when neither
// is present.
func TestTurn4EarlierAnswerIsVisible(t *testing.T) {
	judge(t, "I-CTX-1", "loop")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-CTX-1"})
	e.Mock.script(textStep("答复1"), textStep("答复2"))

	first := e.runTurn(map[string]any{"input": "第1轮问题"})
	if replyOf(first) != "答复1" {
		t.Fatalf("first turn reply=%q; want 答复1", replyOf(first))
	}
	e.runTurn(map[string]any{"flow_session_id": first.Session.ID, "input": "第2轮问题"})

	requests := e.Mock.requests()
	if len(requests) < 2 {
		t.Fatalf("model called %d times; want 2", len(requests))
	}
	followUp := requestText(requests[len(requests)-1])
	visible := strings.Contains(followUp, "答复1")

	// Record the metric either way: the count is what makes the behaviour
	// visible over time, including when it changes.
	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		first.Session.ID, map[string]any{
			"answer_visible": visible,
			"channel":        "assistant message or declared record",
		})

	if !visible {
		t.Fatalf("follow-up request cannot see the previous answer (neither assistant message nor record); messages=%s", e.Mock.summary())
	}
}
