package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type jsonRPCFlowRuntimeStub struct {
	startCalls    int
	handleCalls   int
	resumeCalls   int
	approvalCalls int
	status        types.FlowSession
	startResult   types.FlowTurnResult
	handleResult  types.FlowTurnResult
	resumeResult  types.FlowTurnResult
	approvalRes   types.FlowTurnResult
	approvalErr   error
	lastDecision  types.HITLResponse
	err           error
}

func (s *jsonRPCFlowRuntimeStub) Start(context.Context, types.StartFlowRequest) (types.FlowTurnResult, error) {
	s.startCalls++
	return s.startResult, s.err
}

func (s *jsonRPCFlowRuntimeStub) HandleInput(context.Context, string, string) (types.FlowTurnResult, error) {
	s.handleCalls++
	return s.handleResult, s.err
}

func (s *jsonRPCFlowRuntimeStub) Resume(context.Context, string, string) (types.FlowTurnResult, error) {
	s.resumeCalls++
	return s.resumeResult, s.err
}

func (s *jsonRPCFlowRuntimeStub) Cancel(context.Context, string) error {
	return nil
}

func (s *jsonRPCFlowRuntimeStub) Status(context.Context, string) (types.FlowSession, error) {
	return s.status, nil
}

func (s *jsonRPCFlowRuntimeStub) ResumeApprovalWithResponse(
	_ context.Context, _ string, decision types.HITLResponse,
) (types.FlowTurnResult, error) {
	s.approvalCalls++
	s.lastDecision = decision
	return s.approvalRes, s.approvalErr
}

// jsonRPCNoApprovalRuntimeStub is a FlowRuntime that cannot resume approvals at
// all, so the "approve" method has to report the missing capability instead of
// pretending the decision was applied. It deliberately does not embed
// jsonRPCFlowRuntimeStub: embedding would promote the approval methods too and
// the stub would silently satisfy the optional interface.
type jsonRPCNoApprovalRuntimeStub struct {
	status types.FlowSession
}

var _ types.FlowRuntime = (*jsonRPCNoApprovalRuntimeStub)(nil)

func (s *jsonRPCNoApprovalRuntimeStub) Start(context.Context, types.StartFlowRequest) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, nil
}

func (s *jsonRPCNoApprovalRuntimeStub) HandleInput(context.Context, string, string) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, nil
}

func (s *jsonRPCNoApprovalRuntimeStub) Resume(context.Context, string, string) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, nil
}

func (s *jsonRPCNoApprovalRuntimeStub) Cancel(context.Context, string) error { return nil }

func (s *jsonRPCNoApprovalRuntimeStub) Status(context.Context, string) (types.FlowSession, error) {
	return s.status, nil
}

func testFlowTurnResult(status types.SessionStatus, reply string) types.FlowTurnResult {
	return types.FlowTurnResult{
		Session: types.FlowSession{
			ID:        "fs-1",
			FlowID:    "test-flow",
			Status:    status,
			CreatedAt: time.Unix(1, 0).UTC(),
			UpdatedAt: time.Unix(2, 0).UTC(),
		},
		Turn: types.FlowTurn{
			ID:     "ft-1",
			Status: types.TurnCompleted,
			Input:  "hello",
		},
		Reply: reply,
		Records: []types.SharedRecord{{
			RecordID: "record-1",
			Kind:     "diagnosis",
			Name:     "DiagnosisReport",
			Summary:  "found one issue",
			Status:   types.RecordActive,
			Revision: 1,
		}},
		TeamResults: []types.TeamTurnResult{{
			Usage: types.TokenUsage{
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalTokens:      30,
			},
		}},
	}
}

