package flow

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// recordCapturingTeamRuntime 的协调员在激活 worker 之前先发布一条 Flow 级 record；
// worker 把自己收到的 records 记下来，供测试断言。
type recordCapturingTeamRuntime struct {
	mu          sync.Mutex
	workerSaw   []string
	workerSawCh chan struct{}
	once        sync.Once
}

func (r *recordCapturingTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	switch req.TeamTurn.TeamID {
	case "default":
		r.mu.Lock()
		first := len(r.workerSaw) == 0
		r.mu.Unlock()
		if !first {
			return types.TeamTurnResult{Turn: req.TeamTurn, Reply: "done", Next: &types.Route{Action: types.NextProceed}}, nil
		}
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Records: []types.SharedRecord{{
				RecordID: "reply-1", Kind: "reply", Name: "CoordinatorReply",
				Scope: types.RecordScopeFlow, Status: types.RecordActive, Revision: 1,
				Data: map[string]any{"repo": "genie"},
			}},
			Next: &types.Route{Action: types.NextActivate, Teams: []string{"worker"}},
		}, nil
	case "worker":
		r.mu.Lock()
		for _, rec := range req.Records {
			r.workerSaw = append(r.workerSaw, rec.Name)
		}
		if len(r.workerSaw) == 0 {
			r.workerSaw = append(r.workerSaw, "<none>")
		}
		r.mu.Unlock()
		r.once.Do(func() { close(r.workerSawCh) })
		return types.TeamTurnResult{Turn: req.TeamTurn, Next: &types.Route{Action: types.NextReturn}}, nil
	}
	return types.TeamTurnResult{Turn: req.TeamTurn, Next: &types.Route{Action: types.NextFail, Reason: "unexpected team"}}, nil
}

func recordCapturingDefinitions() *types.Definitions {
	return &types.Definitions{
		Flow: types.Flow{
			ID:          "records-to-async",
			EntryTeamID: "default",
			Teams: map[string]types.FlowTeamBinding{
				"default": {
					ID: "default", TeamID: "default-team",
					CanActivate: []string{"worker"},
					Inputs:      types.InputSpec{UserMessage: true},
				},
				"worker": {
					ID: "worker", TeamID: "worker-team",
					// worker 声明只要 CoordinatorReply；异步路径必须按它筛选并送达。
					Inputs: types.InputSpec{FlowRecords: []string{"CoordinatorReply"}},
				},
			},
		},
		Teams: map[string]types.Team{
			"default-team": {ID: "default-team"},
			"worker-team":  {ID: "worker-team"},
		},
	}
}

// 回归：后台（异步）派发的 team 必须像同步路径一样收到它 flow 绑定里声明的 Flow 级 record。
// 修复前 dispatchWork 把 records 传成 nil，导致 command call 的
// ${records.CoordinatorReply.Data.repo} 报 "record not found among call inputs"。
func TestAsyncDispatchedTeamReceivesDeclaredFlowRecords(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(files)
	evidence := storage.NewJSONLEvidenceStore(files)
	fake := &recordCapturingTeamRuntime{workerSawCh: make(chan struct{})}
	runtime := newTestRuntime(recordCapturingDefinitions(), fake, sessions, evidence)
	runtime.SetAsyncDispatch(true)

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{FlowID: "records-to-async", Input: "go"})
	require.NoError(t, err)

	// workerSawCh only proves the worker's round has run, not that the
	// dispatcher has finished with the session: it still has to record the
	// work message's delivered event and the result message it posts back to
	// the coordinator. Returning here races those writes against
	// t.TempDir()'s RemoveAll, which fails with "directory not empty" on
	// Linux, so wait for the dispatcher's last write first.
	<-fake.workerSawCh
	waitForResultMessage(t, runtime, result.Session.ID, "default")

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Contains(t, fake.workerSaw, "CoordinatorReply",
		"async-dispatched team must receive the Flow records its binding declares; saw %v", fake.workerSaw)
}
