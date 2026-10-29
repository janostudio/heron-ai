package state

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// The pointer names the store and the reading rules; it never carries an entry.
// An injected entry is what let a previous turn's verdict answer a new question.
func TestPointerText(t *testing.T) {
	text := PointerText("coordinator")
	assert.Contains(t, text, ".agents/data/agents/coordinator/state/")
	assert.Contains(t, text, `action="list"`)
	assert.Contains(t, text, "not the result of the current task")
	assert.Contains(t, text, "updated_at")
	assert.Contains(t, text, "in_progress")
	assert.NotContains(t, text, "Confirmed:")
	assert.Empty(t, PointerText(""))
	assert.Empty(t, PointerText("   "))
}

// ListWithMeta is the read path that replaced injection: the caller gets the
// entries and the metadata needed to judge whether they belong to this task.
func TestListWithMetaCarriesFreshness(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{})
	ctx := context.Background()

	before := time.Now().Add(-time.Minute).UTC()
	require.NoError(t, store.SetGoal(ctx, "worker", "fix the parser"))
	require.NoError(t, store.Add(ctx, "worker", FieldNextSteps, "verify the fix", "", "worker"))

	list, err := store.ListWithMeta(ctx, "worker", FieldNextSteps)
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	assert.Equal(t, "verify the fix", list.Items[0].Text)
	assert.Equal(t, "fix the parser", list.Goal)
	assert.Greater(t, list.Revision, 0, "the revision identifies the snapshot the entries came from")
	assert.False(t, list.UpdatedAt.IsZero(), "updated_at is the only age signal now that state is not injected")
	assert.True(t, list.UpdatedAt.After(before))
}

func TestListWithMetaUnknownField(t *testing.T) {
	store := NewStore(storage.NewFileStore(t.TempDir()), Limits{})
	_, err := store.ListWithMeta(context.Background(), "worker", StateField("bogus"))
	require.Error(t, err)
}

func TestListWithMetaEmptyGoalField(t *testing.T) {
	store := NewStore(storage.NewFileStore(t.TempDir()), Limits{})
	list, err := store.ListWithMeta(context.Background(), "worker", FieldGoal)
	require.NoError(t, err)
	assert.Empty(t, list.Items)
	assert.Empty(t, list.Goal)
	assert.Zero(t, list.Revision)
}

// The tool-facing result shape is what the model parses; keep its JSON tags
// stable.
func TestStateListJSONShape(t *testing.T) {
	encoded, err := json.Marshal(types.StateList{
		UpdatedAt: time.Unix(0, 0).UTC(),
		Revision:  3,
		Goal:      "g",
		Items:     []types.StateItem{{ID: "i-1", Text: "do it"}},
	})
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"updated_at"`)
	assert.Contains(t, string(encoded), `"revision":3`)
	assert.Contains(t, string(encoded), `"items":[{"id":"i-1","text":"do it"}]`)
}
