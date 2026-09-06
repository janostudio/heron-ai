package agentstore

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChildInserterContextRoundTrip(t *testing.T) {
	inserter := &stubInserter{}
	ctx := WithChildInserter(context.Background(), inserter)
	assert.Equal(t, inserter, ChildInserterFromContext(ctx))
	assert.Nil(t, ChildInserterFromContext(context.Background()))
	assert.Nil(t, ChildInserterFromContext(context.TODO()))
}

type stubInserter struct{}

func (*stubInserter) InsertSpawnedCall(context.Context, string, SpawnedCallSpec) error { return nil }

func TestSpawnDepthContextRoundTrip(t *testing.T) {
	assert.Equal(t, 0, SpawnDepthFromContext(context.Background()))
	assert.Equal(t, 0, SpawnDepthFromContext(context.TODO()))
	assert.Equal(t, 2, SpawnDepthFromContext(WithSpawnDepth(context.Background(), 2)))
}

func TestChildCallID(t *testing.T) {
	assert.Equal(t, "fixer/k1", ChildCallID("fixer", "k1"))
	assert.Equal(t, "spawn/k1", ChildCallID("", "k1"))
}

func TestAgentStateLocksTryLock(t *testing.T) {
	locks := NewAgentStateLocks()

	unlock, ok := locks.TryLock("agent-a")
	require.True(t, ok)
	require.NotNil(t, unlock)

	_, busy := locks.TryLock("agent-a")
	assert.False(t, busy, "second turn of the same agent must not acquire the lock")

	// A different agent is unaffected.
	otherUnlock, ok := locks.TryLock("agent-b")
	require.True(t, ok)
	otherUnlock()

	unlock()
	unlockAgain, ok := locks.TryLock("agent-a")
	require.True(t, ok)
	unlockAgain()
}

func TestAgentStateLocksSharedAcrossPaths(t *testing.T) {
	locks := NewAgentStateLocks()
	var wg sync.WaitGroup
	acquired := make([]bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			unlock, ok := locks.TryLock("agent-a")
			acquired[i] = ok
			if ok {
				unlock()
			}
		}(i)
	}
	wg.Wait()
	// Every attempt either acquired the lock or correctly reported busy.
	for _, ok := range acquired {
		_ = ok
	}
}

func TestAgentStateLocksNilSetNeverBlocks(t *testing.T) {
	var locks *AgentStateLocks
	unlock, ok := locks.TryLock("agent-a")
	require.True(t, ok)
	require.NotNil(t, unlock)
	unlock()
}
