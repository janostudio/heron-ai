package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// loopingCoordinatorTeamRuntime 的协调员永远激活同一个 worker，worker 每次
// 都正常结束。这会无界地热循环，直到引擎的 max_activation_retries 兜底介入。
type loopingCoordinatorTeamRuntime struct{}

func (loopingCoordinatorTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	switch req.TeamTurn.TeamID {
	case "default":
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Next: &types.Route{Action: types.NextActivate, Teams: []string{"worker"}},
		}, nil
	default:
		return types.TeamTurnResult{
			Turn: types.TeamTurn{Status: types.TurnCompleted},
			Next: &types.Route{Action: types.NextProceed},
		}, nil
	}
}

func loopingCoordinatorDefinitions() *types.Definitions {
	return &types.Definitions{
		Flow: types.Flow{
			ID:          "loop-guard",
			EntryTeamID: "default",
			Teams: map[string]types.FlowTeamBinding{
				"default": {
					ID:          "default",
					TeamID:      "default-team",
					CanActivate: []string{"worker"},
				},
				"worker": {ID: "worker", TeamID: "worker-team"},
			},
		},
		Teams: map[string]types.Team{
			"default-team": {ID: "default-team"},
			"worker-team":  {ID: "worker-team"},
		},
	}
}

// 死循环被兜底打断时，flow_turn 落盘事件必须带上失败原因——否则用户只看到
// "failed" 却不知道为何失败（实测 gap：error 只进了 FlowTurnResult.Error，
// 没进事件 payload）。
func TestRuntimeActivationLoopFailurePersistsErrorInEvent(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(files)
	runtime := newTestRuntime(loopingCoordinatorDefinitions(), loopingCoordinatorTeamRuntime{}, sessions, nil)
	runtime.SetLimits(types.RuntimeLimits{MaxActivationRetries: 2})

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "loop-guard",
		Input:  "loop forever",
	})
	require.Error(t, err, "activation loop must fail, not silently succeed")
	require.Equal(t, types.SessionWaitingInput, result.Session.Status, "a failed turn must leave the session continuable")
	require.Equal(t, types.TurnFailed, result.Turn.Status)
	require.Contains(t, result.Error, "repeated Flow activation")

	// 事件流里必须能读到同一条失败原因。
	replay, readErr := sessions.Replay(context.Background(), result.Session.ID)
	require.NoError(t, readErr)

	var found bool
	for _, event := range replay.Events {
		if event.Type != types.EventFlowTurnCompleted {
			continue
		}
		if got, _ := event.Payload["error"].(string); got != "" {
			require.Contains(t, got, "repeated Flow activation")
			found = true
		}
	}
	require.True(t, found, "flow_turn.completed event must carry the failure error")
}