func TestJSONRPCServerTurnStartsNewSession(t *testing.T) {
	stub := &jsonRPCFlowRuntimeStub{
		startResult: testFlowTurnResult(types.SessionWaitingInput, "done"),
	}
	var output bytes.Buffer
	server := newJSONRPCServer(stub, "test-flow", &output)

	err := server.Serve(context.Background(), strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"turn","params":{"input":"hello"}}`+"\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	if stub.startCalls != 1 {
		t.Fatalf("start calls = %d, want 1", stub.startCalls)
	}

	var response jsonRPCResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatalf("unexpected error: %+v", response.Error)
	}
	result, ok := response.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", response.Result)
	}
	if result["session_id"] != "fs-1" || result["reply"] != "done" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result["usage"].(map[string]any)["total_tokens"] != float64(30) {
		t.Fatalf("unexpected usage: %#v", result["usage"])
	}
}

func TestJSONRPCServerContinuesSessionWithCorrectMethod(t *testing.T) {
	t.Run("waiting input uses resume", func(t *testing.T) {
		stub := &jsonRPCFlowRuntimeStub{
			status:       types.FlowSession{ID: "fs-1", Status: types.SessionWaitingInput},
			resumeResult: testFlowTurnResult(types.SessionWaitingInput, "resumed"),
		}
		var output bytes.Buffer
		server := newJSONRPCServer(stub, "test-flow", &output)
		err := server.Serve(context.Background(), strings.NewReader(
			`{"jsonrpc":"2.0","id":"r1","method":"turn","params":{"session_id":"fs-1","input":"continue"}}`+"\n",
		))
		if err != nil {
			t.Fatal(err)
		}
		if stub.resumeCalls != 1 || stub.handleCalls != 0 {
			t.Fatalf("resume=%d handle=%d", stub.resumeCalls, stub.handleCalls)
		}
	})

	t.Run("running session uses handle input", func(t *testing.T) {
		stub := &jsonRPCFlowRuntimeStub{
			status:       types.FlowSession{ID: "fs-1", Status: types.SessionRunning},
			handleResult: testFlowTurnResult(types.SessionWaitingInput, "handled"),
		}
		var output bytes.Buffer
		server := newJSONRPCServer(stub, "test-flow", &output)
		err := server.Serve(context.Background(), strings.NewReader(
			`{"jsonrpc":"2.0","id":2,"method":"turn","params":{"session_id":"fs-1","input":"continue"}}`+"\n",
		))
		if err != nil {
			t.Fatal(err)
		}
		if stub.handleCalls != 1 || stub.resumeCalls != 0 {
			t.Fatalf("resume=%d handle=%d", stub.resumeCalls, stub.handleCalls)
		}
	})

	// Legacy sessions recorded before the lifecycle refactor can still be
	// continued with the same session_id: completed is no longer a sealed
	// terminal state.
	t.Run("legacy completed session uses handle input", func(t *testing.T) {
		stub := &jsonRPCFlowRuntimeStub{
			status:       types.FlowSession{ID: "fs-1", Status: types.SessionCompleted},
			handleResult: testFlowTurnResult(types.SessionWaitingInput, "handled"),
		}
		var output bytes.Buffer
		server := newJSONRPCServer(stub, "test-flow", &output)
		err := server.Serve(context.Background(), strings.NewReader(
			`{"jsonrpc":"2.0","id":3,"method":"turn","params":{"session_id":"fs-1","input":"continue"}}`+"\n",
		))
		if err != nil {
			t.Fatal(err)
		}
		if stub.handleCalls != 1 || stub.resumeCalls != 0 {
			t.Fatalf("resume=%d handle=%d", stub.resumeCalls, stub.handleCalls)
		}
	})
}

// A session parked at an approval gate must not accept a plain turn: routing
// the input through HandleInput would silently bypass the human-in-the-loop
// gate documented in CONNECT-CLI-JSONRPC.md §6.
func TestJSONRPCServerRejectsTurnWhileWaitingApproval(t *testing.T) {
	stub := &jsonRPCFlowRuntimeStub{
		status:       types.FlowSession{ID: "fs-1", Status: types.SessionWaitingApproval},
		handleResult: testFlowTurnResult(types.SessionWaitingApproval, "must not run"),
		resumeResult: testFlowTurnResult(types.SessionWaitingApproval, "must not run"),
	}
	var output bytes.Buffer
	server := newJSONRPCServer(stub, "test-flow", &output)

	err := server.Serve(context.Background(), strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"turn","params":{"session_id":"fs-1","input":"continue"}}`+"\n",
	))
	require.NoError(t, err)
	require.Equal(t, 0, stub.handleCalls, "HandleInput must not bypass the approval gate")
	require.Equal(t, 0, stub.resumeCalls, "Resume must not bypass the approval gate")

	var response jsonRPCResponse
	require.NoError(t, json.Unmarshal(output.Bytes(), &response))
	require.NotNil(t, response.Error, "turn must be rejected")
	require.Equal(t, jsonRPCSessionFailed, response.Error.Code)
	require.Contains(t, response.Error.Message, "approval")

	data, ok := response.Error.Data.(map[string]any)
	require.True(t, ok, "error data = %#v", response.Error.Data)
	require.Equal(t, "fs-1", data["session_id"])
	require.Equal(t, string(types.SessionWaitingApproval), data["status"])
}

