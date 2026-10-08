package team

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/runtime/call"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// recordingSessionWriter captures every event the Team runtime appends so a
// test can assert on the event headers, not only on the returned result.
type recordingSessionWriter struct {
	mu     sync.Mutex
	events []storage.SessionEvent
}

func (w *recordingSessionWriter) Append(
	_ context.Context,
	_ string,
	_ storage.EventLayer,
	event storage.SessionEvent,
) (storage.SessionEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, event)
	return event, nil
}

func (w *recordingSessionWriter) Replay(_ context.Context, _ string) (*storage.SessionReplay, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	events := append([]storage.SessionEvent(nil), w.events...)
	return &storage.SessionReplay{Events: events}, nil
}

func (w *recordingSessionWriter) Subscribe(_ context.Context, _ string, _ int64) (<-chan storage.SessionEvent, error) {
	return nil, nil
}

func (w *recordingSessionWriter) eventsOfType(eventType string) []storage.SessionEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	var matched []storage.SessionEvent
	for _, event := range w.events {
		if event.Type == eventType {
			matched = append(matched, event)
		}
	}
	return matched
}

// routingCallExecutor returns one configured route per call id, so a test can
// make sibling calls disagree about where the Team should go next.
type routingCallExecutor struct {
	routes map[string]types.NextAction
}

func (e *routingCallExecutor) Type() types.CallType { return types.CallCommand }

func (e *routingCallExecutor) Execute(_ context.Context, req types.CallRequest) (types.CallResult, error) {
	action := e.routes[req.Call.ID]
	if action == "" {
		action = types.NextProceed
	}
	return types.CallResult{
		Status: types.TurnCompleted,
		Reply:  req.Call.ID + " completed",
		Next:   &types.Route{Action: action, Reason: "from " + req.Call.ID},
	}, nil
}

func TestResolveNextPrefersMostSevereRouteInsteadOfNameOrder(t *testing.T) {
	tests := []struct {
		name     string
		routes   map[string]types.NextAction
		expected types.NextAction
		reason   string
	}{
		{
			name:     "fail beats activate even when fail is named last",
			routes:   map[string]types.NextAction{"apollo": types.NextActivate, "zulu": types.NextFail},
			expected: types.NextFail,
			reason:   "from zulu",
		},
		{
			name:     "coordinate beats return",
			routes:   map[string]types.NextAction{"apollo": types.NextReturn, "zulu": types.NextCoordinate},
			expected: types.NextCoordinate,
			reason:   "from zulu",
		},
		{
			name:     "wait approval beats activate",
			routes:   map[string]types.NextAction{"apollo": types.NextActivate, "zulu": types.NextWaitApproval},
			expected: types.NextWaitApproval,
			reason:   "from zulu",
		},
		{
			name:     "activate beats return",
			routes:   map[string]types.NextAction{"apollo": types.NextReturn, "zulu": types.NextActivate},
			expected: types.NextActivate,
			reason:   "from zulu",
		},
		{
			name:     "proceed loses to any explicit route",
			routes:   map[string]types.NextAction{"apollo": types.NextProceed, "zulu": types.NextReturn},
			expected: types.NextReturn,
			reason:   "from zulu",
		},
		{
			name:     "equally severe routes fall back to name order",
			routes:   map[string]types.NextAction{"apollo": types.NextActivate, "zulu": types.NextActivate},
			expected: types.NextActivate,
			reason:   "from apollo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := make(map[string]types.CallResult, len(tt.routes))
			for name, action := range tt.routes {
				results[name] = types.CallResult{
					Status: types.TurnCompleted,
					Next:   &types.Route{Action: action, Reason: "from " + name},
				}
			}
			next := resolveNext(results)
			require.Equal(t, tt.expected, next.Action)
			require.Equal(t, tt.reason, next.Reason)
		})
	}
}

func TestResolveNextProceedsWhenNoCallRoutes(t *testing.T) {
	results := map[string]types.CallResult{
		"apollo": {Status: types.TurnCompleted},
		"zulu":   {Status: types.TurnCompleted, Next: &types.Route{Action: types.NextProceed}},
	}
	require.Equal(t, types.NextProceed, resolveNext(results).Action)
	require.Equal(t, types.NextProceed, resolveNext(nil).Action)
}

// Go randomizes map iteration order, so a selection that depends on it shows
// up as a flaky result here rather than as a wrong route in production.
func TestResolveNextIsDeterministicAcrossMapIterationOrder(t *testing.T) {
	actions := []types.NextAction{
		types.NextActivate, types.NextReturn, types.NextFail,
		types.NextCoordinate, types.NextWaitTool, types.NextWaitApproval,
	}
	results := make(map[string]types.CallResult, len(actions))
	names := []string{"delta", "alpha", "mike", "bravo", "zulu", "kilo"}
	for i, name := range names {
		results[name] = types.CallResult{
			Status: types.TurnCompleted,
			Next:   &types.Route{Action: actions[i], Reason: "from " + name},
		}
	}
	sort.Strings(names)
	for i := 0; i < 200; i++ {
		next := resolveNext(results)
		require.Equal(t, types.NextFail, next.Action, "iteration %d selected %q", i, next.Reason)
		require.Equal(t, "from mike", next.Reason)
	}
}

