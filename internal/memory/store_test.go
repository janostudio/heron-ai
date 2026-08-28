package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestStoreTeamMemoryUsesFixedMarkdownAndReloads(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{TeamMaxChars: 4000, MaxItems: 20})

	err := store.SaveTeam(context.Background(), types.MemorySnapshot{
		SessionID:     "fs-1",
		TeamID:        "diagnose",
		Goal:          "find the root cause",
		Confirmed:     []string{"callback path is reachable"},
		OpenQuestions: []string{"is retry idempotent?"},
		NextSteps:     []string{"inspect retry.go"},
		RecordIDs:     []string{"rec-1"},
	})
	require.NoError(t, err)

	data, err := files.Read(".agents/data/sessions/fs-1/teams/diagnose/memory.md")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), "---\n"))
	require.Contains(t, string(data), "# Confirmed")
	require.Contains(t, string(data), "callback path is reachable")

	loaded, err := store.LoadTeam(context.Background(), "fs-1", "diagnose")
	require.NoError(t, err)
	require.Equal(t, types.MemoryScopeTeam, loaded.Scope)
	require.Equal(t, "find the root cause", loaded.Goal)
	require.Equal(t, 1, loaded.Revision)
}

func TestStoreAgentMemoryHasLimit(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{AgentMaxChars: 300, MaxItems: 50})

	err := store.SaveAgent(context.Background(), types.MemorySnapshot{
		SessionID: "fs-1",
		TeamID:    "diagnose",
		CallID:    "inspect",
		Goal:      strings.Repeat("goal ", 100),
		Confirmed: []string{strings.Repeat("fact ", 100)},
	})
	require.NoError(t, err)
	data, readErr := files.Read(".agents/data/sessions/fs-1/agents/diagnose/inspect/memory.md")
	require.NoError(t, readErr)
	require.LessOrEqual(t, len(data), 300)
}

func TestReduceKeepsRecentItems(t *testing.T) {
	snapshot := Reduce(types.MemorySnapshot{
		Confirmed: []string{"one", "two", "three"},
	}, 2)
	require.Equal(t, []string{"two", "three"}, snapshot.Confirmed)
}
