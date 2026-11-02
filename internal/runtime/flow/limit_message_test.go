package flow

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A backstop fires when the system has already stopped working, so its message
// is the only diagnostic left. It has to name the cycle, not just the count.
func TestDescribeActivationBurstNamesTheCycle(t *testing.T) {
	counts := map[string]int{
		"owner→worktree": 47,
		"worktree→owner": 46,
		"owner→fix":      7,
	}
	recent := []string{"owner→worktree", "worktree→owner", "owner→worktree", "worktree→owner"}

	got := describeActivationBurst(100, counts, recent)

	require.Contains(t, got, "activated 100")
	require.Contains(t, got, "top:")
	require.Contains(t, got, "owner→worktree ×47")
	require.Contains(t, got, "worktree→owner ×46")
	require.Contains(t, got, "recent:")
	require.Contains(t, got, "runtime.max_team_turns",
		"the message must point at the setting that raises the limit")
	t.Logf("flow_turn.failed error reads: flow turn exceeded max team turns: 100 (%s)", got)
}

// Ordering must be stable: two runs over the same counts produce the same text,
// otherwise the error is hard to grep for.
func TestDescribeActivationBurstIsStable(t *testing.T) {
	counts := map[string]int{"b→c": 2, "a→b": 2, "c→a": 2}

	first := describeActivationBurst(10, counts, nil)
	for i := 0; i < 20; i++ {
		require.Equal(t, first, describeActivationBurst(10, counts, nil))
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
