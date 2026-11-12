package view

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/eval"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// newWebFixture builds a session with enough events for the page: a user turn,
// a model reply, a tool call, and a published record.
func newWebFixture(t *testing.T) (*Handler, string) {
	t.Helper()
	files := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(files)
	const sessionID = "fs_web_fixture"
	ctx := context.Background()

	appendEvent := func(eventType string, turnID string, payload map[string]any) {
		_, err := writer.Append(ctx, sessionID, storage.LayerFlow, storage.SessionEvent{
			EventHeader: types.EventHeader{Type: eventType, FlowSessionID: sessionID, FlowTurnID: turnID},
			Payload:     payload,
		})
		require.NoError(t, err)
	}
	turn := types.FlowTurn{
		ID:            "ft_1",
		FlowSessionID: sessionID,
		Status:        types.TurnWaitingInput,
		Origin:        "user",
		Input:         "看看构建为什么失败",
	}
	// Every real session starts here; the fixture needs it too now that the
	// record reports the session status from this event.
	appendEvent(types.EventFlowSessionCreated, "", map[string]any{
		"session": types.FlowSession{
			ID: sessionID, FlowID: "demo", Status: types.SessionIdle,
		},
	})
	appendEvent(types.EventFlowTurnStarted, "ft_1", map[string]any{"input": turn.Input, "turn": turn})
	appendEvent(types.EventAgentModelResponse, "ft_1", map[string]any{
		"model": "gpt-4o", "text": "我调用一次工具。", "round": 0,
	})
	appendEvent(types.EventToolCallStarted, "ft_1", map[string]any{
		"tool_name": "Bash", "tool_call_id": "call_1",
		"arguments": map[string]any{"command": "make build"},
	})
	appendEvent(types.EventToolCallFailed, "ft_1", map[string]any{
		"tool_name": "Bash", "tool_call_id": "call_1", "error": "exit=2",
	})
	appendEvent(types.EventFlowTurnWaitingInput, "ft_1", map[string]any{
		"turn": turn, "origin": "user",
		"reply": "构建失败，原因是锁文件过期。", "intermediate": false,
	})

	handler := NewRuntimeHandlerWithSessionsAndTasks(nil, writer, nil, nil)
	handler.SetFileStore(files)
	return handler, sessionID
}

// One user message leaves more than one FlowTurn behind: the dispatch to a
// downstream Team, then the continuation after the tool result. The KPI counts
// what the user asked for, and reports the rest separately — a single question
// used to read as four turns.
func TestFactsCountUserTurnsSeparatelyFromInternalOnes(t *testing.T) {
	facts := &eval.SessionFacts{FlowTurns: []eval.FlowTurnFact{
		{FlowTurnID: "ft_user", Origin: "user", Input: "查一下主分支"},
		{FlowTurnID: "ft_tool", Origin: "tool"},
		{FlowTurnID: "ft_dispatch"}, // 下游 Team 的派发轮，没有 turn.started
		{FlowTurnID: "ft_legacy", Input: "旧记录没有 origin"},
	}}

	view := projectFacts(facts)
	require.Equal(t, 2, view.Turns, "user turns: the origin=user one and the legacy one")
	require.Equal(t, 2, view.InternalTurns, "tool continuation and the dispatch turn")
}

