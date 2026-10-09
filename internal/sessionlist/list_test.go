package sessionlist

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
)

// writeSession materializes one session directory under root/.agents/data/sessions.
func writeSession(t *testing.T, root, id string, flowLines, teamLines []string) {
	t.Helper()
	dir := filepath.Join(root, SessionsDir, id)
	require.NoError(t, os.MkdirAll(dir, 0755))
	writeLines(t, filepath.Join(dir, "flow.jsonl"), flowLines)
	if teamLines != nil {
		writeLines(t, filepath.Join(dir, "team.jsonl"), teamLines)
	}
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	content := ""
	for _, line := range lines {
		content += line + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
}

func TestListEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	sessions, err := List(storage.NewFileStore(root), SessionsDir)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestListMissingDirectoryIsNotAnError(t *testing.T) {
	sessions, err := List(storage.NewFileStore(t.TempDir()), SessionsDir)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestListSummarizesSession(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "fs_abc", []string{
		`{"seq":1,"type":"flow_session.created","created_at":"2026-10-09T09:00:00Z","payload":{"session":{"id":"fs_abc","flow_id":"default","status":"created","updated_at":"2026-10-09T09:00:00Z"}}}`,
		`{"seq":2,"type":"flow_turn.started","created_at":"2026-10-09T09:00:01Z","payload":{"input":"hello world"}}`,
		`{"seq":3,"type":"flow_turn.started","created_at":"2026-10-09T09:01:00Z","payload":{"input":"second"}}`,
		`{"seq":4,"type":"flow_turn.waiting_input","created_at":"2026-10-09T09:02:00Z","payload":{}}`,
	}, []string{
		`{"seq":1,"type":"agent_turn.completed","payload":{"call_result":{"Usage":{"prompt_tokens":100,"total_tokens":120}}}}`,
		`{"seq":2,"type":"agent_turn.completed","payload":{"call_result":{"Usage":{"prompt_tokens":200,"total_tokens":230}}}}`,
	})

	sessions, err := List(storage.NewFileStore(root), SessionsDir)
	require.NoError(t, err)
	require.Len(t, sessions, 1)

	s := sessions[0]
	assert.Equal(t, "fs_abc", s.ID)
	assert.Equal(t, "hello world", s.Title)
	assert.Equal(t, 2, s.Turns)
	assert.Equal(t, int64(350), s.Tokens)
	assert.False(t, s.Active, "waiting_input is idle, not active")
	assert.Equal(t, time.Date(2026, 10, 9, 9, 2, 0, 0, time.UTC), s.UpdatedAt.UTC())
}

func TestListMarksRunningSessionActive(t *testing.T) {
	root := t.TempDir()
	// A turn in flight: flow_turn.started with no terminal/waiting event after.
	writeSession(t, root, "fs_run", []string{
		`{"seq":1,"type":"flow_session.created","created_at":"2026-10-09T09:00:00Z","payload":{"session":{"id":"fs_run","flow_id":"default","status":"running","updated_at":"2026-10-09T09:00:00Z"}}}`,
		`{"seq":2,"type":"flow_turn.started","created_at":"2026-10-09T09:00:01Z","payload":{"input":"in flight"}}`,
	}, nil)

	sessions, err := List(storage.NewFileStore(root), SessionsDir)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.True(t, sessions[0].Active, "a turn with no terminal event is still running")
}

func TestListNotActiveAfterWaitingInput(t *testing.T) {
	root := t.TempDir()
	// Even though the last flow_session.updated still says "running" (the engine
	// never rewrites it), flow_turn.waiting_input means the turn is at rest.
	writeSession(t, root, "fs_idle", []string{
		`{"seq":1,"type":"flow_session.created","created_at":"2026-10-09T09:00:00Z","payload":{"session":{"id":"fs_idle","flow_id":"default","status":"created","updated_at":"2026-10-09T09:00:00Z"}}}`,
		`{"seq":2,"type":"flow_session.updated","created_at":"2026-10-09T09:00:01Z","payload":{"session":{"id":"fs_idle","flow_id":"default","status":"running","updated_at":"2026-10-09T09:00:01Z"}}}`,
		`{"seq":3,"type":"flow_turn.started","created_at":"2026-10-09T09:00:02Z","payload":{"input":"done"}}`,
		`{"seq":4,"type":"flow_turn.waiting_input","created_at":"2026-10-09T09:00:03Z","payload":{}}`,
	}, nil)

	sessions, err := List(storage.NewFileStore(root), SessionsDir)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.False(t, sessions[0].Active, "waiting_input is at rest")
}

func TestListSkipsCorruptLines(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "fs_bad", []string{
		`{"seq":1,"type":"flow_session.created","created_at":"2026-10-09T09:00:00Z","payload":{"session":{"id":"fs_bad","status":"waiting_input","updated_at":"2026-10-09T09:00:00Z"}}}`,
		`{ this is not json`,
		`{"seq":3,"type":"flow_turn.started","created_at":"2026-10-09T09:00:01Z","payload":{"input":"survives"}}`,
	}, []string{
		`{"seq":1,"type":"agent_turn.completed","payload":{"call_result":{"Usage":{"total_tokens":10}}}}`,
		`garbage`,
	})

	sessions, err := List(storage.NewFileStore(root), SessionsDir)
	require.NoError(t, err)
	require.Len(t, sessions, 1, "a corrupt line must not hide the session")
	assert.Equal(t, "survives", sessions[0].Title)
	assert.Equal(t, 1, sessions[0].Turns)
	assert.Equal(t, int64(10), sessions[0].Tokens)
}

