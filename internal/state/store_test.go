package state

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestStoreTeamStateUsesFixedMarkdownAndReloads(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{TeamMaxChars: 4000, MaxItems: 20})

	err := store.SaveTeam(context.Background(), types.StateSnapshot{
		SessionID:     "fs-1",
		TeamID:        "diagnose",
		Goal:          "find the root cause",
		Confirmed:     []types.StateItem{{ID: "i-1", Text: "callback path is reachable", AddedBy: "role-actor(e-1)", HandledBy: "role-actor(e-2)"}},
		OpenQuestions: []types.StateItem{{ID: "i-2", Text: "is retry idempotent?"}},
		NextSteps:     []types.StateItem{{ID: "i-3", Text: "inspect retry.go"}},
		RecordIDs:     []string{"rec-1"},
	})
	require.NoError(t, err)

	data, err := files.Read(".agents/data/sessions/fs-1/teams/diagnose/state.md")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), "---\n"))
	require.Contains(t, string(data), "# Confirmed")
	require.Contains(t, string(data), "callback path is reachable")
	require.Contains(t, string(data), "[added_by=role-actor(e-1), handled_by=role-actor(e-2)]")

	loaded, err := store.LoadTeam(context.Background(), "fs-1", "diagnose")
	require.NoError(t, err)
	require.Equal(t, types.StateScopeTeam, loaded.Scope)
	require.Equal(t, "find the root cause", loaded.Goal)
	require.Equal(t, 1, loaded.Revision)
	require.Equal(t, "role-actor(e-1)", loaded.Confirmed[0].AddedBy)
	require.Equal(t, "role-actor(e-2)", loaded.Confirmed[0].HandledBy)
}

func TestEncodeOmitsEmptyAttribution(t *testing.T) {
	// A plain item with no attribution must not render the bracket suffix.
	snapshot := types.StateSnapshot{
		SessionID: "fs-1",
		TeamID:    "t",
		Confirmed: []types.StateItem{{ID: "i-1", Text: "plain item"}},
	}
	data, err := encode(snapshot)
	require.NoError(t, err)
	require.Contains(t, string(data), "- i-1: plain item\n")
	require.NotContains(t, string(data), "added_by")
	require.NotContains(t, string(data), "handled_by")
}

func TestStoreAgentStateHasLimit(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{AgentMaxChars: 300, MaxItems: 50})

	err := store.SaveAgent(context.Background(), types.StateSnapshot{
		SessionID: "fs-1",
		TeamID:    "diagnose",
		CallID:    "inspect",
		Goal:      strings.Repeat("goal ", 100),
		Confirmed: []types.StateItem{{ID: "i-1", Text: strings.Repeat("fact ", 100)}},
	})
	require.NoError(t, err)
	data, readErr := files.Read(".agents/data/sessions/fs-1/agents/diagnose/inspect/state.md")
	require.NoError(t, readErr)
	require.LessOrEqual(t, len(data), 300)
}

func TestReduceKeepsRecentItems(t *testing.T) {
	snapshot := Reduce(types.StateSnapshot{
		Confirmed: []types.StateItem{
			{ID: "i-1", Text: "one"},
			{ID: "i-2", Text: "two"},
			{ID: "i-3", Text: "three"},
		},
	}, 2)
	require.Equal(t, []types.StateItem{{ID: "i-2", Text: "two"}, {ID: "i-3", Text: "three"}}, snapshot.Confirmed)
}
