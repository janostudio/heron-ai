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

	// 会话忙时用短超时退出等待，验证"落盘但不跑回合"。
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := rt.ResumeForToolResult(ctx, session.ID, TeamResultSignal{
		TaskID: "task-1", CallerTeam: "default", CallID: "coordinator", Status: "done", Reply: "x",
	})

	require.NoError(t, err)
	stored, _ := transcripts.Load(context.Background(), id)
	require.Len(t, stored.Messages, 4, "忙的时候也要落盘：回调只触发一次，丢了就永远丢了")
	require.Equal(t, "tool", stored.Messages[3].Role)
	require.Empty(t, recorder.recorded(), "但不启动回合")
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

// 完成回调可能对同一个任务触发多次（进程重启会重扫终态任务）。转录本身就是
// 队列的账本：同一个 task 只进一次，第二次调用必须是 no-op，否则会追加两对
// Collect 并把回合跑两遍。
func TestResumeForToolResultIsIdempotent(t *testing.T) {
	recorder := &inputRecordingTeamRuntime{}
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), recorder, writer, nil)
	transcripts := newMemoryTranscripts()
	rt.SetTranscriptStore(transcripts)

	session := seedMailboxSession(t, rt, "fs-idem", types.SessionWaitingInput)
	id := callerTranscriptID(session.ID, "default", "coordinator")
	require.NoError(t, transcripts.Save(context.Background(), types.AgentTranscript{
		ID:            id,
		FlowSessionID: session.ID,
		TeamID:        "default",
		CallID:        "coordinator",
		Messages: []types.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "查主分支"},
			{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "c0", Name: "Team"}}},
			{Role: "tool", ToolCallID: "c0", Content: `{"status":"accepted"}`},
		},
	}))

	signal := TeamResultSignal{
		TaskID: "task-9", CallerTeam: "default", CallID: "coordinator", Status: "done", Reply: "master",
	}
	require.NoError(t, rt.ResumeForToolResult(context.Background(), session.ID, signal))
	first, _ := transcripts.Load(context.Background(), id)
	require.Len(t, first.Messages, 6, "第一次追加一对")

	require.NoError(t, rt.ResumeForToolResult(context.Background(), session.ID, signal))
	second, _ := transcripts.Load(context.Background(), id)
	require.Len(t, second.Messages, 6, "重复投递不再追加")
}

// 被调用方的结果回到调用方时，调用方声明了 flow_records，那条记录必须落进
// 本轮的记录窗口。窗口若按"收到结果的那一刻"起算，刚产出的记录会因为它早
// 了几百毫秒而被丢掉：调用方拿到了空输入，展示层也收不到 B→A 的消费边
// （A→B fixture，2026-10-28 实测无 shared_record.consumed）。
func TestResumeForToolResultKeepsRecordsPublishedAfterDispatch(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(fileStore)
	evidence := storage.NewJSONLEvidenceStore(fileStore)

	recorder := &inputRecordingTeamRuntime{}
	defs := testDefinitions()
	defs.Flow.Teams["default"] = types.FlowTeamBinding{
		ID:     "default",
		TeamID: "default-team",
		Inputs: types.InputSpec{FlowRecords: []string{"OwnerReply"}},
	}
	rt := newTestRuntime(defs, recorder, writer, evidence)
	transcripts := newMemoryTranscripts()
	rt.SetTranscriptStore(transcripts)

	session := seedMailboxSession(t, rt, "fs-scope", types.SessionWaitingInput)

	dispatched := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, evidence.Publish(context.Background(), session.ID, types.SharedRecord{
		RecordID:  "stale-1",
		Name:      "StaleReply",
		Kind:      "agent_result",
		Scope:     types.RecordScopeFlow,
		Status:    types.RecordActive,
		Revision:  1,
		Summary:   "上一轮的结论",
		CreatedAt: dispatched.Add(-time.Minute),
	}))
	require.NoError(t, evidence.Publish(context.Background(), session.ID, types.SharedRecord{
		RecordID:  "fresh-1",
		Name:      "OwnerReply",
		Kind:      "agent_result",
		Scope:     types.RecordScopeFlow,
		Status:    types.RecordActive,
		Revision:  1,
		Summary:   "版本号是 v9.9.9。",
		CreatedAt: dispatched.Add(30 * time.Second),
	}))

	tasks := &memoryTaskStore{}
	tasks.tasks = []types.ToolTask{{
		ID: "task-scope", FlowSessionID: session.ID, ToolCallID: "c0",
		ToolName: types.ToolNameTeamRun, Status: types.ToolTaskCompleted, CreatedAt: dispatched,
	}}
	rt.SetTaskStore(tasks)

	id := callerTranscriptID(session.ID, "default", "coordinator")
	require.NoError(t, transcripts.Save(context.Background(), types.AgentTranscript{
		ID:            id,
		FlowSessionID: session.ID,
		TeamID:        "default",
		CallID:        "coordinator",
		Messages: []types.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "查一下版本号"},
			{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "c0", Name: "Team"}}},
			{Role: "tool", ToolCallID: "c0", Content: `{"status":"accepted"}`},
		},
	}))

	require.NoError(t, rt.ResumeForToolResult(context.Background(), session.ID, TeamResultSignal{
		TaskID: "task-scope", CallerTeam: "default", CallID: "coordinator",
		Status: "done", Reply: "版本号是 v9.9.9。",
	}))

	recorder.mu.Lock()
	seen := append([]string(nil), recorder.records...)
	recorder.mu.Unlock()
	require.Contains(t, seen, "OwnerReply",
		"派发之后产出的记录必须进入调用方本轮窗口")
	require.NotContains(t, seen, "StaleReply",
		"派发之前的记录仍要挡在外面，否则上一轮的结论会当成本轮事实")
}
