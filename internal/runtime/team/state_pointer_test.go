package team

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/runtime/call"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// The state pointer is injected for every Agent that declares the State tool,
// not only for spawned children: a static call reads the same cross-session
// scope through the same tool. Missing it here would leave coordinator/owner
// able to call State but never told the store exists.
func TestTeamRuntimeAddsStatePointerForStaticCall(t *testing.T) {
	tests := []struct {
		name       string
		tools      types.ToolConfig
		wantBlock  bool
		agentID    string
		pointerTxt string
	}{
		{
			name:       "declares State",
			tools:      types.ToolConfig{Builtin: []string{"Read", "State"}},
			wantBlock:  true,
			agentID:    "coordinator",
			pointerTxt: "coordinator/state/",
		},
		{
			name:      "does not declare State",
			tools:     types.ToolConfig{Builtin: []string{"Read", "Grep"}},
			wantBlock: false,
			agentID:   "worker",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &blockCapturingRunner{}
			registry := call.NewRegistry()
			require.NoError(t, registry.Register(call.NewAgentExecutor(runner)))
			runtime := newTestRuntime(registry, map[string]types.AgentConfig{
				tt.agentID: {Name: tt.agentID, Tools: tt.tools},
			})

			_, err := runtime.Run(context.Background(), types.TeamTurnRequest{
				FlowSession: types.FlowSession{ID: "fs-1"},
				FlowTurn:    types.FlowTurn{ID: "ft-1"},
				TeamSession: types.TeamSession{ID: "ts-1"},
				TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "team-1"},
				Team: types.Team{
					ID: "team-1",
					Calls: map[string]types.Call{
						"work": {ID: "work", Type: types.CallAgent, AgentID: tt.agentID, Responsibility: "do work"},
					},
				},
			})
			require.NoError(t, err)

			var pointers []types.ContextBlock
			for _, block := range runner.blocks {
				if block.Kind == "state_pointer" {
					pointers = append(pointers, block)
				}
			}
			if !tt.wantBlock {
				assert.Empty(t, pointers, "an Agent without the State tool must not get a state pointer")
				return
			}
			require.Len(t, pointers, 1)
			pointer := pointers[0]
			assert.Contains(t, pointer.Text, tt.pointerTxt)
			assert.Equal(t, "system", pointer.Placement, "policy belongs to the system prompt")
			assert.Equal(t, "stable", pointer.Stability, "the path does not change per turn, so it can cache")
			assert.False(t, pointer.Compressible, "compacting the pointer away would hide the store")
		})
	}
}
