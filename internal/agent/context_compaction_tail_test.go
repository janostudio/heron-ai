package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 合并两次压缩摘要时，最新那份必须完整留下：它的结尾是 "Current Work /
// Next Step"，正是下一回合要照着做的东西。旧摘要先被压缩。
func TestMergeCompactionSummaryKeepsNewestIntact(t *testing.T) {
	oldSummary := "OLD-HEAD" + strings.Repeat("A", 9000) + "OLD-TAIL"
	newSummary := "NEW-HEAD" + strings.Repeat("B", 9000) + "NEW-TAIL"

	merged := mergeCompactionSummary(oldSummary, newSummary)

	require.Contains(t, merged, "NEW-HEAD")
	require.Contains(t, merged, "NEW-TAIL", "最新摘要的结尾（下一步）不能被截掉")
	require.Contains(t, merged, "OLD-HEAD", "旧摘要的头部保留，作为背景")
	require.NotContains(t, merged, "OLD-TAIL", "旧摘要从尾部开始丢")
}

// 摘要超出预算时，保留头尾、丢掉中间：头是目标与文件清单，尾是当前工作与
// 下一步，中间才是可以牺牲的细节。
func TestFitCompactionSummaryKeepsHeadAndTail(t *testing.T) {
	summary := "HEAD-goal" + strings.Repeat("M", 4000) + "TAIL-next-step"

	fitted := fitCompactionSummary(summary, nil, charCountEstimator{}, 400)
	require.NotNil(t, fitted)

	require.Contains(t, fitted.Content, "HEAD-goal")
	require.Contains(t, fitted.Content, "TAIL-next-step", "下一步必须活下来")
	require.Contains(t, fitted.Content, "middle omitted")
	require.Less(t, len([]rune(fitted.Content)), 600, "结果要落回预算内")
	require.NotContains(t, fitted.Content, strings.Repeat("M", 500), "中间那段要整段消失")
}
