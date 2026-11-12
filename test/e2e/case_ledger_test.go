//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestLedger1SummaryTurnSeesDispatchedTask covers I-LEDGER-1: a caller always
// sees the tasks it dispatched and has not closed the loop on.
//
// The failure this guards is silent and only shows up long after the fact. The
// conversation that dispatched a task lives in the caller's context; compaction
// eventually summarises it away. When the task reports back, the caller is left
// with an answer it cannot place — it does not know which job this is, whether
// siblings are still running, or whether it already summarised this one. The
// ledger is the part of the context that compaction protects, so the task has to
// be in it.
//
// The assertion is on the summary turn, not the dispatch turn: the dispatch turn
// has the task in its own conversation anyway, so seeing it there would prove
// nothing.
func TestLedger1SummaryTurnSeesDispatchedTask(t *testing.T) {
	judge(t, "I-LEDGER-1", "dispatch")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-LEDGER-1", fixture: "dispatch"})
	e.Mock.script(
		step{match: []string{"You are 接待"}, toolCalls: []mockToolCall{{
			ID: "call_w1", Name: "Team", Args: map[string]any{
				"subagent_type": "worker", "prompt": "查 heron-ai 的主分支并回报", "description": "查主分支",
			},
		}}},
		step{match: []string{"You are 执行者"}, text: "主分支是 master"},
		step{match: []string{"You are 接待"}, text: "结论：主分支是 master"},
	)

	result := e.runTurn(map[string]any{"input": "查一下 heron-ai 的主分支"})
	waitForEvent(t, e.Root, result.Session.ID, 25*time.Second, func(events []event) bool {
		return terminalCount(events) >= 1
	})
	time.Sleep(1200 * time.Millisecond)

	rounds := requestsOf(e.Mock.requests(), "You are 接待")
	if len(rounds) < 2 {
		t.Fatalf("frontdesk ran %d rounds; want a dispatch round and a summary round", len(rounds))
	}
	summary := requestText(rounds[len(rounds)-1])

	if !strings.Contains(summary, "[任务清单]") {
		t.Fatalf("summary turn has no task ledger; the caller cannot tell which task reported back:\n%s", summary)
	}
	if !strings.Contains(summary, "查 heron-ai 的主分支并回报") {
		t.Fatalf("ledger does not say what the task was; the task key alone is not enough after compaction:\n%s", summary)
	}
	if !strings.Contains(summary, "handle=") {
		t.Fatalf("ledger has no handle; the caller cannot read the full answer back:\n%s", summary)
	}

	writeTrace(t, e, httpTrace{Method: "POST", Path: "/api/run", Status: 200},
		result.Session.ID, map[string]any{"rounds": len(rounds), "ledger_present": true})
}
