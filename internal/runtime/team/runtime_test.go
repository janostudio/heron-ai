package team

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/runtime/member"
	"github.com/heron-ai/heron-engine/pkg/types"
)

type fakeMemberExecutor struct {
	memberType types.MemberType
	mu         sync.Mutex
	started    map[string]time.Time
	records    map[string][]types.SharedRecord
}

func newFakeMemberExecutor(memberType types.MemberType) *fakeMemberExecutor {
	return &fakeMemberExecutor{
		memberType: memberType,
		started:    make(map[string]time.Time),
		records:    make(map[string][]types.SharedRecord),
	}
}

func (e *fakeMemberExecutor) Type() types.MemberType {
	return e.memberType
}

func (e *fakeMemberExecutor) Execute(_ context.Context, req types.MemberRequest) (types.MemberResult, error) {
	e.mu.Lock()
	e.started[req.Member.ID] = time.Now()
	e.mu.Unlock()

	if req.Member.ID == "synthesize" {
		if len(req.Records) != 2 {
			return types.MemberResult{
				Status: types.TurnFailed,
				Error:  fmt.Sprintf("expected 2 input records, got %d", len(req.Records)),
			}, nil
		}
	}

	recordName := req.Member.Output.Record
	var records []types.SharedRecord
	if recordName != "" {
		record := types.SharedRecord{
			RecordID: fmt.Sprintf("record-%s", req.Member.ID),
			Kind:     "test",
			Name:     recordName,
			Scope:    types.RecordScopeTeam,
			Summary:  req.Member.ID + " completed",
			Status:   types.RecordActive,
			Revision: 1,
		}
		records = []types.SharedRecord{record}
		e.mu.Lock()
		e.records[req.Member.ID] = records
		e.mu.Unlock()
	}

	return types.MemberResult{
		Status:  types.TurnCompleted,
		Reply:   req.Member.ID + " completed",
		Records: records,
		Next:    &types.Route{Action: types.NextProceed},
	}, nil
}

func TestRuntimeRunsIndependentMembersInParallelAndUsesDependencies(t *testing.T) {
	executor := newFakeMemberExecutor(types.MemberCommand)
	registry := member.NewRegistry()
	require.NoError(t, registry.Register(executor))
	runtime := NewRuntime(registry)

	req := types.TeamTurnRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamSession: types.TeamSession{ID: "ts-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "review"},
		Team: types.Team{
			ID: "review",
			Members: map[string]types.Member{
				"security": {
					ID:   "security",
					Type: types.MemberCommand,
					Command: &types.CommandSpec{
						Command: "security-check",
					},
					Output: types.OutputSpec{
						Record: "SecurityReview",
					},
				},
				"performance": {
					ID:   "performance",
					Type: types.MemberCommand,
					Command: &types.CommandSpec{
						Command: "performance-check",
					},
					Output: types.OutputSpec{
						Record: "PerformanceReview",
					},
				},
				"synthesize": {
					ID:   "synthesize",
					Type: types.MemberCommand,
					Command: &types.CommandSpec{
						Command: "synthesize-review",
					},
					DependsOn: []string{"security", "performance"},
					Inputs: types.InputSpec{
						Records: []types.InputBinding{
							{From: "security", Record: "SecurityReview"},
							{From: "performance", Record: "PerformanceReview"},
						},
					},
					Output: types.OutputSpec{
						Record: "ReviewReport",
					},
				},
			},
			Output: types.OutputSpec{
				From:   "synthesize",
				Record: "ReviewReport",
			},
		},
	}

	result, err := runtime.Run(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, types.NextProceed, result.Next.Action)
	require.Equal(t, []string{"record-synthesize"}, result.Turn.RecordIDs)
	require.Contains(t, result.Reply, "synthesize completed")
	require.Len(t, result.MemberResults, 3)
	require.Contains(t, executor.records["security"][0].Name, "SecurityReview")
	require.Contains(t, executor.records["performance"][0].Name, "PerformanceReview")

	executor.mu.Lock()
	securityStarted := executor.started["security"]
	performanceStarted := executor.started["performance"]
	synthesizeStarted := executor.started["synthesize"]
	executor.mu.Unlock()
	require.False(t, securityStarted.IsZero())
	require.False(t, performanceStarted.IsZero())
	require.False(t, synthesizeStarted.IsZero())
	require.True(t, synthesizeStarted.After(securityStarted))
	require.True(t, synthesizeStarted.After(performanceStarted))
}

func TestRuntimeReturnsCoordinateWhenMemberFails(t *testing.T) {
	executor := &failingMemberExecutor{}
	registry := member.NewRegistry()
	require.NoError(t, registry.Register(executor))
	runtime := NewRuntime(registry)

	result, err := runtime.Run(context.Background(), types.TeamTurnRequest{
		Team: types.Team{
			ID: "verify",
			Members: map[string]types.Member{
				"test": {
					ID:   "test",
					Type: types.MemberCommand,
					Command: &types.CommandSpec{
						Command: "test-command",
					},
				},
			},
		},
	})
	require.Error(t, err)
	require.Equal(t, types.NextCoordinate, result.Next.Action)
	require.Contains(t, result.Error, "test failed")
}

func TestRuntimeLimitsCallsPerTeamTurnNotAcrossFlow(t *testing.T) {
	executor := newFakeMemberExecutor(types.MemberCommand)
	registry := member.NewRegistry()
	require.NoError(t, registry.Register(executor))
	runtime := NewRuntime(registry)

	result, err := runtime.Run(context.Background(), types.TeamTurnRequest{
		Team: types.Team{
			ID: "diagnose",
			Members: map[string]types.Member{
				"snapshot": {
					ID:   "snapshot",
					Type: types.MemberCommand,
					Command: &types.CommandSpec{
						Command: "snapshot",
					},
				},
				"explore": {
					ID:   "explore",
					Type: types.MemberCommand,
					Command: &types.CommandSpec{
						Command: "explore",
					},
				},
				"inspect": {
					ID:        "inspect",
					Type:      types.MemberCommand,
					DependsOn: []string{"snapshot", "explore"},
					Command: &types.CommandSpec{
						Command: "inspect",
					},
				},
			},
		},
		Limits: types.RuntimeLimits{
			MaxCallsPerTeamTurn: 2,
			MaxParallelCalls:    2,
		},
	})
	require.ErrorContains(t, err, "team turn exceeded max calls: 2")
	require.Equal(t, types.NextCoordinate, result.Next.Action)
}

type failingMemberExecutor struct{}

func (e *failingMemberExecutor) Type() types.MemberType {
	return types.MemberCommand
}

func (e *failingMemberExecutor) Execute(_ context.Context, req types.MemberRequest) (types.MemberResult, error) {
	return types.MemberResult{
		Status: types.TurnFailed,
		Error:  req.Member.ID + " failed",
	}, nil
}
