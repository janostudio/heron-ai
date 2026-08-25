package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type jsonRPCFlowRuntimeStub struct {
	startCalls   int
	handleCalls  int
	resumeCalls  int
	status       types.FlowSession
	startResult  types.FlowTurnResult
	handleResult types.FlowTurnResult
	resumeResult types.FlowTurnResult
	err          error
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
		startResult: testFlowTurnResult(types.SessionCompleted, "done"),
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
			resumeResult: testFlowTurnResult(types.SessionCompleted, "resumed"),
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
			handleResult: testFlowTurnResult(types.SessionCompleted, "handled"),
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
}

func TestJSONRPCServerIgnoresNotifications(t *testing.T) {
	stub := &jsonRPCFlowRuntimeStub{
		startResult: testFlowTurnResult(types.SessionCompleted, "done"),
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
		startResult: testFlowTurnResult(types.SessionCompleted, "done"),
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
