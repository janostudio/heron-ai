package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestTranscriptIDSeparatesTeamInstances is the isolation contract for
// conversations: an isolated instance gets its own transcript, while a shared
// instance keys on the Team alone.
func TestTranscriptIDSeparatesTeamInstances(t *testing.T) {
	unscoped := types.AgentRequest{FlowSessionID: "fs", TeamID: "dev", CallID: "code"}
	require.Equal(t, "fs:dev:code", transcriptID(unscoped))

	shared := types.AgentRequest{
		FlowSessionID: "fs", TeamID: "dev", TeamSessionID: "fs:dev", CallID: "code",
	}
	require.Equal(t, "fs:dev:code", transcriptID(shared),
		"a shared instance keys on the Team alone")

	first := types.AgentRequest{
		FlowSessionID: "fs", TeamID: "dev", TeamSessionID: "fs:dev:1", CallID: "code",
	}
	second := types.AgentRequest{
		FlowSessionID: "fs", TeamID: "dev", TeamSessionID: "fs:dev:2", CallID: "code",
	}
	require.Equal(t, "fs:dev:1:code", transcriptID(first))
	require.Equal(t, "fs:dev:2:code", transcriptID(second))
	require.NotEqual(t, transcriptID(first), transcriptID(second),
		"two isolated instances must not share a conversation")
}