func TestExecuteFlowTurnRejectsWaitingApprovalSession(t *testing.T) {
	stub := &jsonRPCFlowRuntimeStub{
		status: types.FlowSession{ID: "fs-1", Status: types.SessionWaitingApproval},
	}
	_, err := executeFlowTurn(context.Background(), stub, "test-flow", "fs-1", "continue")
	require.ErrorIs(t, err, errTurnBlockedByApproval)
	require.Equal(t, 0, stub.handleCalls)
	require.Equal(t, 0, stub.resumeCalls)
}

func TestJSONRPCServerMapsCanceledTurnToCanceledErrorCode(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "bare context.Canceled", err: context.Canceled},
		{name: "wrapped context.Canceled", err: fmt.Errorf("agent round aborted: %w", context.Canceled)},
	} {
		t.Run(test.name, func(t *testing.T) {
			stub := &jsonRPCFlowRuntimeStub{err: test.err}
			var output bytes.Buffer
			server := newJSONRPCServer(stub, "test-flow", &output)
			require.NoError(t, server.Serve(context.Background(), strings.NewReader(
				`{"jsonrpc":"2.0","id":1,"method":"turn","params":{"input":"hello"}}`+"\n",
			)))

			var response jsonRPCResponse
			require.NoError(t, json.Unmarshal(output.Bytes(), &response))
			require.NotNil(t, response.Error)
			require.Equal(t, jsonRPCRequestCancelled, response.Error.Code)
		})
	}
}

func TestJSONRPCServerIgnoresNotifications(t *testing.T) {
	stub := &jsonRPCFlowRuntimeStub{
		startResult: testFlowTurnResult(types.SessionWaitingInput, "done"),
	}
	var output bytes.Buffer
	server := newJSONRPCServer(stub, "test-flow", &output)

	err := server.Serve(context.Background(), strings.NewReader(
		`{"jsonrpc":"2.0","method":"turn","params":{"input":"hello"}}`+"\n"+
			`{"jsonrpc":"2.0","id":1,"method":"turn","params":{"input":"hello"}}`+"\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("response lines = %d, want 1:\n%s", len(lines), output.String())
	}
	if stub.startCalls != 1 {
		t.Fatalf("start calls = %d, want 1", stub.startCalls)
	}
}

func TestValidJSONRPCID(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want bool
	}{
		{raw: `"request-1"`, want: true},
		{raw: `1`, want: true},
		{raw: `1.5`, want: true},
		{raw: `null`, want: false},
		{raw: `true`, want: false},
		{raw: `{}`, want: false},
		{raw: `[]`, want: false},
	} {
		if got := validJSONRPCID(json.RawMessage(test.raw)); got != test.want {
			t.Errorf("validJSONRPCID(%s) = %v, want %v", test.raw, got, test.want)
		}
	}
}

func TestJSONRPCServerReturnsProtocolErrorsAndContinues(t *testing.T) {
	stub := &jsonRPCFlowRuntimeStub{
		startResult: testFlowTurnResult(types.SessionWaitingInput, "done"),
	}
	var output bytes.Buffer
	server := newJSONRPCServer(stub, "test-flow", &output)

	input := strings.Join([]string{
		`not-json`,
		`{"jsonrpc":"2.0","id":1,"method":"unknown","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"turn","params":{"input":"hello"}}`,
	}, "\n") + "\n"
	if err := server.Serve(context.Background(), strings.NewReader(input)); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("response lines = %d, want 3:\n%s", len(lines), output.String())
	}
	for i, line := range lines {
		var response jsonRPCResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
	}
	if stub.startCalls != 1 {
		t.Fatalf("start calls = %d, want 1", stub.startCalls)
	}
}

