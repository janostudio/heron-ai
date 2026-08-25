package flow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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

func TestRuntimeRecoveryStatusFindsInterruptedMember(t *testing.T) {
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
		MemberID:      "test",
		MemberTurnID:  "mt-1",
		MemberType:    types.MemberCommand,
		Payload:       map[string]any{"input": "fix it"},
	}))

	status, err := runtime.RecoveryStatus(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, types.SessionInterrupted, status.Session.Status)
	require.Len(t, status.Interrupted, 1)
	require.Equal(t, "mt-1", status.Interrupted[0].MemberTurnID)
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
