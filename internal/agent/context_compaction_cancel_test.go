package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// blockingCompactor never finishes on its own: it models a provider that
// accepted the request and stopped answering. It reports the context error it
// observed, which is what proves the cancellation reached it.
type blockingCompactor struct {
	entered  chan struct{}
	observed chan error
}

func newBlockingCompactor() *blockingCompactor {
	return &blockingCompactor{
		entered:  make(chan struct{}, 8),
		observed: make(chan error, 8),
	}
}

func (b *blockingCompactor) Compact(ctx context.Context, _ [][]types.Message) (string, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case b.observed <- ctx.Err():
	default:
	}
	return "", ctx.Err()
}

func newCompactionPressureManager(t *testing.T, compactor Compactor) *MessageContextManager {
	t.Helper()
	// Chosen so that a few 400-character messages cross the compaction
	// threshold while the post-compaction context still fits the hard limit:
	// the test needs the compactor to run, not the hard-limit failure path.
	manager := NewContextManagerWithCompactor(types.ContextConfig{MaxInputTokens: 300}, nil, compactor)
	require.NoError(t, manager.AddMessage(types.Message{Role: "system", Content: "system"}))
	return manager
}

// fillBelowThreshold adds messages that stay under the compaction threshold, so
// the next AddMessage is the one that triggers it. Two messages on purpose: a
// third 400-character message already crosses the limit with
// MaxInputTokens=300, and the plain AddMessage in this helper would then block
// on the compactor with no context to cancel it.
func fillBelowThreshold(t *testing.T, manager *MessageContextManager) {
	t.Helper()
	for i := 0; i < 2; i++ {
		require.NoError(t, manager.AddMessage(types.Message{
			Role:    "user",
			Content: strings.Repeat("x", 400),
		}))
	}
}

// TestAddMessageCompactionHonoursContext covers the defect: compaction ran on
// context.Background(), so a provider that stopped answering held the turn
// past its own deadline with nothing able to cancel the summarisation call.
func TestAddMessageCompactionHonoursContext(t *testing.T) {
	compactor := newBlockingCompactor()
	manager := newCompactionPressureManager(t, compactor)
	fillBelowThreshold(t, manager)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := manager.AddMessageContext(ctx, types.Message{Role: "user", Content: strings.Repeat("x", 400)})
	elapsed := time.Since(start)

	// The compactor failing is not fatal by design (the mechanical summary
	// takes over), so the assertion is about the cancellation reaching it and
	// the call returning promptly, not about the error value.
	_ = err
	require.Less(t, elapsed, 5*time.Second, "compaction must stop when the turn context ends")
	select {
	case observed := <-compactor.observed:
		require.ErrorIs(t, observed, context.DeadlineExceeded,
			"the compactor must see the turn context end, not a background context")
	case <-time.After(time.Second):
		t.Fatal("the compactor never observed a cancelled context")
	}
}

// TestCompactHookRunsOutsideTheManagerLock covers the second half: the hook is
// a session write, and running it while holding the manager mutex made every
// reader (Messages, Stats) wait for a disk append. A hook that reads the
// manager is the cheapest way to prove the lock is not held.
func TestCompactHookRunsOutsideTheManagerLock(t *testing.T) {
	manager := NewContextManagerWithCompactor(types.ContextConfig{MaxInputTokens: 300}, nil, mechanicalCompactor{})
	fillBelowThreshold(t, manager)

	readInside := make(chan struct{})
	manager.SetCompactHook(func(string, int) {
		// Would deadlock against a held write lock.
		_ = manager.Messages()
		close(readInside)
	})

	done := make(chan error, 1)
	go func() {
		done <- manager.AddMessage(types.Message{Role: "user", Content: strings.Repeat("y", 400)})
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("compaction hook deadlocked: it ran while the manager lock was held")
	}
	select {
	case <-readInside:
	default:
		t.Fatal("the compaction hook never ran")
	}
}