func TestJSONRPCTurnResultFrom(t *testing.T) {
	t.Run("empty result", func(t *testing.T) {
		got := jsonRPCTurnResultFrom(types.FlowTurnResult{})
		require.Empty(t, got.SessionID)
		require.Empty(t, got.FlowTurnID)
		require.Empty(t, got.Status)
		require.Empty(t, got.Reply)
		require.Empty(t, got.Records)
		require.Empty(t, got.Error)
		require.Zero(t, got.Usage.TotalTokens)
	})

	t.Run("full result", func(t *testing.T) {
		in := testFlowTurnResult(types.SessionWaitingInput, "hello")
		got := jsonRPCTurnResultFrom(in)
		require.Equal(t, "fs-1", got.SessionID)
		require.Equal(t, "ft-1", got.FlowTurnID)
		require.Equal(t, types.SessionWaitingInput, got.Status)
		require.Equal(t, "hello", got.Reply)
		require.Len(t, got.Records, 1)
		require.Equal(t, "record-1", got.Records[0].RecordID)
		require.Equal(t, "diagnosis", got.Records[0].Kind)
		require.Equal(t, "DiagnosisReport", got.Records[0].Name)
		require.Equal(t, "found one issue", got.Records[0].Summary)
		require.Equal(t, types.RecordActive, got.Records[0].Status)
		require.Equal(t, 1, got.Records[0].Revision)
		require.Equal(t, 30, got.Usage.TotalTokens)
	})

	t.Run("error result preserves error", func(t *testing.T) {
		in := testFlowTurnResult(types.SessionFailed, "")
		in.Error = "something broke"
		got := jsonRPCTurnResultFrom(in)
		require.Equal(t, "something broke", got.Error)
		require.Equal(t, types.SessionFailed, got.Status)
	})
}

func TestAggregateTeamUsage(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got := aggregateTeamUsage(nil)
		require.Zero(t, got.TotalTokens)
	})

	t.Run("accumulates across agents", func(t *testing.T) {
		results := []types.TeamTurnResult{
			{Usage: types.TokenUsage{PromptTokens: 10, CompletionTokens: 20, ReasoningTokens: 1, TotalTokens: 31, PromptCacheHitTokens: 2, PromptCacheMissTokens: 3, CacheReadInputTokens: 4, CacheCreationInputTokens: 5}},
			{Usage: types.TokenUsage{PromptTokens: 5, CompletionTokens: 15, ReasoningTokens: 2, TotalTokens: 22, PromptCacheHitTokens: 6, PromptCacheMissTokens: 7, CacheReadInputTokens: 8, CacheCreationInputTokens: 9}},
		}
		got := aggregateTeamUsage(results)
		require.Equal(t, 15, got.PromptTokens)
		require.Equal(t, 35, got.CompletionTokens)
		require.Equal(t, 3, got.ReasoningTokens)
		require.Equal(t, 53, got.TotalTokens)
		require.Equal(t, 8, got.PromptCacheHitTokens)
		require.Equal(t, 10, got.PromptCacheMissTokens)
		require.Equal(t, 12, got.CacheReadInputTokens)
		require.Equal(t, 14, got.CacheCreationInputTokens)
	})

	t.Run("dedup is caller responsibility", func(t *testing.T) {
		// aggregateTeamUsage simply sums; identical team results are summed twice.
		results := []types.TeamTurnResult{
			{Usage: types.TokenUsage{TotalTokens: 10}},
			{Usage: types.TokenUsage{TotalTokens: 10}},
		}
		got := aggregateTeamUsage(results)
		require.Equal(t, 20, got.TotalTokens)
	})
}

func TestResolveAPIKey(t *testing.T) {
	t.Setenv("HERON_TEST_KEY", "env-secret")
	t.Setenv("HERON_EMPTY_KEY", "")

	for _, test := range []struct {
		name       string
		configured string
		fallback   string
		want       string
	}{
		{name: "env reference exists", configured: "${HERON_TEST_KEY}", fallback: "fallback", want: "env-secret"},
		{name: "env reference missing", configured: "${HERON_MISSING_KEY}", fallback: "fallback", want: ""},
		{name: "env reference empty value", configured: "${HERON_EMPTY_KEY}", fallback: "fallback", want: ""},
		{name: "literal", configured: "sk-literal", fallback: "fallback", want: "sk-literal"},
		{name: "empty configured uses fallback", configured: "", fallback: "fallback", want: "fallback"},
		{name: "empty configured no fallback", configured: "", fallback: "", want: ""},
		{name: "unbalanced prefix only", configured: "${HERON_TEST_KEY", fallback: "fallback", want: "${HERON_TEST_KEY"},
		{name: "unbalanced suffix only", configured: "HERON_TEST_KEY}", fallback: "fallback", want: "HERON_TEST_KEY}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := resolveAPIKey(test.configured, test.fallback)
			require.Equal(t, test.want, got)
		})
	}
}

