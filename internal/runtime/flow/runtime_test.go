package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/agent"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

type fakeTeamRuntime struct{}

func (fakeTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	switch req.TeamTurn.TeamID {
	case "default":
		return types.TeamTurnResult{
			Turn:  req.TeamTurn,
			Reply: "starting diagnosis",
			Next: &types.Route{
				Action: types.NextActivate,
				Teams:  []string{"verify"},
			},
		}, nil
	case "verify":
		record := types.SharedRecord{
			RecordID: "verification-1",
			Kind:     "verification",
			Name:     "VerificationReport",
			Scope:    types.RecordScopeFlow,
			Summary:  "verification passed",
			Status:   types.RecordActive,
			Revision: 1,
		}
		return types.TeamTurnResult{
			Turn:    req.TeamTurn,
			Reply:   "verification passed",
			Records: []types.SharedRecord{record},
			Next:    &types.Route{Action: types.NextComplete},
		}, nil
	default:
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Next: &types.Route{Action: types.NextFail, Reason: "unexpected team"},
		}, nil
	}
}

func testDefinitions() *types.Definitions {
	return &types.Definitions{
		Flow: types.Flow{
			ID:          "code-fix",
			EntryTeamID: "default",
			Teams: map[string]types.FlowTeamBinding{
				"default": {
					ID:          "default",
					TeamID:      "default-team",
					Coordinator: true,
					CanActivate: []string{"verify"},
					Inputs: types.InputSpec{
						UserMessage: true,
					},
				},
				"verify": {
					ID:        "verify",
					TeamID:    "verify-team",
					DependsOn: []string{"default"},
				},
			},
		},
		Teams: map[string]types.Team{
			"default-team": {ID: "default-team"},
			"verify-team":  {ID: "verify-team"},
		},
	}
}

func TestRuntimeStartsActivatesAndCompletesFlow(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	evidenceStore := storage.NewJSONLEvidenceStore(fileStore)
	runtime := NewRuntime(testDefinitions(), fakeTeamRuntime{}, sessionWriter, evidenceStore, t.TempDir())

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "code-fix",
		Input:  "verify the change",
	})
	require.NoError(t, err)
	require.Equal(t, types.SessionCompleted, result.Session.Status)
	require.Equal(t, types.TurnCompleted, result.Turn.Status)
	require.Contains(t, result.Reply, "verification passed")
	require.Len(t, result.TeamResults, 2)
	require.Len(t, result.Records, 1)

	status, err := runtime.Status(context.Background(), result.Session.ID)
	require.NoError(t, err)
	require.Equal(t, types.SessionCompleted, status.Status)

	replay, err := sessionWriter.Replay(context.Background(), result.Session.ID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(replay.Events), 5)

	records, err := evidenceStore.List(context.Background(), result.Session.ID, types.RecordScopeFlow)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "VerificationReport", records[0].Name)
}

func TestRuntimeResumeRequiresWaitingInput(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := NewRuntime(testDefinitions(), fakeTeamRuntime{}, sessionWriter, nil, t.TempDir())

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{FlowID: "code-fix"})
	require.NoError(t, err)
	_, err = runtime.Resume(context.Background(), result.Session.ID, "continue")
	require.ErrorContains(t, err, "not waiting for input")
}

type aggregateResumeTeamRuntime struct {
	mu      sync.Mutex
	resumes int
	lastReq types.TeamTurnRequest
}

func (r *aggregateResumeTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	r.mu.Lock()
	r.lastReq = req
	if len(req.ResumeCalls) > 0 {
		r.resumes++
	}
	r.mu.Unlock()

	if len(req.ResumeCalls) == 0 {
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Next: &types.Route{Action: types.NextWaitTool},
			CallResults: map[string]types.CallResult{
				"agent-a": {
					Status:       types.TurnWaitingTool,
					CallTurnID:   "tt:a",
					AgentID:      "a",
					TaskID:       "task-a",
					CheckpointID: "checkpoint-a",
					Next:         &types.Route{Action: types.NextWaitTool},
				},
				"agent-b": {
					Status:       types.TurnWaitingTool,
					CallTurnID:   "tt:b",
					AgentID:      "b",
					TaskID:       "task-b",
					CheckpointID: "checkpoint-b",
					Next:         &types.Route{Action: types.NextWaitTool},
				},
			},
			PendingToolTasks: []types.PendingToolTask{
				{CallID: "agent-a", CallTurnID: "tt:a", AgentID: "a", TaskID: "task-a", CheckpointID: "checkpoint-a", Status: types.TurnWaitingTool},
				{CallID: "agent-b", CallTurnID: "tt:b", AgentID: "b", TaskID: "task-b", CheckpointID: "checkpoint-b", Status: types.TurnWaitingTool},
			},
		}, nil
	}
	return types.TeamTurnResult{
		Turn: req.TeamTurn,
		Next: &types.Route{Action: types.NextComplete},
		CallResults: map[string]types.CallResult{
			"agent-a": {Status: types.TurnCompleted, Reply: "a resumed"},
			"agent-b": {Status: types.TurnCompleted, Reply: "b resumed"},
		},
		Reply: "all agents resumed",
	}, nil
}

