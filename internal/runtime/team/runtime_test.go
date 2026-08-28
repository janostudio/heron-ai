package team

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/runtime/call"
	"github.com/heron-ai/heron-engine/pkg/types"
)

type fakeCallExecutor struct {
	callType types.CallType
	mu       sync.Mutex
	started  map[string]time.Time
	records  map[string][]types.SharedRecord
}

func newFakeCallExecutor(callType types.CallType) *fakeCallExecutor {
	return &fakeCallExecutor{
		callType: callType,
		started:  make(map[string]time.Time),
		records:  make(map[string][]types.SharedRecord),
	}
}

func (e *fakeCallExecutor) Type() types.CallType {
	return e.callType
}

func (e *fakeCallExecutor) Execute(_ context.Context, req types.CallRequest) (types.CallResult, error) {
	e.mu.Lock()
	e.started[req.Call.ID] = time.Now()
	e.mu.Unlock()

	if req.Call.ID == "synthesize" {
		if len(req.Records) != 2 {
			return types.CallResult{
				Status: types.TurnFailed,
				Error:  fmt.Sprintf("expected 2 input records, got %d", len(req.Records)),
			}, nil
		}
	}

	recordName := req.Call.Output.Record
	var records []types.SharedRecord
	if recordName != "" {
		record := types.SharedRecord{
			RecordID: fmt.Sprintf("record-%s", req.Call.ID),
			Kind:     "test",
			Name:     recordName,
			Scope:    types.RecordScopeTeam,
			Summary:  req.Call.ID + " completed",
			Status:   types.RecordActive,
			Revision: 1,
		}
		records = []types.SharedRecord{record}
		e.mu.Lock()
		e.records[req.Call.ID] = records
		e.mu.Unlock()
	}

	return types.CallResult{
		Status:  types.TurnCompleted,
		Reply:   req.Call.ID + " completed",
		Records: records,
		Next:    &types.Route{Action: types.NextProceed},
	}, nil
}

func TestRuntimeRunsIndependentCallsInParallelAndUsesDependencies(t *testing.T) {
	executor := newFakeCallExecutor(types.CallCommand)
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(executor))
	runtime := NewRuntime(registry)

	req := types.TeamTurnRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamSession: types.TeamSession{ID: "ts-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "review"},
		Team: types.Team{
			ID: "review",
			Calls: map[string]types.Call{
				"security": {
					ID:   "security",
					Type: types.CallCommand,
					Command: &types.CommandSpec{
						Command: "security-check",
					},
					Output: types.OutputSpec{
						Record: "SecurityReview",
					},
				},
				"performance": {
					ID:   "performance",
					Type: types.CallCommand,
					Command: &types.CommandSpec{
						Command: "performance-check",
					},
					Output: types.OutputSpec{
						Record: "PerformanceReview",
					},
				},
				"synthesize": {
					ID:   "synthesize",
					Type: types.CallCommand,
					Command: &types.CommandSpec{
						Command: "synthesize-review",
					},
					DependsOn: []string{"security", "performance"},
					Inputs: types.InputSpec{
						Records: []types.InputBinding{
							{From: "security", Record: "SecurityReview"},
							{From: "performance", Record: "PerformanceReview"},
						},
					},
					Output: types.OutputSpec{
						Record: "ReviewReport",
					},
				},
			},
			Output: types.OutputSpec{
				From:   "synthesize",
				Record: "ReviewReport",
			},
		},
	}

	result, err := runtime.Run(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, types.NextProceed, result.Next.Action)
	require.Equal(t, []string{"record-synthesize"}, result.Turn.RecordIDs)
	require.Contains(t, result.Reply, "synthesize completed")
	require.Len(t, result.CallResults, 3)
	require.Contains(t, executor.records["security"][0].Name, "SecurityReview")
	require.Contains(t, executor.records["performance"][0].Name, "PerformanceReview")

	executor.mu.Lock()
	securityStarted := executor.started["security"]
	performanceStarted := executor.started["performance"]
	synthesizeStarted := executor.started["synthesize"]
	executor.mu.Unlock()
	require.False(t, securityStarted.IsZero())
	require.False(t, performanceStarted.IsZero())
	require.False(t, synthesizeStarted.IsZero())
	require.True(t, synthesizeStarted.After(securityStarted))
	require.True(t, synthesizeStarted.After(performanceStarted))
}

