package flow

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// ListTasks returns the tasks one caller instance dispatched, newest instance
// last. It reads the event stream instead of a side index: the stream is the
// fact source, so a restarted process answers identically.
func (r *Runtime) ListTasks(ctx context.Context, spec types.TaskCatalogSpec) ([]types.TaskInfo, error) {
	if strings.TrimSpace(spec.FlowSessionID) == "" {
		return nil, fmt.Errorf("task catalog requires a flow session")
	}
	if strings.TrimSpace(spec.CallerTeamSessionID) == "" {
		return nil, fmt.Errorf("task catalog requires the caller instance")
	}
	sessions, replies, err := r.taskSessions(ctx, spec)
	if err != nil {
		return nil, err
	}
	tasks := make([]types.TaskInfo, 0, len(sessions))
	for _, session := range sessions {
		tasks = append(tasks, taskInfoOf(session, replies[session.ID]))
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].TeamSessionID < tasks[j].TeamSessionID
	})
	return tasks, nil
}

// GetTask returns one dispatched task by its business key.
func (r *Runtime) GetTask(ctx context.Context, spec types.TaskCatalogSpec, taskKey string) (types.TaskInfo, error) {
	if strings.TrimSpace(taskKey) == "" {
		return types.TaskInfo{}, fmt.Errorf("task key is required")
	}
	if strings.TrimSpace(spec.FlowSessionID) == "" {
		return types.TaskInfo{}, fmt.Errorf("task catalog requires a flow session")
	}
	if strings.TrimSpace(spec.CallerTeamSessionID) == "" {
		return types.TaskInfo{}, fmt.Errorf("task catalog requires the caller instance")
	}
	sessions, replies, err := r.taskSessions(ctx, spec)
	if err != nil {
		return types.TaskInfo{}, err
	}
	for _, session := range sessions {
		if session.TaskKey == taskKey {
			return taskInfoOf(session, replies[session.ID]), nil
		}
	}
	return types.TaskInfo{}, fmt.Errorf("task %q not found among the tasks this instance dispatched", taskKey)
}

// taskSessions folds the per_task instances one caller dispatched, plus the
// last reply each produced.
func (r *Runtime) taskSessions(ctx context.Context, spec types.TaskCatalogSpec) ([]types.TeamSession, map[string]string, error) {
	events, err := r.replayFlowEvents(ctx, spec.FlowSessionID)
	if err != nil {
		return nil, nil, err
	}
	latest := make(map[string]types.TeamSession)
	for _, event := range events {
		switch event.Type {
		case types.EventTeamSessionCreated, types.EventTeamSessionUpdated:
			session, ok := decodeTeamSession(event.Payload)
			if !ok {
				continue
			}
			latest[session.ID] = session
		}
	}

	sessions := make([]types.TeamSession, 0, len(latest))
	for _, session := range latest {
		if !session.IsPerTask() {
			continue
		}
		if session.FlowSessionID != "" && session.FlowSessionID != spec.FlowSessionID {
			continue
		}
		if session.DispatchedBy == nil || session.DispatchedBy.TeamSessionID != spec.CallerTeamSessionID {
			continue
		}
		sessions = append(sessions, session)
	}

	replies := make(map[string]string, len(sessions))
	for _, event := range events {
		switch event.Type {
		case types.EventFlowTurnWaitingInput, types.EventFlowTurnCompleted:
			if event.TeamSessionID == "" {
				continue
			}
			if text := payloadString(event.Payload, "reply"); strings.TrimSpace(text) != "" {
				replies[event.TeamSessionID] = text
			}
		}
	}
	return sessions, replies, nil
}

func taskInfoOf(session types.TeamSession, lastReply string) types.TaskInfo {
	return types.TaskInfo{
		TaskKey:       session.TaskKey,
		TeamID:        session.TeamID,
		TeamSessionID: session.ID,
		Status:        session.Status,
		DispatchCount: session.DispatchCount,
		ReopenCount:   session.ReopenCount,
		TaskContextID: session.TaskContextID,
		LastReply:     lastReply,
	}
}
