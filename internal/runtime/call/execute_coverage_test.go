package call

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/workspace"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// stubAgentRunner lets each test control the result/error returned from Run.
type stubAgentRunner struct {
	result *types.AgentResult
	err    error
}

func (r *stubAgentRunner) Run(_ context.Context, _ types.AgentConfig, _ types.AgentRequest) (*types.AgentResult, error) {
	return r.result, r.err
}

func validAgentRequest() types.CallRequest {
	return types.CallRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamSession: types.TeamSession{ID: "ts-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "team-1"},
		Call: types.Call{
			ID:      "answer",
			Type:    types.CallAgent,
			AgentID: "assistant",
		},
		AgentDefinition: &types.AgentConfig{Name: "assistant"},
		CallTurnID:      "ct-1",
	}
}

func TestAgentExecutorType(t *testing.T) {
	exec := NewAgentExecutor(&stubAgentRunner{})
	assert.Equal(t, types.CallAgent, exec.Type())
}

func TestAgentExecutorNilRunner(t *testing.T) {
	exec := &AgentExecutor{runner: nil}

	result, err := exec.Execute(context.Background(), validAgentRequest())

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Contains(t, err.Error(), "runner is nil")
}

func TestAgentExecutorNilDefinition(t *testing.T) {
	exec := NewAgentExecutor(&stubAgentRunner{})
	req := validAgentRequest()
	req.AgentDefinition = nil

	result, err := exec.Execute(context.Background(), req)

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Contains(t, err.Error(), "agent definition is required")
}

func TestAgentExecutorWithTimeoutError(t *testing.T) {
	exec := NewAgentExecutor(&stubAgentRunner{})
	req := validAgentRequest()
	req.Call.Timeout = "not-a-duration"

	result, err := exec.Execute(context.Background(), req)

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Contains(t, err.Error(), "invalid timeout")
}

func TestAgentExecutorRunError(t *testing.T) {
	exec := NewAgentExecutor(&stubAgentRunner{err: errors.New("boom")})

	result, err := exec.Execute(context.Background(), validAgentRequest())

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Equal(t, "boom", result.Error)
}

func TestAgentExecutorNilResult(t *testing.T) {
	exec := NewAgentExecutor(&stubAgentRunner{result: nil})

	result, err := exec.Execute(context.Background(), validAgentRequest())

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Contains(t, err.Error(), "nil result")
}

func TestAgentExecutorDefaultStatus(t *testing.T) {
	// Empty Status in the result should default to TurnCompleted.
	exec := NewAgentExecutor(&stubAgentRunner{result: &types.AgentResult{
		Reply: "ok",
	}})

	result, err := exec.Execute(context.Background(), validAgentRequest())

	require.NoError(t, err)
	assert.Equal(t, types.TurnCompleted, result.Status)
	assert.Equal(t, "ok", result.Reply)
}

func TestAgentExecutorCheckpointID(t *testing.T) {
	exec := NewAgentExecutor(&stubAgentRunner{result: &types.AgentResult{
		Status:     types.TurnCompleted,
		Reply:      "ok",
		Checkpoint: &types.AgentCheckpoint{ID: "cp-1"},
	}})

	result, err := exec.Execute(context.Background(), validAgentRequest())

	require.NoError(t, err)
	assert.Equal(t, "cp-1", result.CheckpointID)
	assert.NotNil(t, result.Checkpoint)
}

