package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type orderCapturingCompactor struct {
	groups [][]types.Message
}

func (c *orderCapturingCompactor) Compact(_ context.Context, groups [][]types.Message) (string, error) {
	c.groups = groups
	return "compacted", nil
}

// groupIndexes reads the "g<N>" marker each test message carries, in the order
// the compactor received them. Groups headed by an assistant reply carry no
// marker and are skipped: the marker is what makes the order observable.
func (c *orderCapturingCompactor) groupIndexes(t *testing.T) []int {
	t.Helper()
	indexes := make([]int, 0, len(c.groups))
	for _, group := range c.groups {
		if len(group) == 0 {
			continue
		}
		var index int
		if _, err := fmt.Sscanf(group[0].Content, "g%d", &index); err != nil {
			continue
		}
		indexes = append(indexes, index)
	}
	return indexes
}

func restoreNumberedGroups(t *testing.T, manager *MessageContextManager, count, charsPerMessage int) {
	t.Helper()
	messages := []types.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "anchor"},
	}
	for i := 1; i <= count; i++ {
		messages = append(messages, types.Message{
			Role:    "user",
			Content: fmt.Sprintf("g%d %s", i, strings.Repeat("x", charsPerMessage)),
		})
		messages = append(messages, types.Message{
			Role:    "assistant",
			Content: fmt.Sprintf("reply to g%d", i),
		})
	}
	require.NoError(t, manager.RestoreActive(messages))
}

// TestCompactionHandsGroupsToCompactorOldestFirst pins the order the compactor
// receives. The selection loop walks groups newest-first (it keeps the tail), so
// an unreversed slice described the conversation backwards: the mechanical
// summary and the LLM compactor's "Errors / Current Work / Next Step" template
// filled their sections with the wrong turns.
//
// RestoreActive rather than AddMessage: AddMessage compacts on every append once
// the threshold is crossed, which would drop the old groups before the test can
// observe the order they were handed over in.
func TestCompactionHandsGroupsToCompactorOldestFirst(t *testing.T) {
	compactor := &orderCapturingCompactor{}
	manager := NewContextManagerWithCompactor(types.ContextConfig{MaxInputTokens: 10000}, nil, compactor)
	restoreNumberedGroups(t, manager, 8, 4400)

	require.NoError(t, manager.CompactForce(context.Background()))

	indexes := compactor.groupIndexes(t)
	require.GreaterOrEqual(t, len(indexes), 2, "the budget must have dropped more than one group for this test to mean anything")
	for i := 1; i < len(indexes); i++ {
		require.Less(t, indexes[i-1], indexes[i],
			"groups must reach the compactor oldest-first, got %v", indexes)
	}
	require.Equal(t, len(indexes), indexes[len(indexes)-1]-indexes[0]+1,
		"dropped groups must be contiguous, got %v", indexes)
}

// TestCompactionForcedBranchIsAlsoChronological covers the other producer of
// `dropped`: when the budget already fits everything, the forced path appends a
// prefix of groups. That branch is ordered by construction and must stay that
// way after the loop fix.
func TestCompactionForcedBranchIsAlsoChronological(t *testing.T) {
	compactor := &orderCapturingCompactor{}
	manager := NewContextManagerWithCompactor(types.ContextConfig{MaxInputTokens: 100000}, nil, compactor)
	restoreNumberedGroups(t, manager, 4, 10)

	require.NoError(t, manager.CompactForce(context.Background()))

	indexes := compactor.groupIndexes(t)
	require.NotEmpty(t, indexes)
	for i := 1; i < len(indexes); i++ {
		require.Less(t, indexes[i-1], indexes[i],
			"forced branch must hand groups oldest-first, got %v", indexes)
	}
	require.Equal(t, 1, indexes[0])
}
