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

func peerActivate(teams ...string) types.TeamTurnResult {
	return types.TeamTurnResult{Next: &types.Route{Action: types.NextActivate, Teams: teams}}
}

func peerProceed() types.TeamTurnResult {
	return types.TeamTurnResult{Next: &types.Route{Action: types.NextProceed}}
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
	t.Helper()
	sessions := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	runtime := newTestRuntime(defs, fake, sessions, nil)
	runtime.SetLimits(limits)
	runtime.SetAsyncDispatch(async)
	return runtime.Start(context.Background(), types.StartFlowRequest{FlowID: "peer", Input: "go"})
}

// Any Team that declares can_activate may activate other Teams, entry or not.
// Teams it activates return to it, and siblings returning in one batch wake it
// once with all of their results.
func TestPeerTeamActivatesAndReceivesReturns(t *testing.T) {
	fake := newPeerRuntime(map[string]func(int) types.TeamTurnResult{
		"entry": func(run int) types.TeamTurnResult {
			if run == 1 {
				return peerActivate("mid")
			}
			return peerProceed()
		},
		"mid": func(run int) types.TeamTurnResult {
			switch run {
			case 1:
				return peerActivate("leaf-a")
			case 2:
				return peerActivate("leaf-b", "leaf-c")
			default:
				return peerProceed()
			}
		},
	})
	defs := peerFlow("entry", map[string][]string{
		"entry": {"mid"},
		"mid":   {"leaf-a", "leaf-b", "leaf-c"},
	}, "entry", "mid", "leaf-a", "leaf-b", "leaf-c")

	result, err := runPeer(t, defs, fake, types.RuntimeLimits{}, false)
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)

	require.Equal(t, 3, fake.runs["mid"], "mid: activated, then leaf-a returns, then leaf-b and leaf-c return together")
	for _, leaf := range []string{"leaf-a", "leaf-b", "leaf-c"} {
		require.Equal(t, 1, fake.runs[leaf])
		require.Equal(t, []string{"mid"}, fake.callers[leaf], "%s must return to its activator", leaf)
	}
	require.Equal(t, 2, fake.runs["entry"], "entry runs again only when mid proceeds")
}

// A Team without can_activate cannot activate, whether or not it is the entry.
func TestTeamWithoutCanActivateCannotActivate(t *testing.T) {
	fake := newPeerRuntime(map[string]func(int) types.TeamTurnResult{
		"entry": func(int) types.TeamTurnResult { return peerActivate("leaf") },
	})
	defs := peerFlow("entry", nil, "entry", "leaf")

	result, err := runPeer(t, defs, fake, types.RuntimeLimits{}, false)
	require.Error(t, err)
	require.Equal(t, types.SessionFailed, result.Session.Status)
	require.Contains(t, result.Error, "cannot activate")
	require.Equal(t, 0, fake.runs["leaf"])
}

// can_activate also bounds the targets a Team may name.
func TestActivationOutsideCanActivateIsRejected(t *testing.T) {
	fake := newPeerRuntime(map[string]func(int) types.TeamTurnResult{
		"entry": func(int) types.TeamTurnResult { return peerActivate("other") },
	})
	defs := peerFlow("entry", map[string][]string{"entry": {"leaf"}}, "entry", "leaf", "other")

	result, err := runPeer(t, defs, fake, types.RuntimeLimits{}, false)
	require.Error(t, err)
	require.Equal(t, types.SessionFailed, result.Session.Status)
	require.Contains(t, result.Error, `cannot activate "other"`)
	require.Equal(t, 0, fake.runs["other"])
}

// The Team that is entered first is the only one with no activator. When it
// proceeds there is nobody to return to, so the session waits for the user.
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
func TestFailureReturnsToActivator(t *testing.T) {
	fake := newPeerRuntime(map[string]func(int) types.TeamTurnResult{
		"entry": func(run int) types.TeamTurnResult {
			if run == 1 {
				return peerActivate("mid")
			}
			return peerProceed()
		},
		"mid": func(run int) types.TeamTurnResult {
			if run == 1 {
				return peerActivate("leaf")
			}
			return peerProceed()
		},
		"leaf": func(int) types.TeamTurnResult { return peerFail("leaf broke") },
	})
	defs := peerFlow("entry", map[string][]string{
		"entry": {"mid"},
		"mid":   {"leaf"},
	}, "entry", "mid", "leaf")
	// mid declares it wants to hear about failures.
	binding := defs.Flow.Teams["mid"]
	binding.Inputs = types.InputSpec{FlowRecords: []string{"TeamFailureReport"}}
	defs.Flow.Teams["mid"] = binding

	result, err := runPeer(t, defs, fake, types.RuntimeLimits{}, false)
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)

	require.Equal(t, 2, fake.runs["mid"], "failure of leaf must come back to mid, its activator")
	require.Contains(t, fake.seen["mid"], "TeamFailureReport")
	require.Equal(t, 2, fake.runs["entry"], "entry only resumes once mid proceeds; the failure did not skip mid")
}

