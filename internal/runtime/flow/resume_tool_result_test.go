package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// memoryTranscripts is the conversation store the flow writes a finished Team
// call into. Only the operations ResumeForToolResult needs are implemented.
type memoryTranscripts struct {
	mu          sync.Mutex
	transcripts map[string]types.AgentTranscript
}

func newMemoryTranscripts() *memoryTranscripts {
	return &memoryTranscripts{transcripts: make(map[string]types.AgentTranscript)}
}

func (s *memoryTranscripts) Save(_ context.Context, transcript types.AgentTranscript) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcripts[transcript.ID] = transcript
	return nil
}

func (s *memoryTranscripts) Load(_ context.Context, id string) (*types.AgentTranscript, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	transcript, ok := s.transcripts[id]
	if !ok {
		return nil, errTranscriptMissing
	}
	return &transcript, nil
}

type transcriptMissingError struct{}

func (transcriptMissingError) Error() string { return "transcript not found" }

var errTranscriptMissing = transcriptMissingError{}

// 上游 Team 交回结果时，它要作为 tool 消息进入调用方的对话，而不是又一条
// 用户消息——否则模型会把"我的调用返回了"重新理解成"用户又问了一遍"。
func TestResumeForToolResultAppendsToolResult(t *testing.T) {
	recorder := &inputRecordingTeamRuntime{}
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), recorder, writer, nil)
	transcripts := newMemoryTranscripts()
	rt.SetTranscriptStore(transcripts)

	session := seedMailboxSession(t, rt, "fs-tool", types.SessionWaitingInput)
	id := callerTranscriptID(session.ID, "default", "coordinator")
	require.NoError(t, transcripts.Save(context.Background(), types.AgentTranscript{
		ID:            id,
		FlowSessionID: session.ID,
		TeamID:        "default",
		CallID:        "coordinator",
		Messages: []types.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "查看cls线上日志"},
			{Role: "assistant", Content: `{"tool":"Team"}`},
			{Role: "tool", ToolCallID: "c1", Content: `{"status":"accepted"}`},
		},
		UpdatedAt: time.Now().UTC(),
	}))

	err := rt.ResumeForToolResult(context.Background(), session.ID, TeamResultSignal{
		TaskID:     "task-1",
		TeamID:     "owner",
		CallerTeam: "default",
		CallID:     "coordinator",
		Status:     "done",
		Reply:      "最近一天 5,945,696 条请求",
	})

	require.NoError(t, err)
	stored, loadErr := transcripts.Load(context.Background(), id)
	require.NoError(t, loadErr)
	require.Len(t, stored.Messages, 6, "结果追加两条：一次 Collect 调用 + 它的 tool 结果")

	call := stored.Messages[4]
	require.Equal(t, "assistant", call.Role)
	require.Len(t, call.ToolCalls, 1)
	require.Equal(t, "Collect", call.ToolCalls[0].Name)

	result := stored.Messages[5]
	require.Equal(t, "tool", result.Role, "结果必须是 tool 消息，不是 user 消息")
	require.Equal(t, call.ToolCalls[0].ID, result.ToolCallID, "call 与结果必须配对")
	require.Contains(t, result.Content, "5,945,696")

	require.NotEmpty(t, recorder.recorded(), "结果回来后要把 loop 重新跑起来")
}

// 会话正忙时不打断：结果留在任务记录上，下一回合再读。
func TestResumeForToolResultSkipsBusySession(t *testing.T) {
	recorder := &inputRecordingTeamRuntime{}
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), recorder, writer, nil)
	transcripts := newMemoryTranscripts()
	rt.SetTranscriptStore(transcripts)

	session := seedMailboxSession(t, rt, "fs-busy", types.SessionRunning)
	id := callerTranscriptID(session.ID, "default", "coordinator")
	require.NoError(t, transcripts.Save(context.Background(), types.AgentTranscript{
		ID:            id,
		FlowSessionID: session.ID,
		TeamID:        "default",
		CallID:        "coordinator",
		Messages: []types.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "查看cls线上日志"},
		},
	}))

	err := rt.ResumeForToolResult(context.Background(), session.ID, TeamResultSignal{
		TaskID: "task-1", CallerTeam: "default", CallID: "coordinator", Status: "done", Reply: "x",
	})

	require.NoError(t, err)
	stored, _ := transcripts.Load(context.Background(), id)
	require.Len(t, stored.Messages, 2, "忙的时候不注入")
	require.Empty(t, recorder.recorded(), "也不启动回合")
}

// 没有转录就无处可写：不静默创建一份空对话。
func TestResumeForToolResultWithoutTranscript(t *testing.T) {
	recorder := &inputRecordingTeamRuntime{}
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), recorder, writer, nil)
	transcripts := newMemoryTranscripts()
	rt.SetTranscriptStore(transcripts)

	session := seedMailboxSession(t, rt, "fs-none", types.SessionWaitingInput)

	err := rt.ResumeForToolResult(context.Background(), session.ID, TeamResultSignal{
		TaskID: "task-1", CallerTeam: "default", CallID: "coordinator", Status: "done", Reply: "x",
	})

	require.NoError(t, err)
	require.Empty(t, recorder.recorded())
}

// 同一个 Team 在同一个会话里不能并发跑两轮：它是单一执行单元。第二个调用要
// 明确失败，而不是与第一轮交叠。
func TestRunTeamRefusesConcurrentCall(t *testing.T) {
	recorder := &inputRecordingTeamRuntime{}
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), recorder, writer, nil)
	session := seedMailboxSession(t, rt, "fs-busy-team", types.SessionWaitingInput)

	require.True(t, rt.tryMarkTeamBusy(session.ID, "default"))
	defer rt.clearTeamBusy(session.ID, "default")

	outcome, err := rt.RunTeam(context.Background(), types.TeamCallSpec{
		FlowSessionID: session.ID,
		CallerTeam:    "default",
		TeamID:        "default",
		Item:          "再跑一次",
	})

	require.NoError(t, err)
	require.Equal(t, "failed", outcome.Status)
	require.Contains(t, outcome.Error, "already running")
}

// 目录只投影调用方自己的出边：模型看到的是"我在这个位置能派谁"，不是整张图。
func TestListTeamsReturnsCallerEdges(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-catalog", types.SessionWaitingInput)

	entries, err := rt.ListTeams(context.Background(), types.TeamCatalogSpec{
		FlowSessionID: session.ID,
		CallerTeam:    "default",
	})

	require.NoError(t, err)
	require.Len(t, entries, 1, "default 只能派 verify")
	require.Equal(t, "verify", entries[0].TeamID)
}

// 叶子 Team 没有出边：目录为空，而不是列出别的 Team 的。
func TestListTeamsEmptyForLeafTeam(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-catalog-leaf", types.SessionWaitingInput)

	entries, err := rt.ListTeams(context.Background(), types.TeamCatalogSpec{
		FlowSessionID: session.ID,
		CallerTeam:    "verify",
	})

	require.NoError(t, err)
	require.Empty(t, entries)
}
