package flow

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// delegationTeamRuntime scripts a delegation chain:
//
//	coordinator -> delegator -> worker(s) -> back to the delegator -> coordinator
//
// The delegator is a Team that is not the coordinator but declares can_activate.
// Its steps are supplied by stepFn so each test drives a different route
// sequence. Workers finish with a plain proceed and no on_proceed, so where they
// return to is decided by the engine, which is what these tests observe.
type delegationTeamRuntime struct {
	mu      sync.Mutex
	runs    map[string]int
	callers map[string][]string
	stepFn  func(run int) *types.Route
	// coordinatorTarget is the Team the coordinator activates on its first run.
	// Empty means the delegator.
	coordinatorTarget string
}

func newDelegationRuntime(stepFn func(run int) *types.Route) *delegationTeamRuntime {
	return &delegationTeamRuntime{
		runs:    map[string]int{},
		callers: map[string][]string{},
		stepFn:  stepFn,
	}
}

func (r *delegationTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	id := req.TeamTurn.TeamID
	r.mu.Lock()
	r.runs[id]++
	run := r.runs[id]
	r.callers[id] = append(r.callers[id], req.TeamTurn.CallerTeam)
	r.mu.Unlock()

	switch id {
	case "coordinator":
		if run == 1 {
			target := r.coordinatorTarget
			if target == "" {
				target = "delegator"
			}
			return types.TeamTurnResult{
				Turn: req.TeamTurn,
				Next: &types.Route{Action: types.NextActivate, Teams: []string{target}},
			}, nil
		}
		return types.TeamTurnResult{Turn: req.TeamTurn, Reply: "done", Next: &types.Route{Action: types.NextProceed}}, nil
	case "delegator":
		return types.TeamTurnResult{Turn: req.TeamTurn, Next: r.stepFn(run)}, nil
	default:
		return types.TeamTurnResult{
			Turn: types.TeamTurn{Status: types.TurnCompleted},
			Next: &types.Route{Action: types.NextProceed},
		}, nil
	}
}

func delegationDefinitions(delegatorCanActivate []string) *types.Definitions {
	return &types.Definitions{
		Flow: types.Flow{
			ID:          "delegation",
			EntryTeamID: "coordinator",
			Teams: map[string]types.FlowTeamBinding{
				"coordinator": {ID: "coordinator", TeamID: "coordinator-team", Coordinator: true, CanActivate: []string{"delegator", "worker-a"}},
				"delegator":   {ID: "delegator", TeamID: "delegator-team", CanActivate: delegatorCanActivate},
				"worker-a":    {ID: "worker-a", TeamID: "worker-a-team"},
				"worker-b":    {ID: "worker-b", TeamID: "worker-b-team"},
				"worker-c":    {ID: "worker-c", TeamID: "worker-c-team"},
			},
		},
		Teams: map[string]types.Team{
			"coordinator-team": {ID: "coordinator-team"},
			"delegator-team":   {ID: "delegator-team"},
			"worker-a-team":    {ID: "worker-a-team"},
			"worker-b-team":    {ID: "worker-b-team"},
			"worker-c-team":    {ID: "worker-c-team"},
		},
	}
}

func startDelegation(t *testing.T, defs *types.Definitions, fake *delegationTeamRuntime, limits types.RuntimeLimits) (types.FlowTurnResult, error) {
	t.Helper()
	sessions := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	runtime := newTestRuntime(defs, fake, sessions, nil)
	runtime.SetLimits(limits)
	return runtime.Start(context.Background(), types.StartFlowRequest{FlowID: "delegation", Input: "go"})
}

func activate(teams ...string) *types.Route {
	return &types.Route{Action: types.NextActivate, Teams: teams}
}

func proceed() *types.Route { return &types.Route{Action: types.NextProceed} }

// A Team that declares can_activate may activate Teams of its own. The Teams it
// activates return to it, not to the coordinator, and siblings that finish in
// the same batch wake it once with all of their records visible.
func TestDelegatorActivatesTeamsAndGetsThemBack(t *testing.T) {
	fake := newDelegationRuntime(func(run int) *types.Route {
		switch run {
		case 1:
			return activate("worker-a")
		case 2:
			return activate("worker-b", "worker-c")
		default:
			return proceed()
		}
	})
	result, err := startDelegation(t,
		delegationDefinitions([]string{"worker-a", "worker-b", "worker-c"}), fake, types.RuntimeLimits{})
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)

	// Delegator runs: once when the coordinator activates it, once after
	// worker-a returns, once after worker-b and worker-c return together.
	require.Equal(t, 3, fake.runs["delegator"])
	require.Equal(t, 1, fake.runs["worker-a"])
	require.Equal(t, 1, fake.runs["worker-b"])
	require.Equal(t, 1, fake.runs["worker-c"])

	// Each worker was activated by the delegator.
	require.Equal(t, []string{"delegator"}, fake.callers["worker-a"])
	require.Equal(t, []string{"delegator"}, fake.callers["worker-b"])
	require.Equal(t, []string{"delegator"}, fake.callers["worker-c"])

	// The delegator's final proceed falls through to the coordinator.
	require.Equal(t, 2, fake.runs["coordinator"])
}

// A Team without can_activate still cannot activate anything.
func TestTeamWithoutCanActivateCannotActivate(t *testing.T) {
	fake := newDelegationRuntime(func(int) *types.Route { return activate("worker-a") })
	result, err := startDelegation(t, delegationDefinitions(nil), fake, types.RuntimeLimits{})
	require.Error(t, err)
	require.Equal(t, types.SessionFailed, result.Session.Status)
	require.Contains(t, result.Error, "cannot activate other teams")
	require.Equal(t, 0, fake.runs["worker-a"])
}

