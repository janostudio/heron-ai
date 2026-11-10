package flow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestSelectFlowRecordsIsolatesTaskContexts is the record-side of instance
// isolation: a record produced inside task context A is invisible to task B,
// while session-wide records (empty context) stay visible to everyone.
func TestSelectFlowRecordsIsolatesTaskContexts(t *testing.T) {
	spec := types.InputSpec{FlowRecords: []string{"Report"}}
	record := func(id, context string) types.SharedRecord {
		return types.SharedRecord{
			RecordID:  id,
			Name:      "Report",
			CreatedAt: time.Now().UTC(),
			Producer:  types.ProducerRef{TaskContextID: context},
		}
	}
	records := []types.SharedRecord{
		record("a", "fs:dev:1"),
		record("b", "fs:dev:2"),
		record("shared", ""),
	}

	first := selectFlowRecordsInContext(records, spec, time.Time{}, "fs:dev:1", "", "")
	require.Len(t, first, 2, "own task record + the session-wide one")
	require.Equal(t, "a", first[0].RecordID)
	require.Equal(t, "shared", first[1].RecordID)

	second := selectFlowRecordsInContext(records, spec, time.Time{}, "fs:dev:2", "", "")
	require.Len(t, second, 2)
	require.Equal(t, "b", second[0].RecordID)

	// The historical entry point stays session-wide: no task context means only
	// records outside any task.
	wide := selectFlowRecords(records, spec, time.Time{})
	require.Len(t, wide, 1)
	require.Equal(t, "shared", wide[0].RecordID)
}

// TestSelectFlowRecordsSkipsProducerAndSettledProgress covers what a Team may
// read as input: not a progress line whose call has already come back, and not
// its own words from an earlier turn.
func TestSelectFlowRecordsSkipsProducerAndSettledProgress(t *testing.T) {
	spec := types.InputSpec{FlowRecords: []string{"Reply"}}
	now := time.Now().UTC()
	record := func(id, producer, status, turn string) types.SharedRecord {
		return types.SharedRecord{
			RecordID:  id,
			Name:      "Reply",
			CreatedAt: now,
			Status:    types.RecordStatus(status),
			Producer:  types.ProducerRef{TeamID: producer, FlowTurnID: turn},
		}
	}
	records := []types.SharedRecord{
		record("theirs", "dev-lead", "active", "ft-earlier"),
		record("progress", "dev-lead", "pending", "ft-earlier"),
		// The same id twice: the append-only store keeps both revisions, and
		// only the later one describes the record now.
		record("settled", "dev-lead", "pending", "ft-earlier"),
		record("settled", "dev-lead", "invalidated", "ft-earlier"),
		// Own words: unreadable when they come from an earlier turn, readable
		// inside the turn that produced them (a Team continuing its own work).
		record("own-old", "frontdesk", "active", "ft-earlier"),
		record("own-now", "frontdesk", "active", "ft-now"),
	}

	selected := selectFlowRecordsInContext(records, spec, time.Time{}, "", "frontdesk", "ft-now")
	ids := make([]string, 0, len(selected))
	for _, item := range selected {
		ids = append(ids, item.RecordID)
	}
	require.ElementsMatch(t, []string{"theirs", "own-now"}, ids,
		"过期的进度、已作废的记录、上一轮自己的话都不是输入；本轮自己的记录仍然可读")
}
