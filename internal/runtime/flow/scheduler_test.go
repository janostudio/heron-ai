package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// concurrentTeamRuntime gates each Team on its own channel, so a test can hold
// several Teams running at the same time and observe the overlap. The entry
// Team activates both leaves in one route.
type concurrentTeamRuntime struct {
	aGate chan struct{} // released to let "leaf-a" finish
	bGate chan struct{} // released to let "leaf-b" finish

	mu       sync.Mutex
	startedA bool
	startedB bool
	entryRun int
	startCh  chan string
}

func (c *concurrentTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	switch req.TeamTurn.TeamID {
	case "entry":
		c.mu.Lock()
		c.entryRun++
		first := c.entryRun == 1
		c.mu.Unlock()
		if !first {
			// Only the first round dispatches the leaves. Re-activating
			// them on every round keeps the chain alive until the
			// activation budget kills it — dozens of extra rounds whose
			// churn is not what this test is about, and which turn "the
			// chain has settled" into a moving target.
			return types.TeamTurnResult{Turn: req.TeamTurn, Next: &types.Route{Action: types.NextProceed}}, nil
		}
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Next: &types.Route{Action: types.NextActivate, Teams: []string{"leaf-a", "leaf-b"}},
		}, nil
	case "leaf-a":
		c.mu.Lock()
		c.startedA = true
		c.mu.Unlock()
		c.signal(req.TeamTurn.TeamID)
		<-c.aGate
		return types.TeamTurnResult{Turn: req.TeamTurn, Next: &types.Route{Action: types.NextProceed}}, nil
	case "leaf-b":
		c.mu.Lock()
		c.startedB = true
		c.mu.Unlock()
		c.signal(req.TeamTurn.TeamID)
		<-c.bGate
		return types.TeamTurnResult{Turn: req.TeamTurn, Next: &types.Route{Action: types.NextProceed}}, nil
	default:
		return types.TeamTurnResult{Turn: req.TeamTurn, Next: &types.Route{Action: types.NextProceed}}, nil
	}
}

func (c *concurrentTeamRuntime) signal(team string) {
	if c.startCh == nil {
		return
	}
	select {
	case c.startCh <- team:
	default:
	}
}

// TestConcurrentDispatchRunsTeamsInParallel is the concurrency assertion: two
// Teams dispatched from one route run at the same time. Both are held on their
// own gate, so the test only passes if the second starts while the first is
// still blocked — with a serialized scheduler the second would never start.
func TestConcurrentDispatchRunsTeamsInParallel(t *testing.T) {
	sessions := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	fake := &concurrentTeamRuntime{
		aGate:   make(chan struct{}),
		bGate:   make(chan struct{}),
		startCh: make(chan string, 2),
	}
	rt := newTestRuntime(peerFlow("entry", map[string][]string{
		"entry":  {"leaf-a", "leaf-b"},
		"leaf-a": {}, "leaf-b": {},
	}, "entry", "leaf-a", "leaf-b"), fake, sessions, nil)
	rt.SetAsyncDispatch(true)
	// No waker: this test is about the two Teams running at once, not about
	// the wake path (which dispatch_test.go and peer_model_test.go cover).
	// Without one the background work is exactly the two dispatch
	// goroutines, so "no Team is executing" is a deterministic end state.
	// With a waker the two results wake the entry at the same time and the
	// second wake is dropped on purpose while the first resume is running
	// (dispatch.wakeIfIdle), which leaves mail pending forever.

	result, err := rt.Start(context.Background(), types.StartFlowRequest{FlowID: "peer", Input: "go"})
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)

	// Both Teams must start while neither has been released.
	started := map[string]bool{}
	timeout := time.After(5 * time.Second)
	for len(started) < 2 {
		select {
		case team := <-fake.startCh:
			started[team] = true
		case <-timeout:
			t.Fatalf("expected both leaf-a and leaf-b to start concurrently, got %v", started)
		}
	}

	close(fake.aGate)
	close(fake.bGate)

	// Let the dispatch goroutines finish before the temp dir goes away:
	// they keep writing delivered/result events after Start returned, and a
	// RemoveAll that races those writes fails with "directory not empty" on
	// Linux.
	waitForDispatchersDone(t, rt, result.Session.ID)
}

// TestSameTeamDoesNotRunTwoRoundsAtOnce pins the guard: two messages addressed
// to the same Team never run concurrently, because a Team is one execution unit.
func TestSameTeamDoesNotRunTwoRoundsAtOnce(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	require.True(t, rt.tryMarkTeamBusy("owner"), "first claim wins")
	require.False(t, rt.tryMarkTeamBusy("owner"), "a busy team cannot be claimed again")
	rt.clearTeamBusy("owner")
	require.True(t, rt.tryMarkTeamBusy("owner"), "the slot is reusable after release")
}
