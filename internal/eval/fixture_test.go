package eval

import (
	"context"
	"testing"
	"time"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// The fixtures below are written through the same storage primitives the
// runtime writes through, so the jsonl layout, the Seq allocation across the
// three layer files and the payload encoding are the real ones rather than a
// test-only imitation of them.

const (
	fixtureSession = "fs_eval_fixture"
	fixtureFlow1   = "ft_fixture_1"
	fixtureFlow2   = "ft_fixture_2"
	fixtureTeam1   = "tt_fixture_1"
	fixtureTeam2   = "tt_fixture_2"
	fixtureTeam3   = "tt_fixture_3"
)

type fixtureBuilder struct {
	t        *testing.T
	store    storage.FileStore
	writer   *storage.JSONLSessionWriter
	evidence *storage.JSONLEvidenceStore
	clock    time.Time
}

func newFixture(t *testing.T) *fixtureBuilder {
	store := storage.NewFileStore(t.TempDir())
	return &fixtureBuilder{
		t:        t,
		store:    store,
		writer:   storage.NewJSONLSessionWriter(store),
		evidence: storage.NewJSONLEvidenceStore(store),
		clock:    time.Date(2026, 9, 16, 8, 0, 0, 0, time.UTC),
	}
}

// observe runs the Observer over everything written so far.
func (f *fixtureBuilder) observe() *SessionFacts {
	f.t.Helper()
	facts, err := NewObserver(NewFileFactSource(f.store)).Observe(context.Background(), fixtureSession)
	if err != nil {
		f.t.Fatalf("observe: %v", err)
	}
	return facts
}

func (f *fixtureBuilder) tick() time.Time {
	f.clock = f.clock.Add(time.Second)
	return f.clock
}

func (f *fixtureBuilder) append(layer storage.EventLayer, header types.EventHeader, payload map[string]any) {
	f.t.Helper()
	header.CreatedAt = f.tick()
	event := storage.SessionEvent{EventHeader: header, Payload: payload}
	if _, err := f.writer.Append(context.Background(), fixtureSession, layer, event); err != nil {
		f.t.Fatalf("append %s: %v", event.Type, err)
	}
}

// build writes a two-FlowTurn session:
//
//	ft_1 "fix the bug"
//	  tt_1 (team research): agent call searcher (tools, workspace writes,
//	       one failed tool call) + command call build (bash, exit 1)
//	  tt_2 (team default):  agent call answer
//	ft_2 "thanks"
//	  tt_3 (team default):  agent call answer + agent call reviewer that
//	       started but never completed
func (f *fixtureBuilder) build() *fixtureBuilder {
	session := map[string]any{"id": fixtureSession, "flow_id": "eval_fixture", "status": types.SessionCreated}

	f.append(storage.LayerFlow, types.EventHeader{Type: types.EventFlowSessionCreated, FlowSessionID: fixtureSession},
		map[string]any{"session": session})
	f.append(storage.LayerFlow, types.EventHeader{Type: types.EventFlowSessionUpdated, FlowSessionID: fixtureSession, FlowTurnID: fixtureFlow1, Attempt: 1},
		map[string]any{"session": map[string]any{"id": fixtureSession, "flow_id": "eval_fixture", "status": types.SessionRunning}})
	f.append(storage.LayerFlow, types.EventHeader{Type: types.EventFlowTurnStarted, FlowSessionID: fixtureSession, FlowTurnID: fixtureFlow1, Attempt: 1},
		map[string]any{
			"input": "fix the bug",
			"turn": types.FlowTurn{
				ID: fixtureFlow1, FlowSessionID: fixtureSession, Attempt: 1,
				Input: "fix the bug", Status: types.TurnRunning, StartedAt: f.clock,
			},
		})

	f.teamTurn(fixtureFlow1, fixtureTeam1, "research", "fs_eval_fixture:research", []string{"searcher", "build"})
	f.teamTurn(fixtureFlow1, fixtureTeam2, "default", "fs_eval_fixture:default", []string{"answer"})

	finished := f.clock
	f.append(storage.LayerFlow, types.EventHeader{Type: types.EventFlowTurnCompleted, FlowSessionID: fixtureSession, FlowTurnID: fixtureFlow1, Attempt: 1},
		map[string]any{
			"session": map[string]any{"id": fixtureSession, "flow_id": "eval_fixture", "status": types.SessionWaitingInput},
			"turn": types.FlowTurn{
				ID: fixtureFlow1, FlowSessionID: fixtureSession, Attempt: 1,
				Input: "fix the bug", Status: types.TurnCompleted,
				Next:      &types.Route{Action: types.NextProceed},
				RecordIDs: []string{"research-findings"},
				StartedAt: f.clock.Add(-time.Minute), FinishedAt: &finished,
			},
		})

	f.append(storage.LayerFlow, types.EventHeader{Type: types.EventFlowTurnStarted, FlowSessionID: fixtureSession, FlowTurnID: fixtureFlow2, Attempt: 1},
		map[string]any{
			"input": "thanks",
			"turn": types.FlowTurn{
				ID: fixtureFlow2, FlowSessionID: fixtureSession, Attempt: 1,
				Input: "thanks", Status: types.TurnRunning, StartedAt: f.clock,
			},
		})
	f.teamTurn(fixtureFlow2, fixtureTeam3, "default", "fs_eval_fixture:default", []string{"answer", "reviewer"})

	finished = f.clock
	f.append(storage.LayerFlow, types.EventHeader{Type: types.EventFlowTurnWaitingInput, FlowSessionID: fixtureSession, FlowTurnID: fixtureFlow2, Attempt: 1},
		map[string]any{
			"session": map[string]any{"id": fixtureSession, "flow_id": "eval_fixture", "status": types.SessionWaitingInput},
			"turn": types.FlowTurn{
				ID: fixtureFlow2, FlowSessionID: fixtureSession, Attempt: 1,
				Input: "thanks", Status: types.TurnWaitingInput,
				Next:      &types.Route{Action: types.NextProceed},
				StartedAt: f.clock.Add(-time.Minute), FinishedAt: &finished,
			},
		})
	return f
}

// teamTurn emits one TeamTurn and its calls. The reviewer call is left
// started-but-unfinished on purpose so the interrupted case is covered.
func (f *fixtureBuilder) teamTurn(flowTurnID, teamTurnID, teamID, teamSessionID string, calls []string) {
	f.append(storage.LayerFlow, types.EventHeader{
		Type: types.EventTeamSessionCreated, FlowSessionID: fixtureSession, FlowTurnID: flowTurnID,
		TeamSessionID: teamSessionID, TeamTurnID: teamTurnID, TeamID: teamID, Attempt: 1,
	}, map[string]any{"team_session": types.TeamSession{
		ID: teamSessionID, FlowSessionID: fixtureSession, TeamID: teamID,
		Status: types.SessionRunning, CreatedAt: f.clock,
	}})

	f.append(storage.LayerFlow, types.EventHeader{
		Type: types.EventTeamTurnStarted, FlowSessionID: fixtureSession, FlowTurnID: flowTurnID,
		TeamSessionID: teamSessionID, TeamTurnID: teamTurnID, TeamID: teamID, Attempt: 1,
	}, map[string]any{
		"input": "fix the bug",
		"team_turn": types.TeamTurn{
			ID: teamTurnID, FlowTurnID: flowTurnID, TeamSessionID: teamSessionID, TeamID: teamID,
			Attempt: 1, Status: types.TurnRunning, StartedAt: f.clock,
		},
	})

	results := make(map[string]types.CallResult)
	for _, callID := range calls {
		results[callID] = f.call(flowTurnID, teamTurnID, callID, teamID)
	}

	finished := f.clock
	f.append(storage.LayerFlow, types.EventHeader{
		Type: types.EventTeamTurnCompleted, FlowSessionID: fixtureSession, FlowTurnID: flowTurnID,
		TeamSessionID: teamSessionID, TeamTurnID: teamTurnID, TeamID: teamID, Attempt: 1,
	}, map[string]any{"team_result": types.TeamTurnResult{
		Turn: types.TeamTurn{
			ID: teamTurnID, FlowTurnID: flowTurnID, TeamSessionID: teamSessionID, TeamID: teamID,
			Attempt: 1, Status: types.TurnCompleted, Next: &types.Route{Action: types.NextProceed},
			StartedAt: f.clock.Add(-time.Minute), FinishedAt: &finished,
		},
		CallResults: results,
		Next:        &types.Route{Action: types.NextProceed},
	}})

	f.append(storage.LayerFlow, types.EventHeader{
		Type: types.EventTeamSessionUpdated, FlowSessionID: fixtureSession, FlowTurnID: flowTurnID,
		TeamSessionID: teamSessionID, TeamTurnID: teamTurnID, TeamID: teamID, Attempt: 1,
	}, map[string]any{"team_session": types.TeamSession{
		ID: teamSessionID, FlowSessionID: fixtureSession, TeamID: teamID,
		Status: types.SessionCompleted, CreatedAt: f.clock.Add(-time.Minute), UpdatedAt: f.clock,
	}})
}

func (f *fixtureBuilder) call(flowTurnID, teamTurnID, callID, teamID string) types.CallResult {
	callTurnID := teamTurnID + ":" + callID
	// Command and webhook calls have no Agent: only the agent call type
	// resolves to an agent id.
	agentID := teamID + "-" + callID
	callType := types.CallAgent
	layer := storage.LayerTeam
	startEvent := types.EventAgentTurnStarted
	doneEvent := types.EventAgentTurnCompleted
	if callID == "build" {
		callType = types.CallCommand
		agentID = ""
		startEvent = types.EventCommandTurnStarted
		doneEvent = types.EventCommandTurnCompleted
	}
	header := types.EventHeader{
		FlowSessionID: fixtureSession, FlowTurnID: flowTurnID, TeamID: teamID, TeamTurnID: teamTurnID,
		CallID: callID, CallTurnID: callTurnID, CallType: callType, Attempt: 1,
	}
	startHeader := header
	startHeader.Type = startEvent

	if callType == types.CallAgent {
		f.append(layer, types.EventHeader{
			Type: types.EventAgentSessionCreated, FlowSessionID: fixtureSession, FlowTurnID: flowTurnID,
			TeamID:        teamID,
			TeamSessionID: "fs_eval_fixture:" + teamID, TeamTurnID: teamTurnID,
			CallID: callID, CallTurnID: callTurnID, CallType: callType, Attempt: 1,
		}, map[string]any{"agent_session": types.AgentSession{
			ID: "fs_eval_fixture:" + teamID + ":" + callID, TeamSessionID: "fs_eval_fixture:" + teamID,
			CallID: callID, AgentID: agentID, Status: types.SessionRunning, CreatedAt: f.clock,
		}})
	}

	f.append(layer, startHeader, map[string]any{
		"input": "fix the bug",
		"call": types.Call{
			ID: callID, Type: callType, AgentID: agentID,
			Responsibility: "do the work",
		},
	})

	if callID == "reviewer" {
		// Started but never completed: an interrupted call.
		return types.CallResult{Status: types.TurnRunning, CallTurnID: callTurnID}
	}

	switch callID {
	case "searcher":
		f.append(storage.LayerAgent, types.EventHeader{
			Type: types.EventAgentModelResponse, FlowSessionID: fixtureSession, TeamID: teamID,
			TeamTurnID: teamTurnID, CallID: callID, CallTurnID: callTurnID, CallType: callType, Attempt: 1,
		}, map[string]any{"round": 0, "model": "test-model", "finish_reason": "tool_calls"})
		for _, done := range []bool{true, false} {
			f.append(storage.LayerAgent, types.EventHeader{
				Type: types.EventToolCallStarted, FlowSessionID: fixtureSession, TeamID: teamID,
				TeamTurnID: teamTurnID, CallID: callID, CallTurnID: callTurnID, CallType: callType, Attempt: 1,
			}, map[string]any{"round": 1, "tool_name": "Read", "arguments": map[string]any{"path": "a.go"}})
			eventType := types.EventToolCallCompleted
			if !done {
				eventType = types.EventToolCallFailed
			}
			f.append(storage.LayerAgent, types.EventHeader{
				Type: eventType, FlowSessionID: fixtureSession, TeamID: teamID,
				TeamTurnID: teamTurnID, CallID: callID, CallTurnID: callTurnID, CallType: callType, Attempt: 1,
			}, map[string]any{"round": 1, "tool_name": "Read"})
		}
	case "build":
		f.append(storage.LayerAgent, types.EventHeader{
			Type: types.EventToolCallStarted, FlowSessionID: fixtureSession, TeamID: teamID,
			TeamTurnID: teamTurnID, CallID: callID, CallTurnID: callTurnID, CallType: callType, Attempt: 1,
		}, map[string]any{"round": 0, "tool_name": "Bash", "arguments": map[string]any{"command": "make"}})
	}

	result := callResult(callTurnID, agentID, callID)
	doneHeader := header
	doneHeader.Type = doneEvent
	f.append(layer, doneHeader, map[string]any{"call_result": result})
	return result
}

func callResult(callTurnID, agentID, callID string) types.CallResult {
	result := types.CallResult{
		Status:     types.TurnCompleted,
		CallTurnID: callTurnID,
		AgentID:    agentID,
		Next:       &types.Route{Action: types.NextProceed},
	}
	switch callID {
	case "searcher":
		result.Requests = []types.ModelRequestStats{
			{Round: 0, MessageCount: 2, Usage: types.TokenUsage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110}},
			{Round: 1, MessageCount: 6, Usage: types.TokenUsage{PromptTokens: 200, CompletionTokens: 20, TotalTokens: 220}},
		}
		result.Usage = types.TokenUsage{PromptTokens: 300, CompletionTokens: 30, TotalTokens: 330}
		result.ToolCalls = 3
		result.WorkspaceOps = []types.WorkspaceOperation{
			{Kind: "read", Path: "a.go"},
			{Kind: "write", Path: "b.go"},
			{Kind: "test", Command: "go test ./...", ExitCode: 0},
		}
		result.Records = []types.SharedRecord{{
			RecordID: "searcher-evidence", Kind: "agent_result", Name: "SearcherEvidence",
			Scope: types.RecordScopeTeam, Status: types.RecordActive, Revision: 1,
			Producer: types.ProducerRef{CallID: "searcher", CallTurnID: callTurnID},
		}}
	case "build":
		result.WorkspaceOps = []types.WorkspaceOperation{
			{Kind: "bash", Command: "make", ExitCode: 1},
		}
	case "answer":
		result.Requests = []types.ModelRequestStats{
			{MessageCount: 2, Usage: types.TokenUsage{PromptTokens: 60, CompletionTokens: 6, TotalTokens: 66}},
		}
		result.Usage = types.TokenUsage{PromptTokens: 60, CompletionTokens: 6, TotalTokens: 66}
	}
	return result
}

// publish writes a Flow-scope SharedRecord to both the event stream and
// evidence.jsonl, which is what the runtime does for promoted records.
func (f *fixtureBuilder) publish(record types.SharedRecord) *fixtureBuilder {
	f.t.Helper()
	f.append(storage.LayerFlow, types.EventHeader{
		Type: types.EventSharedRecordPublished, FlowSessionID: fixtureSession,
		FlowTurnID: record.Producer.FlowTurnID, TeamSessionID: record.Producer.TeamID,
		TeamTurnID: record.Producer.TeamTurnID, TeamID: record.Producer.TeamID,
		CallID: record.Producer.CallID, CallTurnID: record.Producer.CallTurnID,
	}, map[string]any{"record": record})
	if err := f.evidence.Publish(context.Background(), fixtureSession, record); err != nil {
		f.t.Fatalf("publish evidence: %v", err)
	}
	return f
}