func TestBuildCallInputKeepsRawUserInputAndDoesNotDuplicatePromptSections(t *testing.T) {
	configured := types.Call{
		ID: "answer",
		Inputs: types.InputSpec{
			UserMessage: true,
			Records:     []types.InputBinding{{Record: "Report"}},
		},
	}
	input := buildCallInput(
		"Please inspect the project.",
		[]types.SharedRecord{{
			Name:    "Report",
			Kind:    "test",
			Summary: "fixture is ready",
		}},
		nil,
		configured,
	)
	require.Equal(t, "Please inspect the project.", input)
	require.NotContains(t, input, "## Input")
	require.NotContains(t, input, "## Shared Records")
}

func TestBuildCallInputOmitsUserInputWhenCallDoesNotRequestIt(t *testing.T) {
	input := buildCallInput(
		"do not leak this",
		nil,
		nil,
		types.Call{ID: "summarize", Inputs: types.InputSpec{
			Records: []types.InputBinding{{Record: "Report"}},
		}},
	)
	require.Empty(t, input)
}

func TestRuntimeReturnsCoordinateWhenCallFails(t *testing.T) {
	executor := &failingCallExecutor{}
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(executor))
	runtime := NewRuntime(registry)

	result, err := runtime.Run(context.Background(), types.TeamTurnRequest{
		Team: types.Team{
			ID: "verify",
			Calls: map[string]types.Call{
				"test": {
					ID:   "test",
					Type: types.CallCommand,
					Command: &types.CommandSpec{
						Command: "test-command",
					},
				},
			},
		},
	})
	require.Error(t, err)
	require.Equal(t, types.NextCoordinate, result.Next.Action)
	require.Contains(t, result.Error, "test failed")
}

func TestRuntimeLimitsCallsPerTeamTurnNotAcrossFlow(t *testing.T) {
	executor := newFakeCallExecutor(types.CallCommand)
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(executor))
	runtime := NewRuntime(registry)

	result, err := runtime.Run(context.Background(), types.TeamTurnRequest{
		Team: types.Team{
			ID: "diagnose",
			Calls: map[string]types.Call{
				"snapshot": {
					ID:   "snapshot",
					Type: types.CallCommand,
					Command: &types.CommandSpec{
						Command: "snapshot",
					},
				},
				"explore": {
					ID:   "explore",
					Type: types.CallCommand,
					Command: &types.CommandSpec{
						Command: "explore",
					},
				},
				"inspect": {
					ID:        "inspect",
					Type:      types.CallCommand,
					DependsOn: []string{"snapshot", "explore"},
					Command: &types.CommandSpec{
						Command: "inspect",
					},
				},
			},
		},
		Limits: types.RuntimeLimits{
			MaxCallsPerTeamTurn: 2,
			MaxParallelCalls:    2,
		},
	})
	require.ErrorContains(t, err, "team turn exceeded max calls: 2")
	require.Equal(t, types.NextCoordinate, result.Next.Action)
}

type waitingCallExecutor struct {
	mu       sync.Mutex
	executed []string
}

func (e *waitingCallExecutor) Type() types.CallType {
	return types.CallAgent
}

