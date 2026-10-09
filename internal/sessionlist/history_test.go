package sessionlist

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
)

func TestHistoryPairsInputAndReply(t *testing.T) {
	root := t.TempDir()
	id := "fs_hist"
	writeSession(t, root, id, []string{
		`{"seq":1,"type":"flow_session.created","payload":{"session":{"id":"fs_hist","status":"waiting_input"}}}`,
		`{"seq":2,"type":"flow_turn.started","payload":{"input":"hello"}}`,
		`{"seq":3,"type":"team_turn.completed","payload":{"team_result":{"Reply":"Hi there."}}}`,
		`{"seq":4,"type":"flow_turn.waiting_input","payload":{}}`,
		`{"seq":5,"type":"flow_turn.started","payload":{"input":"bye"}}`,
		`{"seq":6,"type":"team_turn.completed","payload":{"team_result":{"Reply":"Bye."}}}`,
		`{"seq":7,"type":"flow_turn.waiting_input","payload":{}}`,
	}, nil)

	turns, err := History(storage.NewFileStore(root), filepath.Join(SessionsDir, id))
	require.NoError(t, err)
	require.Len(t, turns, 2)
	assert.Equal(t, "hello", turns[0].Input)
	require.Len(t, turns[0].Messages, 1)
	assert.Equal(t, "Hi there.", turns[0].Messages[0].Reply)
	assert.Equal(t, "bye", turns[1].Input)
	require.Len(t, turns[1].Messages, 1)
	assert.Equal(t, "Bye.", turns[1].Messages[0].Reply)
}

func TestHistorySkipsCorruptLines(t *testing.T) {
	root := t.TempDir()
	id := "fs_bad"
	writeSession(t, root, id, []string{
		`{"seq":1,"type":"flow_turn.started","payload":{"input":"question"}}`,
		`not json at all`,
		`{"seq":3,"type":"team_turn.completed","payload":{"team_result":{"Reply":"answer"}}}`,
	}, nil)

	turns, err := History(storage.NewFileStore(root), filepath.Join(SessionsDir, id))
	require.NoError(t, err)
	require.Len(t, turns, 1)
	assert.Equal(t, "question", turns[0].Input)
	require.Len(t, turns[0].Messages, 1)
	assert.Equal(t, "answer", turns[0].Messages[0].Reply)
}

func TestHistoryEmptySession(t *testing.T) {
	root := t.TempDir()
	id := "fs_none"
	writeSession(t, root, id, []string{
		`{"seq":1,"type":"flow_session.created","payload":{"session":{"id":"fs_none"}}}`,
	}, nil)

	turns, err := History(storage.NewFileStore(root), filepath.Join(SessionsDir, id))
	require.NoError(t, err)
	assert.Empty(t, turns)
}

// History must capture each turn's token usage so a resumed TUI can restore the
// session's cumulative count (ISSUE-tui-resume-tokens-zero).
func TestHistoryCapturesUsage(t *testing.T) {
	root := t.TempDir()
	id := "fs_usage"
	writeSession(t, root, id, []string{
		`{"seq":1,"type":"flow_turn.started","payload":{"input":"q1"}}`,
		`{"seq":2,"type":"team_turn.completed","payload":{"team_result":{"Reply":"a1","Usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}}}`,
		`{"seq":3,"type":"flow_turn.started","payload":{"input":"q2"}}`,
		`{"seq":4,"type":"team_turn.completed","payload":{"team_result":{"Reply":"a2","Usage":{"prompt_tokens":200,"completion_tokens":30,"total_tokens":230}}}}`,
	}, nil)

	turns, err := History(storage.NewFileStore(root), filepath.Join(SessionsDir, id))
	require.NoError(t, err)
	require.Len(t, turns, 2)
	assert.Equal(t, 120, turns[0].Usage.TotalTokens)
	assert.Equal(t, 100, turns[0].Usage.PromptTokens)
	assert.Equal(t, 230, turns[1].Usage.TotalTokens)
}

// History must reconstruct each reply's team/agent identity from CallResults so
// a resumed transcript attributes replies the same way a live one does
// (ISSUE-tui-resume-identity-and-round-count).
func TestHistoryCapturesCallIdentity(t *testing.T) {
	root := t.TempDir()
	id := "fs_ident"
	writeSession(t, root, id, []string{
		`{"seq":1,"type":"flow_turn.started","payload":{"input":"go"}}`,
		`{"seq":2,"type":"team_turn.completed","payload":{"team_result":{"Turn":{"team_id":"diagnose"},"Reply":"merged","CallResults":{"root_cause":{"AgentID":"root-cause-analyst","Reply":"cause is X"},"search":{"AgentID":"issue-searcher","Reply":"found 3"}}}}}`,
	}, nil)

	turns, err := History(storage.NewFileStore(root), filepath.Join(SessionsDir, id))
	require.NoError(t, err)
	require.Len(t, turns, 1)
	require.Len(t, turns[0].Messages, 2)

	// Sorted by call name: root_cause before search.
	assert.Equal(t, "diagnose", turns[0].Messages[0].TeamID)
	assert.Equal(t, "root_cause", turns[0].Messages[0].CallID)
	assert.Equal(t, "root-cause-analyst", turns[0].Messages[0].AgentID)
	assert.Equal(t, "cause is X", turns[0].Messages[0].Reply)
	assert.Equal(t, "issue-searcher", turns[0].Messages[1].AgentID)
}

// Without a CallResults breakdown, History falls back to the team-level reply
// but still carries the team id.
func TestHistoryFallsBackToTeamReply(t *testing.T) {
	root := t.TempDir()
	id := "fs_team"
	writeSession(t, root, id, []string{
		`{"seq":1,"type":"flow_turn.started","payload":{"input":"go"}}`,
		`{"seq":2,"type":"team_turn.completed","payload":{"team_result":{"Turn":{"team_id":"default"},"Reply":"team reply"}}}`,
	}, nil)

	turns, err := History(storage.NewFileStore(root), filepath.Join(SessionsDir, id))
	require.NoError(t, err)
	require.Len(t, turns, 1)
	require.Len(t, turns[0].Messages, 1)
	assert.Equal(t, "default", turns[0].Messages[0].TeamID)
	assert.Equal(t, "team reply", turns[0].Messages[0].Reply)
}

// A team turn with ops but an empty reply still yields a message (the tool
// summary is worth showing).
func TestHistoryKeepsOpsOnlyCall(t *testing.T) {
	root := t.TempDir()
	id := "fs_ops"
	writeSession(t, root, id, []string{
		`{"seq":1,"type":"flow_turn.started","payload":{"input":"go"}}`,
		`{"seq":2,"type":"team_turn.completed","payload":{"team_result":{"Turn":{"team_id":"intake"},"CallResults":{"acquire":{"AgentID":"searcher","Reply":"","WorkspaceOps":[{"kind":"bash","command":"acquire.sh genie","exit_code":0}]}}}}}`,
	}, nil)

	turns, err := History(storage.NewFileStore(root), filepath.Join(SessionsDir, id))
	require.NoError(t, err)
	require.Len(t, turns, 1)
	require.Len(t, turns[0].Messages, 1)
	msg := turns[0].Messages[0]
	assert.Empty(t, msg.Reply)
	require.Len(t, msg.Ops, 1)
	assert.Equal(t, "bash", msg.Ops[0].Kind)
	assert.Equal(t, "acquire.sh genie", msg.Ops[0].Subject)
}
