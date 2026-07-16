package context

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ========== AgentStateStore tests ==========

func TestAgentStateStore_ReadEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	fs := storage.NewFileStore(tmpDir)
	store := NewAgentStateStore(fs)
	ctx := context.Background()

	state, err := store.Read(ctx, "run-001", "team1", "agent1")
	require.NoError(t, err)
	assert.NotNil(t, state)
	assert.Empty(t, state)
}

func TestAgentStateStore_WriteThenRead(t *testing.T) {
	tmpDir := t.TempDir()
	fs := storage.NewFileStore(tmpDir)
	store := NewAgentStateStore(fs)
	ctx := context.Background()

	state := map[string]any{
		"key1": "value1",
		"key2": 42,
		"key3": true,
	}
	err := store.Write(ctx, "run-001", "team1", "agent1", state)
	require.NoError(t, err)

	readState, err := store.Read(ctx, "run-001", "team1", "agent1")
	require.NoError(t, err)
	assert.Equal(t, "value1", readState["key1"])
	assert.Equal(t, float64(42), readState["key2"])
	assert.Equal(t, true, readState["key3"])
}

func TestAgentStateStore_ReadNonExistentReturnsEmptyMap(t *testing.T) {
	tmpDir := t.TempDir()
	fs := storage.NewFileStore(tmpDir)
	store := NewAgentStateStore(fs)
	ctx := context.Background()

	state, err := store.Read(ctx, "run-001", "team1", "nonexistent")
	require.NoError(t, err)
	assert.NotNil(t, state)
	assert.Empty(t, state)
}

// ========== AgentMemoryStore tests ==========

func TestAgentMemoryStore_AppendSingle(t *testing.T) {
	tmpDir := t.TempDir()
	fs := storage.NewFileStore(tmpDir)
	store := NewAgentMemoryStore(fs)
	ctx := context.Background()

	entry := types.MemoryEntry{
		Content:    "User prefers concise answers",
		Importance: "high",
		Source:     "user_feedback",
		Round:      1,
		Timestamp:  "2024-01-01T00:00:00Z",
	}
	err := store.Append(ctx, "run-001", "team1", "agent1", entry)
	require.NoError(t, err)

	entries, err := store.ListRecent(ctx, "run-001", "team1", "agent1", 10)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, "User prefers concise answers", entries[0].Content)
	assert.Equal(t, "high", entries[0].Importance)
}

func TestAgentMemoryStore_AppendMultiple(t *testing.T) {
	tmpDir := t.TempDir()
	fs := storage.NewFileStore(tmpDir)
	store := NewAgentMemoryStore(fs)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		entry := types.MemoryEntry{
			Content:    "memory " + string(rune('0'+i)),
			Importance: "medium",
			Source:     "test",
			Round:      i + 1,
			Timestamp:  "2024-01-01T00:00:00Z",
		}
		err := store.Append(ctx, "run-001", "team1", "agent1", entry)
		require.NoError(t, err)
	}

	entries, err := store.ListRecent(ctx, "run-001", "team1", "agent1", 0)
	require.NoError(t, err)
	assert.Len(t, entries, 5)
}

func TestAgentMemoryStore_ListRecentWithLimit(t *testing.T) {
	tmpDir := t.TempDir()
	fs := storage.NewFileStore(tmpDir)
	store := NewAgentMemoryStore(fs)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		entry := types.MemoryEntry{
			Content:    "memory " + string(rune('0'+i)),
			Importance: "medium",
			Source:     "test",
			Round:      i + 1,
			Timestamp:  "2024-01-01T00:00:00Z",
		}
		err := store.Append(ctx, "run-001", "team1", "agent1", entry)
		require.NoError(t, err)
	}

	entries, err := store.ListRecent(ctx, "run-001", "team1", "agent1", 3)
	require.NoError(t, err)
	assert.Len(t, entries, 3)
	// Should return the last 3
	assert.Equal(t, "memory 7", entries[0].Content)
	assert.Equal(t, "memory 8", entries[1].Content)
	assert.Equal(t, "memory 9", entries[2].Content)
}

func TestAgentMemoryStore_ListRecentEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	fs := storage.NewFileStore(tmpDir)
	store := NewAgentMemoryStore(fs)
	ctx := context.Background()

	entries, err := store.ListRecent(ctx, "run-001", "team1", "agent1", 10)
	require.NoError(t, err)
	assert.Nil(t, entries)
}

// ========== RunLog tests ==========

