package member

import (
	"context"
	"fmt"
	"sync"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// Registry maps the declarative member type to an executor implementation.
type Registry struct {
	mu        sync.RWMutex
	executors map[types.MemberType]types.MemberExecutorProvider
}

func NewRegistry() *Registry {
	return &Registry{
		executors: make(map[types.MemberType]types.MemberExecutorProvider),
	}
}

func (r *Registry) Register(executor types.MemberExecutorProvider) error {
	if executor == nil {
		return fmt.Errorf("member executor is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executors[executor.Type()] = executor
	return nil
}

func (r *Registry) Lookup(memberType types.MemberType) (types.MemberExecutorProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	executor, ok := r.executors[memberType]
	if !ok {
		return nil, fmt.Errorf("member executor for type %q is not registered", memberType)
	}
	return executor, nil
}

func (r *Registry) Execute(ctx context.Context, req types.MemberRequest) (types.MemberResult, error) {
	executor, err := r.Lookup(req.Member.Type)
	if err != nil {
		return types.MemberResult{Status: types.TurnFailed, Error: err.Error()}, err
	}
	return executor.Execute(ctx, req)
}
