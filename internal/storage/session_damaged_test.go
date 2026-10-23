package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestReplayToleratesDamagedLine is the regression guard for a single bad byte
// killing a session: decode returned an error, and since the first Append
// initialises its sequence counter from a replay, the session became
// permanently unreadable *and* unwritable.
func TestReplayToleratesDamagedLine(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	writer := NewJSONLSessionWriter(store)
	ctx := context.Background()
	sessionID := "fs_damaged"

	_, err := writer.Append(ctx, sessionID, LayerFlow, SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventFlowSessionCreated, FlowSessionID: sessionID},
		Payload:     map[string]any{"session": types.FlowSession{ID: sessionID}},
	})
	require.NoError(t, err)

	// Corrupt the file the way a torn write or a manual edit would.
	path := filepath.Join(dir, ".agents", "data", "sessions", sessionID, "flow.jsonl")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, []byte("{not json\n")...), 0o644))

	replay, err := writer.Replay(ctx, sessionID)
	require.NoError(t, err, "one damaged line must not make the session unreadable")
	require.Equal(t, 1, replay.Skipped)
	require.Len(t, replay.Events, 1)

	// And the session must still be appendable.
	_, err = writer.Append(ctx, sessionID, LayerFlow, SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventFlowSessionUpdated, FlowSessionID: sessionID},
		Payload:     map[string]any{"session": types.FlowSession{ID: sessionID}},
	})
	require.NoError(t, err, "the session must stay writable after a damaged line")
}
