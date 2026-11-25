package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestBuildToolSchemasInjectsOnlyDirectTools pins the split: a builtin tool that
// tools.direct leaves out is not rendered into the request at all. Offering it
// alongside REPL is what gives the model two ways to do the same job, and it picks
// the direct one.
func TestBuildToolSchemasInjectsOnlyDirectTools(t *testing.T) {
	loop := &TurnLoop{}
	loop.SetScriptDescription(func([]string) string { return "CATALOG" })
	schemas := loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{
			Builtin: []string{"Bash", "Read", "Grep", "REPL"},
			Direct:  []string{"REPL"},
		},
	})
	names := collectSchemaNames(schemas)
	require.True(t, names["REPL"])
	require.False(t, names["Bash"], "a sandbox-only tool must not be injected as a direct call")
	require.False(t, names["Read"])
	require.False(t, names["Grep"])
}

// TestScriptCatalogListsSandboxToolsOnly keeps the two surfaces in step: what the
// REPL description advertises must be exactly what left the direct list. A catalog
// built from Tools.Builtin would advertise tools that are also injected, which is
// the double offer this field exists to remove.
func TestScriptCatalogListsSandboxToolsOnly(t *testing.T) {
	var catalogued []string
	loop := &TurnLoop{}
	loop.SetScriptDescription(func(names []string) string {
		catalogued = names
		return "CATALOG"
	})
	loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{
			Builtin: []string{"Bash", "Read", "REPL", "WebSearch"},
			Direct:  []string{"REPL", "WebSearch"},
		},
	})
	require.Equal(t, []string{"Bash", "Read"}, catalogued)
}

// TestScriptBindableToolsExcludesInjectedTools keeps execution aligned with the
// catalog: a tool the model calls directly is not also bound inside a program.
func TestScriptBindableToolsExcludesInjectedTools(t *testing.T) {
	loop := &TurnLoop{}
	bindable := loop.scriptBindableTools(types.AgentConfig{
		Tools: types.ToolConfig{
			Builtin: []string{"Bash", "Read", "Grep", "REPL", "Team", "TeamSession"},
			Direct:  []string{"REPL", "Team", "TeamSession"},
		},
	})
	require.Equal(t, []string{"Bash", "Grep", "Read"}, bindable)
}

// TestDirectWithoutNarrowingKeepsEveryTool is the compatibility case: definitions
// written before tools.direct existed declare nothing and must be injected whole.
func TestDirectWithoutNarrowingKeepsEveryTool(t *testing.T) {
	loop := &TurnLoop{}
	schemas := loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{Builtin: []string{"Bash", "Read"}},
	})
	names := collectSchemaNames(schemas)
	require.True(t, names["Bash"])
	require.True(t, names["Read"])
}

// sandboxCallRecorder records the calls that reach execution, so a test can tell
// "refused by the surface" from "executed and failed later".
type sandboxCallRecorder struct{ calls []string }

func (e *sandboxCallRecorder) Execute(_ context.Context, name string, _ map[string]any) (*types.ToolResult, error) {
	e.calls = append(e.calls, name)
	return &types.ToolResult{Success: true, Content: "ok"}, nil
}

func runNarrowed(t *testing.T, model *mockModelProvider, executor ToolExecutor) types.AgentResult {
	t.Helper()
	loop := NewTurnLoop(model, executor, nil, NewRouteParser(), nil, nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hi"}}})
	loop.SetScriptDescription(func([]string) string { return "CATALOG" })
	result, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "narrowed",
		Tools: types.ToolConfig{Builtin: []string{"Bash", "Read", "REPL"}, Direct: []string{"REPL"}},
		Loop:  types.LoopConfig{MaxRounds: 3},
	}, types.AgentRequest{
		CallID:        "call",
		AgentID:       "narrowed",
		AgentTurnID:   "turn",
		ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hi"}},
	})
	require.NoError(t, err)
	return *result
}

// TestRunAdvertisesOnlyDirectTools is the end-to-end half of the schema test: it
// reads the tool list the loop actually handed the model.
func TestRunAdvertisesOnlyDirectTools(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{{Text: `{"reply":"ok"}`, FinishReason: "stop"}}}
	runNarrowed(t, model, &mockToolExecutor{})
	require.Equal(t, map[string]bool{"REPL": true}, collectSchemaNames(model.lastTools))
}

// TestRunRefusesDirectCallToSandboxTool covers the case the schema cannot: a model
// that emits a name it was never offered. The sandbox tool passes the allowlist (a
// program needs it to), so only the call-site check keeps it out of the executor.
func TestRunRefusesDirectCallToSandboxTool(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{
		{Text: "using bash", ToolCalls: []types.ToolCall{{ID: "c1", Name: "Bash", Arguments: map[string]any{"command": "ls"}}}},
		{Text: `{"reply":"ok"}`, FinishReason: "stop"},
	}}
	executor := &sandboxCallRecorder{}
	result := runNarrowed(t, model, executor)
	require.NotNil(t, result.Reply)
	require.NotContains(t, executor.calls, "Bash", "a sandbox-only tool must not reach the executor as a direct call")

	var told string
	for _, message := range model.lastMessages {
		if strings.Contains(message.Content, "inside a program") {
			told = message.Content
		}
	}
	require.Contains(t, told, "call it inside a program as tools.Bash")
}

// TestDeclaringREPLAloneNarrowsTheSurface is the configuration this design exists
// for: one list, no direct field. The code tools leave the schema and show up in the
// catalog instead.
func TestDeclaringREPLAloneNarrowsTheSurface(t *testing.T) {
	var catalogued []string
	loop := &TurnLoop{}
	loop.SetScriptDescription(func(names []string) string {
		catalogued = names
		return "CATALOG"
	})
	schemas := loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{Builtin: []string{"REPL", "Team"}},
	})
	names := collectSchemaNames(schemas)
	require.True(t, names["REPL"])
	require.True(t, names["Team"])
	require.True(t, names["Collect"], "Team implies Collect")
	require.False(t, names["Bash"], "the code tools are implied, not injected")
	require.Equal(t, []string{"Bash", "Glob", "Grep", "Read", "Write"}, catalogued)
}

// TestDirectCallToImplicitCodeToolIsRefused covers the refusal on the same shape:
// Bash is never declared, only implied, and calling it at the conversation layer is
// still refused rather than executed.
func TestDirectCallToImplicitCodeToolIsRefused(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{
		{ToolCalls: []types.ToolCall{{ID: "c1", Name: "Bash", Arguments: map[string]any{"command": "ls"}}}},
		{Text: `{"reply":"ok"}`, FinishReason: "stop"},
	}}
	executor := &sandboxCallRecorder{}
	loop := NewTurnLoop(model, executor, nil, NewRouteParser(), nil, nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hi"}}})
	loop.SetScriptDescription(func([]string) string { return "CATALOG" })
	_, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "repl-only",
		Tools: types.ToolConfig{Builtin: []string{"REPL", "Team"}},
		Loop:  types.LoopConfig{MaxRounds: 3},
	}, types.AgentRequest{
		CallID:        "call",
		AgentID:       "repl-only",
		AgentTurnID:   "turn",
		ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hi"}},
	})
	require.NoError(t, err)
	require.NotContains(t, executor.calls, "Bash")
}
