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

// blockingTeamRuntime blocks the activated "verify" team on a gate channel so a
// test can prove the dispatching turn does not wait for it. The coordinator
// ("default") activates "verify" once, exactly like fakeTeamRuntime.
type blockingTeamRuntime struct {
	gate    chan struct{} // closed to release "verify"
	started chan string   // receives the team ID when "verify" starts

	mu        sync.Mutex
	activated bool
}

func (b *blockingTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	switch req.TeamTurn.TeamID {
	case "default":
		b.mu.Lock()
		activated := b.activated
		b.activated = true
		b.mu.Unlock()
		if activated {
			return types.TeamTurnResult{
				Turn:  req.TeamTurn,
				Reply: "coordination finished",
				Next:  &types.Route{Action: types.NextProceed},
			}, nil
		}
		return types.TeamTurnResult{
			Turn:  req.TeamTurn,
			Reply: "starting diagnosis",
			Next: &types.Route{
				Action: types.NextActivate,
				Teams:  []string{"verify"},
			},
		}, nil
	case "verify":
		if b.started != nil {
			select {
			case b.started <- req.TeamTurn.TeamID:
			default:
			}
		}
		<-b.gate // block until the test releases the team
		return types.TeamTurnResult{
			Turn:    req.TeamTurn,
			Reply:   "verification passed",
			Records: []types.SharedRecord{{RecordID: "verification-1", Name: "VerificationReport", Scope: types.RecordScopeFlow, Status: types.RecordActive, Revision: 1}},
			Next:    &types.Route{Action: types.NextReturn},
		}, nil
	default:
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Next: &types.Route{Action: types.NextFail, Reason: "unexpected team"},
		}, nil
	}
}

// TestAsyncDispatchDoesNotBlockOnDispatchedTeam is the core P0 assertion: an
// async-dispatched team is executed in the background, so the coordinator's
// FlowTurn returns immediately (waiting_input) even while the team is still
// blocked; after the team is released, the work message lands delivered and a
// pending result message appears in the coordinator's inbox.

func TestAsyncDispatchDoesNotWakeRunningSession(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	fake := &blockingTeamRuntime{
		gate:    make(chan struct{}),
		started: make(chan string, 1),
	}
	runtime := newTestRuntime(testDefinitions(), fake, sessionWriter, nil)
	runtime.SetAsyncDispatch(true)

	var mu sync.Mutex
	var wakes []string
	runtime.SetWaker(func(sessionID string) {
		mu.Lock()
		wakes = append(wakes, sessionID)
		mu.Unlock()
	})
	wakeCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(wakes)
	}

	// Drive the turn directly so we can hold the session in "running" while
	// the background dispatch completes.
	ctx := context.Background()
	session := types.FlowSession{
		ID:        newID("fs"),
		FlowID:    "code-fix",
		Status:    types.SessionCreated,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, runtime.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventFlowSessionCreated, FlowSessionID: session.ID},
		Payload:     map[string]any{"session": session},
	}))

	turn := types.FlowTurn{ID: newID("ft"), FlowSessionID: session.ID, Status: types.TurnRunning}
	msg, err := runtime.enqueueWorkMessage(ctx, session, turn,
		types.MailboxAddress{TeamID: "verify"},
		map[string]any{"caller_team": "default"},
	)
	require.NoError(t, err)

	// Hold the session in running and start the dispatch.
	running := session
	running.Status = types.SessionRunning
	require.NoError(t, runtime.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventFlowSessionUpdated, FlowSessionID: session.ID},
		Payload:     map[string]any{"session": running},
	}))
	runtime.launchAsyncDispatch(session, turn.ID, "verify", msg)

	<-fake.started
	close(fake.gate)

	// The dispatch finishes, but the session is running, so no wake.
	require.Eventually(t, func() bool {
		box, err := runtime.loadMailbox(ctx, session.ID)
		if err != nil {
			return false
		}
		for _, m := range box {
			if m.Kind == mailboxKindResult {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, 0, wakeCount(), "running session must not be woken")
}
