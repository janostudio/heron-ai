package flow

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

type failureAggregationTeamRuntime struct {
	mu                sync.Mutex
	defaultRuns       int
	failedWorkerRuns  int
	blockedWorkerRuns int
	coordinatorInput  []types.SharedRecord
}

func (r *failureAggregationTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch req.TeamTurn.TeamID {
	case "default":
		r.defaultRuns++
		if r.defaultRuns == 1 {
			return types.TeamTurnResult{
				Turn: req.TeamTurn,
				Next: &types.Route{
					Action: types.NextActivate,
					Teams:  []string{"successful", "failed", "blocked"},
				},
			}, nil
		}
		r.coordinatorInput = append([]types.SharedRecord(nil), req.Records...)
		return types.TeamTurnResult{
			Turn:  req.TeamTurn,
			Reply: "coordinator summarized sibling results",
			Next:  &types.Route{Action: types.NextProceed},
		}, nil
	case "successful":
		return types.TeamTurnResult{
			Turn: types.TeamTurn{Status: types.TurnCompleted},
			Records: []types.SharedRecord{{
				RecordID: "successful-1",
				Kind:     "worker_result",
				Name:     "SuccessfulWorkerReport",
				Scope:    types.RecordScopeFlow,
				Summary:  "successful worker completed",
				Status:   types.RecordActive,
				Revision: 1,
			}},
			Next: &types.Route{Action: types.NextProceed},
		}, nil
	case "failed":
		r.failedWorkerRuns++
		return types.TeamTurnResult{
			Turn:  types.TeamTurn{Status: types.TurnFailed},
			Error: "agent model failed after model retries",
			CallResults: map[string]types.CallResult{
				"agent": {
					Status: types.TurnFailed,
					Error:  "llm chat: provider unavailable",
				},
			},
			Next: &types.Route{
				Action: types.NextCoordinate,
				Reason: "agent model failed after model retries",
			},
		}, nil
	case "blocked":
		r.blockedWorkerRuns++
		return types.TeamTurnResult{
			Turn:  types.TeamTurn{Status: types.TurnCompleted},
			Reply: "must not run after failed dependency",
			Next:  &types.Route{Action: types.NextProceed},
		}, nil
	default:
		return types.TeamTurnResult{
			Turn:  types.TeamTurn{Status: types.TurnFailed},
			Error: "unexpected team",
			Next:  &types.Route{Action: types.NextFail, Reason: "unexpected team"},
		}, nil
	}
}

func TestRuntimeEscalatesTeamFailureToCoordinatorWithoutReplayingFailedTeam(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(files)
	evidence := storage.NewJSONLEvidenceStore(files)
	teamRuntime := &failureAggregationTeamRuntime{}
	runtime := NewRuntime(
		failureAggregationDefinitions(),
		teamRuntime,
		sessions,
		evidence,
		t.TempDir(),
	)

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "failure-aggregation",
		Input:  "run workers",
	})
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)
	require.Contains(t, result.Reply, "coordinator summarized sibling results")
	require.Len(t, result.TeamResults, 4, "default, two siblings, then one aggregation pass")

	teamRuntime.mu.Lock()
	defaultRuns := teamRuntime.defaultRuns
	failedWorkerRuns := teamRuntime.failedWorkerRuns
	blockedWorkerRuns := teamRuntime.blockedWorkerRuns
	coordinatorInput := append([]types.SharedRecord(nil), teamRuntime.coordinatorInput...)
	teamRuntime.mu.Unlock()

	require.Equal(t, 2, defaultRuns)
	require.Equal(t, 1, failedWorkerRuns, "a Team failure is escalated, not automatically replayed")
	require.Equal(t, 0, blockedWorkerRuns, "a Team depending on the failed Team is discarded")
	require.Contains(t, recordNames(coordinatorInput), "SuccessfulWorkerReport")
	require.Contains(t, recordNames(coordinatorInput), "TeamFailureReport")

	records, err := evidence.List(context.Background(), result.Session.ID, types.RecordScopeFlow)
	require.NoError(t, err)
	require.Contains(t, recordNames(records), "SuccessfulWorkerReport")
	require.Contains(t, recordNames(records), "TeamFailureReport")
}

func failureAggregationDefinitions() *types.Definitions {
	return &types.Definitions{
		Flow: types.Flow{
			ID:          "failure-aggregation",
			EntryTeamID: "default",
			Teams: map[string]types.FlowTeamBinding{
				"default": {
					ID:          "default",
					TeamID:      "default-team",
					Coordinator: true,
					CanActivate: []string{"successful", "failed", "blocked"},
					Inputs: types.InputSpec{
						FlowRecords: []string{"SuccessfulWorkerReport"},
					},
				},
				"successful": {
					ID:        "successful",
					TeamID:    "successful-team",
					DependsOn: []string{"default"},
				},
				"failed": {
					ID:        "failed",
					TeamID:    "failed-team",
					DependsOn: []string{"default"},
				},
				"blocked": {
					ID:        "blocked",
					TeamID:    "blocked-team",
					DependsOn: []string{"failed"},
				},
			},
		},
		Teams: map[string]types.Team{
			"default-team":    {ID: "default-team"},
			"successful-team": {ID: "successful-team"},
			"failed-team":     {ID: "failed-team"},
			"blocked-team":    {ID: "blocked-team"},
		},
	}
}

func recordNames(records []types.SharedRecord) []string {
	names := make([]string, 0, len(records))
	for _, record := range records {
		names = append(names, record.Name)
	}
	return names
}