func TestAgentExecutorResultError(t *testing.T) {
	exec := NewAgentExecutor(&stubAgentRunner{result: &types.AgentResult{
		Status: types.TurnCompleted,
		Reply:  "partial",
		Error:  "agent failed",
	}})

	result, err := exec.Execute(context.Background(), validAgentRequest())

	require.NoError(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Equal(t, "agent failed", result.Error)
}

func TestAgentExecutorParsedInRecord(t *testing.T) {
	parsed := map[string]any{"answer": 42}
	exec := NewAgentExecutor(&stubAgentRunner{result: &types.AgentResult{
		Status: types.TurnCompleted,
		Reply:  "done",
		Parsed: parsed,
	}})

	req := validAgentRequest()
	req.Call.Output.Record = "AnswerRecord"

	result, err := exec.Execute(context.Background(), req)

	require.NoError(t, err)
	require.Len(t, result.Records, 1)
	assert.Equal(t, "AnswerRecord", result.Records[0].Name)
	assert.Equal(t, "agent_result", result.Records[0].Kind)
	assert.Equal(t, "done", result.Records[0].Data["reply"])
	assert.Equal(t, parsed, result.Records[0].Data["parsed"])
}

func TestAgentExecutorNoParsedInRecord(t *testing.T) {
	exec := NewAgentExecutor(&stubAgentRunner{result: &types.AgentResult{
		Status: types.TurnCompleted,
		Reply:  "done",
	}})

	req := validAgentRequest()
	req.Call.Output.Record = "AnswerRecord"

	result, err := exec.Execute(context.Background(), req)

	require.NoError(t, err)
	require.Len(t, result.Records, 1)
	_, exists := result.Records[0].Data["parsed"]
	assert.False(t, exists, "parsed key should be absent when result.Parsed is nil")
}

func TestCommandExecutorType(t *testing.T) {
	exec := NewCommandExecutor(nil)
	assert.Equal(t, types.CallCommand, exec.Type())
}

func newWorkspaceExecutor(t *testing.T) *CommandExecutor {
	t.Helper()
	service, err := workspace.New(t.TempDir())
	require.NoError(t, err)
	return NewCommandExecutor(service)
}

func commandRequest(command string, args ...string) types.CallRequest {
	return types.CallRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "team-1"},
		Call: types.Call{
			ID:      "run",
			Type:    types.CallCommand,
			Command: &types.CommandSpec{Command: command, Args: args},
		},
		CallTurnID: "ct-1",
	}
}

func TestCommandExecutorSuccess(t *testing.T) {
	exec := newWorkspaceExecutor(t)

	result, err := exec.Execute(context.Background(), commandRequest("echo hello"))

	require.NoError(t, err)
	assert.Equal(t, types.TurnCompleted, result.Status)
	assert.Equal(t, "hello", result.Reply)
	require.Len(t, result.WorkspaceOps, 1)
	assert.Equal(t, 0, result.WorkspaceOps[0].ExitCode)
}