func TestListTruncatesLongTitle(t *testing.T) {
	root := t.TempDir()
	long := "先确认当前分支，然后需要切换到 feat/134 分支之后再重新构建整个项目并跑一遍完整的测试套件确认没有回归问题然后提交代码"
	writeSession(t, root, "fs_long", []string{
		`{"seq":1,"type":"flow_session.created","created_at":"2026-10-09T09:00:00Z","payload":{"session":{"id":"fs_long","status":"waiting_input","updated_at":"2026-10-09T09:00:00Z"}}}`,
		`{"seq":2,"type":"flow_turn.started","created_at":"2026-10-09T09:00:01Z","payload":{"input":"` + long + `"}}`,
	}, nil)

	sessions, err := List(storage.NewFileStore(root), SessionsDir)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	title := sessions[0].Title
	assert.True(t, len([]rune(title)) <= TitleMaxRunes+1, "title should be truncated, got %d runes", len([]rune(title)))
	assert.True(t, len([]rune(title)) < len([]rune(long)))
}

func TestListSkipsNonSessionEntries(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, SessionsDir), 0755))
	// A stray file (not a directory) must not become a session.
	require.NoError(t, os.WriteFile(filepath.Join(root, SessionsDir, "README.md"), []byte("x"), 0644))
	// An empty directory (no flow.jsonl) lists to no children -> skipped.
	require.NoError(t, os.MkdirAll(filepath.Join(root, SessionsDir, "empty"), 0755))

	sessions, err := List(storage.NewFileStore(root), SessionsDir)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestListSortsByUpdatedAtDescending(t *testing.T) {
	root := t.TempDir()
	mk := func(id, ts string) {
		writeSession(t, root, id, []string{
			`{"seq":1,"type":"flow_session.created","created_at":"` + ts + `","payload":{"session":{"id":"` + id + `","status":"waiting_input","updated_at":"` + ts + `"}}}`,
		}, nil)
	}
	mk("old", "2026-10-01T00:00:00Z")
	mk("new", "2026-10-09T00:00:00Z")
	mk("mid", "2026-10-05T00:00:00Z")

	sessions, err := List(storage.NewFileStore(root), SessionsDir)
	require.NoError(t, err)
	require.Len(t, sessions, 3)
	assert.Equal(t, []string{"new", "mid", "old"}, []string{sessions[0].ID, sessions[1].ID, sessions[2].ID})
}
