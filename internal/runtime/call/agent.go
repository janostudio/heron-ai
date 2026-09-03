package call

import (
	"context"
	"fmt"
	"time"

	"github.com/heron-ai/heron-engine/internal/agentstore"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// AgentRunner is the V1 Agent execution seam.
type AgentRunner interface {
	Run(ctx context.Context, agent types.AgentConfig, req types.AgentRequest) (*types.AgentResult, error)
}

type AgentExecutor struct {
	runner AgentRunner
}

func NewAgentExecutor(runner AgentRunner) *AgentExecutor {
	return &AgentExecutor{runner: runner}
}

func (e *AgentExecutor) Type() types.CallType {
	return types.CallAgent
}

func (e *AgentExecutor) Execute(ctx context.Context, req types.CallRequest) (types.CallResult, error) {
	if e.runner == nil {
		return types.CallResult{Status: types.TurnFailed, Error: "agent runner is nil"}, fmt.Errorf("agent runner is nil")
	}
	if req.AgentDefinition == nil {
		return types.CallResult{Status: types.TurnFailed, Error: "agent definition is required"}, fmt.Errorf("agent definition is required for call %q", req.Call.ID)
	}

	execCtx, cancel, err := withTimeout(ctx, req.Call.Timeout, req.AgentDefinition.Loop.Timeout)
	if err != nil {
		return types.CallResult{Status: types.TurnFailed, Error: err.Error()}, err
	}
	defer cancel()

	// Spawned children publishing with deliver=downstream write into this
	// collector; the records are attached to this call's result below so
	// downstream consumers aggregate them like any same-name record.
	spawnCollector := agentstore.NewRecordCollector(req.Call.Output.Record, types.ProducerRef{
		FlowSessionID: req.FlowSession.ID,
		FlowTurnID:    req.FlowTurn.ID,
		TeamID:        req.TeamTurn.TeamID,
		TeamTurnID:    req.TeamTurn.ID,
		CallID:        req.Call.ID,
		CallTurnID:    callTurnID(req),
	})
	execCtx = agentstore.WithRecordCollector(execCtx, spawnCollector)

	result, err := e.runner.Run(execCtx, *req.AgentDefinition, types.AgentRequest{
		FlowSessionID:      req.FlowSession.ID,
		TeamID:             req.TeamTurn.TeamID,
		TeamTurnID:         req.TeamTurn.ID,
		CallID:             req.Call.ID,
		CallTurnID:         req.CallTurnID,
		AgentID:            req.Call.AgentID,
		AgentTurnID:        agentTurnID(req),
		Attempt:            req.Attempt,
		RecoveryOf:         req.RecoveryOf,
		ResumeCheckpointID: req.ResumeCheckpointID,
		ResumeTaskID:       req.ResumeTaskID,
		ResumeApprovalID:   req.ResumeApprovalID,
		ResumeApproval:     req.ResumeApproval,
		ContextBlocks:      req.ContextBlocks,
		Variables:          req.Variables,
		MaxAgentRounds:     req.Limits.WithDefaults().MaxAgentRounds,
		MaxParallelTools:   req.Limits.WithDefaults().MaxParallelTools,
	})
	if err != nil {
		return types.CallResult{Status: types.TurnFailed, Error: err.Error()}, err
	}
	if result == nil {
		return types.CallResult{Status: types.TurnFailed, Error: "agent runner returned nil result"}, fmt.Errorf("agent %q returned nil result", req.Call.ID)
	}

	callResult := types.CallResult{
		Status:          result.Status,
		Reply:           result.Reply,
		CallTurnID:      agentTurnID(req),
		AgentID:         req.Call.AgentID,
		Usage:           result.Usage,
		Requests:        result.Requests,
		WorkspaceOps:    result.WorkspaceOps,
		ToolCalls:       result.ToolCalls,
		Next:            result.Next,
		Checkpoint:      result.Checkpoint,
		TaskID:          result.TaskID,
		PendingApproval: result.PendingApproval,
		Approval:        result.Approval,
	}
	if callResult.Status == "" {
		callResult.Status = types.TurnCompleted
	}
	if result.Checkpoint != nil {
		callResult.CheckpointID = result.Checkpoint.ID
	}
	if result.Error != "" {
		callResult.Status = types.TurnFailed
		callResult.Error = result.Error
	}
	if recordName := req.Call.Output.Record; recordName != "" {
		data := map[string]any{"reply": result.Reply}
		if result.Parsed != nil {
			data["parsed"] = result.Parsed
		}
		callResult.Records = []types.SharedRecord{newCallRecord(
			req,
			recordName,
			"agent_result",
			result.Reply,
			data,
		)}
		callResult.Records[0].Basis = basisFromWorkspaceOps(result.WorkspaceOps)
	}
	// Records published by spawned children (deliver=downstream) share the
	// call's output.record name; one call publishing several records is
	// already supported by the team record selection.
	callResult.Records = append(callResult.Records, spawnCollector.Records()...)
	return callResult, nil
}

func agentTurnID(req types.CallRequest) string {
	if req.AgentTurnID != "" {
		return req.AgentTurnID
	}
	return req.CallTurnID
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

func newCallRecord(req types.CallRequest, name, kind, summary string, data map[string]any) types.SharedRecord {
	return types.SharedRecord{
		RecordID: fmt.Sprintf("%s-%d", req.Call.ID, time.Now().UnixNano()),
		Kind:     kind,
		Name:     name,
		Scope:    types.RecordScopeTeam,
		Producer: types.ProducerRef{
			FlowSessionID: req.FlowSession.ID,
			FlowTurnID:    req.FlowTurn.ID,
			TeamID:        req.TeamTurn.TeamID,
			TeamTurnID:    req.TeamTurn.ID,
			CallID:        req.Call.ID,
			CallTurnID:    req.CallTurnID,
		},
		Summary:   summary,
		Data:      data,
		Status:    types.RecordActive,
		Revision:  1,
		CreatedAt: time.Now().UTC(),
	}
}