func aggregateResumeDefinitions() *types.Definitions {
	return &types.Definitions{
		Flow: types.Flow{
			ID:          "aggregate",
			EntryTeamID: "entry",
			Teams: map[string]types.FlowTeamBinding{
				"entry": {ID: "entry", TeamID: "entry-team", Coordinator: true},
			},
		},
		Teams: map[string]types.Team{
			"entry-team": {ID: "entry-team"},
		},
	}
}

func TestRuntimeResumeAggregatesMultipleToolTasks(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(files)
	tasks := agent.NewFileToolTaskStore(files)
	now := time.Now().UTC()
	for _, id := range []string{"task-a", "task-b"} {
		require.NoError(t, tasks.Save(context.Background(), types.ToolTask{
			ID: id, Status: types.ToolTaskCompleted, Progress: 1,
			UpdatedAt: now,
		}))
	}
	teamRuntime := &aggregateResumeTeamRuntime{}
	runtime := NewRuntime(aggregateResumeDefinitions(), teamRuntime, sessions, nil, t.TempDir())
	runtime.SetTaskStore(tasks)

	first, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "aggregate", Input: "run both agents",
	})
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingTool, first.Session.Status)
	require.Len(t, first.PendingToolTasks, 2)

	resumed, err := runtime.Resume(context.Background(), first.Session.ID, "")
	require.NoError(t, err)
	require.Equal(t, types.SessionCompleted, resumed.Session.Status)
	require.Equal(t, 1, teamRuntime.resumes)
	require.Len(t, teamRuntime.lastReq.ResumeCalls, 2)
	require.Contains(t, resumed.Reply, "all agents resumed")
}

func TestRuntimeResumeWaitsUntilAllToolTasksAreTerminal(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(files)
	tasks := agent.NewFileToolTaskStore(files)
	now := time.Now().UTC()
	require.NoError(t, tasks.Save(context.Background(), types.ToolTask{
		ID: "task-a", Status: types.ToolTaskCompleted, UpdatedAt: now,
	}))
	require.NoError(t, tasks.Save(context.Background(), types.ToolTask{
		ID: "task-b", Status: types.ToolTaskRunning, UpdatedAt: now,
	}))
	teamRuntime := &aggregateResumeTeamRuntime{}
	runtime := NewRuntime(aggregateResumeDefinitions(), teamRuntime, sessions, nil, t.TempDir())
	runtime.SetTaskStore(tasks)

	first, err := runtime.Start(context.Background(), types.StartFlowRequest{FlowID: "aggregate"})
	require.NoError(t, err)
	waiting, err := runtime.Resume(context.Background(), first.Session.ID, "")
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingTool, waiting.Session.Status)
	require.Len(t, waiting.PendingToolTasks, 2)
	assert.Equal(t, 0, teamRuntime.resumes)
}

type approvalTeamRuntime struct {
	resumed bool
}

func (r *approvalTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	if req.ResumeApproval != nil {
		r.resumed = true
		return types.TeamTurnResult{
			Turn:  req.TeamTurn,
			Reply: "approved tool completed",
			Next:  &types.Route{Action: types.NextComplete},
			CallResults: map[string]types.CallResult{
				"danger": {
					Status:   types.TurnCompleted,
					Reply:    "approved",
					Approval: req.ResumeApproval,
				},
			},
		}, nil
	}
	return types.TeamTurnResult{
		Turn: types.TeamTurn{
			ID:     req.TeamTurn.ID,
			TeamID: req.TeamTurn.TeamID,
			Status: types.TurnWaitingApproval,
		},
		Next: &types.Route{Action: types.NextWaitApproval},
		CallResults: map[string]types.CallResult{
			"danger": {
				Status:       types.TurnWaitingApproval,
				CallTurnID:   "tt:danger",
				CheckpointID: "cp-danger",
				PendingApproval: &types.AgentPendingApproval{
					RequestID:  "approval-1",
					CallID:     "danger",
					ToolCallID: "tool-1",
					ToolName:   "Bash",
					Reason:     "dangerous command",
				},
				Next: &types.Route{Action: types.NextWaitApproval},
			},
		},
		PendingApprovals: []types.AgentPendingApproval{{
			RequestID:  "approval-1",
			CallID:     "danger",
			ToolCallID: "tool-1",
			ToolName:   "Bash",
			Reason:     "dangerous command",
		}},
	}, nil
}

