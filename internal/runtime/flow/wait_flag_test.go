package flow

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func waitFlagDefinitions() *types.Definitions {
	return &types.Definitions{
		Flow: types.Flow{
			ID:          "wait-flags",
			EntryTeamID: "a",
			Teams: map[string]types.FlowTeamBinding{
				"a": {ID: "a", TeamID: "a-team", CanActivate: []string{"b", "c"}, Inputs: types.InputSpec{UserMessage: true}},
				"b": {ID: "b", TeamID: "b-team", CanActivate: []string{}},
				"c": {ID: "c", TeamID: "c-team", CanActivate: []string{"d"}},
				"d": {ID: "d", TeamID: "d-team"},
			},
		},
		Teams: map[string]types.Team{
			"a-team": {ID: "a-team"},
			"b-team": {ID: "b-team"},
			"c-team": {ID: "c-team"},
			"d-team": {ID: "d-team"},
		},
	}
}

// batchWaitTeamRuntime parks "b" waiting for a Tool in the first batch while a
// sibling branch keeps activating Teams, so the turn has more batches to run
// before the queue drains.
type batchWaitTeamRuntime struct {
	mu    sync.Mutex
	calls map[string]int
}

func (b *batchWaitTeamRuntime) count(team string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls[team]++
	return b.calls[team]
}

func (b *batchWaitTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	team := req.TeamTurn.TeamID
	call := b.count(team)
	switch team {
	case "a":
		if call == 1 {
			return types.TeamTurnResult{
				Turn:  req.TeamTurn,
				Reply: "dispatching both branches",
				Next:  &types.Route{Action: types.NextActivate, Teams: []string{"b", "c"}},
			}, nil
		}
		return types.TeamTurnResult{Turn: req.TeamTurn, Reply: "done", Next: &types.Route{Action: types.NextProceed}}, nil
	case "b":
		turn := req.TeamTurn
		turn.Status = types.TurnWaitingTool
		return types.TeamTurnResult{
			Turn:  turn,
			Reply: "waiting for a Tool",
			Next:  &types.Route{Action: types.NextWaitTool},
		}, nil
	case "c":
		if call == 1 {
			return types.TeamTurnResult{
				Turn:  req.TeamTurn,
				Reply: "activating d",
				Next:  &types.Route{Action: types.NextActivate, Teams: []string{"d"}},
			}, nil
		}
		return types.TeamTurnResult{Turn: req.TeamTurn, Reply: "c finished", Next: &types.Route{Action: types.NextProceed}}, nil
	case "d":
		return types.TeamTurnResult{Turn: req.TeamTurn, Reply: "d finished", Next: &types.Route{Action: types.NextProceed}}, nil
	default:
		return types.TeamTurnResult{Turn: req.TeamTurn, Next: &types.Route{Action: types.NextFail, Reason: "unexpected team"}}, nil
	}
}

// TestWaitDecisionSurvivesLaterBatches is the regression guard for the wait
// flags being re-declared per batch: batch 1 parks a Team waiting for a Tool,
// later batches finish routing and end waiting for the user, and the session
// used to report waiting_input — losing the fact that a Tool task was still
// outstanding, so its completion had nothing to resume.
func TestWaitDecisionSurvivesLaterBatches(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	evidenceStore := storage.NewJSONLEvidenceStore(fileStore)
	runtime := newTestRuntime(waitFlagDefinitions(), &batchWaitTeamRuntime{calls: map[string]int{}}, sessionWriter, evidenceStore)

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "wait-flags",
		Input:  "start both branches",
	})
	require.NoError(t, err)

	require.Equal(t, types.SessionWaitingTool, result.Session.Status,
		"a Tool wait from an earlier batch must not be overwritten by a later wait-for-user batch")
	require.Equal(t, types.TurnWaitingTool, result.Turn.Status)
}
