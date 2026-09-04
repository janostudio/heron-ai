package agent

import (
	"context"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// Compactor produces a compact summary for dropped message groups.
type Compactor interface {
	Compact(ctx context.Context, groups [][]types.Message) (string, error)
}

// mechanicalCompactor is the default LLM-free summary.
type mechanicalCompactor struct{}

func (mechanicalCompactor) Compact(_ context.Context, groups [][]types.Message) (string, error) {
	return buildContextSummary(groups, 0), nil
}
