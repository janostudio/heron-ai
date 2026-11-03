package state

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func newMergeStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore(storage.NewFileStore(t.TempDir()), Limits{})
	store.limits.MaxItems = 500
	return store
}

// TestSaveTeamMergesConcurrentWriters is the fix for the silent lost update:
// two instances of the same Team load state at their turn start and save at its
// end. The later save must add its entries without erasing the earlier one's.
func TestSaveTeamMergesConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	store := newMergeStore(t)

	// Instance A loaded state, then added its item and saved.
	first := types.StateSnapshot{
		SessionID: "fs", TeamID: "dev",
		Goal:      "fix the bug",
		Confirmed: []types.StateItem{{ID: "a1", Text: "A: root cause found"}},
	}
	require.NoError(t, store.SaveTeam(ctx, first))

	// Instance B loaded before A saved (so its copy lacks a1), then saved its
	// own addition.
	stale := types.StateSnapshot{
		SessionID: "fs", TeamID: "dev",
		NextSteps: []types.StateItem{{ID: "b1", Text: "B: applying fix"}},
	}
	require.NoError(t, store.SaveTeam(ctx, stale))

	loaded, err := store.LoadTeam(ctx, "fs", "dev")
	require.NoError(t, err)
	require.Equal(t, "fix the bug", loaded.Goal,
		"the incoming snapshot must not erase the goal it never saw")
	require.Len(t, loaded.Confirmed, 1, "A's entry must survive B's save")
	require.Len(t, loaded.NextSteps, 1, "B's entry must survive")
	require.Equal(t, "a1", loaded.Confirmed[0].ID)
	require.Equal(t, "b1", loaded.NextSteps[0].ID)
}

// TestSaveTeamConcurrentWritersAllLand is the same contract under real
// concurrency: N goroutines save from the same stale base, every entry lands.
func TestSaveTeamConcurrentWritersAllLand(t *testing.T) {
	ctx := context.Background()
	store := newMergeStore(t)
	const writers = 16

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			snapshot := types.StateSnapshot{
				SessionID: "fs", TeamID: "dev",
				NextSteps: []types.StateItem{{
					ID:   fmt.Sprintf("w-%02d", i),
					Text: fmt.Sprintf("writer %d", i),
				}},
			}
			require.NoError(t, store.SaveTeam(ctx, snapshot))
		}(i)
	}
	wg.Wait()

	loaded, err := store.LoadTeam(ctx, "fs", "dev")
	require.NoError(t, err)
	require.Len(t, loaded.NextSteps, writers,
		"every writer's entry must survive the merge")
	seen := make(map[string]bool, writers)
	for _, item := range loaded.NextSteps {
		seen[item.ID] = true
	}
	require.Len(t, seen, writers)
}

// TestSaveAgentMergesConcurrentWriters pins the same contract for the per-call
// agent state, which per_task instances of one Team share.
func TestSaveAgentMergesConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	store := newMergeStore(t)

	require.NoError(t, store.SaveAgent(ctx, types.StateSnapshot{
		SessionID: "fs", TeamID: "dev", CallID: "code",
		Confirmed: []types.StateItem{{ID: "a1", Text: "A: edit applied"}},
	}))
	require.NoError(t, store.SaveAgent(ctx, types.StateSnapshot{
		SessionID: "fs", TeamID: "dev", CallID: "code",
		RecordIDs: []string{"rec-2"},
	}))

	loaded, err := store.LoadAgent(ctx, "fs", "dev", "code")
	require.NoError(t, err)
	require.Len(t, loaded.Confirmed, 1)
	require.Equal(t, []string{"rec-2"}, loaded.RecordIDs)
}
