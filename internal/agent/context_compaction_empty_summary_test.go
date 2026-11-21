package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestCompactHookFiresWhenDroppedGroupsHaveNoSummary covers the gap that made
// a compaction invisible: the hook used to run only when the summariser
// produced text, so a pass that dropped messages without a usable summary left
// the context smaller and the session log unchanged. Nothing downstream could
// then tell that history had been folded away.
//
// Whitespace-only messages are the cheapest way to reach that state: they cost
// tokens (so the compaction is worth running) but contribute no summary text.
func TestCompactHookFiresWhenDroppedGroupsHaveNoSummary(t *testing.T) {
	manager := NewContextManagerWithCompactor(types.ContextConfig{MaxInputTokens: 300}, nil, mechanicalCompactor{})
	require.NoError(t, manager.AddMessage(types.Message{Role: "system", Content: "system"}))

	type call struct {
		summary string
		dropped int
	}
	calls := make([]call, 0, 1)
	manager.SetCompactHook(func(summary string, dropped int) {
		calls = append(calls, call{summary: summary, dropped: dropped})
	})

	for i := 0; i < 3; i++ {
		require.NoError(t, manager.AddMessage(types.Message{
			Role:    "user",
			Content: strings.Repeat(" ", 400),
		}))
	}

	require.Len(t, calls, 1, "a compaction that dropped groups must report itself")
	require.Equal(t, 1, calls[0].dropped)
	require.Empty(t, calls[0].summary, "the dropped groups carry no text to summarise")
	require.Empty(t, contextSummaryOf(manager.Messages()),
		"the context must hold no summary, so the hook must not claim one")
}

// TestCompactHookSummaryMatchesRecordedContext pins the other half: what the
// hook reports is the summary that ended up in the context, not the text the
// compactor returned before the budget had its say. A later replay trusts this
// value to rebuild the model input, so it cannot be the pre-trim text.
func TestCompactHookSummaryMatchesRecordedContext(t *testing.T) {
	manager := NewContextManagerWithCompactor(types.ContextConfig{MaxInputTokens: 400}, nil, mechanicalCompactor{})
	require.NoError(t, manager.AddMessage(types.Message{Role: "system", Content: "system"}))

	reported := make([]string, 0, 1)
	manager.SetCompactHook(func(summary string, dropped int) {
		reported = append(reported, summary)
	})

	for i := 0; i < 4; i++ {
		require.NoError(t, manager.AddMessage(types.Message{
			Role:    "user",
			Content: strings.Repeat("x", 400),
		}))
	}

	require.Len(t, reported, 1)
	require.NotEmpty(t, reported[0])
	require.Equal(t, contextSummaryOf(manager.Messages()), reported[0],
		"the hook must report the summary the context actually carries")
}

// TestCompactHookReportsSummaryTrimmedAway covers the case where a summary was
// produced and then removed by the hard-limit trim: the hook still has to fire
// (groups were dropped) and it has to report "" — claiming a summary that is
// not in the context would make a replay invent context the model never read.
func TestCompactHookReportsSummaryTrimmedAway(t *testing.T) {
	manager := NewContextManagerWithCompactor(types.ContextConfig{MaxInputTokens: 300}, nil, mechanicalCompactor{})
	require.NoError(t, manager.AddMessage(types.Message{Role: "system", Content: "system"}))

	reported := make([]string, 0, 1)
	dropped := make([]int, 0, 1)
	manager.SetCompactHook(func(summary string, count int) {
		reported = append(reported, summary)
		dropped = append(dropped, count)
	})

	for i := 0; i < 3; i++ {
		require.NoError(t, manager.AddMessage(types.Message{
			Role:    "user",
			Content: strings.Repeat("x", 400),
		}))
	}

	require.Len(t, reported, 1)
	require.Greater(t, dropped[0], 0)
	require.Equal(t, contextSummaryOf(manager.Messages()), reported[0])
	require.Empty(t, reported[0], "the hard limit took the summary out of the context")
}

// TestContextSummaryOf covers the extraction itself: summary present, summary
// absent, and a message that carries the marker with nothing after it.
func TestContextSummaryOf(t *testing.T) {
	require.Empty(t, contextSummaryOf(nil))
	require.Empty(t, contextSummaryOf([]types.Message{{Role: "assistant", Content: "plain"}}))
	require.Equal(t, "recap",
		contextSummaryOf([]types.Message{
			{Role: "system", Content: "system"},
			{Role: "user", Content: appendCompactionSummary("", "recap")},
			{Role: "assistant", Content: "after"},
		}))
	require.Empty(t, contextSummaryOf([]types.Message{
		{Role: "user", Content: compactedContextPreamble + "\n\n" + compactedContextMarker},
	}))
}
