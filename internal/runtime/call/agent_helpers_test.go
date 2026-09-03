package call

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestAgentTurnID(t *testing.T) {
	tests := []struct {
		name string
		req  types.CallRequest
		want string
	}{
		{
			name: "explicit agent turn id",
			req: types.CallRequest{
				AgentTurnID: "at-1",
				CallTurnID:  "ct-1",
			},
			want: "at-1",
		},
		{
			name: "falls back to call turn id",
			req: types.CallRequest{
				CallTurnID: "ct-1",
			},
			want: "ct-1",
		},
		{
			name: "both empty",
			req:  types.CallRequest{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, agentTurnID(tt.req))
		})
	}
}

func TestBasisFromWorkspaceOps(t *testing.T) {
	tests := []struct {
		name string
		ops  []types.WorkspaceOperation
		want []types.BasisRef
	}{
		{
			name: "read op mapped to basis ref",
			ops: []types.WorkspaceOperation{{
				Kind:        "read",
				Path:        "/src/main.go",
				Revision:    "rev-1",
				Lines:       []int{1, 2, 3},
				Excerpt:     "package main",
				TurnID:      "tt-1",
				OperationID: "op-1",
			}},
			want: []types.BasisRef{{
				Kind:             "workspace_file",
				Path:             "/src/main.go",
				Revision:         "rev-1",
				Lines:            []int{1, 2, 3},
				Excerpt:          "package main",
				SourceTurnID:     "tt-1",
				SourceToolCallID: "op-1",
			}},
		},
		{
			name: "non-read kind skipped",
			ops: []types.WorkspaceOperation{{
				Kind: "write",
				Path: "/src/main.go",
			}},
			want: []types.BasisRef{},
		},
		{
			name: "empty path skipped",
			ops: []types.WorkspaceOperation{{
				Kind: "read",
				Path: "",
			}},
			want: []types.BasisRef{},
		},
		{
			name: "mixed ops keep only reads with paths",
			ops: []types.WorkspaceOperation{
				{Kind: "write", Path: "/a"},
				{Kind: "read", Path: "/b", Revision: "r2", TurnID: "t2", OperationID: "o2"},
				{Kind: "read", Path: ""},
				{Kind: "search", Path: "/c"},
			},
			want: []types.BasisRef{{
				Kind:             "workspace_file",
				Path:             "/b",
				Revision:         "r2",
				SourceTurnID:     "t2",
				SourceToolCallID: "o2",
			}},
		},
		{
			name: "empty input",
			ops:  nil,
			want: []types.BasisRef{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, basisFromWorkspaceOps(tt.ops))
		})
	}
}
