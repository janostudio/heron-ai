package storage

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestValidatePathSegmentAcceptsRealIDs(t *testing.T) {
	for _, value := range []string{"fs_001", "s13", "agent-1", "team.owner", "a", "0d68726-abc_DEF.2"} {
		require.NoError(t, ValidatePathSegment("session id", value), "value %q must be accepted", value)
	}
}

func TestValidatePathSegmentRejectsEscapes(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"blank":             "   ",
		"parent":            "..",
		"current":           ".",
		"dots":              "...",
		"relative escape":   "../../etc/passwd",
		"absolute":          "/tmp/heron",
		"windows absolute":  `C:\tmp\heron`,
		"backslash":         `a\b`,
		"slash":             "a/b",
		"nul":               "a\x00b",
		"trailing space":    "fs_001 ",
		"leading space":     " fs_001",
		"newline":           "fs_001\n",
		"colon":             "fs:001",
		"over long segment": strings.Repeat("a", maxPathSegmentLength+1),
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			require.Error(t, ValidatePathSegment("session id", value), "value %q must be rejected", value)
		})
	}
}

// TestSessionWriterRejectsTraversalIDs covers the entry point the JSON-RPC and
// HTTP layers reach: the id used to be joined into .agents/data/sessions
// unchecked, so "../x" wrote outside the workspace.
func TestSessionWriterRejectsTraversalIDs(t *testing.T) {
	writer := NewJSONLSessionWriter(NewFileStore(t.TempDir()))
	event := SessionEvent{EventHeader: types.EventHeader{Type: types.EventFlowSessionCreated}}

	for _, id := range []string{"../../escaped", "/tmp/escaped", "a/b"} {
		_, err := writer.Append(context.Background(), id, LayerFlow, event)
		require.Error(t, err, "Append must reject session id %q", id)

		_, err = writer.Replay(context.Background(), id)
		require.Error(t, err, "Replay must reject session id %q", id)

		_, err = writer.Subscribe(context.Background(), id, 0)
		require.Error(t, err, "Subscribe must reject session id %q", id)
	}
}

func TestEvidenceStoreRejectsTraversalIDs(t *testing.T) {
	store := NewJSONLEvidenceStore(NewFileStore(t.TempDir()))
	record := types.SharedRecord{RecordID: "r1", Name: "OwnerReply"}

	require.Error(t, store.Publish(context.Background(), "../../escaped", record))
	_, err := store.List(context.Background(), "../../escaped", "")
	require.Error(t, err)
}