// The tree has to show one user question with the execution turns it started;
// a dispatch turn belongs under the turn that called the Team tool, not beside
// it as another 回合.
func TestRecordNestsExecutionTurnsUnderTheirUserTurn(t *testing.T) {
	events := []storage.SessionEvent{
		{EventHeader: types.EventHeader{Type: types.EventFlowTurnStarted, FlowSessionID: "fs_x", FlowTurnID: "ft_user"}},
		{EventHeader: types.EventHeader{Type: types.EventTeamTurnStarted, FlowSessionID: "fs_x",
			FlowTurnID: "ft_exec", TeamTurnID: "tt_1", TeamID: "dev-lead"}},
	}
	facts := &eval.SessionFacts{
		FlowSessionID: "fs_x",
		Events:        events,
		Graph:         eval.NewGraph(events),
		FlowTurns: []eval.FlowTurnFact{
			{FlowTurnID: "ft_user", Origin: "user", Input: "查分支"},
			{FlowTurnID: "ft_exec", Origin: "tool", CausedBy: "ft_user"},
		},
	}

	nodes := buildRecordResponse(facts, 0, 0)["nodes"].([]nodeView)
	byID := make(map[string]nodeView, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	require.True(t, byID["ft_user"].UserTurn)
	require.Equal(t, "fs_x", byID["ft_user"].Parent, "a user turn hangs off the session")
	require.Equal(t, "ft_user", byID["ft_exec"].Parent)
	require.False(t, byID["ft_exec"].UserTurn)
	require.Equal(t, "tool", byID["ft_exec"].Origin)
}

func TestSessionListReturnsIndex(t *testing.T) {
	handler, sessionID := newWebFixture(t)

	recorder := httptest.NewRecorder()
	handler.HandleSessionList(recorder, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Sessions []sessionRow `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Len(t, payload.Sessions, 1)
	require.Equal(t, sessionID, payload.Sessions[0].ID)
	require.Equal(t, 1, payload.Sessions[0].Turns)
}

// The page needs the timeline, the tree and the counts from one call: they
// describe the same events, and fetching them separately would force the page
// to stitch the results together again.
func TestRecordReturnsTimelineTreeAndFacts(t *testing.T) {
	handler, sessionID := newWebFixture(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{id}/record", handler.HandleRecord)
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/record", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Session struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"session"`
		Events   []eventView `json:"events"`
		Nodes    []nodeView  `json:"nodes"`
		Facts    factsView   `json:"facts"`
		Latest   int64       `json:"latest_seq"`
		Truncate int         `json:"truncated"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))

	require.Equal(t, sessionID, payload.Session.ID)
	require.NotEmpty(t, payload.Session.Status, "the record must carry the session status so a settled conversation needs no stream")
	require.Len(t, payload.Events, 6)
	require.Equal(t, int64(6), payload.Latest)
	require.Zero(t, payload.Truncate)

	// Facts come from eval observations, not from counting in the handler.
	require.Equal(t, 1, payload.Facts.Turns)
	require.Equal(t, []string{"gpt-4o"}, payload.Facts.Models)
	// Tool counts come from CallTurnFacts, which need team/call events. This
	// fixture only carries flow-layer events, so they stay zero here; the
	// session.tool_call.* events below are what the page renders.

	kinds := make(map[string]bool)
	for _, node := range payload.Nodes {
		kinds[node.Kind] = true
	}
	require.True(t, kinds["flow_session"], "the tree must include the session root")
	require.True(t, kinds["flow_turn"], "the tree must include the turn")
}

func TestRecordPaginatesByAfterSeq(t *testing.T) {
	handler, sessionID := newWebFixture(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{id}/record", handler.HandleRecord)
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/record?after_seq=3&limit=10", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Events []eventView `json:"events"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Len(t, payload.Events, 3, "seq 4 to 6 remain after after_seq=3")
	require.Equal(t, int64(4), payload.Events[0].Seq)
}

func TestRecordRejectsBadLimit(t *testing.T) {
	handler, sessionID := newWebFixture(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{id}/record", handler.HandleRecord)
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/record?limit=abc", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestRecordUnknownSessionIs404(t *testing.T) {
	handler, _ := newWebFixture(t)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{id}/record", handler.HandleRecord)
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/fs_missing/record", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusNotFound, recorder.Code)
}

// A handler built without a file store must answer, not panic: the same binary
// serves machine clients that never touch the page.
func TestWebHandlersWithoutFileStore(t *testing.T) {
	handler := NewHandler()

	recorder := httptest.NewRecorder()
	handler.HandleSessionList(recorder, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestWebStaticServesEmbeddedPage(t *testing.T) {
	handler := NewHandler()

	for _, path := range []string{"/", "/s/fs_abc"} {
		recorder := httptest.NewRecorder()
		handler.HandleWebStatic(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, recorder.Code, path)
		require.Contains(t, recorder.Header().Get("Content-Type"), "text/html", path)
		require.Contains(t, recorder.Body.String(), "<main id=\"app\"", path)
	}

	recorder := httptest.NewRecorder()
	handler.HandleWebStatic(recorder, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/css")
	require.Contains(t, recorder.Body.String(), "--accent")
}

// The page is served from an embed FS, so a traversal can only ever miss. This
// test pins that: the alternative (http.Dir) would read the real filesystem.
func TestWebStaticRejectsTraversal(t *testing.T) {
	handler := NewHandler()

	for _, path := range []string{
		"/static/../web.go",
		"/static/..%2f..%2fetc/passwd",
		"/static/",
		"/api/unknown",
	} {
		recorder := httptest.NewRecorder()
		handler.HandleWebStatic(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusNotFound, recorder.Code, path)
		require.NotContains(t, recorder.Body.String(), "package view", path)
	}
}

// HandleRun shares its path with the list endpoint; the method must decide.
func TestRunDispatchesGetToList(t *testing.T) {
	handler, sessionID := newWebFixture(t)

	recorder := httptest.NewRecorder()
	handler.HandleRun(recorder, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.True(t, strings.Contains(recorder.Body.String(), sessionID))
}
