package flow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// progressTeamRuntime answers a dispatched Team the way a Team that hands work
// further down does: it ends its turn with a progress line and a record, while
// the call it dispatched is still running.
//
// The record is attributed to the Team that produced it, so the test can tell
// the callee's progress apart from the caller's own turn: the caller runs too,
// on the way back.
type progressTeamRuntime struct{}

func (progressTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	teamID := req.TeamTurn.TeamID
	return types.TeamTurnResult{
		Turn:  req.TeamTurn,
		Reply: "已转给执行侧，正在取证。",
		Records: []types.SharedRecord{{
			RecordID: "progress-" + teamID,
			Name:     "WorkerReport",
			Kind:     "agent_result",
			Scope:    types.RecordScopeTeam,
			Status:   types.RecordActive,
			Revision: 1,
			Summary:  "已转给执行侧，正在取证。",
			Producer: types.ProducerRef{TeamID: teamID, CallID: "call-" + teamID},
		}},
		Next: &types.Route{Action: types.NextProceed},
	}, nil
}

// 一个团队把活派下去、自己的回合先结束时，它说的那句话只是进度。记录必须带
// pending 状态，否则下一层会把它当结论转述给用户（fs_2864455499636f2b）。
func TestProgressRecordIsPendingWhileDownstreamRuns(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(fileStore)
	evidence := storage.NewJSONLEvidenceStore(fileStore)

	defs := testDefinitions()
	defs.Flow.Teams["verify"] = types.FlowTeamBinding{
		ID:     "verify",
		TeamID: "verify-team",
		Inputs: types.InputSpec{FlowRecords: []string{"WorkerReport"}},
	}
	rt := newTestRuntime(defs, progressTeamRuntime{}, writer, evidence)
	session := seedMailboxSession(t, rt, "fs-pending", types.SessionWaitingInput)
	rt.SetTaskStore(&memoryTaskStore{tasks: []types.ToolTask{{
		ID:            "task-verify",
		FlowSessionID: session.ID,
		ToolName:      types.ToolNameTeamRun,
		Status:        types.ToolTaskRunning,
		Arguments:     map[string]any{"caller_team": "verify", "call_id": "call-verify", "team_id": "probe"},
	}}})

	result, err := rt.runTurnWithOrigin(context.Background(), defs, session, "", nil,
		[]activation{{teamID: "verify", callerTeam: "default"}},
		turnOriginTool, session.CreatedAt, nil, nil)
	require.NoError(t, err)

	records, err := rt.loadHistoricalFlowRecords(context.Background(), session.ID)
	require.NoError(t, err)
	byTeam := make(map[string]types.SharedRecord, len(records))
	for _, record := range records {
		byTeam[record.Producer.TeamID] = record
	}
	require.Equal(t, types.RecordPending, byTeam["verify"].Status,
		"下游还在跑时，这条只是进度，不是结论")
	require.Equal(t, types.RecordActive, byTeam["default"].Status,
		"没有在途下游的团队，它的答复就是结论")

	// 返回给本轮的记录必须与发布态一致：只改发布副本会让同一轮里的后继步骤
	// 仍看到 active，进度会被当结论读走。
	inMemory := make(map[string]types.RecordStatus, len(result.Records))
	for _, record := range result.Records {
		inMemory[record.Producer.TeamID] = record.Status
	}
	require.Equal(t, types.RecordPending, inMemory["verify"], "内存态与发布态必须一致")
}

// 下游回来了，那条进度就不再描述现状：失败作废，成功被后续结论取代。留着
// active 会让两分钟前的"正在取证"在下游已经死掉之后继续被当现状复述。
func TestSettleCallerProgressRetiresPendingRecords(t *testing.T) {
	for _, tc := range []struct {
		name   string
		failed bool
		want   types.RecordStatus
	}{
		{name: "call failed", failed: true, want: types.RecordInvalidated},
		{name: "call returned", failed: false, want: types.RecordSuperseded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fileStore := storage.NewFileStore(t.TempDir())
			writer := storage.NewJSONLSessionWriter(fileStore)
			evidence := storage.NewJSONLEvidenceStore(fileStore)
			rt := newTestRuntime(testDefinitions(), calleeRecordTeamRuntime{}, writer, evidence)
			ctx := context.Background()

			require.NoError(t, evidence.Publish(ctx, "fs-settle", types.SharedRecord{
				RecordID: "progress", Name: "CoordinatorReply", Kind: "agent_result",
				Scope: types.RecordScopeFlow, Status: types.RecordPending, Revision: 1,
				Producer: types.ProducerRef{TeamID: "frontdesk", CallID: "call-1"},
			}))
			// 同一个团队并行派出的另一个 call：它的进度与本 call 无关。
			require.NoError(t, evidence.Publish(ctx, "fs-settle", types.SharedRecord{
				RecordID: "sibling", Name: "CoordinatorReply", Kind: "agent_result",
				Scope: types.RecordScopeFlow, Status: types.RecordPending, Revision: 1,
				Producer: types.ProducerRef{TeamID: "frontdesk", CallID: "call-2"},
			}))
			require.NoError(t, evidence.Publish(ctx, "fs-settle", types.SharedRecord{
				RecordID: "conclusion", Name: "OwnerReply", Kind: "agent_result",
				Scope: types.RecordScopeFlow, Status: types.RecordActive, Revision: 1,
				Producer: types.ProducerRef{TeamID: "dev-lead", CallID: "call-3"},
			}))

			require.NoError(t, rt.settleCallerProgress(ctx, "fs-settle", "frontdesk", "call-1", tc.failed))

			progress, err := evidence.Get(ctx, "fs-settle", "progress")
			require.NoError(t, err)
			require.Equal(t, tc.want, progress.Status)
			require.Equal(t, 2, progress.Revision, "作废是新增一个 revision，历史不能改写")

			sibling, err := evidence.Get(ctx, "fs-settle", "sibling")
			require.NoError(t, err)
			require.Equal(t, types.RecordPending, sibling.Status, "并行 call 的进度不能被别的 call 结算")

			conclusion, err := evidence.Get(ctx, "fs-settle", "conclusion")
			require.NoError(t, err)
			require.Equal(t, types.RecordActive, conclusion.Status, "不是 pending 的记录不受影响")
		})
	}
}

