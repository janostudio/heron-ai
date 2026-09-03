package agent

import (
	"context"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// Summarizer produces a compact summary for dropped message groups.
type Summarizer interface {
	Summarize(ctx context.Context, groups [][]types.Message) (string, error)
}

// mechanicalSummarizer is the default LLM-free summary.
type mechanicalSummarizer struct{}

func (mechanicalSummarizer) Summarize(_ context.Context, groups [][]types.Message) (string, error) {
	return buildContextSummary(groups, 0), nil
}