// When the entry Team itself fails there is no activator to report to, so the
// session fails.
func TestEntryFailureFailsSession(t *testing.T) {
	fake := newPeerRuntime(map[string]func(int) types.TeamTurnResult{
		"entry": func(int) types.TeamTurnResult { return peerFail("entry broke") },
	})
	defs := peerFlow("entry", nil, "entry")

	result, err := runPeer(t, defs, fake, types.RuntimeLimits{}, false)
	require.Error(t, err)
	require.Equal(t, types.SessionFailed, result.Session.Status)
}

// TeamFailureReport is an ordinary record. A Team that does not list it in its
// inputs does not see it; no Team receives it implicitly.
func TestTeamFailureReportIsNotInjectedImplicitly(t *testing.T) {
	fake := newPeerRuntime(map[string]func(int) types.TeamTurnResult{
		"entry": func(run int) types.TeamTurnResult {
			if run == 1 {
				return peerActivate("leaf")
			}
			return peerProceed()
		},
		"leaf": func(int) types.TeamTurnResult { return peerFail("leaf broke") },
	})
	defs := peerFlow("entry", map[string][]string{"entry": {"leaf"}}, "entry", "leaf")

	_, err := runPeer(t, defs, fake, types.RuntimeLimits{}, false)
	require.NoError(t, err)
	require.NotContains(t, fake.seen["entry"], "TeamFailureReport",
		"entry did not list TeamFailureReport in its inputs, so it must not see it")
}

// Repeated activation of the same target stays bounded by max_activation_retries
// for every Team, not only the entry.
func TestRepeatedActivationIsBoundedForAnyTeam(t *testing.T) {
	step := func(run int) types.TeamTurnResult {
		if run <= 2 {
			return peerActivate("leaf")
		}
		return peerProceed()
	}
	build := func() (*peerRuntime, *types.Definitions) {
		fake := newPeerRuntime(map[string]func(int) types.TeamTurnResult{
			"entry": func(run int) types.TeamTurnResult {
				if run == 1 {
					return peerActivate("mid")
				}
				return peerProceed()
			},
			"mid": step,
		})
		defs := peerFlow("entry", map[string][]string{
			"entry": {"mid"},
			"mid":   {"leaf"},
		}, "entry", "mid", "leaf")
		return fake, defs
	}

	t.Run("within the limit", func(t *testing.T) {
		fake, defs := build()
		result, err := runPeer(t, defs, fake, types.RuntimeLimits{MaxActivationRetries: 2}, false)
		require.NoError(t, err)
		require.Equal(t, types.SessionWaitingInput, result.Session.Status)
		require.Equal(t, 2, fake.runs["leaf"])
	})
	t.Run("over the limit", func(t *testing.T) {
		fake, defs := build()
		result, err := runPeer(t, defs, fake, types.RuntimeLimits{MaxActivationRetries: 1}, false)
		require.Error(t, err)
		require.Equal(t, types.SessionFailed, result.Session.Status)
		require.Contains(t, result.Error, "repeated Flow activation")
	})
}

// Background dispatch is chosen by the target, not by who activates it. A target
// that activates other Teams of its own must run inside the current turn so its
// activations and their returns are handled; a target that finishes by itself
// may run in the background. This holds for any activator.
func TestBackgroundDispatchDependsOnTargetNotActivator(t *testing.T) {
	t.Run("target that activates others runs in the turn", func(t *testing.T) {
		fake := newPeerRuntime(map[string]func(int) types.TeamTurnResult{
			"entry": func(run int) types.TeamTurnResult {
				if run == 1 {
					return peerActivate("mid")
				}
				return peerProceed()
			},
			"mid": func(run int) types.TeamTurnResult {
				if run == 1 {
					return peerActivate("leaf")
				}
				return peerProceed()
			},
		})
		defs := peerFlow("entry", map[string][]string{
			"entry": {"mid"},
			"mid":   {"leaf"},
		}, "entry", "mid", "leaf")

		result, err := runPeer(t, defs, fake, types.RuntimeLimits{}, true)
		require.NoError(t, err)
		require.Equal(t, types.SessionWaitingInput, result.Session.Status)
		require.Equal(t, 1, fake.runs["leaf"], "mid's own activation must not be dropped")
		require.Equal(t, []string{"mid"}, fake.callers["leaf"])
	})

	t.Run("target that finishes by itself may run in the background", func(t *testing.T) {
		fake := newPeerRuntime(map[string]func(int) types.TeamTurnResult{
			"entry": func(run int) types.TeamTurnResult {
				if run == 1 {
					return peerActivate("leaf")
				}
				return peerProceed()
			},
		})
		defs := peerFlow("entry", map[string][]string{"entry": {"leaf"}}, "entry", "leaf")

		result, err := runPeer(t, defs, fake, types.RuntimeLimits{}, true)
		require.NoError(t, err)
		require.Equal(t, types.SessionWaitingInput, result.Session.Status)
	})
}
