package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// CheckpointRecoveryReport describes the checkpoint states found during
// process startup. Startup recovery never resumes an LLM call implicitly;
// it only validates pending pointers and lets the caller resume explicitly.
type CheckpointRecoveryReport struct {
	Total        int
	WaitingInput int
	WaitingTool  int
	ReadyTasks   int
	Failed       []string
	Orphaned     []string
}

func RecoverCheckpoints(
	ctx context.Context,
	checkpoints types.AgentCheckpointStore,
	tasks types.ToolTaskStore,
) (CheckpointRecoveryReport, error) {
	var report CheckpointRecoveryReport
	if checkpoints == nil {
		return report, errors.New("checkpoint store is not configured")
	}
	items, err := checkpoints.List(ctx)
	if err != nil {
		return report, err
	}
	report.Total = len(items)

	for _, checkpoint := range items {
		switch checkpoint.Status {
		case types.TurnWaitingInput:
			report.WaitingInput++
		case types.TurnWaitingTool:
			report.WaitingTool++
			if checkpoint.PendingTool == nil || checkpoint.PendingTool.TaskID == "" {
				checkpoint.Status = types.TurnFailed
				checkpoint.Error = "waiting_tool checkpoint has no pending task"
				if err := checkpoints.Save(ctx, checkpoint); err != nil {
					return report, err
				}
				report.Failed = append(report.Failed, checkpoint.ID)
				continue
			}
			if tasks == nil {
				return report, errors.New("tool task store is not configured")
			}
			task, taskErr := tasks.Load(ctx, checkpoint.PendingTool.TaskID)
			if taskErr != nil {
				if errors.Is(taskErr, ErrToolTaskNotFound) {
					checkpoint.Status = types.TurnFailed
					checkpoint.Error = fmt.Sprintf("pending Tool task %q was not found", checkpoint.PendingTool.TaskID)
					if err := checkpoints.Save(ctx, checkpoint); err != nil {
						return report, err
					}
					report.Orphaned = append(report.Orphaned, checkpoint.ID)
					continue
				}
				return report, taskErr
			}
			if task.Status == types.ToolTaskCompleted {
				report.ReadyTasks++
			}
		}
	}
	return report, nil
}