func TestJSONRPCServerReturnsFlowErrorWithoutExitingConnection(t *testing.T) {
	stub := &jsonRPCFlowRuntimeStub{
		err: errors.New("flow turn failed"),
	}
	var output bytes.Buffer
	server := newJSONRPCServer(stub, "test-flow", &output)
	if err := server.Serve(context.Background(), strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"turn","params":{"input":"hello"}}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"turn","params":{"input":"again"}}`+"\n",
	)); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("response lines = %d, want 2", len(lines))
	}
	if stub.startCalls != 2 {
		t.Fatalf("start calls = %d, want 2", stub.startCalls)
	}
}

// serveOne sends a single request line and returns the decoded response.
func serveOne(t *testing.T, runtime types.FlowRuntime, line string) jsonRPCResponse {
	t.Helper()
	var output bytes.Buffer
	server := newJSONRPCServer(runtime, "test-flow", &output)
	require.NoError(t, server.Serve(context.Background(), strings.NewReader(line+"\n")))

	var response jsonRPCResponse
	require.NoError(t, json.Unmarshal(output.Bytes(), &response))
	return response
}

func approvalWaitingStub() *jsonRPCFlowRuntimeStub {
	return &jsonRPCFlowRuntimeStub{
		status:      types.FlowSession{ID: "fs-1", Status: types.SessionWaitingApproval},
		approvalRes: testFlowTurnResult(types.SessionWaitingInput, "resumed after approval"),
	}
}

// TestJSONRPCServerApproveResumesGate is the regression test for the JSON-RPC
// approval deadlock: "turn" is rejected at a gate on purpose, so without an
// "approve" method a JSON-RPC-only bridge has no way to advance the session.
func TestJSONRPCServerApproveResumesGate(t *testing.T) {
	stub := approvalWaitingStub()
	response := serveOne(t, stub,
		`{"jsonrpc":"2.0","id":1,"method":"approve","params":{"session_id":"fs-1","approval_id":"req-1","approved":true,"reason":"looks safe"}}`)

	require.Nil(t, response.Error, "approve must be a supported method")
	require.Equal(t, 1, stub.approvalCalls)
	require.Equal(t, "req-1", stub.lastDecision.RequestID)
	require.True(t, stub.lastDecision.Approved)
	require.Equal(t, "looks safe", stub.lastDecision.Reason)
	require.Equal(t, "jsonrpc", stub.lastDecision.Channel, "channel must record where the decision came from")
	require.False(t, stub.lastDecision.DecidedAt.IsZero(), "the decision must be timestamped for audit")

	result, ok := response.Result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fs-1", result["session_id"])
	require.Equal(t, "resumed after approval", result["reply"])
}

// TestJSONRPCServerApproveRecordsDenial verifies an explicit denial is passed
// through as false rather than dropped: false is a decision, not a default.
func TestJSONRPCServerApproveRecordsDenial(t *testing.T) {
	stub := approvalWaitingStub()
	response := serveOne(t, stub,
		`{"jsonrpc":"2.0","id":1,"method":"approve","params":{"session_id":"fs-1","approval_id":"req-1","approved":false,"reason":"too risky"}}`)

	require.Nil(t, response.Error)
	require.Equal(t, 1, stub.approvalCalls)
	require.False(t, stub.lastDecision.Approved)
	require.Equal(t, "too risky", stub.lastDecision.Reason)
}

// TestJSONRPCServerApproveRequiresExplicitDecision verifies an omitted
// "approved" is a protocol error instead of a silent deny or approve.
func TestJSONRPCServerApproveRequiresExplicitDecision(t *testing.T) {
	stub := approvalWaitingStub()
	response := serveOne(t, stub,
		`{"jsonrpc":"2.0","id":1,"method":"approve","params":{"session_id":"fs-1","approval_id":"req-1"}}`)

	require.NotNil(t, response.Error)
	require.Equal(t, jsonRPCInvalidParams, response.Error.Code)
	require.Equal(t, 0, stub.approvalCalls, "no decision must reach the runtime")
}

// TestJSONRPCServerApproveRequiresSessionAndApprovalID covers the remaining
// invalid-params branches.
func TestJSONRPCServerApproveRequiresSessionAndApprovalID(t *testing.T) {
	cases := map[string]string{
		"missing session_id":  `{"jsonrpc":"2.0","id":1,"method":"approve","params":{"approval_id":"req-1","approved":true}}`,
		"missing approval_id": `{"jsonrpc":"2.0","id":1,"method":"approve","params":{"session_id":"fs-1","approved":true}}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			stub := approvalWaitingStub()
			response := serveOne(t, stub, line)
			require.NotNil(t, response.Error)
			require.Equal(t, jsonRPCInvalidParams, response.Error.Code)
			require.Equal(t, 0, stub.approvalCalls)
		})
	}
}

