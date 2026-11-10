package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// The tool already built the request. Re-deriving it from the reply text loses
// whatever the parser does not know about, so the structured value wins.
func TestPendingInputPrefersStructuredValue(t *testing.T) {
	structured := &types.AgentPendingInput{
		Question: "Which repository and mode?",
		Header:   "Setup",
		Fields: []types.PendingInputField{
			{Name: "repo", Type: "string"},
			{Name: "mode", Type: "enum", Options: []string{"read", "write"}, Required: true},
		},
	}
	result := &types.AgentResult{
		Status: types.TurnWaitingInput,
		// Deliberately not JSON: the fallback parser would produce a request
		// with no fields at all, which is exactly the regression to catch.
		Reply:        "Which repository and mode?",
		PendingInput: structured,
	}

	pending := pendingInputFromResult(result)
	require.NotNil(t, pending)
	require.Same(t, structured, pending)
	require.Len(t, pending.Fields, 2)
	require.Equal(t, "enum", pending.Fields[1].Type)
}

// An older checkpoint, or the loop guard's prose question, has no structured
// value. That path must still yield a usable request.
func TestPendingInputFallsBackToReplyJSON(t *testing.T) {
	result := &types.AgentResult{
		Status: types.TurnWaitingInput,
		Reply: `{"question":"Which repository and mode?","header":"Setup",` +
			`"fields":[{"name":"repo","description":"target repository"},` +
			`{"name":"mode","type":"enum","options":["read","write"],"required":true,"default":"read"}]}`,
	}

	pending := pendingInputFromResult(result)
	require.NotNil(t, pending)
	require.Equal(t, "Which repository and mode?", pending.Question)
	require.Equal(t, "Setup", pending.Header)
	require.Len(t, pending.Fields, 2)
	require.Equal(t, "repo", pending.Fields[0].Name)
	require.Equal(t, "target repository", pending.Fields[0].Description)
	require.Equal(t, []string{"read", "write"}, pending.Fields[1].Options)
	require.True(t, pending.Fields[1].Required)
	require.Equal(t, "read", pending.Fields[1].Default)
}

// The loop guard asks in prose. There is no JSON to parse, so the reply itself
// is the question.
func TestPendingInputFallsBackToPlainText(t *testing.T) {
	result := &types.AgentResult{
		Status: types.TurnWaitingInput,
		Reply:  "I keep calling the same tool. How should I proceed?",
	}

	pending := pendingInputFromResult(result)
	require.NotNil(t, pending)
	require.Equal(t, "I keep calling the same tool. How should I proceed?", pending.Question)
	require.Empty(t, pending.Fields)
}

// A completed turn is not waiting for anything, whatever fields it carries.
func TestPendingInputIgnoredWhenTurnDidNotSuspend(t *testing.T) {
	result := &types.AgentResult{
		Status:       types.TurnCompleted,
		PendingInput: &types.AgentPendingInput{Question: "ignored"},
	}
	require.Nil(t, pendingInputFromResult(result))
	require.Nil(t, pendingInputFromResult(nil))
}
