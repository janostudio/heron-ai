package call

import (
	"context"
	"fmt"
	"sync"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// Registry maps the declarative call type to an executor implementation.
type Registry struct {
	mu        sync.RWMutex
	executors map[types.CallType]types.CallExecutorProvider
}

func NewRegistry() *Registry {
	return &Registry{
		executors: make(map[types.CallType]types.CallExecutorProvider),
	}
}

func (r *Registry) Register(executor types.CallExecutorProvider) error {
	if executor == nil {
		return fmt.Errorf("call executor is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executors[executor.Type()] = executor
	return nil
}

func (r *Registry) Lookup(callType types.CallType) (types.CallExecutorProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	executor, ok := r.executors[callType]
	if !ok {
		return nil, fmt.Errorf("call executor for type %q is not registered", callType)
	}
	return executor, nil
}

func (r *Registry) Execute(ctx context.Context, req types.CallRequest) (types.CallResult, error) {
	executor, err := r.Lookup(req.Call.Type)
	if err != nil {
		return types.CallResult{Status: types.TurnFailed, Error: err.Error()}, err
	}
	return executor.Execute(ctx, req)
}
