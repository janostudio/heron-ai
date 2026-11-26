package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func testConditionalRules() []types.ConditionalRule {
	return []types.ConditionalRule{
		{ID: "docs-placement", Paths: []string{"docs/**"}, Body: "DOCS BODY"},
		{ID: "sanitization", Paths: []string{"repos/heron-ai/**", "repos/heron-connect/**"}, Body: "SANITIZE BODY"},
	}
}

// TestRuleNoteSetInjectsOncePerRule is the dedupe contract: a turn that writes
// into docs/ five times must not carry five copies of the same rule. Repeating a
// body on every round is how a rule becomes noise.
func TestRuleNoteSetInjectsOncePerRule(t *testing.T) {
	notes := newRuleNoteSet(testConditionalRules())
	require.True(t, notes.empty())

	added := notes.observe([]pathTarget{{Path: "docs/self/x.md", Kind: "write"}})
	require.Len(t, added, 1)
	assert.Equal(t, "docs-placement", added[0].ID)
	assert.Equal(t, "docs/self/x.md", added[0].Path)
	assert.Equal(t, "write", added[0].Kind)

	assert.Empty(t, notes.observe([]pathTarget{{Path: "docs/self/y.md", Kind: "write"}}),
		"the same rule must not be added twice")
	assert.Len(t, notes.notes, 1)
}

// TestRuleNoteSetIgnoresUnrelatedPaths: the whole point is that a turn which
// never goes near a directory pays nothing for its contract.
func TestRuleNoteSetIgnoresUnrelatedPaths(t *testing.T) {
	notes := newRuleNoteSet(testConditionalRules())
	assert.Empty(t, notes.observe([]pathTarget{{Path: "internal/agent/runtime.go", Kind: "write"}}))
	require.True(t, notes.empty())
	_, ok := notes.noteMessage()
	assert.False(t, ok)
}

// TestRuleNoteSetMessageCarriesBodyAndTrigger: the model has to be able to tell
// which contract this is and what made it apply, otherwise a rule arriving
// mid-turn reads like an unrelated instruction.
func TestRuleNoteSetMessageCarriesBodyAndTrigger(t *testing.T) {
	notes := newRuleNoteSet(testConditionalRules())
	notes.observe([]pathTarget{{Path: "repos/heron-ai/internal/x.go", Kind: "write"}})

	message, ok := notes.noteMessage()
	require.True(t, ok)
	assert.Equal(t, "system", message.Role, "a rule note is not a turn the user took")
	assert.Contains(t, message.Content, "[rule:sanitization trigger=repos/heron-ai/internal/x.go kind=write]")
	assert.Contains(t, message.Content, "SANITIZE BODY")
	assert.NotContains(t, message.Content, "DOCS BODY", "only triggered rules travel")
}

// TestRuleNoteSetSeedFromCheckpointOps: a resumed turn already triggered these
// rules before it paused; they must not arrive a second time.
func TestRuleNoteSetSeedFromCheckpointOps(t *testing.T) {
	notes := newRuleNoteSet(testConditionalRules())
	notes.seedFromOps([]types.WorkspaceOperation{{Kind: "write", Path: "docs/self/x.md"}})

	require.Len(t, notes.notes, 1)
	assert.Empty(t, notes.observe([]pathTarget{{Path: "docs/self/x.md", Kind: "write"}}),
		"the restored rule must not be injected again")
}

// TestRuleNoteSetWithoutRules: the mechanism costs nothing when no rule declares
// paths, which is the state of every configuration that has not adopted it.
func TestRuleNoteSetWithoutRules(t *testing.T) {
	notes := newRuleNoteSet(nil)
	assert.Empty(t, notes.observe([]pathTarget{{Path: "docs/x.md", Kind: "write"}}))
	_, ok := notes.noteMessage()
	assert.False(t, ok)
}
