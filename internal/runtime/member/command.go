package member

import (
	"context"
	"fmt"
	"strings"

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

func (e *CommandExecutor) Type() types.MemberType {
	return types.MemberCommand
}

func (e *CommandExecutor) Execute(ctx context.Context, req types.MemberRequest) (types.MemberResult, error) {
	if req.Member.Command == nil || strings.TrimSpace(req.Member.Command.Command) == "" {
		return types.MemberResult{Status: types.TurnFailed, Error: "command is required"}, fmt.Errorf("command is required for member %q", req.Member.ID)
	}
	if e.workspace == nil {
		return types.MemberResult{Status: types.TurnFailed, Error: "workspace is not configured"}, fmt.Errorf("workspace is not configured")
	}

	command := req.Member.Command.Command
	args := req.Member.Command.Args
	execCtx, cancel, err := withTimeout(ctx, req.Member.Timeout, req.Member.Command.Timeout)
	if err != nil {
		return types.MemberResult{Status: types.TurnFailed, Error: err.Error()}, err
	}
	defer cancel()

	execution, err := e.workspace.Run(execCtx, workspace.CommandRequest{
		TurnID:  memberTurnID(req),
		Command: command,
		Args:    args,
		Stdin:   req.Input,
	})
	output := execution.Stdout
	errorOutput := execution.Stderr
	summary := strings.TrimSpace(output)
	if summary == "" {
		summary = strings.TrimSpace(errorOutput)
	}

	result := types.MemberResult{
		Status:       types.TurnCompleted,
		Reply:        summary,
		WorkspaceOps: []types.WorkspaceOperation{execution.Operation},
	}
	if err != nil {
		result.Status = types.TurnFailed
		result.Error = err.Error()
	}
	if recordName := req.Member.Output.Record; recordName != "" {
		result.Records = []types.SharedRecord{newMemberRecord(
			req,
			recordName,
			"command_result",
			summary,
			map[string]any{
				"exit_code": execution.ExitCode,
				"stdout":    output,
				"stderr":    errorOutput,
				"passed":    err == nil && execution.ExitCode == 0,
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