// can_activate also bounds which Teams a delegator may name.
func TestDelegatorCannotActivateOutsideItsCanActivate(t *testing.T) {
	fake := newDelegationRuntime(func(int) *types.Route { return activate("worker-b") })
	result, err := startDelegation(t, delegationDefinitions([]string{"worker-a"}), fake, types.RuntimeLimits{})
	require.Error(t, err)
	require.Equal(t, types.SessionFailed, result.Session.Status)
	require.Contains(t, result.Error, `cannot activate "worker-b"`)
	require.Equal(t, 0, fake.runs["worker-b"])
}

// A delegator may activate the same Team again after it returns. Repeats are
// bounded by max_activation_retries like any other repeated activation, so the
// engine's loop guard still applies.
func TestDelegatorRepeatActivationIsBoundedByActivationRetries(t *testing.T) {
	step := func(run int) *types.Route {
		if run <= 2 {
			return activate("worker-a")
		}
		return proceed()
	}

	t.Run("within the limit", func(t *testing.T) {
		fake := newDelegationRuntime(step)
		result, err := startDelegation(t,
			delegationDefinitions([]string{"worker-a"}), fake, types.RuntimeLimits{MaxActivationRetries: 2})
		require.NoError(t, err)
		require.Equal(t, types.SessionWaitingInput, result.Session.Status)
		require.Equal(t, 2, fake.runs["worker-a"])
		require.Equal(t, 3, fake.runs["delegator"])
	})

	t.Run("over the limit", func(t *testing.T) {
		fake := newDelegationRuntime(step)
		result, err := startDelegation(t,
			delegationDefinitions([]string{"worker-a"}), fake, types.RuntimeLimits{MaxActivationRetries: 1})
		require.Error(t, err)
		require.Equal(t, types.SessionFailed, result.Session.Status)
		require.Contains(t, result.Error, "repeated Flow activation")
	})
}

// A Team activated directly by the coordinator still returns to the
// coordinator; only Teams activated by a delegator return to it.
func TestTeamReturnsToWhoeverActivatedIt(t *testing.T) {
	fake := newDelegationRuntime(func(run int) *types.Route {
		if run == 1 {
			return activate("worker-a")
		}
		return proceed()
	})
	_, err := startDelegation(t, delegationDefinitions([]string{"worker-a"}), fake, types.RuntimeLimits{})
	require.NoError(t, err)
	require.Equal(t, []string{"delegator"}, fake.callers["worker-a"])
	require.Equal(t, 2, fake.runs["coordinator"], "worker-a's return must not wake the coordinator")
}

// With background dispatch enabled, only the coordinator's activations go
// through the mailbox. A delegator's targets run in the current turn and return
// to it, rather than posting their result to the coordinator's inbox.
func TestDelegatorActivationBypassesBackgroundDispatch(t *testing.T) {
	fake := newDelegationRuntime(func(run int) *types.Route {
		if run == 1 {
			return activate("worker-a")
		}
		return proceed()
	})
	defs := delegationDefinitions([]string{"worker-a"})
	defs.Flow.EntryTeamID = "delegator"

	sessions := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	runtime := newTestRuntime(defs, fake, sessions, nil)
	runtime.SetAsyncDispatch(true)

	_, err := runtime.Start(context.Background(), types.StartFlowRequest{FlowID: "delegation", Input: "go"})
	require.NoError(t, err)
	require.Equal(t, 1, fake.runs["worker-a"])
	require.Equal(t, []string{"delegator"}, fake.callers["worker-a"])
}

// With background dispatch enabled, a coordinator that activates a delegator
// must hand it to the current turn rather than the mailbox: the background
// path runs a single Team turn and discards its route, so the delegator's own
// activations would never happen.
func TestCoordinatorActivatingDelegatorStaysInTurnUnderBackgroundDispatch(t *testing.T) {
	fake := newDelegationRuntime(func(run int) *types.Route {
		if run == 1 {
			return activate("worker-a")
		}
		return proceed()
	})
	sessions := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	runtime := newTestRuntime(delegationDefinitions([]string{"worker-a"}), fake, sessions, nil)
	runtime.SetAsyncDispatch(true)

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{FlowID: "delegation", Input: "go"})
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)

	require.Equal(t, 1, fake.runs["worker-a"], "the delegator's activation must run, not be dropped")
	require.Equal(t, []string{"delegator"}, fake.callers["worker-a"])
	require.Equal(t, 2, fake.runs["delegator"], "delegator wakes again when worker-a returns")
	require.Equal(t, 2, fake.runs["coordinator"], "coordinator resumes after the delegator proceeds")
}

// Background dispatch is still used for Teams that do not delegate.
func TestCoordinatorStillDispatchesPlainTeamsInBackground(t *testing.T) {
	fake := newDelegationRuntime(proceed2)
	fake.coordinatorTarget = "worker-a"
	sessions := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	runtime := newTestRuntime(delegationDefinitions([]string{"worker-a"}), fake, sessions, nil)
	runtime.SetAsyncDispatch(true)

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{FlowID: "delegation", Input: "go"})
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)
	require.Equal(t, 0, fake.runs["delegator"])
}

func proceed2(int) *types.Route { return proceed() }
