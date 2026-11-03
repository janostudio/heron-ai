package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestTranscriptIDSeparatesTeamInstances is the isolation contract for
// conversations: a per_task instance gets its own transcript, while a resident
// instance (and any request written before the field existed) keeps the
// historical key byte for byte.
func TestTranscriptIDSeparatesTeamInstances(t *testing.T) {
	legacy := types.AgentRequest{FlowSessionID: "fs", TeamID: "dev", CallID: "code"}
	require.Equal(t, "fs:dev:code", transcriptID(legacy))

	resident := types.AgentRequest{
		FlowSessionID: "fs", TeamID: "dev", TeamSessionID: "fs:dev", CallID: "code",
	}
	require.Equal(t, "fs:dev:code", transcriptID(resident),
		"a resident instance must keep the historical transcript key")

	first := types.AgentRequest{
		FlowSessionID: "fs", TeamID: "dev", TeamSessionID: "fs:dev:1", CallID: "code",
	}
	second := types.AgentRequest{
		FlowSessionID: "fs", TeamID: "dev", TeamSessionID: "fs:dev:2", CallID: "code",
	}
	require.Equal(t, "fs:dev:1:code", transcriptID(first))
	require.Equal(t, "fs:dev:2:code", transcriptID(second))
	require.NotEqual(t, transcriptID(first), transcriptID(second),
		"two per_task instances must not share a conversation")
}