// 恢复轮要把当初派下去的任务一并交回：被唤醒的团队得知道自己原来在干什么，
// 而不是靠翻 State 便签猜（fs_2864455499636f2b 花了 3 分半）。
func TestResumeForToolResultCarriesOriginalTask(t *testing.T) {
	recorder := &inputRecordingTeamRuntime{}
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), recorder, writer, nil)
	transcripts := newMemoryTranscripts()
	rt.SetTranscriptStore(transcripts)

	session := seedMailboxSession(t, rt, "fs-original", types.SessionWaitingInput)
	rt.SetTaskStore(&memoryTaskStore{tasks: []types.ToolTask{{
		ID:            "task-original",
		FlowSessionID: session.ID,
		ToolName:      types.ToolNameTeamRun,
		Status:        types.ToolTaskFailed,
		CreatedAt:     time.Now().UTC().Add(-time.Minute),
		Arguments:     map[string]any{"item": "在 genie 仓库定位云函数接入方案"},
	}}})

	id := callerTranscriptID(session.ID, "default", "coordinator")
	require.NoError(t, transcripts.Save(context.Background(), types.AgentTranscript{
		ID:            id,
		FlowSessionID: session.ID,
		TeamID:        "default",
		CallID:        "coordinator",
		Messages: []types.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "分析 genie baas 云函数接入"},
		},
	}))

	err := rt.ResumeForToolResult(context.Background(), session.ID, TeamResultSignal{
		TaskID:     "task-original",
		TeamID:     "probe",
		CallerTeam: "default",
		CallID:     "coordinator",
		Status:     string(types.ToolTaskFailed),
		Error:      "task was interrupted by process restart; explicit retry is required",
	})
	// 失败的派发不是可以总结的结果：这一轮以失败结束，调用方拿到错误本身。
	require.ErrorContains(t, err, "interrupted by process restart")

	stored, loadErr := transcripts.Load(context.Background(), id)
	require.NoError(t, loadErr)
	require.Len(t, stored.Messages, 4)
	result := stored.Messages[3]
	require.Equal(t, "tool", result.Role)
	require.Contains(t, result.Content, "interrupted by process restart")
	require.Contains(t, result.Content, "在 genie 仓库定位云函数接入方案",
		"原任务要随结果回到模型上下文，不能只留一句错误")
}

// 取消不是结果：一轮以失败结束，不能把"没做成"总结成答复，也不能把调用方的
// 进度当作已被结论取代。
func TestResumeForToolResultTreatsCancelledCallAsFailure(t *testing.T) {
	recorder := &inputRecordingTeamRuntime{}
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), recorder, writer, nil)
	transcripts := newMemoryTranscripts()
	rt.SetTranscriptStore(transcripts)

	session := seedMailboxSession(t, rt, "fs-cancel", types.SessionWaitingInput)
	id := callerTranscriptID(session.ID, "default", "coordinator")
	require.NoError(t, transcripts.Save(context.Background(), types.AgentTranscript{
		ID:            id,
		FlowSessionID: session.ID,
		TeamID:        "default",
		CallID:        "coordinator",
		Messages:      []types.Message{{Role: "system", Content: "sys"}},
	}))

	err := rt.ResumeForToolResult(context.Background(), session.ID, TeamResultSignal{
		TaskID:     "task-cancel",
		TeamID:     "probe",
		CallerTeam: "default",
		CallID:     "coordinator",
		Status:     string(types.ToolTaskCancelled),
	})
	require.ErrorContains(t, err, "cancelled")
}
