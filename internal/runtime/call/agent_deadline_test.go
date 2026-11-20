package call

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type ctxCapturingRunner struct {
	hasDeadline bool
}

func (r *ctxCapturingRunner) Run(ctx context.Context, _ types.AgentConfig, _ types.AgentRequest) (*types.AgentResult, error) {
	_, r.hasDeadline = ctx.Deadline()
	return &types.AgentResult{Status: types.TurnCompleted, Reply: "ok"}, nil
}

// agent call 不再有任何墙钟上限：即使 call.timeout / loop.timeout 都写着，
// 运行期也不套 deadline。停止条件由轮次、工具调用数、无进展检测决定，
// 挂死由模型传输超时与单次工具超时兜住。
func TestAgentExecutorSetsNoDeadline(t *testing.T) {
	runner := &ctxCapturingRunner{}
	req := validAgentRequest()
	req.Call.Timeout = "5m"
	req.AgentDefinition.Loop.Timeout = "10m"

	_, err := NewAgentExecutor(runner).Execute(context.Background(), req)

	require.NoError(t, err)
	assert.False(t, runner.hasDeadline, "agent call must not get a deadline from any timeout field")
}