func TestRunLog_AppendSingleMessage(t *testing.T) {
	tmpDir := t.TempDir()
	fs := storage.NewFileStore(tmpDir)
	log := NewRunLog(fs)
	ctx := context.Background()

	msg := types.Message{
		Role:    "user",
		Content: "Hello, world!",
	}
	err := log.Append(ctx, "run-001", msg)
	require.NoError(t, err)

	messages, err := log.List(ctx, "run-001")
	require.NoError(t, err)
	assert.Len(t, messages, 1)
	assert.Equal(t, "user", messages[0].Role)
	assert.Equal(t, "Hello, world!", messages[0].Content)
}

func TestRunLog_ListMessages(t *testing.T) {
	tmpDir := t.TempDir()
	fs := storage.NewFileStore(tmpDir)
	log := NewRunLog(fs)
	ctx := context.Background()

	messages := []types.Message{
		{Role: "user", Content: "msg1"},
		{Role: "assistant", Content: "msg2"},
		{Role: "user", Content: "msg3"},
	}
	for _, msg := range messages {
		err := log.Append(ctx, "run-001", msg)
		require.NoError(t, err)
	}

	result, err := log.List(ctx, "run-001")
	require.NoError(t, err)
	assert.Len(t, result, 3)
}

func TestRunLog_ListRecentWithLimit(t *testing.T) {
	tmpDir := t.TempDir()
	fs := storage.NewFileStore(tmpDir)
	log := NewRunLog(fs)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		msg := types.Message{
			Role:    "user",
			Content: "msg" + string(rune('0'+i)),
		}
		err := log.Append(ctx, "run-001", msg)
		require.NoError(t, err)
	}

	result, err := log.ListRecent(ctx, "run-001", 3)
	require.NoError(t, err)
	assert.Len(t, result, 3)
	assert.Equal(t, "msg7", result[0].Content)
	assert.Equal(t, "msg8", result[1].Content)
	assert.Equal(t, "msg9", result[2].Content)
}

func TestRunLog_ListEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	fs := storage.NewFileStore(tmpDir)
	log := NewRunLog(fs)
	ctx := context.Background()

	messages, err := log.List(ctx, "run-001")
	require.NoError(t, err)
	assert.Nil(t, messages)
}

// ========== ContextCompressor tests ==========

func TestCompressMessages_KeepsSystemMessages(t *testing.T) {
	compressor := NewContextCompressor(10000)
	ctx := context.Background()

	messages := []types.Message{
		{Role: "system", Content: "You are a helpful assistant"},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
	}

	result, err := compressor.CompressMessages(ctx, messages)
	require.NoError(t, err)
	// System message should always be kept
	assert.Equal(t, "system", result[0].Role)
	assert.Equal(t, "You are a helpful assistant", result[0].Content)
}

func TestCompressMessages_TrimsOverflow(t *testing.T) {
	compressor := NewContextCompressor(50) // very low limit
	ctx := context.Background()

	largeContent := strings.Repeat("x", 200) // ~50 tokens

	messages := []types.Message{
		{Role: "system", Content: "You are a helper"},
		{Role: "user", Content: largeContent},
		{Role: "user", Content: "Short"},
	}

	result, err := compressor.CompressMessages(ctx, messages)
	require.NoError(t, err)
	// System message should still be there
	assert.True(t, len(result) >= 1)
	assert.Equal(t, "system", result[0].Role)
}

func TestCompressMessages_ZeroMaxTokens(t *testing.T) {
	compressor := NewContextCompressor(0) // disabled
	ctx := context.Background()

	messages := []types.Message{
		{Role: "system", Content: "System prompt"},
		{Role: "user", Content: "Hello"},
	}

	result, err := compressor.CompressMessages(ctx, messages)
	require.NoError(t, err)
	assert.Len(t, result, 2) // all messages kept when compression disabled
}

func TestCompressContent_NoTruncation(t *testing.T) {
	compressor := NewContextCompressor(1000)
	ctx := context.Background()

	content := "This is a short message"
	result, err := compressor.CompressContent(ctx, content)
	require.NoError(t, err)
	assert.Equal(t, content, result)
}

func TestCompressContent_Truncation(t *testing.T) {
	compressor := NewContextCompressor(10) // very low limit
	ctx := context.Background()

	content := strings.Repeat("This is a long message. ", 20) // many tokens
	result, err := compressor.CompressContent(ctx, content)
	require.NoError(t, err)
	assert.Less(t, len(result), len(content))
	assert.Contains(t, result, "[truncated]")
}

