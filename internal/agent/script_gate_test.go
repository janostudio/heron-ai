package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/tool/script"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// A batch without Script must not gain a gate: the gate costs a closure and a
// context value per turn, and more importantly its presence is how the runtime
// knows the sandbox is in play.
func TestScriptGateOnlyWhenScriptIsCalled(t *testing.T) {
	loop := &TurnLoop{}
	ctx := loop.withScriptGate(context.Background(), types.AgentConfig{}, types.AgentRequest{}, 1,
		[]types.ToolCall{{Name: "Read"}, {Name: "Bash"}})
	_, ok := script.GateFromContext(ctx)
	assert.False(t, ok)
}

func TestScriptGateIsPublishedWhenScriptIsCalled(t *testing.T) {
	loop := &TurnLoop{}
	ctx := loop.withScriptGate(context.Background(), types.AgentConfig{}, types.AgentRequest{}, 1,
		[]types.ToolCall{{Name: script.ToolName}})
	gate, ok := script.GateFromContext(ctx)
	require.True(t, ok)
	require.NotNil(t, gate)
}

// Nesting has to be refused before the gate delegates: an inner Script would get
// its own step budget and replay log, so the outer step count would stop
// describing the work done.
func TestScriptGateRefusesNestedScript(t *testing.T) {
	loop := &TurnLoop{}
	ctx := loop.withScriptGate(context.Background(), types.AgentConfig{}, types.AgentRequest{}, 1,
		[]types.ToolCall{{Name: script.ToolName}})
	gate, ok := script.GateFromContext(ctx)
	require.True(t, ok)

	result, err := gate(ctx, script.ToolName, map[string]any{"code": "return 1;"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "cannot call itself")
}
