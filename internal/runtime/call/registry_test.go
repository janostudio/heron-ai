package call

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// stubExecutor is a minimal CallExecutorProvider for Registry tests.
type stubExecutor struct {
	callType types.CallType
	req      *types.CallRequest
	result   types.CallResult
	err      error
}

func (s *stubExecutor) Type() types.CallType {
	return s.callType
}

func (s *stubExecutor) Execute(_ context.Context, req types.CallRequest) (types.CallResult, error) {
	s.req = &req
	return s.result, s.err
}

func TestRegistryRegisterRejectsNil(t *testing.T) {
	r := NewRegistry()
	err := r.Register(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestRegistryLookupUnregistered(t *testing.T) {
	r := NewRegistry()
	exec, err := r.Lookup(types.CallAgent)
	require.Error(t, err)
	assert.Nil(t, exec)
	assert.Contains(t, err.Error(), "not registered")
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	exec := &stubExecutor{callType: types.CallAgent}

	require.NoError(t, r.Register(exec))

	got, err := r.Lookup(types.CallAgent)
	require.NoError(t, err)
	assert.Same(t, exec, got)
}

func TestRegistryRegisterOverwrites(t *testing.T) {
	r := NewRegistry()
	first := &stubExecutor{callType: types.CallAgent}
	second := &stubExecutor{callType: types.CallAgent}

	require.NoError(t, r.Register(first))
	require.NoError(t, r.Register(second))

	got, err := r.Lookup(types.CallAgent)
	require.NoError(t, err)
	assert.Same(t, second, got, "second registration should overwrite the first")
}

func TestRegistryExecuteDelegates(t *testing.T) {
	r := NewRegistry()
	exec := &stubExecutor{
		callType: types.CallCommand,
		result:   types.CallResult{Status: types.TurnCompleted, Reply: "done"},
	}
	require.NoError(t, r.Register(exec))

	req := types.CallRequest{
		Call: types.Call{ID: "build", Type: types.CallCommand},
	}
	result, err := r.Execute(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, types.TurnCompleted, result.Status)
	assert.Equal(t, "done", result.Reply)
	require.NotNil(t, exec.req)
	assert.Equal(t, "build", exec.req.Call.ID)
}

func TestRegistryExecuteUnregisteredReturnsFailed(t *testing.T) {
	r := NewRegistry()

	result, err := r.Execute(context.Background(), types.CallRequest{
		Call: types.Call{ID: "x", Type: types.CallWebhook},
	})
	require.Error(t, err)
	assert.Equal(t, types.TurnFailed, result.Status)
	assert.Equal(t, err.Error(), result.Error)
}
