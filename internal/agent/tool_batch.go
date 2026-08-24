package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// BatchToolExecutor is the V1 Idea 12 implementation:
// - read_only tools may run concurrently;
// - serial or unknown tools run one at a time;
// - returned results retain the original ToolCall order.
type BatchToolExecutor interface {
	Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error)
	ExecutionSpec(name string) types.ToolExecutionSpec
}

type ToolBatchExecutor struct {
	executor     BatchToolExecutor
	maxParallel  int
	parallelSafe bool
}

func NewToolBatchExecutor(executor BatchToolExecutor, maxParallel int, parallelSafe bool) *ToolBatchExecutor {
	if maxParallel <= 0 {
		maxParallel = 5
	}
	return &ToolBatchExecutor{
		executor:     executor,
		maxParallel:  maxParallel,
		parallelSafe: parallelSafe,
	}
}

func (e *ToolBatchExecutor) Execute(ctx context.Context, calls []types.ToolCall) []*types.ToolResult {
	results := make([]*types.ToolResult, len(calls))
	if len(calls) == 0 {
		return results
	}
	if e.executor == nil || !e.parallelSafe {
		for i, call := range calls {
			results[i] = e.executeOne(ctx, call)
		}
		return results
	}

	for i := 0; i < len(calls); {
		if e.executor.ExecutionSpec(calls[i].Name).Class != types.ToolReadOnly {
			results[i] = e.executeOne(ctx, calls[i])
			i++
			continue
		}

		// Run a contiguous read-only segment concurrently. Serial calls
		// remain barriers, so a batch [Read, Write, Read] preserves the
		// original execution order instead of moving Write ahead of Read.
		start := i
		for i < len(calls) && e.executor.ExecutionSpec(calls[i].Name).Class == types.ToolReadOnly {
			i++
		}
		e.executeReadOnlySegment(ctx, calls, results, start, i)
	}
	return results
}

func (e *ToolBatchExecutor) executeReadOnlySegment(
	ctx context.Context,
	calls []types.ToolCall,
	results []*types.ToolResult,
	start int,
	end int,
) {
	sem := make(chan struct{}, e.maxParallel)
	var wg sync.WaitGroup
	for index := start; index < end; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[index] = &types.ToolResult{Success: false, Error: ctx.Err().Error()}
				return
			}
			defer func() { <-sem }()
			results[index] = e.executeOne(ctx, calls[index])
		}(index)
	}
	wg.Wait()
}

func (e *ToolBatchExecutor) executeOne(ctx context.Context, call types.ToolCall) *types.ToolResult {
	result, err := e.executor.Execute(ctx, call.Name, call.Arguments)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("%s: %v", call.Name, err)}
	}
	if result == nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("%s returned nil result", call.Name)}
	}
	return result
}