func TestRunSelectsMostSevereCallRouteForTeam(t *testing.T) {
	executor := &routingCallExecutor{routes: map[string]types.NextAction{
		"alpha": types.NextActivate,
		"zulu":  types.NextFail,
	}}
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(executor))
	runtime := newTestRuntime(registry, nil)

	result, err := runtime.Run(context.Background(), types.TeamTurnRequest{
		TeamTurn: types.TeamTurn{ID: "tt-1", TeamID: "route"},
		Team: types.Team{
			ID: "route",
			Calls: map[string]types.Call{
				"alpha": {ID: "alpha", Type: types.CallCommand, Command: &types.CommandSpec{Command: "a"}},
				"zulu":  {ID: "zulu", Type: types.CallCommand, Command: &types.CommandSpec{Command: "z"}},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, types.NextFail, result.Next.Action)
}

// usageCallExecutor reports a per-call token usage so the Team aggregate can
// be compared against the sum of its parts.
type usageCallExecutor struct{}

func (e *usageCallExecutor) Type() types.CallType { return types.CallCommand }

func (e *usageCallExecutor) Execute(_ context.Context, req types.CallRequest) (types.CallResult, error) {
	return types.CallResult{
		Status: types.TurnCompleted,
		Reply:  req.Call.ID + " completed",
		Usage: types.TokenUsage{
			PromptTokens:     len(req.Call.ID),
			CompletionTokens: 2,
			TotalTokens:      len(req.Call.ID) + 2,
		},
		Next: &types.Route{Action: types.NextProceed},
	}, nil
}

// team_result.Usage is the aggregate of CallResults[].Usage, not an
// independent measurement: a consumer must read one layer, never add them.
func TestRunUsageIsAggregateOfCallResultsNotAnExtraMeasurement(t *testing.T) {
	executor := &usageCallExecutor{}
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(executor))
	runtime := newTestRuntime(registry, nil)

	result, err := runtime.Run(context.Background(), types.TeamTurnRequest{
		TeamTurn: types.TeamTurn{ID: "tt-1", TeamID: "usage"},
		Team: types.Team{
			ID: "usage",
			Calls: map[string]types.Call{
				"alpha": {ID: "alpha", Type: types.CallCommand, Command: &types.CommandSpec{Command: "a"}},
				"bravo": {ID: "bravo", Type: types.CallCommand, Command: &types.CommandSpec{Command: "b"}},
				// Already completed in an earlier turn: its usage is an input
				// to this turn's aggregate, not work to run again.
				"resumed": {ID: "resumed", Type: types.CallCommand, Command: &types.CommandSpec{Command: "r"}},
			},
		},
		ResumeResults: map[string]types.CallResult{
			"resumed": {
				Status: types.TurnCompleted,
				Usage:  types.TokenUsage{PromptTokens: 100, CompletionTokens: 1, TotalTokens: 101},
			},
		},
	})
	require.NoError(t, err)

	var sum types.TokenUsage
	for _, callResult := range result.CallResults {
		addUsage(&sum, callResult.Usage)
	}
	require.Equal(t, sum, result.Usage)
	require.Equal(t, 5+5+2+2+101, result.Usage.TotalTokens)
}

func TestAppendCallCompletedCarriesAttemptAndRecoveryOf(t *testing.T) {
	writer := &recordingSessionWriter{}
	runtime := newTestRuntime(call.NewRegistry(), nil)
	runtime.SetSessionWriter(writer)

	req := types.CallRequest{
		Call:        types.Call{ID: "alpha", Type: types.CallAgent, AgentID: "agent-a"},
		CallTurnID:  "tt-1:alpha",
		Attempt:     3,
		RecoveryOf:  "tt-0",
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "retry"},
		TeamSession: types.TeamSession{ID: "ts-1"},
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
	}
	result := types.CallResult{
		Status:     types.TurnCompleted,
		AgentID:    "agent-a",
		CallTurnID: req.CallTurnID,
		Usage:      types.TokenUsage{TotalTokens: 7},
		Next:       &types.Route{Action: types.NextProceed},
	}
	require.NoError(t, runtime.appendCallCompleted(context.Background(), req, result, nil))

	completed := writer.eventsOfType(types.EventAgentTurnCompleted)
	require.Len(t, completed, 1)
	assert.Equal(t, 3, completed[0].Attempt)
	assert.Equal(t, "tt-0", completed[0].RecoveryOf)
	assert.Equal(t, "alpha", completed[0].CallID)
}

// A retried TeamTurn must produce completed events the consumer can tell
// apart from the first attempt: without Attempt/RecoveryOf on the completed
// event, summing token usage over the event stream double counts every retry.
func TestRunWritesCompletedEventsCarryingAttempt(t *testing.T) {
	executor := &usageCallExecutor{}
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(executor))
	writer := &recordingSessionWriter{}
	runtime := newTestRuntime(registry, nil)
	runtime.SetSessionWriter(writer)

	_, err := runtime.Run(context.Background(), types.TeamTurnRequest{
		TeamTurn: types.TeamTurn{ID: "tt-2", TeamID: "usage", Attempt: 2, RecoveryOf: "tt-1"},
		Team: types.Team{
			ID: "usage",
			Calls: map[string]types.Call{
				"alpha": {ID: "alpha", Type: types.CallCommand, Command: &types.CommandSpec{Command: "a"}},
			},
		},
	})
	require.NoError(t, err)

	completed := writer.eventsOfType(types.EventCommandTurnCompleted)
	require.Len(t, completed, 1)
	assert.Equal(t, 2, completed[0].Attempt)
	assert.Equal(t, "tt-1", completed[0].RecoveryOf)
	assert.Equal(t, "alpha", completed[0].CallID)

	started := writer.eventsOfType(types.EventCommandTurnStarted)
	require.Len(t, started, 1)
	assert.Equal(t, completed[0].Attempt, started[0].Attempt,
		"started and completed events of one attempt must carry the same attempt")
	assert.Equal(t, fmt.Sprint(completed[0].RecoveryOf), fmt.Sprint(started[0].RecoveryOf))
}
