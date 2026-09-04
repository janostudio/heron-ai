package call

import (
	"context"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/internal/workspace"
	"github.com/heron-ai/heron-engine/pkg/types"
)

type CommandExecutor struct {
	workspace *workspace.Service
}

func NewCommandExecutor(workspaces ...*workspace.Service) *CommandExecutor {
	var service *workspace.Service
	if len(workspaces) > 0 {
		service = workspaces[0]
	}
	return &CommandExecutor{workspace: service}
}

func (e *CommandExecutor) Type() types.CallType {
	return types.CallCommand
}

func (e *CommandExecutor) Execute(ctx context.Context, req types.CallRequest) (types.CallResult, error) {
	if req.Call.Command == nil || strings.TrimSpace(req.Call.Command.Command) == "" {
		return types.CallResult{Status: types.TurnFailed, Error: "command is required"}, fmt.Errorf("command is required for call %q", req.Call.ID)
	}
	if e.workspace == nil {
		return types.CallResult{Status: types.TurnFailed, Error: "workspace is not configured"}, fmt.Errorf("workspace is not configured")
	}
	if req.RecoveryOf != "" && req.Call.Command.ReplayPolicy != types.ReplayAllow && req.Call.Command.ReplayPolicy != types.ReplayIdempotent {
		return types.CallResult{
			Status: types.TurnFailed,
			Error:  "command replay is not allowed by replay_policy",
		}, fmt.Errorf("command call %q replay policy is %q", req.Call.ID, req.Call.Command.ReplayPolicy)
	}
	if req.RecoveryOf != "" &&
		req.Call.Command.ReplayPolicy == types.ReplayIdempotent &&
		strings.TrimSpace(req.Call.Command.IdempotencyKey) == "" {
		return types.CallResult{
			Status: types.TurnFailed,
			Error:  "command idempotent replay requires idempotency_key",
		}, fmt.Errorf("command call %q idempotent replay requires idempotency_key", req.Call.ID)
	}

	command := req.Call.Command.Command
	args := req.Call.Command.Args
	execCtx, cancel, err := withTimeout(ctx, req.Call.Timeout, req.Call.Command.Timeout)
	if err != nil {
		return types.CallResult{Status: types.TurnFailed, Error: err.Error()}, err
	}
	defer cancel()

	execution, err := e.workspace.Run(execCtx, workspace.CommandRequest{
		TurnID:  callTurnID(req),
		Command: command,
		Args:    args,
		Env:     commandEnv(req),
		Stdin:   req.Input,
	})
	output := execution.Stdout
	errorOutput := execution.Stderr
	summary := strings.TrimSpace(output)
	if summary == "" {
		summary = strings.TrimSpace(errorOutput)
	}

	result := types.CallResult{
		Status:       types.TurnCompleted,
		Reply:        summary,
		WorkspaceOps: []types.WorkspaceOperation{execution.Operation},
	}
	if err != nil {
		logging.Error("command execution failed", map[string]any{
			"flow_session_id": req.FlowSession.ID,
			"team_id":         req.TeamTurn.TeamID,
			"call_id":         req.Call.ID,
			"error":           err.Error(),
		})
		result.Status = types.TurnFailed
		result.Error = err.Error()
	}
	if recordName := req.Call.Output.Record; recordName != "" {
		passed := commandPassed(err, execution.ExitCode, output, errorOutput)
		result.Records = []types.SharedRecord{newCallRecord(
			req,
			recordName,
			"command_result",
			summary,
			map[string]any{
				"exit_code": execution.ExitCode,
				"stdout":    output,
				"stderr":    errorOutput,
				"passed":    passed,
			},
		)}
	}
	if len(result.Records) > 0 {
		result.Records[0].Basis = []types.BasisRef{{
			Kind:         "workspace_operation",
			SourceTurnID: req.TeamTurn.ID,
			Excerpt:      execution.Operation.Summary,
		}}
	}
	return result, nil
}

func commandPassed(err error, exitCode int, stdout, stderr string) bool {
	if err != nil || exitCode != 0 {
		return false
	}
	// Deterministic verification scripts may intentionally publish a failed
	// business assertion with process exit 0 so the Team can inspect and route
	// it. Preserve that fact in SharedRecord.data instead of reporting passed.
	output := strings.ToLower(stdout + "\n" + stderr)
	return !strings.Contains(output, "result failed") &&
		!strings.Contains(output, "status=failed")
}