func TestCompressContent_ZeroMaxTokens(t *testing.T) {
	compressor := NewContextCompressor(0) // disabled
	ctx := context.Background()

	content := strings.Repeat("x", 1000)
	result, err := compressor.CompressContent(ctx, content)
	require.NoError(t, err)
	assert.Equal(t, content, result)
}

func TestCompressToolResult(t *testing.T) {
	compressor := NewContextCompressor(100)
	ctx := context.Background()

	shortResult := "short result"
	result, err := compressor.CompressToolResult(ctx, "read_file", shortResult)
	require.NoError(t, err)
	assert.Equal(t, shortResult, result)

	longResult := strings.Repeat("x", 500)
	result, err = compressor.CompressToolResult(ctx, "read_file", longResult)
	require.NoError(t, err)
	assert.Contains(t, result, "[result truncated")
	assert.Less(t, len(result), len(longResult)+50) // +50 for truncation message
}

func TestCompressToolResult_ZeroMaxTokens(t *testing.T) {
	compressor := NewContextCompressor(0)
	ctx := context.Background()

	longResult := strings.Repeat("x", 5000)
	result, err := compressor.CompressToolResult(ctx, "read_file", longResult)
	require.NoError(t, err)
	assert.Contains(t, result, "[result truncated")
}

func TestSplitLines(t *testing.T) {
	input := "line1\nline2\nline3"
	result := splitLines(input)
	assert.Len(t, result, 3)
	assert.Equal(t, "line1", result[0])
	assert.Equal(t, "line2", result[1])
	assert.Equal(t, "line3", result[2])
}

func TestSplitLines_SingleLine(t *testing.T) {
	result := splitLines("single line")
	assert.Len(t, result, 1)
	assert.Equal(t, "single line", result[0])
}

func TestSplitLines_Empty(t *testing.T) {
	result := splitLines("")
	assert.Len(t, result, 0)
}

// ========== Integration test ==========

func TestIntegration_AgentStateAndMemory(t *testing.T) {
	tmpDir := t.TempDir()
	fs := storage.NewFileStore(tmpDir)
	ctx := context.Background()

	stateStore := NewAgentStateStore(fs)
	memStore := NewAgentMemoryStore(fs)

	// Write state
	state := map[string]any{"round": 3, "goal": "achieved"}
	err := stateStore.Write(ctx, "run-001", "team1", "agent1", state)
	require.NoError(t, err)

	// Write memory
	entry := types.MemoryEntry{
		Content:    "Completed task",
		Importance: "high",
		Source:     "agent",
		Round:      3,
		Timestamp:  "2024-01-01T00:00:00Z",
	}
	err = memStore.Append(ctx, "run-001", "team1", "agent1", entry)
	require.NoError(t, err)

	// Read back state
	readState, err := stateStore.Read(ctx, "run-001", "team1", "agent1")
	require.NoError(t, err)
	assert.Equal(t, float64(3), readState["round"])

	// Read back memory
	entries, err := memStore.ListRecent(ctx, "run-001", "team1", "agent1", 10)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, "Completed task", entries[0].Content)
}

func TestIntegration_RunLogAndCompressor(t *testing.T) {
	tmpDir := t.TempDir()
	fs := storage.NewFileStore(tmpDir)
	ctx := context.Background()

	log := NewRunLog(fs)
	compressor := NewContextCompressor(1000)

	// Append some messages
	messages := []types.Message{
		{Role: "system", Content: "You are a helpful assistant"},
		{Role: "user", Content: "Tell me about Go"},
		{Role: "assistant", Content: "Go is a programming language"},
	}
	for _, msg := range messages {
		err := log.Append(ctx, "run-001", msg)
		require.NoError(t, err)
	}

	// Read back and compress
	allMessages, err := log.List(ctx, "run-001")
	require.NoError(t, err)
	assert.Len(t, allMessages, 3)

	compressed, err := compressor.CompressMessages(ctx, allMessages)
	require.NoError(t, err)
	// All should fit since maxTokens is 1000
	assert.Len(t, compressed, 3)

	// Test serialization round-trip
	for _, msg := range allMessages {
		data, err := json.Marshal(msg)
		require.NoError(t, err)
		var decoded types.Message
		err = json.Unmarshal(data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, msg.Role, decoded.Role)
		assert.Equal(t, msg.Content, decoded.Content)
	}
}