// TestJSONRPCServerApproveRejectsSessionNotWaiting verifies a mistimed approve
// is reported as a state error (-32002), not as a generic turn failure.
func TestJSONRPCServerApproveRejectsSessionNotWaiting(t *testing.T) {
	stub := &jsonRPCFlowRuntimeStub{
		status:      types.FlowSession{ID: "fs-1", Status: types.SessionWaitingInput},
		approvalRes: testFlowTurnResult(types.SessionWaitingInput, "must not run"),
	}
	response := serveOne(t, stub,
		`{"jsonrpc":"2.0","id":1,"method":"approve","params":{"session_id":"fs-1","approval_id":"req-1","approved":true}}`)

	require.NotNil(t, response.Error)
	require.Equal(t, jsonRPCSessionFailed, response.Error.Code)
	require.Equal(t, 0, stub.approvalCalls, "approve must not run against a session that is not at a gate")
}

// TestJSONRPCServerApproveReportsUnconfiguredRuntime verifies a runtime without
// approval support says so (-32003) rather than silently accepting the decision.
func TestJSONRPCServerApproveReportsUnconfiguredRuntime(t *testing.T) {
	stub := &jsonRPCNoApprovalRuntimeStub{
		status: types.FlowSession{ID: "fs-1", Status: types.SessionWaitingApproval},
	}
	response := serveOne(t, stub,
		`{"jsonrpc":"2.0","id":1,"method":"approve","params":{"session_id":"fs-1","approval_id":"req-1","approved":true}}`)

	require.NotNil(t, response.Error)
	require.Equal(t, jsonRPCRuntimeFailed, response.Error.Code)
}

// TestJSONRPCTurnResultCarriesPendingApprovalID verifies the caller can learn
// the approval_id it needs for "approve" from the turn that parked the session.
func TestJSONRPCTurnResultCarriesPendingApprovalID(t *testing.T) {
	stub := &jsonRPCFlowRuntimeStub{
		startResult: testFlowTurnResult(types.SessionWaitingApproval, "needs approval"),
	}
	stub.startResult.PendingApprovals = []types.AgentPendingApproval{{
		RequestID:  "req-1",
		ToolName:   "Bash",
		ToolCallID: "call_1",
		Reason:     "dangerous command",
	}}
	response := serveOne(t, stub,
		`{"jsonrpc":"2.0","id":1,"method":"turn","params":{"input":"run it"}}`)

	require.Nil(t, response.Error)
	result, ok := response.Result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, string(types.SessionWaitingApproval), result["status"])

	pending, ok := result["pending_approvals"].([]any)
	require.True(t, ok, "pending_approvals must be exposed so the caller can approve")
	require.Len(t, pending, 1)
	require.Equal(t, "req-1", pending[0].(map[string]any)["request_id"])
}

// TestJSONRPCServerRejectsTurnWhileWaitingTool verifies a turn against a
// session parked on async Tool tasks is a state error (-32002): a new turn
// cannot collect those results, and starting one would orphan the tasks.
func TestJSONRPCServerRejectsTurnWhileWaitingTool(t *testing.T) {
	stub := &jsonRPCFlowRuntimeStub{
		status:       types.FlowSession{ID: "fs-1", Status: types.SessionWaitingTool},
		handleResult: testFlowTurnResult(types.SessionWaitingTool, "must not run"),
	}
	response := serveOne(t, stub,
		`{"jsonrpc":"2.0","id":1,"method":"turn","params":{"session_id":"fs-1","input":"continue"}}`)

	require.NotNil(t, response.Error)
	require.Equal(t, jsonRPCSessionFailed, response.Error.Code)
	require.Equal(t, 0, stub.handleCalls, "no new turn may start while Tool tasks are pending")
	require.Equal(t, 0, stub.resumeCalls)

	data, ok := response.Error.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fs-1", data["session_id"])
	require.Equal(t, string(types.SessionWaitingTool), data["status"])
}
