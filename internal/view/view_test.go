package view

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestHandler_HandleStatus(t *testing.T) {
	h := NewHandler()
	req := httptest.NewRequest("GET", "/status", nil)
	rec := httptest.NewRecorder()

	h.HandleStatus(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandler_HandleRun(t *testing.T) {
	h := NewHandler()
	body := `{"message":"hello"}`
	req := httptest.NewRequest("POST", "/run", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleRun(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestSSEWriter_WriteEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	writer, err := NewSSEWriter(rec)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}

	err = writer.WriteChunk("hello")
	if err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}

	body := rec.Body.String()
	if body == "" {
		t.Error("expected non-empty body")
	}
}

type handlerFlowRuntime struct {
	started bool
	handled bool
}

func (r *handlerFlowRuntime) Start(_ context.Context, req types.StartFlowRequest) (types.FlowTurnResult, error) {
	r.started = true
	return types.FlowTurnResult{
		Session: types.FlowSession{ID: "fs-1", FlowID: req.FlowID},
		Turn:    types.FlowTurn{Input: req.Input},
	}, nil
}

func (r *handlerFlowRuntime) HandleInput(_ context.Context, sessionID, input string) (types.FlowTurnResult, error) {
	r.handled = true
	return types.FlowTurnResult{
		Session: types.FlowSession{ID: sessionID},
		Turn:    types.FlowTurn{Input: input},
	}, nil
}

func (r *handlerFlowRuntime) Resume(context.Context, string, string) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, nil
}
func (r *handlerFlowRuntime) Cancel(context.Context, string) error { return nil }
func (r *handlerFlowRuntime) Status(context.Context, string) (types.FlowSession, error) {
	return types.FlowSession{}, nil
}

func TestHandler_HandleRunWithFlowSessionIDUsesHandleInput(t *testing.T) {
	runtime := &handlerFlowRuntime{}
	handler := NewRuntimeHandler(runtime)
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"flow_session_id":"fs-1","message":"continue"}`))
	rec := httptest.NewRecorder()

	handler.HandleRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !runtime.handled || runtime.started {
		t.Fatalf("expected HandleInput only, started=%v handled=%v", runtime.started, runtime.handled)
	}
}

func TestHandler_HandleTurn(t *testing.T) {
	runtime := &handlerFlowRuntime{}
	handler := NewRuntimeHandler(runtime)
	req := httptest.NewRequest(http.MethodPost, "/turn?session_id=fs-1", strings.NewReader(`{"input":"next"}`))
	rec := httptest.NewRecorder()

	handler.HandleTurn(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result types.FlowTurnResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Turn.Input != "next" || !runtime.handled {
		t.Fatalf("unexpected result %#v, handled=%v", result, runtime.handled)
	}
}

type richHandlerFlowRuntime struct {
	handlerFlowRuntime
	blocks []types.ContextBlock
}

func (r *richHandlerFlowRuntime) Start(context.Context, types.StartFlowRequest) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, nil
}

func (r *richHandlerFlowRuntime) HandleInputWithContext(_ context.Context, _ string, input string, blocks []types.ContextBlock) (types.FlowTurnResult, error) {
	r.blocks = blocks
	return types.FlowTurnResult{Session: types.FlowSession{ID: "fs-rich"}, Turn: types.FlowTurn{Input: input}}, nil
}

func (r *richHandlerFlowRuntime) ResumeWithContext(context.Context, string, string, []types.ContextBlock) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, nil
}

func TestHandler_HandleTurnPassesMultimediaContent(t *testing.T) {
	runtime := &richHandlerFlowRuntime{}
	handler := NewRuntimeHandler(runtime)
	body := `{"content":[{"type":"text","text":"分析图片"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"cG5n"}}]}`
	req := httptest.NewRequest(http.MethodPost, "/turn?session_id=fs-rich", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleTurn(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, runtime.blocks, 1)
	require.Equal(t, "分析图片", runtime.blocks[0].Text)
	require.Len(t, runtime.blocks[0].Parts, 1)
	require.Equal(t, "image", runtime.blocks[0].Parts[0].Type)
}

func TestHandler_HandleStreamReplaysFromLastEventID(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(fileStore)
	for _, eventType := range []string{"one", "two", types.EventFlowTurnCompleted} {
		if _, err := writer.Append(context.Background(), "fs-1", storage.LayerFlow, storage.SessionEvent{EventHeader: types.EventHeader{Type: eventType}}); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewRuntimeHandlerWithSessions(&handlerFlowRuntime{}, writer)
	req := httptest.NewRequest(http.MethodGet, "/stream?session_id=fs-1", nil)
	req.Header.Set("Last-Event-ID", "1")
	rec := httptest.NewRecorder()

	handler.HandleStream(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `"type":"one"`) || !strings.Contains(body, `"type":"two"`) || !strings.Contains(body, `"type":"flow_turn.completed"`) {
		t.Fatalf("unexpected SSE replay: %s", body)
	}
	if !strings.Contains(body, "id: 2") || !strings.Contains(body, "id: 3") {
		t.Fatalf("missing event ids: %s", body)
	}
}

func TestHandler_HandleRecoveryStatus(t *testing.T) {
	runtime := &recoveryHandlerRuntime{}
	handler := NewRuntimeHandler(runtime)
	req := httptest.NewRequest(http.MethodGet, "/recovery/status?session_id=fs-1", nil)
	rec := httptest.NewRecorder()

	handler.HandleRecoveryStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var status types.RecoveryStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode recovery status: %v", err)
	}
	if status.Session.ID != "fs-1" || len(status.Interrupted) != 1 {
		t.Fatalf("unexpected recovery status: %#v", status)
	}
}

func TestHandler_HandleTaskStatusAndCancel(t *testing.T) {
	tasks := &testTaskStore{
		task: types.ToolTask{ID: "task-1", Status: types.ToolTaskRunning, Progress: 0.25},
	}
	handler := NewRuntimeHandlerWithSessionsAndTasks(
		&handlerFlowRuntime{},
		nil,
		tasks,
		tasks,
	)

	statusReq := httptest.NewRequest(http.MethodGet, "/tasks?task_id=task-1", nil)
	statusRec := httptest.NewRecorder()
	handler.HandleTaskStatus(statusRec, statusReq)
	require.Equal(t, http.StatusOK, statusRec.Code)
	require.Contains(t, statusRec.Body.String(), `"task-1"`)

	cancelReq := httptest.NewRequest(http.MethodPost, "/tasks/cancel", strings.NewReader(`{"task_id":"task-1"}`))
	cancelRec := httptest.NewRecorder()
	handler.HandleTaskCancel(cancelRec, cancelReq)
	require.Equal(t, http.StatusOK, cancelRec.Code)
	require.Equal(t, types.ToolTaskCancelled, tasks.task.Status)
}

type approvalHandlerRuntime struct {
	approved bool
}

func (r *approvalHandlerRuntime) Start(context.Context, types.StartFlowRequest) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, nil
}
func (r *approvalHandlerRuntime) HandleInput(context.Context, string, string) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, nil
}
func (r *approvalHandlerRuntime) Resume(context.Context, string, string) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, nil
}
func (r *approvalHandlerRuntime) Cancel(context.Context, string) error { return nil }
func (r *approvalHandlerRuntime) Status(context.Context, string) (types.FlowSession, error) {
	return types.FlowSession{}, nil
}
func (r *approvalHandlerRuntime) ResumeApproval(
	_ context.Context, sessionID, approvalID string, approved bool, reason string,
) (types.FlowTurnResult, error) {
	r.approved = approved
	return types.FlowTurnResult{
		Session: types.FlowSession{ID: sessionID, Status: types.SessionCompleted},
		Reply:   approvalID + ":" + reason,
	}, nil
}

func TestHandler_HandleApprovalResumesFlow(t *testing.T) {
	runtime := &approvalHandlerRuntime{}
	handler := NewRuntimeHandler(runtime)
	req := httptest.NewRequest(
		http.MethodPost,
		"/approvals?session_id=fs-approval",
		strings.NewReader(`{"approval_id":"approval-1","approved":true,"reason":"approved"}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleApproval(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, runtime.approved)
	require.Contains(t, rec.Body.String(), `"fs-approval"`)
}

