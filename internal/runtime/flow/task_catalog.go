package flow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/storage"
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

// ListTaskTrees groups this instance's tasks by task context: one entry per
// dispatched tree, tasks ordered by their instance id (which is also the
// dispatch order).
func (r *Runtime) ListTaskTrees(ctx context.Context, spec types.TaskCatalogSpec) ([]types.TaskTree, error) {
	tasks, err := r.ListTasks(ctx, spec)
	if err != nil {
		return nil, err
	}
	// Descendants belong to the tree but were dispatched by the task instance,
	// not by this caller: include them by task context so the ops view shows
	// the whole tree instead of only its roots.
	known := make(map[string]bool, len(tasks))
	contexts := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		known[task.TeamSessionID] = true
		contexts[task.TaskContextID] = true
	}
	if len(contexts) > 0 {
		events, replayErr := r.replayFlowEvents(ctx, spec.FlowSessionID)
		if replayErr != nil {
			return nil, replayErr
		}
		latest := make(map[string]types.TeamSession)
		for _, event := range events {
			switch event.Type {
			case types.EventTeamSessionCreated, types.EventTeamSessionUpdated:
				session, ok := decodeTeamSession(event.Payload)
				if ok {
					latest[session.ID] = session
				}
			}
		}
		for _, session := range latest {
			if !session.IsPerTask() || known[session.ID] {
				continue
			}
			if !contexts[session.TaskContextID] {
				continue
			}
			known[session.ID] = true
			tasks = append(tasks, taskInfoOf(session, ""))
		}
	}
	order := make([]string, 0, len(tasks))
	grouped := make(map[string][]types.TaskInfo, len(tasks))
	for _, task := range tasks {
		contextID := task.TaskContextID
		if contextID == "" {
			contextID = task.TeamSessionID
		}
		if _, seen := grouped[contextID]; !seen {
			order = append(order, contextID)
		}
		grouped[contextID] = append(grouped[contextID], task)
	}
	trees := make([]types.TaskTree, 0, len(order))
	for _, contextID := range order {
		trees = append(trees, types.TaskTree{TaskContextID: contextID, Tasks: grouped[contextID]})
	}
	sort.SliceStable(trees, func(i, j int) bool {
		return trees[i].TaskContextID < trees[j].TaskContextID
	})
	return trees, nil
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

// activeTaskCount counts the open per_task instances of one Flow session. Closed
// instances do not count: the limit is about work in flight, not about history.
func (r *Runtime) activeTaskCount(ctx context.Context, sessionID string) (int, error) {
	events, err := r.replayFlowEvents(ctx, sessionID)
	if err != nil {
		return 0, err
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
	count := 0
	for _, session := range latest {
		if session.IsPerTask() && session.Status != types.SessionClosed {
			count++
		}
	}
	return count, nil
}

// CloseTask soft-closes the task and every instance in its task context.
//
// Cascade goes down the task context (the root a dispatch created), so closing
// a task also closes the sub-tasks it handed out — they exist only to serve it.
// Nothing is deleted: transcripts and records stay readable, and dispatching the
// same task_key again reopens the same instance.
func (r *Runtime) CloseTask(ctx context.Context, spec types.TaskCatalogSpec, taskKey string) (types.TaskInfo, error) {
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
	var target types.TeamSession
	found := false
	for _, session := range sessions {
		if session.TaskKey == taskKey {
			target = session
			found = true
		}
	}
	if !found {
		return types.TaskInfo{}, fmt.Errorf("task %q not found among the tasks this instance dispatched", taskKey)
	}

	now := time.Now().UTC()
	for _, session := range sessions {
		if session.TaskContextID != target.TaskContextID {
			continue
		}
		if session.Status == types.SessionClosed {
			continue
		}
		session.Status = types.SessionClosed
		session.ClosedAt = &now
		session.UpdatedAt = now
		if err := r.appendSessionEvent(ctx, spec.FlowSessionID, storage.SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventTeamSessionUpdated,
				FlowSessionID: spec.FlowSessionID,
			},
			Payload: map[string]any{"team_session": session},
		}); err != nil {
			return types.TaskInfo{}, err
		}
	}

	target.Status = types.SessionClosed
	return taskInfoOf(target, replies[target.ID]), nil
}

// FindTasksBySubscription returns the open tasks that subscribed to one source
// (an issue, a PR, a CI pipeline, a chat thread). It is the lookup behind
// "the comment on #143 belongs to the task that asked for #143".
func (r *Runtime) FindTasksBySubscription(ctx context.Context, flowSessionID, source string) ([]types.TaskInfo, error) {
	if strings.TrimSpace(flowSessionID) == "" {
		return nil, fmt.Errorf("task lookup requires a flow session")
	}
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("task lookup requires a subscription source")
	}
	events, err := r.replayFlowEvents(ctx, flowSessionID)
	if err != nil {
		return nil, err
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
	tasks := make([]types.TaskInfo, 0, 1)
	for _, session := range latest {
		if !session.IsPerTask() || session.Status == types.SessionClosed {
			continue
		}
		for _, subscription := range session.Subscriptions {
			if subscription == source {
				tasks = append(tasks, taskInfoOf(session, ""))
				break
			}
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].TeamSessionID < tasks[j].TeamSessionID
	})
	return tasks, nil
}

// DeliverSubscriptionEvent routes one source event to the task that subscribed
// to it, as a mailbox work message carrying the task_key.
//
// Deduplication is by event id: the same source event (a retried webhook, a
// replayed cursor) never starts the task twice. It reads the mailbox events, so
// the answer survives a restart.
func (r *Runtime) DeliverSubscriptionEvent(
	ctx context.Context,
	flowSessionID, source, eventID, input string,
) (types.TaskInfo, error) {
	if strings.TrimSpace(eventID) == "" {
		return types.TaskInfo{}, fmt.Errorf("subscription delivery requires an event id")
	}
	tasks, err := r.FindTasksBySubscription(ctx, flowSessionID, source)
	if err != nil {
		return types.TaskInfo{}, err
	}
	if len(tasks) == 0 {
		return types.TaskInfo{}, fmt.Errorf("no open task subscribed to %q", source)
	}
	target := tasks[0]

	delivered, err := r.subscriptionDelivered(ctx, flowSessionID, eventID)
	if err != nil {
		return types.TaskInfo{}, err
	}
	if delivered {
		return target, nil
	}

	session, err := r.loadSession(ctx, flowSessionID)
	if err != nil {
		return types.TaskInfo{}, err
	}
	_, err = r.enqueueWorkMessage(ctx, session, types.FlowTurn{FlowSessionID: flowSessionID},
		types.MailboxAddress{TeamID: target.TeamID},
		map[string]any{
			"task_key": target.TaskKey,
			"source":   source,
			"event_id": eventID,
			"input":    input,
		})
	if err != nil {
		return types.TaskInfo{}, err
	}
	return target, nil
}

func (r *Runtime) subscriptionDelivered(ctx context.Context, flowSessionID, eventID string) (bool, error) {
	box, err := r.loadMailbox(ctx, flowSessionID)
	if err != nil {
		return false, err
	}
	for _, msg := range box {
		if value, ok := msg.Payload["event_id"].(string); ok && value == eventID {
			return true, nil
		}
	}
	return false, nil
}
