package member

import (
	"context"
	"fmt"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// SubagentRunner is the V1 Subagent execution seam.
type SubagentRunner interface {
	Run(ctx context.Context, agent types.AgentConfig, req types.SubagentRequest) (*types.SubagentResult, error)
}

type SubagentExecutor struct {
	runner SubagentRunner
}

func NewSubagentExecutor(runner SubagentRunner) *SubagentExecutor {
	return &SubagentExecutor{runner: runner}
}

func (e *SubagentExecutor) Type() types.MemberType {
	return types.MemberSubagent
}

func (e *SubagentExecutor) Execute(ctx context.Context, req types.MemberRequest) (types.MemberResult, error) {
	if e.runner == nil {
		return types.MemberResult{Status: types.TurnFailed, Error: "subagent runner is nil"}, fmt.Errorf("subagent runner is nil")
	}
	if req.AgentDefinition == nil {
		return types.MemberResult{Status: types.TurnFailed, Error: "agent definition is required"}, fmt.Errorf("agent definition is required for member %q", req.Member.ID)
	}

	execCtx, cancel, err := withTimeout(ctx, req.Member.Timeout, req.AgentDefinition.Loop.Timeout)
	if err != nil {
		return types.MemberResult{Status: types.TurnFailed, Error: err.Error()}, err
	}
	defer cancel()

	result, err := e.runner.Run(execCtx, *req.AgentDefinition, types.SubagentRequest{
		MemberID:         req.Member.ID,
		AgentID:          req.Member.AgentID,
		Responsibility:   req.Member.Responsibility,
		Input:            req.Input,
		Records:          req.Records,
		TeamMemory:       req.TeamMemory,
		SubagentMemory:   req.SubagentMemory,
		KnowledgeText:    req.KnowledgeText,
		Variables:        req.Variables,
		MaxToolCalls:     req.Limits.WithDefaults().MaxToolCalls,
		MaxParallelTools: req.Limits.WithDefaults().MaxParallelTools,
	})
	if err != nil {
		return types.MemberResult{Status: types.TurnFailed, Error: err.Error()}, err
	}
	if result == nil {
		return types.MemberResult{Status: types.TurnFailed, Error: "subagent runner returned nil result"}, fmt.Errorf("subagent %q returned nil result", req.Member.ID)
	}

	memberResult := types.MemberResult{
		Status:       types.TurnCompleted,
		Reply:        result.Reply,
		Usage:        result.Usage,
		WorkspaceOps: result.WorkspaceOps,
		ToolCalls:    result.ToolCalls,
		Next:         result.Next,
	}
	if result.Error != "" {
		memberResult.Status = types.TurnFailed
		memberResult.Error = result.Error
	}
	if recordName := req.Member.Output.Record; recordName != "" {
		data := map[string]any{"reply": result.Reply}
		if result.Parsed != nil {
			data["parsed"] = result.Parsed
		}
		memberResult.Records = []types.SharedRecord{newMemberRecord(
			req,
			recordName,
			"subagent_result",
			result.Reply,
			data,
		)}
		memberResult.Records[0].Basis = basisFromWorkspaceOps(result.WorkspaceOps)
	}
	return memberResult, nil
}

func basisFromWorkspaceOps(operations []types.WorkspaceOperation) []types.BasisRef {
	basis := make([]types.BasisRef, 0, len(operations))
	for _, operation := range operations {
		if operation.Kind != "read" || operation.Path == "" {
			continue
		}
		basis = append(basis, types.BasisRef{
			Kind:             "workspace_file",
			Path:             operation.Path,
			Revision:         operation.Revision,
			Lines:            operation.Lines,
			Excerpt:          operation.Excerpt,
			SourceTurnID:     operation.TurnID,
			SourceToolCallID: operation.OperationID,
		})
	}
	return basis
}

func newMemberRecord(req types.MemberRequest, name, kind, summary string, data map[string]any) types.SharedRecord {
	return types.SharedRecord{
		RecordID: fmt.Sprintf("%s-%d", req.Member.ID, time.Now().UnixNano()),
		Kind:     kind,
		Name:     name,
		Scope:    types.RecordScopeTeam,
		Producer: types.ProducerRef{
			FlowSessionID: req.FlowSession.ID,
			FlowTurnID:    req.FlowTurn.ID,
			TeamID:        req.TeamTurn.TeamID,
			TeamTurnID:    req.TeamTurn.ID,
			MemberID:      req.Member.ID,
			MemberTurnID:  req.MemberTurnID,
		},
		Summary:   summary,
		Data:      data,
		Status:    types.RecordActive,
		Revision:  1,
		CreatedAt: time.Now().UTC(),
	}
}
