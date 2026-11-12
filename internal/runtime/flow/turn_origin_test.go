package flow

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 每个回合的来源都要说清楚：唤醒回合会把用户原话原样再填一次 input，
// 只靠那句话模型分不出"用户又说话了"和"派出去的活回来了"。
func TestTurnOriginTextExplainsWakeups(t *testing.T) {
	require.Contains(t, turnOriginText(turnOriginUser), "user's own request")
	require.Contains(t, turnOriginText(turnOriginMailbox), "reported back")
	require.Contains(t, turnOriginText(turnOriginMailbox), "did not send a new message")
	require.Contains(t, turnOriginText(turnOriginResume), "paused")
	require.Contains(t, turnOriginText(turnOriginWakeup), "no new user message")
	require.Equal(t, "", turnOriginText(""), "未知来源不编造说明")
}
