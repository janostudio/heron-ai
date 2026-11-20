package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// 回退到模型容量时不该有任何告警：这是默认路径。
func TestContextConfigWarningsQuietOnModelCapacity(t *testing.T) {
	config := types.ContextConfig{MaxInputTokens: 960000}
	require.Empty(t, contextConfigWarnings(config, 960000))
}

// 把窗口写小会被当成合法配置接受，运行时只表现为"压缩来得特别早"。
// 这条告警把它点出来，避免再从事件流反推。
func TestContextConfigWarningsNarrowedWindow(t *testing.T) {
	warnings := contextConfigWarnings(types.ContextConfig{MaxInputTokens: 60000}, 960000)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "narrows the model window")
	require.Contains(t, warnings[0], "budget.max_input_tokens")
}

// 只覆盖窗口时不报比率问题：比率顺序由 loader 校验，不在运行时兜底。
func TestContextConfigWarningsWindowOnly(t *testing.T) {
	warnings := contextConfigWarnings(types.ContextConfig{MaxInputTokens: 60000}, 960000)
	require.Len(t, warnings, 1)
}
