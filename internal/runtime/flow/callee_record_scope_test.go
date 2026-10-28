package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// calleeRecordTeamRuntime answers a dispatched Team with a record the way an
// agent call does: the record is produced in team scope (its default).
type calleeRecordTeamRuntime struct{}

func (calleeRecordTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	return types.TeamTurnResult{
		Turn:  req.TeamTurn,
		Reply: "版本号是 v9.9.9。",
		Records: []types.SharedRecord{{
			RecordID: "worker-report-1",
			Name:     "WorkerReport",
			Kind:     "agent_result",
			Scope:    types.RecordScopeTeam,
			Status:   types.RecordActive,
			Revision: 1,
			Summary:  "版本号是 v9.9.9。",
		}},
		Next: &types.Route{Action: types.NextProceed},
	}, nil
}

// 被派发的 Team 交回的记录默认是 team 作用域，而调用方按 flow_records 名字在
// flow 作用域里查找。若引擎不在派发路径上提升，调用方的后续回合永远读到空，
// 展示层也拿不到 B→A 的消费事实（A→B fixture，2026-10-28）。
func TestDispatchedCalleeRecordIsVisibleToTheFlow(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(fileStore)
	evidence := storage.NewJSONLEvidenceStore(fileStore)

	defs := testDefinitions()
	defs.Flow.Teams["verify"] = types.FlowTeamBinding{
		ID:     "verify",
		TeamID: "verify-team",
		Inputs: types.InputSpec{FlowRecords: []string{"WorkerReport"}},
	}
	rt := newTestRuntime(defs, calleeRecordTeamRuntime{}, writer, evidence)
	session := seedMailboxSession(t, rt, "fs-callee", types.SessionWaitingInput)

	_, err := rt.runTurnWithOrigin(context.Background(), defs, session, "", nil,
		[]activation{{teamID: "verify", callerTeam: "default"}},
		turnOriginTool, session.CreatedAt, nil)
	require.NoError(t, err)

	records, err := rt.loadHistoricalFlowRecords(context.Background(), session.ID)
	require.NoError(t, err)
	names := make([]string, 0, len(records))
	for _, record := range records {
		names = append(names, record.Name)
	}
	require.Contains(t, names, "WorkerReport",
		"被派发 Team 的产出必须进入 flow 作用域，调用方才能按声明读到")
}

// 入口 Team 自己产出的记录保持 team 作用域：它没有调用方，提升只会让后续
// 用户回合把上一轮的结论当成事实。
func TestEntryTeamRecordStaysInTeamScope(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(fileStore)
	evidence := storage.NewJSONLEvidenceStore(fileStore)

	defs := testDefinitions()
	rt := newTestRuntime(defs, calleeRecordTeamRuntime{}, writer, evidence)
	session := seedMailboxSession(t, rt, "fs-entry", types.SessionWaitingInput)

	_, err := rt.runTurnWithOrigin(context.Background(), defs, session, "查版本号", nil,
		[]activation{{teamID: "default"}}, turnOriginUser, session.CreatedAt, nil)
	require.NoError(t, err)

	records, err := rt.loadHistoricalFlowRecords(context.Background(), session.ID)
	require.NoError(t, err)
	require.Empty(t, records,
		"没有调用方的 Team 产出不进入 flow 作用域")
}
