package flow

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// These tests pin the peer model of the collaboration network: Teams are equal,
// and the only structural difference is which one is entered first. Routing is
// derived from who activated whom (callerTeam) and from each binding's
// can_activate. No Team is special-cased by role.

// peerRuntime scripts a Team's route per (team, run) and records how often each
// Team ran, who activated it, and which records it saw.
type peerRuntime struct {
	mu      sync.Mutex
	runs    map[string]int
	callers map[string][]string
	seen    map[string][]string
	script  map[string]func(run int) types.TeamTurnResult
}

func newPeerRuntime(script map[string]func(run int) types.TeamTurnResult) *peerRuntime {
	return &peerRuntime{
		runs:    map[string]int{},
		callers: map[string][]string{},
		seen:    map[string][]string{},
		script:  script,
	}
}

func (r *peerRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	id := req.TeamTurn.TeamID
	r.mu.Lock()
	r.runs[id]++
	run := r.runs[id]
	r.callers[id] = append(r.callers[id], req.TeamTurn.CallerTeam)
	for _, rec := range req.Records {
		r.seen[id] = append(r.seen[id], rec.Name)
	}
	step := r.script[id]
	r.mu.Unlock()

	var result types.TeamTurnResult
	if step != nil {
		result = step(run)
	} else {
		result = types.TeamTurnResult{Next: &types.Route{Action: types.NextProceed}}
	}
	if result.Turn.ID == "" {
		result.Turn = req.TeamTurn
	}
	return result, nil
}

func peerFail(reason string) types.TeamTurnResult {
	return types.TeamTurnResult{
		Error: reason,
		Turn:  types.TeamTurn{Status: types.TurnFailed},
		Next:  &types.Route{Action: types.NextFail, Reason: reason},
	}
}

// peerFlow builds a Flow whose bindings never mention a coordinator. activates
// maps a Team to the Teams it declares in can_activate.
func peerFlow(entry string, activates map[string][]string, teams ...string) *types.Definitions {
	bindings := map[string]types.FlowTeamBinding{}
	defs := map[string]types.Team{}
	for _, name := range teams {
		bindings[name] = types.FlowTeamBinding{ID: name, TeamID: name + "-team", CanActivate: activates[name]}
		defs[name+"-team"] = types.Team{ID: name + "-team"}
	}
	return &types.Definitions{
		Flow:  types.Flow{ID: "peer", EntryTeamID: entry, Teams: bindings},
		Teams: defs,
	}
}

func runPeer(t *testing.T, defs *types.Definitions, fake *peerRuntime, limits types.RuntimeLimits, async bool) (types.FlowTurnResult, error) {
	_, result, err := runPeerRuntime(t, defs, fake, limits, async)
	return result, err
}

// runPeerRuntime is runPeer plus the Runtime it built, for tests that have to
// wait on the background chain the run started.
func runPeerRuntime(t *testing.T, defs *types.Definitions, fake *peerRuntime, limits types.RuntimeLimits, async bool) (*Runtime, types.FlowTurnResult, error) {
	t.Helper()
	sessions := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	runtime := newTestRuntime(defs, fake, sessions, nil)
	runtime.SetLimits(limits)
	runtime.SetAsyncDispatch(async)
	if async {
		// Mirror the app wiring: a finished dispatched Team wakes the session
		// so the next Team with mail is taken. Without a waker the chain
		// stops after the first dispatch and nothing else ever runs.
		runtime.SetWaker(func(sessionID string) {
			go func() {
				if _, err := runtime.ResumeForMailbox(context.Background(), sessionID); err != nil {
					t.Logf("resume for mailbox: %v", err)
				}
			}()
		})
	}
	result, err := runtime.Start(context.Background(), types.StartFlowRequest{FlowID: "peer", Input: "go"})
	return runtime, result, err
}

// Any Team that declares can_activate may activate other Teams, entry or not.
// Teams it activates return to it, and siblings returning in one batch wake it
// once with all of their results.

func TestEntryProceedWaitsForInput(t *testing.T) {
	fake := newPeerRuntime(nil)
	defs := peerFlow("entry", nil, "entry")

	result, err := runPeer(t, defs, fake, types.RuntimeLimits{}, false)
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)
	require.Equal(t, 1, fake.runs["entry"])
}

// A failed Team reports to whoever activated it, not to a fixed Team. The
// activator sees the TeamFailureReport and can decide what to do.

func TestEntryFailureFailsSession(t *testing.T) {
	fake := newPeerRuntime(map[string]func(int) types.TeamTurnResult{
		"entry": func(int) types.TeamTurnResult { return peerFail("entry broke") },
	})
	defs := peerFlow("entry", nil, "entry")

	result, err := runPeer(t, defs, fake, types.RuntimeLimits{}, false)
	require.Error(t, err)
	require.Equal(t, types.SessionWaitingInput, result.Session.Status, "a failed turn must leave the session continuable")
	require.Equal(t, types.TurnFailed, result.Turn.Status)
}

// TeamFailureReport is an ordinary record. A Team that does not list it in its
// inputs does not see it; no Team receives it implicitly.
