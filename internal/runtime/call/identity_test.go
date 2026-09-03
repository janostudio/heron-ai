package call

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestCallTurnID(t *testing.T) {
	tests := []struct {
		name    string
		req     types.CallRequest
		want    string
	}{
		{
			name: "explicit call turn id",
			req: types.CallRequest{
				CallTurnID: "ct-1",
				TeamTurn:   types.TeamTurn{ID: "tt-1"},
				Call:       types.Call{ID: "answer"},
			},
			want: "ct-1",
		},
		{
			name: "falls back to team turn and call id",
			req: types.CallRequest{
				TeamTurn: types.TeamTurn{ID: "tt-1"},
				Call:     types.Call{ID: "answer"},
			},
			want: "tt-1:answer",
		},
		{
			name: "all empty",
			req:  types.CallRequest{},
			want: ":",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, callTurnID(tt.req))
		})
	}
}
