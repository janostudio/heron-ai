package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the path that makes the `hooks:` block of an agent
// definition actually run. Before it existed nothing read
// types.AgentConfig.Hooks, so the block was inert.

func TestWithCommandHooks_UnknownEventIsRejected(t *testing.T) {
	_, err := NewHookExecutor().WithCommandHooks([]types.HookConfig{
		{Event: "on_tool_finish", Command: "true"},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown hook event")
	assert.Contains(t, err.Error(), "on_tool_finish")
}

func TestWithCommandHooks_EmptyCommandIsRejected(t *testing.T) {
	_, err := NewHookExecutor().WithCommandHooks([]types.HookConfig{
		{Event: HookOnStart, Command: "  "},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty command")
}

func TestWithCommandHooks_UnparsableTimeoutIsRejected(t *testing.T) {
	_, err := NewHookExecutor().WithCommandHooks([]types.HookConfig{
		{Event: HookOnStart, Command: "true", Timeout: "30 seconds"},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid timeout")
}

func TestWithCommandHooks_NegativeTimeoutIsRejected(t *testing.T) {
	_, err := NewHookExecutor().WithCommandHooks([]types.HookConfig{
		{Event: HookOnStart, Command: "true", Timeout: "-1s"},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-positive timeout")
}

func TestWithCommandHooks_NoConfigReturnsSameExecutor(t *testing.T) {
	base := NewHookExecutor()
	got, err := base.WithCommandHooks(nil, nil)
	require.NoError(t, err)
	assert.Same(t, base, got)
}

func TestWithCommandHooks_RunsCommandOnItsEvent(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	hooks, err := NewHookExecutor().WithCommandHooks([]types.HookConfig{
		{Event: HookOnToolStart, Command: "echo fired > " + marker},
	}, nil)
	require.NoError(t, err)

	// A different event must not run it.
	require.NoError(t, hooks.Execute(context.Background(), HookOnStart, types.HookPayload{}))
	assert.NoFileExists(t, marker)

	require.NoError(t, hooks.Execute(context.Background(), HookOnToolStart, types.HookPayload{}))
	content, err := os.ReadFile(marker)
	require.NoError(t, err)
	assert.Contains(t, string(content), "fired")
}

func TestWithCommandHooks_CommandSeesPayloadInEnv(t *testing.T) {
	out := filepath.Join(t.TempDir(), "env")
	hooks, err := NewHookExecutor().WithCommandHooks([]types.HookConfig{
		{Event: HookOnToolEnd, Command: `printf '%s|%s|%s' "$HERON_HOOK_EVENT" "$HERON_AGENT_ID" "$HERON_TOOL_NAME" > ` + out},
	}, nil)
	require.NoError(t, err)

	require.NoError(t, hooks.Execute(context.Background(), HookOnToolEnd, types.HookPayload{
		AgentID:  "reviewer",
		ToolName: "Bash",
	}))
	content, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "on_tool_end|reviewer|Bash", string(content))
}

func TestWithCommandHooks_FailingCommandReturnsError(t *testing.T) {
	hooks, err := NewHookExecutor().WithCommandHooks([]types.HookConfig{
		{Event: HookOnStart, Command: "echo boom >&2; exit 3"},
	}, nil)
	require.NoError(t, err)

	err = hooks.Execute(context.Background(), HookOnStart, types.HookPayload{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hook command failed")
	assert.Contains(t, err.Error(), "boom")
}

func TestWithCommandHooks_TimeoutIsAnError(t *testing.T) {
	hooks, err := NewHookExecutor().WithCommandHooks([]types.HookConfig{
		{Event: HookOnStart, Command: "sleep 5", Timeout: "50ms"},
	}, nil)
	require.NoError(t, err)

	start := time.Now()
	err = hooks.Execute(context.Background(), HookOnStart, types.HookPayload{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.Less(t, time.Since(start), 3*time.Second, "timeout must actually bound the command")
}

func TestWithCommandHooks_KeepsAlreadyRegisteredHooks(t *testing.T) {
	base := NewHookExecutor()
	var programmatic bool
	base.Register(HookOnStart, func(context.Context, types.HookPayload) error {
		programmatic = true
		return nil
	})

	hooks, err := base.WithCommandHooks([]types.HookConfig{
		{Event: HookOnStart, Command: "true"},
	}, nil)
	require.NoError(t, err)

	require.NoError(t, hooks.Execute(context.Background(), HookOnStart, types.HookPayload{}))
	assert.True(t, programmatic, "loop-wide hooks must survive merging")
}

func TestWithCommandHooks_DoesNotMutateBaseExecutor(t *testing.T) {
	base := NewHookExecutor()
	marker := filepath.Join(t.TempDir(), "ran")
	_, err := base.WithCommandHooks([]types.HookConfig{
		{Event: HookOnStart, Command: "echo leaked > " + marker},
	}, nil)
	require.NoError(t, err)

	// The shared executor must not pick up one agent's configured hooks.
	require.NoError(t, base.Execute(context.Background(), HookOnStart, types.HookPayload{}))
	assert.NoFileExists(t, marker)
}

// --- TurnLoop integration: the configured hook must fire during a real turn ---

func TestTurnLoop_Run_ConfiguredHookCommandRuns(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	loop := NewTurnLoop(
		&mockModelProvider{},
		&mockToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	_, err := loop.Run(context.Background(), types.AgentConfig{
		Name: "guarded",
		Hooks: []types.HookConfig{
			{Event: HookOnStart, Command: "echo on_start > " + marker},
			{Event: HookOnEnd, Command: "echo on_end >> " + marker},
		},
		Loop: types.LoopConfig{MaxRounds: 1},
	}, types.AgentRequest{AgentID: "guarded"})
	require.NoError(t, err)

	content, err := os.ReadFile(marker)
	require.NoError(t, err, "configured hooks must run during a turn")
	assert.Contains(t, string(content), "on_start")
	assert.Contains(t, string(content), "on_end")
}

func TestTurnLoop_Run_ConfiguredHookFailureFailsTurn(t *testing.T) {
	var errorHookRan string
	marker := filepath.Join(t.TempDir(), "ran")
	base := NewHookExecutor()
	base.Register(HookOnError, func(_ context.Context, payload types.HookPayload) error {
		errorHookRan = payload.Error
		return nil
	})

	loop := NewTurnLoop(
		&mockModelProvider{},
		&mockToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		base,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	result, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "guarded",
		Hooks: []types.HookConfig{{Event: HookOnStart, Command: "exit 7"}},
		Loop:  types.LoopConfig{MaxRounds: 1},
	}, types.AgentRequest{AgentID: "guarded"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Contains(t, result.Error, "hook command failed")
	assert.Contains(t, errorHookRan, "hook command failed", "on_error must see the hook failure")
	assert.NoFileExists(t, marker)
}

func TestTurnLoop_Run_MisconfiguredHookFailsTurn(t *testing.T) {
	loop := NewTurnLoop(
		&mockModelProvider{},
		&mockToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	_, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "guarded",
		Hooks: []types.HookConfig{{Event: "on_wat", Command: "true"}},
		Loop:  types.LoopConfig{MaxRounds: 1},
	}, types.AgentRequest{AgentID: "guarded"})
	require.Error(t, err, "a misdeclared hook must not be silently ignored")
	assert.Contains(t, err.Error(), "unknown hook event")
}

func TestTurnLoop_Run_ToolHooksFromConfigRunPerToolCall(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "tools")
	model := &mockModelProvider{responses: []types.ChatResponse{{
		Text: "",
		ToolCalls: []types.ToolCall{{
			ID:        "call-1",
			Name:      "Read",
			Arguments: map[string]any{"file": "a.go"},
		}},
		Usage: types.TokenUsage{TotalTokens: 5},
	}, {
		Text:  "done",
		Usage: types.TokenUsage{TotalTokens: 5},
	}}}

	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	_, err := loop.Run(context.Background(), types.AgentConfig{
		Name: "guarded",
		Hooks: []types.HookConfig{
			{Event: HookOnToolStart, Command: `echo "$HERON_TOOL_NAME" >> ` + marker},
		},
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
		Loop:  types.LoopConfig{MaxRounds: 2},
	}, types.AgentRequest{
		AgentID:       "guarded",
		ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hello"}},
	})
	require.NoError(t, err)

	content, err := os.ReadFile(marker)
	require.NoError(t, err, "on_tool_start from config must run")
	assert.Contains(t, string(content), "Read")
}
