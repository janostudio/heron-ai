package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

type coordinateRetryTeamRuntime struct{}

func (coordinateRetryTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	switch req.TeamTurn.TeamID {
	case "default":
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Next: &types.Route{
				Action: types.NextActivate,
				Teams:  []string{"broken"},
			},
		}, nil
	case "broken":
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Next: &types.Route{
				Action: types.NextCoordinate,
				Reason: "downstream Agent failed",
			},
		}, nil
	default:
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Next: &types.Route{Action: types.NextFail, Reason: "unexpected Team"},
		}, nil
	}
}

func TestRuntimeStopsRepeatedCoordinateAfterConfiguredRetryLimit(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(files)
	runtime := NewRuntime(
		coordinateRetryDefinitions(),
		coordinateRetryTeamRuntime{},
		sessions,
		nil,
		t.TempDir(),
	)
	runtime.SetLimits(types.RuntimeLimits{
		MaxTeamTurns:         10,
		MaxCoordinateRetries: 1,
		MaxActivationRetries: 10,
	})

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "coordinate-retry",
		Input:  "run",
	})
	require.Error(t, err)
	require.Equal(t, types.SessionFailed, result.Session.Status)
	require.Contains(t, result.Error, "exceeded coordinate retry limit")
}

type repeatedActivationTeamRuntime struct{}

func (repeatedActivationTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	if req.TeamTurn.TeamID == "default" {
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Next: &types.Route{Action: types.NextActivate, Teams: []string{"broken"}},
		}, nil
	}
	return types.TeamTurnResult{
		Turn: req.TeamTurn,
		Next: &types.Route{Action: types.NextCoordinate, Reason: "diagnose unavailable"},
	}, nil
}

func TestRuntimeStopsRepeatedActivationAfterConfiguredRetryLimit(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(files)
	runtime := NewRuntime(
		coordinateRetryDefinitions(),
		repeatedActivationTeamRuntime{},
		sessions,
		nil,
		t.TempDir(),
	)
	runtime.SetLimits(types.RuntimeLimits{
		MaxTeamTurns:         10,
		MaxCoordinateRetries: 1,
		MaxActivationRetries: 1,
	})

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "coordinate-retry",
		Input:  "run",
	})
	require.Error(t, err)
	require.Equal(t, types.SessionFailed, result.Session.Status)
	require.Contains(t, result.Error, "repeated Flow activation")
}

func coordinateRetryDefinitions() *types.Definitions {
	return &types.Definitions{
		Flow: types.Flow{
			ID:          "coordinate-retry",
			EntryTeamID: "default",
			Teams: map[string]types.FlowTeamBinding{
				"default": {
					ID:          "default",
					TeamID:      "default-team",
					Coordinator: true,
					CanActivate: []string{"broken"},
				},
				"broken": {
					ID:        "broken",
					TeamID:    "broken-team",
					DependsOn: []string{"default"},
				},
			},
		},
		Teams: map[string]types.Team{
			"default-team": {ID: "default-team"},
			"broken-team":  {ID: "broken-team"},
		},
	}
}