func TestRuntimeResumeApprovalResumesWaitingAgentCall(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(files)
	teamRuntime := &approvalTeamRuntime{}
	runtime := NewRuntime(aggregateResumeDefinitions(), teamRuntime, sessions, nil, t.TempDir())

	first, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "aggregate", Input: "run dangerous tool",
	})
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingApproval, first.Session.Status)
	require.Len(t, first.PendingApprovals, 1)

	resumed, err := runtime.ResumeApproval(
		context.Background(), first.Session.ID, "approval-1", true, "approved by tester",
	)
	require.NoError(t, err)
	require.Equal(t, types.SessionCompleted, resumed.Session.Status)
	require.True(t, teamRuntime.resumed)
	require.Contains(t, resumed.Reply, "approved tool completed")
}

func TestRuntimeResumeApprovalPreservesAuditingFields(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(files)
	teamRuntime := &approvalTeamRuntime{}
	runtime := NewRuntime(aggregateResumeDefinitions(), teamRuntime, sessions, nil, t.TempDir())

	first, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "aggregate", Input: "run dangerous tool",
	})
	require.NoError(t, err)
	decision := types.HITLResponse{
		RequestID:  "approval-1",
		Approved:   true,
		Reason:     "approved by operator",
		ApproverID: "operator-7",
		Approver:   "QA Operator",
		Channel:    "stream-json",
	}
	resumed, err := runtime.ResumeApprovalWithResponse(context.Background(), first.Session.ID, decision)
	require.NoError(t, err)
	require.Equal(t, types.SessionCompleted, resumed.Session.Status)

	require.NotEmpty(t, resumed.TeamResults)
	var approval *types.HITLResponse
	for _, teamResult := range resumed.TeamResults {
		if callResult, ok := teamResult.CallResults["danger"]; ok && callResult.Approval != nil {
			approval = callResult.Approval
			break
		}
	}
	require.NotNil(t, approval)
	require.Equal(t, "operator-7", approval.ApproverID)
	require.Equal(t, "stream-json", approval.Channel)
}

func TestRuntimeResumeApprovalRejectsUnknownApproval(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(files)
	runtime := NewRuntime(aggregateResumeDefinitions(), &approvalTeamRuntime{}, sessions, nil, t.TempDir())

	first, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "aggregate", Input: "run dangerous tool",
	})
	require.NoError(t, err)
	_, err = runtime.ResumeApproval(context.Background(), first.Session.ID, "unknown-approval", true, "")
	require.ErrorContains(t, err, "not pending")
}

func TestRuntimeRecoveryStatusFindsInterruptedCall(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := NewRuntime(testDefinitions(), fakeTeamRuntime{}, sessionWriter, nil, t.TempDir())

	sessionID := "fs-interrupted"
	now := time.Now().UTC()
	require.NoError(t, appendTestEvent(sessionWriter, sessionID, types.SessionEvent{
		Type:          types.EventFlowSessionCreated,
		FlowSessionID: sessionID,
		Payload:       map[string]any{"session": types.FlowSession{ID: sessionID, FlowID: "code-fix", Status: types.SessionRunning, CreatedAt: now, UpdatedAt: now}},
	}))
	require.NoError(t, appendTestEvent(sessionWriter, sessionID, types.SessionEvent{
		Type:          types.EventFlowTurnStarted,
		FlowSessionID: sessionID,
		FlowTurnID:    "ft-1",
		Payload:       map[string]any{"input": "fix it"},
	}))
	require.NoError(t, appendTestEvent(sessionWriter, sessionID, types.SessionEvent{
		Type:          types.EventTeamTurnStarted,
		FlowSessionID: sessionID,
		FlowTurnID:    "ft-1",
		TeamID:        "verify",
		TeamTurnID:    "tt-1",
		Payload:       map[string]any{"input": "fix it"},
	}))
	require.NoError(t, appendTestEvent(sessionWriter, sessionID, types.SessionEvent{
		Type:          types.EventCommandTurnStarted,
		FlowSessionID: sessionID,
		FlowTurnID:    "ft-1",
		TeamID:        "verify",
		TeamTurnID:    "tt-1",
		CallID:        "test",
		CallTurnID:    "mt-1",
		CallType:      types.CallCommand,
		Payload:       map[string]any{"input": "fix it"},
	}))

	status, err := runtime.RecoveryStatus(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, types.SessionInterrupted, status.Session.Status)
	require.Len(t, status.Interrupted, 1)
	require.Equal(t, "mt-1", status.Interrupted[0].CallTurnID)
}

