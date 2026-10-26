package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// activateTeamRuntime still answers with the old routing action, as a Team
// written before the change would.
type activateTeamRuntime struct{}

func (activateTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	return types.TeamTurnResult{
		Turn: req.TeamTurn,
		Next: &types.Route{Action: types.NextActivate, Teams: []string{"diagnose"}},
	}, nil
}

// 团队之间不再通过路由协议派发：那条路径会结束本回合、由邮箱唤醒下一个回合，
// 而上下文正是这样被拆开重建的。现在只有 Team 工具能派发，路由里的 activate
// 必须明确失败，而不是悄悄换回旧行为。
func TestActivateRouteIsRetired(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	evidenceStore := storage.NewJSONLEvidenceStore(fileStore)
	runtime := newTestRuntime(testDefinitions(), activateTeamRuntime{}, sessionWriter, evidenceStore)

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "code-fix",
		Input:  "查一下线上流量",
	})

	require.Error(t, err, "activate 必须被拒绝")
	require.Contains(t, err.Error(), "Team tool", "错误要指出正确做法")
	require.Len(t, result.TeamResults, 1, "只有发起方自己跑了一轮")
	require.Equal(t, "default", result.TeamResults[0].Turn.TeamID,
		"被派发的 Team 不能跑起来")
}