func TestCommandExecutorNonZeroExit(t *testing.T) {
	exec := newWorkspaceExecutor(t)

	result, err := exec.Execute(context.Background(), commandRequest("echo oops; exit 1"))

	require.NoError(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Equal(t, "oops", result.Reply)
	assert.Contains(t, result.Error, "exit status 1")
}

func TestCommandExecutorCommandNotFound(t *testing.T) {
	exec := newWorkspaceExecutor(t)

	result, err := exec.Execute(context.Background(), commandRequest("definitely-not-a-real-command-xyz"))

	require.NoError(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.NotEmpty(t, result.Error)
}

func TestCommandExecutorStderrOutput(t *testing.T) {
	exec := newWorkspaceExecutor(t)

	result, err := exec.Execute(context.Background(), commandRequest("echo errline >&2"))

	require.NoError(t, err)
	assert.Equal(t, types.TurnCompleted, result.Status)
	// stdout is empty, so summary falls back to stderr.
	assert.Equal(t, "errline", result.Reply)
}

func TestCommandExecutorRecord(t *testing.T) {
	exec := newWorkspaceExecutor(t)
	req := commandRequest("echo hi")
	req.Call.Output.Record = "CmdResult"

	result, err := exec.Execute(context.Background(), req)

	require.NoError(t, err)
	require.Len(t, result.Records, 1)
	assert.Equal(t, "CmdResult", result.Records[0].Name)
	assert.Equal(t, "command_result", result.Records[0].Kind)
	assert.Equal(t, 0, result.Records[0].Data["exit_code"])
	assert.Equal(t, true, result.Records[0].Data["passed"])
	require.Len(t, result.Records[0].Basis, 1)
	assert.Equal(t, "workspace_operation", result.Records[0].Basis[0].Kind)
}

func TestCommandExecutorMissingCommand(t *testing.T) {
	exec := newWorkspaceExecutor(t)
	req := commandRequest("")
	req.Call.Command = &types.CommandSpec{}

	result, err := exec.Execute(context.Background(), req)

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Contains(t, err.Error(), "command is required")
}

func TestCommandExecutorNilWorkspace(t *testing.T) {
	exec := NewCommandExecutor(nil)

	result, err := exec.Execute(context.Background(), commandRequest("echo hi"))

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Contains(t, err.Error(), "workspace is not configured")
}

func TestCommandExecutorReplayPolicyBlocked(t *testing.T) {
	exec := newWorkspaceExecutor(t)
	req := commandRequest("echo hi")
	req.RecoveryOf = "prev-ct"
	req.Call.Command.ReplayPolicy = types.ReplayNever

	result, err := exec.Execute(context.Background(), req)

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Contains(t, err.Error(), "replay policy")
}

func TestCommandExecutorIdempotentReplayRequiresKey(t *testing.T) {
	exec := newWorkspaceExecutor(t)
	req := commandRequest("echo hi")
	req.RecoveryOf = "prev-ct"
	req.Call.Command.ReplayPolicy = types.ReplayIdempotent
	req.Call.Command.IdempotencyKey = ""

	result, err := exec.Execute(context.Background(), req)

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Contains(t, err.Error(), "idempotency_key")
}

func TestCommandExecutorIdempotentReplayAllowed(t *testing.T) {
	exec := newWorkspaceExecutor(t)
	req := commandRequest("echo hi")
	req.RecoveryOf = "prev-ct"
	req.Call.Command.ReplayPolicy = types.ReplayIdempotent
	req.Call.Command.IdempotencyKey = "key-1"

	result, err := exec.Execute(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, types.TurnCompleted, result.Status)
}

func TestWebhookExecutorType(t *testing.T) {
	exec := NewWebhookExecutor(nil)
	assert.Equal(t, types.CallWebhook, exec.Type())
}

func TestWebhookExecutorReplayPolicyBlocked(t *testing.T) {
	exec := NewWebhookExecutor(nil)
	req := webhookRequest("http://example.com")
	req.RecoveryOf = "prev-ct"
	req.Call.Webhook.ReplayPolicy = types.ReplayNever

	result, err := exec.Execute(context.Background(), req)

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Contains(t, err.Error(), "replay policy")
}

func TestWebhookExecutorIdempotentReplayRequiresKey(t *testing.T) {
	exec := NewWebhookExecutor(nil)
	req := webhookRequest("http://example.com")
	req.RecoveryOf = "prev-ct"
	req.Call.Webhook.ReplayPolicy = types.ReplayIdempotent
	req.Call.Webhook.IdempotencyKey = ""

	result, err := exec.Execute(context.Background(), req)

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Contains(t, err.Error(), "idempotency_key")
}

func TestWebhookExecutorIdempotencyKeyHeader(t *testing.T) {
	var gotKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	req := webhookRequest(server.URL)
	req.Call.Webhook.IdempotencyKey = "key-${call_id}"

	exec := NewWebhookExecutor(server.Client())
	_, err := exec.Execute(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "key-notify", gotKey)
}

func TestWebhookExecutorHeadersOverrideContentType(t *testing.T) {
	var gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	req := webhookRequest(server.URL)
	req.Call.Webhook.Headers = map[string]string{"Content-Type": "application/x-custom"}

	exec := NewWebhookExecutor(server.Client())
	_, err := exec.Execute(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, "application/x-custom", gotContentType)
}

func TestWebhookExecutorInvalidURL(t *testing.T) {
	exec := NewWebhookExecutor(nil)
	req := webhookRequest("://bad-url")

	result, err := exec.Execute(context.Background(), req)

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
}

func TestWebhookExecutorClientDoError(t *testing.T) {
	// A client with no transport fails on Do.
	exec := NewWebhookExecutor(&http.Client{Transport: errorTransport{}})
	req := webhookRequest("http://example.com")

	result, err := exec.Execute(context.Background(), req)

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
}

type errorTransport struct{}

func (errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial failure")
}

func TestWebhookExecutorNon2xxWithRecord(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	req := webhookRequest(server.URL)
	req.Call.Output.Record = "HookResult"

	exec := NewWebhookExecutor(server.Client())
	result, err := exec.Execute(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	require.Len(t, result.Records, 1)
	assert.Equal(t, "HookResult", result.Records[0].Name)
	assert.Equal(t, "webhook_result", result.Records[0].Kind)
	assert.Equal(t, http.StatusInternalServerError, result.Records[0].Data["status_code"])
	assert.Equal(t, "boom", result.Records[0].Data["body"])
}

func TestWebhookExecutorReadAllFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	defer server.Close()

	exec := NewWebhookExecutor(server.Client())
	result, err := exec.Execute(context.Background(), webhookRequest(server.URL))

	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Contains(t, strings.ToLower(err.Error()), "unexpected eof")
}