func (e *waitingCallExecutor) Execute(_ context.Context, req types.CallRequest) (types.CallResult, error) {
	e.mu.Lock()
	e.executed = append(e.executed, req.Call.ID)
	e.mu.Unlock()
	if req.ResumeTaskID != "" {
		return types.CallResult{
			Status:     types.TurnCompleted,
			CallTurnID: req.CallTurnID,
			AgentID:    req.Call.AgentID,
			Reply:      req.Call.ID + " resumed",
			Next:       &types.Route{Action: types.NextProceed},
		}, nil
	}
	return types.CallResult{
		Status:       types.TurnWaitingTool,
		CallTurnID:   req.CallTurnID,
		AgentID:      req.Call.AgentID,
		TaskID:       "task-" + req.Call.ID,
		CheckpointID: "checkpoint-" + req.Call.ID,
		Checkpoint: &types.AgentCheckpoint{
			ID:          "checkpoint-" + req.Call.ID,
			AgentID:     req.Call.AgentID,
			AgentTurnID: req.AgentTurnID,
			CallID:      req.Call.ID,
		},
		Next: &types.Route{Action: types.NextWaitTool},
	}, nil
}

func TestRuntimeAggregatesParallelWaitingToolCalls(t *testing.T) {
	executor := &waitingCallExecutor{}
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(executor))
	runtime := NewRuntime(registry, map[string]types.AgentConfig{
		"agent-a": {},
		"agent-b": {},
	})

	result, err := runtime.Run(context.Background(), types.TeamTurnRequest{
		TeamTurn: types.TeamTurn{ID: "tt-wait", TeamID: "parallel"},
		Team: types.Team{
			ID: "parallel",
			Calls: map[string]types.Call{
				"a": {ID: "a", Type: types.CallAgent, AgentID: "agent-a"},
				"b": {ID: "b", Type: types.CallAgent, AgentID: "agent-b"},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, types.TurnWaitingTool, result.Turn.Status)
	require.Equal(t, types.NextWaitTool, result.Next.Action)
	require.Len(t, result.PendingToolTasks, 2)
	require.Len(t, result.CallResults, 2)
	assert.Contains(t, result.PendingToolTasks, types.PendingToolTask{
		CallID: "a", CallTurnID: "tt-wait:a", AgentID: "agent-a",
		AgentTurnID: "tt-wait:a", TaskID: "task-a", CheckpointID: "checkpoint-a",
		Status: types.TurnWaitingTool,
	})
}

func TestRuntimeAggregateResumeSkipsCompletedSiblingCall(t *testing.T) {
	executor := &waitingCallExecutor{}
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(executor))
	runtime := NewRuntime(registry, map[string]types.AgentConfig{
		"agent-a": {},
		"agent-b": {},
	})

	result, err := runtime.Run(context.Background(), types.TeamTurnRequest{
		TeamTurn: types.TeamTurn{ID: "tt-resume", TeamID: "parallel"},
		Team: types.Team{
			ID: "parallel",
			Calls: map[string]types.Call{
				"a": {ID: "a", Type: types.CallAgent, AgentID: "agent-a"},
				"b": {ID: "b", Type: types.CallAgent, AgentID: "agent-b"},
			},
		},
		ResumeResults: map[string]types.CallResult{
			"a": {
				Status:     types.TurnCompleted,
				CallTurnID: "tt-resume:a",
				AgentID:    "agent-a",
				Reply:      "a already completed",
				Next:       &types.Route{Action: types.NextProceed},
			},
		},
		ResumeCalls: map[string]types.TeamCallResume{
			"b": {
				CallTurnID:   "tt-resume:b",
				CheckpointID: "checkpoint-b",
				TaskID:       "task-b",
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, result.Turn.Status)
	require.Len(t, result.CallResults, 2)
	assert.Equal(t, "a already completed", result.CallResults["a"].Reply)
	assert.Equal(t, "b resumed", result.CallResults["b"].Reply)
	executor.mu.Lock()
	defer executor.mu.Unlock()
	assert.Equal(t, []string{"b"}, executor.executed)
}

type failingCallExecutor struct{}

func (e *failingCallExecutor) Type() types.CallType {
	return types.CallCommand
}

func (e *failingCallExecutor) Execute(_ context.Context, req types.CallRequest) (types.CallResult, error) {
	return types.CallResult{
		Status: types.TurnFailed,
		Error:  req.Call.ID + " failed",
	}, nil
}
