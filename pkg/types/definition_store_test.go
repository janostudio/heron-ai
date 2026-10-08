package types

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// storeFixture builds a minimal but structurally real Definitions tree. Tests
// compare trees by the marker value inside them, so the fixture only needs one
// distinguishing field per tree rather than a fully valid configuration.
func storeFixture(marker string) *Definitions {
	return &Definitions{
		Flow:  Flow{ID: marker, EntryTeamID: marker},
		Teams: map[string]Team{marker: {ID: marker, Goal: marker}},
		Agents: map[string]AgentConfig{
			marker: {Name: marker, Body: marker},
		},
	}
}

func TestDefinitionStoreSnapshotIsStable(t *testing.T) {
	original := storeFixture("original")
	store := NewDefinitionStore(original, "/config", "/config/flows/default.yml")

	before := store.Snapshot()
	require.Same(t, original, before)

	store.Swap(storeFixture("replacement"))

	// A snapshot taken before the swap must keep describing the tree that was
	// live when it was taken; a turn already in flight resolves its definitions
	// against it and must not observe a half-switched world.
	require.Same(t, original, before, "pre-swap snapshot must not be repointed by Swap")
	require.Equal(t, "original", before.Flow.ID)
	require.Equal(t, "original", before.Teams["original"].Goal)
	require.Equal(t, "original", before.Agents["original"].Body)
	require.NotContains(t, before.Teams, "replacement")

	after := store.Snapshot()
	require.NotSame(t, before, after)
	require.Equal(t, "replacement", after.Flow.ID)
}

func TestDefinitionStoreSwapNilIsNoop(t *testing.T) {
	original := storeFixture("original")
	store := NewDefinitionStore(original, "/config", "/config/flows/default.yml")

	store.Swap(nil)
	require.Same(t, original, store.Snapshot())

	// A nil store must swallow the call too rather than panic, because a
	// consumer that lost its store reference should fail loudly at its own
	// boundary, not inside a setter.
	var nilStore *DefinitionStore
	require.NotPanics(t, func() { nilStore.Swap(storeFixture("ignored")) })
}

func TestDefinitionStoreReloadPublishesOnSuccess(t *testing.T) {
	original := storeFixture("original")
	store := NewDefinitionStore(original, "/config", "/config/flows/default.yml")

	fresh := storeFixture("fresh")
	var gotCtx context.Context
	err := store.Reload(context.Background(), func(ctx context.Context) (*Definitions, error) {
		gotCtx = ctx
		return fresh, nil
	})
	require.NoError(t, err)
	require.NotNil(t, gotCtx, "reload must hand the caller's context to the loader")
	require.Same(t, fresh, store.Snapshot())
}

func TestDefinitionStoreReloadKeepsOldTreeOnError(t *testing.T) {
	original := storeFixture("original")
	store := NewDefinitionStore(original, "/config", "/config/flows/default.yml")

	loadErr := errors.New("teams/broken.yml: duplicate team definition")
	err := store.Reload(context.Background(), func(context.Context) (*Definitions, error) {
		// The loader may have built a partial tree before failing; the store
		// must ignore it entirely.
		return storeFixture("partial"), loadErr
	})
	require.ErrorIs(t, err, loadErr)
	require.Same(t, original, store.Snapshot(),
		"a failed reload must leave the previous tree running")

	// A loader that reports success without a tree is a loader bug; treating it
	// as a successful reload would clear the engine's definitions.
	err = store.Reload(context.Background(), func(context.Context) (*Definitions, error) {
		return nil, nil
	})
	require.Error(t, err)
	require.Same(t, original, store.Snapshot())
}

func TestDefinitionStoreNilReceiver(t *testing.T) {
	var store *DefinitionStore

	require.Nil(t, store.Snapshot())
	require.Empty(t, store.ConfigRoot())
	require.Empty(t, store.FlowPath())
	require.NotPanics(t, func() {
		// A nil store has nothing to reload into, so a loader would be wasted
		// work; it must not be invoked and must not panic.
		err := store.Reload(context.Background(), func(context.Context) (*Definitions, error) {
			t.Fatal("loader must not run for a nil store")
			return nil, nil
		})
		require.NoError(t, err)
	})
}

func TestDefinitionStoreConcurrentAccess(t *testing.T) {
	store := NewDefinitionStore(storeFixture("seed"), "/config", "/config/flows/default.yml")

	const (
		readers = 64
		writers = 8
		rounds  = 32
	)

	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < rounds; j++ {
				snapshot := store.Snapshot()
				require.NotNil(t, snapshot)
				// Touch the tree to force the race detector to observe reads of
				// the data behind the pointer, not just the pointer itself.
				_ = len(snapshot.Teams)
				_ = snapshot.Flow.ID
				_ = store.ConfigRoot()
				_ = store.FlowPath()
			}
		}()
	}

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			for j := 0; j < rounds; j++ {
				store.Swap(storeFixture("writer"))
			}
		}(i)
	}

	close(start)
	wg.Wait()

	require.NotNil(t, store.Snapshot())
}
