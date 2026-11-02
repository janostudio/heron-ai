package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func sampleLimit() *limitError {
	return &limitError{
		Name:     "max_team_turns",
		Value:    100,
		Observed: 100,
		TopPairs: []string{"owner→worktree ×47", "worktree→owner ×46", "owner→fix ×7"},
		Recent:   []string{"owner→worktree", "worktree→owner", "owner→worktree", "worktree→owner"},
		Detail:   "usually a routing cycle; raise settings.json runtime.max_team_turns only if the hand-offs are legitimate",
	}
}

// The message lands in a log line, so it must name the cycle rather than only
// the threshold: a backstop fires when nothing else works, and this string is
// all the reader has.
func TestLimitErrorMessageNamesTheCycle(t *testing.T) {
	got := sampleLimit().Error()

	require.Contains(t, got, "max_team_turns: 100")
	require.Contains(t, got, "observed 100")
	require.Contains(t, got, "top:")
	require.Contains(t, got, "owner→worktree ×47")
	require.Contains(t, got, "recent:")
	require.Contains(t, got, "runtime.max_team_turns")
	require.NotContains(t, got, "\n", "the payload is JSON; embedded newlines hurt scanning")
}

// A page cannot mark a turn or highlight the looping edges from prose, so the
// same facts must also exist as fields.
func TestLimitErrorPayloadIsStructured(t *testing.T) {
	payload := sampleLimit().Payload()

	require.Equal(t, "max_team_turns", payload["name"])
	require.Equal(t, 100, payload["value"])
	require.Equal(t, 100, payload["observed"])
	require.Equal(t, []string{"owner→worktree ×47", "worktree→owner ×46", "owner→fix ×7"}, payload["top_pairs"])
	require.Len(t, payload["recent"], 4)
	// The human hint stays out of the payload: it is not data.
	require.NotContains(t, payload, "detail")
}

// A limit error with no edges (a threshold on a flat count) still renders.
func TestLimitErrorWithoutEdges(t *testing.T) {
	got := (&limitError{Name: "max_agent_rounds", Value: 200}).Error()
	require.Equal(t, "max_agent_rounds: 200", got)
}

// A tripped backstop must land in the turn event, not only in the log file:
// the log is not what a page reads.
func TestFinishTurnWritesLimitIntoPayload(t *testing.T) {
	rt, writer := newWakeupRuntime(t)
	started := startSession(t, rt)

	result := types.FlowTurnResult{
		Session: started.Session,
		Turn:    types.FlowTurn{ID: "ft_limit", Origin: turnOriginUser},
	}
	limit := sampleLimit()
	// finishTurn hands the cause back to the caller; the point of this test is
	// what it wrote, not what it returned.
	_, err := rt.finishTurn(context.Background(), result, types.SessionFailed, limit)
	require.Error(t, err, "the tripped limit is returned to the caller")

	var found map[string]any
	for _, event := range sessionEvents(t, writer, started.Session.ID) {
		if event.Type != types.EventFlowTurnCompleted {
			continue
		}
		raw, ok := event.Payload["limit"].(map[string]any)
		if ok {
			found = raw
		}
	}
	require.NotNil(t, found, "flow_turn event must carry the structured limit")
	require.Equal(t, "max_team_turns", found["name"])
	// The event round-trips through JSON, so numbers come back as float64.
	require.EqualValues(t, 100, found["value"])
	require.EqualValues(t, 100, found["observed"])
	require.NotEmpty(t, found["top_pairs"], "the looping edges must survive into the event")
}

// The same counts must render identically every run, or the error is hard to
// grep for across attempts.
func TestTopActivationPairsIsStable(t *testing.T) {
	counts := map[string]int{"b→c": 2, "a→b": 2, "c→a": 2}

	first := topActivationPairs(counts)
	for i := 0; i < 20; i++ {
		require.Equal(t, first, topActivationPairs(counts))
	}
}

// Only the top three edges are reported, so a wide fan-out does not turn the
// error into a dump.
func TestTopActivationPairsCapsAtThree(t *testing.T) {
	counts := map[string]int{"a→b": 5, "a→c": 4, "a→d": 3, "a→e": 2, "a→f": 1}

	got := topActivationPairs(counts)

	require.Len(t, got, 3)
	require.Equal(t, "a→b ×5", got[0])
	require.Equal(t, "a→c ×4", got[1])
	require.Equal(t, "a→d ×3", got[2])
}

// The entry Team has no activator; labelling it "→entry" would read as a
// missing value rather than as the start of the turn.
func TestActivationPairLabelsEntry(t *testing.T) {
	require.Equal(t, "entry→worktree", activationPair(activation{teamID: "worktree"}))
	require.Equal(t, "owner→worktree", activationPair(activation{teamID: "worktree", activator: "owner"}))
}
