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
	startCh  chan string
}

func (c *concurrentTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	switch req.TeamTurn.TeamID {
	case "entry":
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
	rt.SetWaker(func(sessionID string) {
		go func() { _, _ = rt.ResumeForMailbox(context.Background(), sessionID) }()
	})

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

	// Let the chain settle so the background goroutines finish before the
	// temp dir goes away.
	require.Eventually(t, func() bool {
		box, err := rt.loadMailbox(context.Background(), result.Session.ID)
		if err != nil {
			return false
		}
		for _, m := range box {
			if m.Status == types.MailboxPending || m.Status == types.MailboxClaimed {
				return false
			}
		}
		return true
	}, 5*time.Second, 20*time.Millisecond)
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
