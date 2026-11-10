package mcp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAdapterReconnectsCrashedStdioServer covers a stdio server dying mid-session.
//
// The failure this guards is quiet and long-lived: the child is killed (crash,
// OOM, a config reload), the adapter still lists its tools, and every call fails
// with "server exited" until the whole engine restarts. From the user's side a
// tool silently disappears and never comes back.
//
// The mock server exits on the first tools/call and answers normally on the
// second start, so the call that hits the crash has to be served by the restarted
// child — not merely fail once and work later.
func TestAdapterReconnectsCrashedStdioServer(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "crashed")
	adapter := newMockAdapter(t, Options{})
	server := mockStdioServer(mockModeCrashOnce, envMockCrashMarker, marker)

	require.NoError(t, adapter.Connect(context.Background(), server), "initial connect")

	// This call kills the server; the client has to restart it and retry.
	result, err := adapter.CallTool(context.Background(), "echo", map[string]any{"message": "after crash"})
	require.NoError(t, err, "调用打死服务端后应重连并完成这次调用")
	require.FileExists(t, marker, "第一个子进程确实崩了（否则本用例没测到重连）")
	require.True(t, result.Success)
	require.Contains(t, result.Content, "echo: after crash")

	// And the connection is usable afterwards, not just for the retry that
	// triggered the restart.
	again, err := adapter.CallTool(context.Background(), "echo", map[string]any{"message": "still alive"})
	require.NoError(t, err)
	require.True(t, again.Success)
	require.Contains(t, again.Content, "echo: still alive")

	// The restarted server was re-handshaken: its tools are still listed.
	require.True(t, adapter.HasTool("echo"), "重连后工具清单应重新建立")
}

// TestAdapterWithoutReconnectReportsTheCrash is the control: with reconnection
// disabled (a negative attempt count), the same crash surfaces as an error. It
// keeps the test above honest — a passing reconnect case has to be the
// reconnection doing the work, not the crash never happening.
func TestAdapterWithoutReconnectReportsTheCrash(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "crashed")
	adapter := newMockAdapter(t, Options{ReconnectAttempts: -1})
	server := mockStdioServer(mockModeCrashOnce, envMockCrashMarker, marker)

	require.NoError(t, adapter.Connect(context.Background(), server))

	_, err := adapter.CallTool(context.Background(), "echo", map[string]any{"message": "boom"})
	require.Error(t, err, "关掉重连后，崩溃必须以错误暴露而不是被吞掉")
	require.FileExists(t, marker)
}

// TestAdapterDoesNotReconnectOnToolError covers the other half: a tool that
// answers with an error is a working server. Restarting it would turn every
// failing tool call into a process restart.
func TestAdapterReconnectsOnlyOnTransportFailure(t *testing.T) {
	require.False(t, isTransportGone(nil))
	require.False(t, isTransportGone(context.Canceled))
	require.False(t, isTransportGone(errString("tool returned an error")))
	require.True(t, isTransportGone(errString(`mcp stdio transport: server "x" exited`)))
	require.True(t, isTransportGone(errString("write |1: broken pipe")))
}

type errString string

func (e errString) Error() string { return string(e) }