func TestRuntimeBlocksNormalInputUntilInterruptedExecutionIsRecovered(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := NewRuntime(testDefinitions(), fakeTeamRuntime{}, sessionWriter, nil, t.TempDir())

	sessionID := "fs-interrupted-input"
	now := time.Now().UTC()
	require.NoError(t, appendTestEvent(sessionWriter, sessionID, types.SessionEvent{
		Type:          types.EventFlowSessionCreated,
		FlowSessionID: sessionID,
		Payload:       map[string]any{"session": types.FlowSession{ID: sessionID, FlowID: "code-fix", Status: types.SessionRunning, CreatedAt: now, UpdatedAt: now}},
	}))
	require.NoError(t, appendTestEvent(sessionWriter, sessionID, types.SessionEvent{
		Type:          types.EventFlowTurnStarted,
		FlowSessionID: sessionID,
		FlowTurnID:    "ft-input",
		Payload:       map[string]any{"input": "original input"},
	}))
	require.NoError(t, appendTestEvent(sessionWriter, sessionID, types.SessionEvent{
		Type:          types.EventTeamTurnStarted,
		FlowSessionID: sessionID,
		FlowTurnID:    "ft-input",
		TeamID:        "verify",
		TeamTurnID:    "tt-input",
		Payload:       map[string]any{"input": "original input"},
	}))

	_, err := runtime.HandleInput(context.Background(), sessionID, "new input")
	require.ErrorContains(t, err, "unfinished execution")
}

func TestRuntimeRecoveryRetryRequiresExplicitSideEffectPermission(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := NewRuntime(testDefinitions(), fakeTeamRuntime{}, sessionWriter, nil, t.TempDir())

	sessionID := "fs-retry-policy"
	now := time.Now().UTC()
	require.NoError(t, appendTestEvent(sessionWriter, sessionID, types.SessionEvent{
		Type:          types.EventFlowSessionCreated,
		FlowSessionID: sessionID,
		Payload:       map[string]any{"session": types.FlowSession{ID: sessionID, FlowID: "code-fix", Status: types.SessionRunning, CreatedAt: now, UpdatedAt: now}},
	}))
	require.NoError(t, appendTestEvent(sessionWriter, sessionID, types.SessionEvent{
		Type:          types.EventFlowTurnStarted,
		FlowSessionID: sessionID,
		FlowTurnID:    "ft-retry",
		Payload:       map[string]any{"input": "retry input"},
	}))
	require.NoError(t, appendTestEvent(sessionWriter, sessionID, types.SessionEvent{
		Type:          types.EventTeamTurnStarted,
		FlowSessionID: sessionID,
		FlowTurnID:    "ft-retry",
		TeamID:        "verify",
		TeamTurnID:    "tt-retry",
		Payload:       map[string]any{"input": "retry input"},
	}))

	_, err := runtime.Recover(context.Background(), sessionID, types.RecoveryRequest{
		Action:       types.RecoveryRetry,
		TargetTurnID: "tt-retry",
	})
	require.ErrorContains(t, err, "allow_side_effect_replay")
}

func TestRuntimeRecoveryRetryRunsContainingTeamAndMarksRecoveryComplete(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := NewRuntime(testDefinitions(), fakeTeamRuntime{}, sessionWriter, nil, t.TempDir())

	sessionID := "fs-retry"
	now := time.Now().UTC()
	require.NoError(t, appendTestEvent(sessionWriter, sessionID, types.SessionEvent{
		Type:          types.EventFlowSessionCreated,
		FlowSessionID: sessionID,
		Payload:       map[string]any{"session": types.FlowSession{ID: sessionID, FlowID: "code-fix", Status: types.SessionRunning, CreatedAt: now, UpdatedAt: now}},
	}))
	require.NoError(t, appendTestEvent(sessionWriter, sessionID, types.SessionEvent{
		Type:          types.EventFlowTurnStarted,
		FlowSessionID: sessionID,
		FlowTurnID:    "ft-retry",
		Payload:       map[string]any{"input": "retry input"},
	}))
	require.NoError(t, appendTestEvent(sessionWriter, sessionID, types.SessionEvent{
		Type:          types.EventTeamTurnStarted,
		FlowSessionID: sessionID,
		FlowTurnID:    "ft-retry",
		TeamID:        "verify",
		TeamTurnID:    "tt-retry",
		Payload:       map[string]any{"input": "retry input"},
	}))

	result, err := runtime.Recover(context.Background(), sessionID, types.RecoveryRequest{
		Action:                types.RecoveryRetry,
		TargetTurnID:          "tt-retry",
		AllowSideEffectReplay: true,
		Input:                 "retry verify",
	})
	require.NoError(t, err)
	require.Equal(t, types.SessionCompleted, result.Session.Status)
	require.Contains(t, result.Reply, "verification passed")

	status, err := runtime.RecoveryStatus(context.Background(), sessionID)
	require.NoError(t, err)
	require.Empty(t, status.Interrupted)
}

func appendTestEvent(writer storage.SessionWriter, sessionID string, event types.SessionEvent) error {
	_, err := writer.Append(context.Background(), sessionID, event)
	return err
}