func TestHandler_HandleTaskStreamSendsInitialAndFinalState(t *testing.T) {
	tasks := &testTaskSubscriber{
		task: types.ToolTask{ID: "task-stream", Status: types.ToolTaskCompleted, Progress: 1},
	}
	handler := NewRuntimeHandlerWithSessionsAndTasks(nil, nil, tasks, tasks)
	req := httptest.NewRequest(http.MethodGet, "/tasks/stream?task_id=task-stream", nil)
	rec := httptest.NewRecorder()

	handler.HandleTaskStream(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "event: task")
	require.Contains(t, rec.Body.String(), `"task-stream"`)
}

type testTaskStore struct {
	task types.ToolTask
}

func (s *testTaskStore) Save(_ context.Context, task types.ToolTask) error {
	s.task = task
	return nil
}
func (s *testTaskStore) Load(_ context.Context, id string) (*types.ToolTask, error) {
	if id != s.task.ID {
		return nil, errors.New("not found")
	}
	task := s.task
	return &task, nil
}
func (s *testTaskStore) List(context.Context) ([]types.ToolTask, error) {
	return []types.ToolTask{s.task}, nil
}
func (s *testTaskStore) Delete(context.Context, string) error { return nil }
func (s *testTaskStore) Cancel(_ context.Context, id string) error {
	if id == s.task.ID {
		s.task.Status = types.ToolTaskCancelled
	}
	return nil
}

type testTaskSubscriber struct {
	task types.ToolTask
}

func (s *testTaskSubscriber) Save(context.Context, types.ToolTask) error { return nil }
func (s *testTaskSubscriber) Load(context.Context, string) (*types.ToolTask, error) {
	task := s.task
	return &task, nil
}
func (s *testTaskSubscriber) List(context.Context) ([]types.ToolTask, error) {
	return []types.ToolTask{s.task}, nil
}
func (s *testTaskSubscriber) Delete(context.Context, string) error { return nil }
func (s *testTaskSubscriber) Subscribe(ctx context.Context, _ string) (<-chan types.ToolTask, error) {
	ch := make(chan types.ToolTask, 1)
	ch <- s.task
	close(ch)
	return ch, nil
}
func (s *testTaskSubscriber) Cancel(context.Context, string) error { return nil }

type recoveryHandlerRuntime struct{ handlerFlowRuntime }

func (r *recoveryHandlerRuntime) RecoveryStatus(context.Context, string) (types.RecoveryStatus, error) {
	return types.RecoveryStatus{
		Session: types.FlowSession{ID: "fs-1", Status: types.SessionInterrupted},
		Interrupted: []types.InterruptedExecution{{
			Kind:        "call_turn",
			CallTurnID:  "mt-1",
			SafeToRetry: false,
		}},
	}, nil
}

func (r *recoveryHandlerRuntime) Recover(context.Context, string, types.RecoveryRequest) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{Session: types.FlowSession{ID: "fs-1"}}, nil
}
